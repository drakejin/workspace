import { INDEX_PREFIX, parseIndexName } from "../naming.js";
import { AUTO_MANAGE_SETTING } from "../opensearch/schema.js";
import { isStatus, type OpenSearchPort } from "../opensearch/port.js";
import type { DocType } from "../types.js";

/** 색인에 있는 문서 하나의 대표값 (chunk_index 0 청크) */
export interface IndexedDoc {
  readonly sourcePath: string;
  readonly team: string;
  readonly docType: DocType;
  readonly contentSha: string | null;
  readonly bodySha: string | null;
  readonly mtimeMs: number | null;
  readonly size: number | null;
  readonly eventTime: string;
  readonly supersedes: string | null;
  readonly supersededBy: string | null;
  readonly summary: string | null;
}

const MAX_DOCS = 10_000;

interface SettingsResponse {
  readonly [index: string]: { settings: Record<string, string> };
}

/**
 * ISM 이 관리하는 hot 인덱스 목록. restore_cold 로 복원한 인덱스(auto_manage=false)는 뺀다 —
 * 데몬은 cold 를 건드리지 않는다. 복원본은 그 시점 사본이라 hot 파일과 대조하지 않는다.
 */
export async function managedIndices(os: OpenSearchPort): Promise<string[]> {
  try {
    // 설정 이름으로 거르면 그 설정이 없는 인덱스가 응답에서 빠진다 — 전체 flat 설정을 받는다
    const out = await os.request<SettingsResponse>("GET", `/${INDEX_PREFIX}*/_settings`, undefined, {
      flat_settings: "true",
      allow_no_indices: "true",
      expand_wildcards: "open",
    });
    return Object.entries(out)
      .filter(([index, { settings }]) => parseIndexName(index) && settings[AUTO_MANAGE_SETTING] !== "false")
      .map(([index]) => index)
      .sort();
  } catch (error) {
    if (isStatus(error, 404)) return [];
    throw error;
  }
}

interface ChunkSource {
  source_path: string;
  team: string;
  doc_type: DocType;
  content_sha?: string;
  body_sha?: string;
  mtime_ms?: number;
  size?: number;
  event_time: string;
  supersedes?: string | null;
  superseded_by?: string | null;
  summary?: string | null;
}

export async function fetchIndexedDocs(os: OpenSearchPort, indices: readonly string[]): Promise<Map<string, IndexedDoc>> {
  if (indices.length === 0) return new Map();
  const out = await os.request<{ hits: { hits: Array<{ _source: ChunkSource }> } }>(
    "POST",
    `/${indices.join(",")}/_search`,
    {
      size: MAX_DOCS,
      query: { term: { chunk_index: 0 } },
      _source: [
        "source_path", "team", "doc_type", "content_sha", "body_sha", "mtime_ms", "size",
        "event_time", "supersedes", "superseded_by", "summary",
      ],
    },
    { ignore_unavailable: "true", allow_no_indices: "true" },
  );
  const docs = new Map<string, IndexedDoc>();
  for (const { _source: s } of out.hits.hits) {
    docs.set(s.source_path, {
      sourcePath: s.source_path,
      team: s.team,
      docType: s.doc_type,
      contentSha: s.content_sha ?? null,
      bodySha: s.body_sha ?? null,
      mtimeMs: s.mtime_ms ?? null,
      size: s.size ?? null,
      eventTime: s.event_time,
      supersedes: s.supersedes ?? null,
      supersededBy: s.superseded_by ?? null,
      summary: s.summary ?? null,
    });
  }
  return docs;
}

/** 문서의 모든 청크에 필드를 덮어쓴다 (stat 갱신, superseded_by 수렴, 증류 반영) */
export async function updateBySource(
  os: OpenSearchPort,
  indices: readonly string[],
  sourcePath: string,
  fields: Record<string, unknown>,
): Promise<number> {
  if (indices.length === 0) return 0;
  const keys = Object.keys(fields);
  const out = await os.request<{ updated: number }>(
    "POST",
    `/${indices.join(",")}/_update_by_query`,
    {
      query: { term: { source_path: sourcePath } },
      script: {
        lang: "painless",
        source: keys.map((k) => `ctx._source['${k}'] = params['${k}'];`).join(" "),
        params: fields,
      },
    },
    { refresh: "true", conflicts: "proceed", ignore_unavailable: "true", allow_no_indices: "true" },
  );
  return out.updated;
}
