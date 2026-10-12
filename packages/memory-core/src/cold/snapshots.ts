import type { MemoryConfig } from "../config.js";
import { parseIndexName, teamIndexPattern, INDEX_PREFIX } from "../naming.js";
import { AUTO_MANAGE_SETTING } from "../opensearch/schema.js";
import { isStatus, type OpenSearchPort } from "../opensearch/port.js";

export interface ColdIndex {
  readonly index: string;
  readonly snapshot: string;
  readonly snapshotTime: string;
  /** 지금 hot에 복원돼 있는가 */
  readonly restored: boolean;
}

interface SnapshotList {
  readonly snapshots: ReadonlyArray<{ snapshot: string; indices: string[]; state: string; start_time: string }>;
}

async function snapshotsOf(os: OpenSearchPort, repository: string): Promise<SnapshotList["snapshots"]> {
  const out = await os.request<SnapshotList>("GET", `/_snapshot/${repository}/_all`);
  return out.snapshots.filter((s) => s.state === "SUCCESS");
}

async function existingIndices(os: OpenSearchPort, pattern: string): Promise<Set<string>> {
  const rows = await os.request<Array<{ index: string }>>("GET", `/_cat/indices/${pattern}`, undefined, {
    format: "json",
    h: "index",
    expand_wildcards: "open",
  }).catch((error: unknown) => {
    if (isStatus(error, 404)) return [];
    throw error;
  });
  return new Set(rows.map((r) => r.index));
}

/** S3(스냅샷 repository)에 있는 팀의 cold 인덱스 목록. 같은 인덱스가 여러 스냅샷에 있으면 최신 것. */
export async function listCold(os: OpenSearchPort, config: MemoryConfig, team: string): Promise<ColdIndex[]> {
  const ofTeam = (index: string) => parseIndexName(index)?.team === team;
  const [snapshots, live] = await Promise.all([
    snapshotsOf(os, config.snapshot.repository),
    existingIndices(os, teamIndexPattern(team)),
  ]);
  const latest = new Map<string, ColdIndex>();
  for (const snap of snapshots) {
    for (const index of snap.indices.filter(ofTeam)) {
      const prev = latest.get(index);
      if (!prev || prev.snapshotTime < snap.start_time) {
        latest.set(index, { index, snapshot: snap.snapshot, snapshotTime: snap.start_time, restored: live.has(index) });
      }
    }
  }
  return [...latest.values()].sort((a, b) => a.index.localeCompare(b.index));
}

/** 스냅샷에서 인덱스 하나를 hot으로 되돌린다. 복원본은 ISM 자동 관리에서 뺀다(바로 다시 cold로 가지 않게). */
export async function restoreCold(os: OpenSearchPort, config: MemoryConfig, index: string): Promise<ColdIndex> {
  const parsed = parseIndexName(index);
  if (!parsed) throw new Error(`memory 인덱스 이름이 아닙니다: ${index}`);
  const candidates = await listCold(os, config, parsed.team);
  const target = candidates.find((c) => c.index === index);
  if (!target) throw new Error(`cold에 ${index} 가 없습니다. list_cold로 먼저 확인하세요`);
  if (target.restored) return target;
  await os.request(
    "POST",
    `/_snapshot/${config.snapshot.repository}/${target.snapshot}/_restore`,
    {
      indices: index,
      include_global_state: false,
      include_aliases: true,
      index_settings: { [AUTO_MANAGE_SETTING]: false },
    },
    { wait_for_completion: "true" },
  );
  return { ...target, restored: true };
}

export interface TierDecision {
  readonly index: string;
  readonly newestEventTime: string | null;
  readonly action: "tiered" | "would-tier" | "kept-hot" | "skipped-restored";
}

interface IndexSettings {
  readonly [index: string]: { settings: { index?: { plugins?: { index_state_management?: { auto_manage?: string } } } } };
}

/**
 * 가장 최신 문서도 hotDays를 넘긴 월별 인덱스를 스냅샷 후 삭제한다.
 * ISM은 인덱스 생성일 기준이라, 오래된 문서를 늦게 적재한 인덱스는 이 함수가 맡는다.
 */
export async function tierExpired(
  os: OpenSearchPort,
  config: MemoryConfig,
  options: { now?: Date; dryRun?: boolean; team?: string } = {},
): Promise<TierDecision[]> {
  const now = options.now ?? new Date();
  const cutoff = now.getTime() - config.hotDays * 24 * 60 * 60 * 1000;
  const pattern = options.team ? teamIndexPattern(options.team) : `${INDEX_PREFIX}*`;
  // mem-ads-* 는 mem-ads-data-* 도 잡으므로 이름에서 파싱한 team으로 다시 거른다
  const indices = [...(await existingIndices(os, pattern))]
    .filter((i) => {
      const parsed = parseIndexName(i);
      return parsed !== null && (!options.team || parsed.team === options.team);
    })
    .sort();
  const decisions: TierDecision[] = [];
  for (const index of indices) {
    decisions.push(await decideAndTier(os, config, index, cutoff, now, options.dryRun ?? false));
  }
  return decisions;
}

async function decideAndTier(
  os: OpenSearchPort,
  config: MemoryConfig,
  index: string,
  cutoff: number,
  now: Date,
  dryRun: boolean,
): Promise<TierDecision> {
  const settings = await os.request<IndexSettings>("GET", `/${index}/_settings`);
  const autoManage = settings[index]?.settings.index?.plugins?.index_state_management?.auto_manage;
  const newest = await newestEventTime(os, index);
  if (autoManage === "false") return { index, newestEventTime: newest, action: "skipped-restored" };
  if (newest !== null && Date.parse(newest) >= cutoff) return { index, newestEventTime: newest, action: "kept-hot" };
  if (dryRun) return { index, newestEventTime: newest, action: "would-tier" };

  const snapshot = `cold-${index}-${now.toISOString().replace(/[-:]/g, "").replace(/\..+$/, "").toLowerCase()}`;
  await os.request(
    "PUT",
    `/_snapshot/${config.snapshot.repository}/${snapshot}`,
    { indices: index, include_global_state: false },
    { wait_for_completion: "true" },
  );
  await os.request("DELETE", `/${index}`);
  return { index, newestEventTime: newest, action: "tiered" };
}

async function newestEventTime(os: OpenSearchPort, index: string): Promise<string | null> {
  const out = await os.request<{ aggregations: { newest: { value_as_string?: string } } }>(
    "POST",
    `/${index}/_search`,
    { size: 0, aggs: { newest: { max: { field: "event_time" } } } },
  );
  return out.aggregations.newest.value_as_string ?? null;
}
