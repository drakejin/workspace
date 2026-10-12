import type { MemoryConfig } from "../config.js";
import { DAEMON_FIELDS, INDEX_TEMPLATE_NAME, ISM_POLICY_ID, indexTemplate, ismPolicy } from "./schema.js";
import { hasErrorType, isStatus, type OpenSearchPort } from "./port.js";
import { INDEX_PREFIX, hotAlias } from "../naming.js";

/** 템플릿, ISM 정책, 스냅샷 repository를 멱등하게 맞춘다. */
export async function bootstrap(os: OpenSearchPort, config: MemoryConfig): Promise<void> {
  await os.request("PUT", `/_index_template/${INDEX_TEMPLATE_NAME}`, indexTemplate());
  // 템플릿은 새 인덱스에만 붙는다. 이미 있는 인덱스에는 데몬 필드를 더한다 (추가만이라 안전)
  await os.request("PUT", `/${INDEX_PREFIX}*/_mapping`, { properties: DAEMON_FIELDS }, {
    allow_no_indices: "true",
    ignore_unavailable: "true",
  });
  await ensureSnapshotRepository(os, config);
  await ensureIsmPolicy(os, config);
}

async function ensureSnapshotRepository(os: OpenSearchPort, config: MemoryConfig): Promise<void> {
  const { repository, type, fsLocation, s3Bucket } = config.snapshot;
  const body =
    type === "fs"
      ? { type: "fs", settings: { location: fsLocation } }
      : { type: "s3", settings: { bucket: s3Bucket, base_path: "snapshots" } };
  await os.request("PUT", `/_snapshot/${repository}`, body);
}

interface PolicyResponse {
  readonly _seq_no: number;
  readonly _primary_term: number;
}

async function ensureIsmPolicy(os: OpenSearchPort, config: MemoryConfig): Promise<void> {
  const path = `/_plugins/_ism/policies/${ISM_POLICY_ID}`;
  const body = ismPolicy(config.hotDays, config.snapshot.repository);
  try {
    const existing = await os.request<PolicyResponse>("GET", path);
    await os.request("PUT", path, body, {
      if_seq_no: String(existing._seq_no),
      if_primary_term: String(existing._primary_term),
    });
  } catch (error) {
    // 409 = 다른 프로세스(데몬·MCP·CLI)가 방금 같은 정책을 썼다. 내용이 같으니 성공으로 본다
    if (isStatus(error, 409)) return;
    if (!isStatus(error, 404)) throw error;
    try {
      await os.request("PUT", path, body);
    } catch (createError) {
      if (!isStatus(createError, 409)) throw createError;
    }
  }
}

/** 월별 인덱스가 없으면 hot alias를 붙여 만든다. */
export async function ensureMonthlyIndex(os: OpenSearchPort, index: string, team: string): Promise<void> {
  try {
    await os.request("PUT", `/${index}`, { aliases: { [hotAlias(team)]: {} } });
  } catch (error) {
    if (!hasErrorType(error, "resource_already_exists_exception")) throw error;
  }
}
