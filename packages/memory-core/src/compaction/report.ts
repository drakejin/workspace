import type { MemoryConfig } from "../config.js";
import { teamIndexPattern } from "../naming.js";
import { isStatus, type OpenSearchPort } from "../opensearch/port.js";

/** 주간 compaction 1·2단계(수명 확인, 충돌 탐지)의 재료. 판단과 다시 쓰기는 에이전트가 한다. */
export interface CompactionReport {
  readonly team: string;
  readonly cutoff: string;
  readonly expired: readonly ExpiredDoc[];
  readonly conflicts: readonly ConflictCandidate[];
  /** 청크가 MAX_CHUNKS를 넘어 일부만 봤는가 */
  readonly truncated: boolean;
}

export interface ExpiredDoc {
  readonly sourcePath: string;
  readonly repo: string | null;
  readonly docType: string;
  readonly eventTime: string;
}

export interface ConflictCandidate {
  readonly repo: string | null;
  readonly topic: string;
  readonly entries: ReadonlyArray<{ sourcePath: string; headerPath: string; eventTime: string; excerpt: string }>;
}

interface ChunkSource {
  readonly source_path: string;
  readonly header_path: string;
  readonly repo: string | null;
  readonly doc_type: string;
  readonly event_time: string;
  readonly text: string;
}

const MAX_CHUNKS = 10_000;
const EXCERPT_CHARS = 240;
/** 거의 모든 문서에 있는 제목이라 같은 주제로 보지 않는다 */
const GENERIC_TOPICS = new Set([
  "에피소드", "배경", "개요", "결과", "참고", "참고 자료", "요약", "목차",
  "overview", "background", "context", "summary", "references", "notes", "todo",
]);

export async function buildCompactionReport(
  os: OpenSearchPort,
  config: MemoryConfig,
  team: string,
  now: Date = new Date(),
): Promise<CompactionReport> {
  const cutoff = new Date(now.getTime() - config.hotDays * 24 * 60 * 60 * 1000);
  const chunks = await fetchChunks(os, team);
  return {
    team,
    cutoff: cutoff.toISOString(),
    expired: findExpired(chunks, cutoff),
    conflicts: findConflicts(chunks),
    truncated: chunks.length >= MAX_CHUNKS,
  };
}

async function fetchChunks(os: OpenSearchPort, team: string): Promise<ChunkSource[]> {
  try {
    const out = await os.request<{ hits: { hits: Array<{ _source: ChunkSource }> } }>(
      "POST",
      `/${teamIndexPattern(team)}/_search`,
      {
        size: MAX_CHUNKS,
        _source: ["source_path", "header_path", "repo", "doc_type", "event_time", "text"],
        sort: [{ event_time: "desc" }],
        query: { term: { team } },
      },
      { ignore_unavailable: "true", allow_no_indices: "true" },
    );
    return out.hits.hits.map((h) => h._source);
  } catch (error) {
    if (isStatus(error, 404)) return [];
    throw error;
  }
}

export function findExpired(chunks: readonly ChunkSource[], cutoff: Date): ExpiredDoc[] {
  const newestBySource = new Map<string, ChunkSource>();
  for (const chunk of chunks) {
    const prev = newestBySource.get(chunk.source_path);
    if (!prev || prev.event_time < chunk.event_time) newestBySource.set(chunk.source_path, chunk);
  }
  return [...newestBySource.values()]
    .filter((c) => Date.parse(c.event_time) < cutoff.getTime())
    .map((c) => ({ sourcePath: c.source_path, repo: c.repo, docType: c.doc_type, eventTime: c.event_time }))
    .sort((a, b) => a.eventTime.localeCompare(b.eventTime));
}

/** 같은 repo 안에서 같은 주제(헤더 경로의 마지막 제목)를 서로 다른 문서가 다루면 후보로 낸다. */
export function findConflicts(chunks: readonly ChunkSource[]): ConflictCandidate[] {
  const groups = new Map<string, { repo: string | null; topic: string; chunks: ChunkSource[] }>();
  for (const chunk of chunks) {
    const topic = topicOf(chunk.header_path);
    if (!topic || GENERIC_TOPICS.has(topic)) continue;
    const key = `${chunk.repo ?? ""}\0${topic}`;
    const group = groups.get(key) ?? { repo: chunk.repo, topic, chunks: [] };
    groups.set(key, { ...group, chunks: [...group.chunks, chunk] });
  }
  return [...groups.values()]
    .filter((g) => new Set(g.chunks.map((c) => c.source_path)).size > 1)
    .map((g) => ({
      repo: g.repo,
      topic: g.topic,
      entries: g.chunks
        .map((c) => ({
          sourcePath: c.source_path,
          headerPath: c.header_path,
          eventTime: c.event_time,
          excerpt: c.text.slice(0, EXCERPT_CHARS),
        }))
        .sort((a, b) => b.eventTime.localeCompare(a.eventTime)),
    }))
    .sort((a, b) => a.topic.localeCompare(b.topic));
}

function topicOf(headerPath: string): string | null {
  const leaf = headerPath.split(" > ").at(-1)?.trim().toLowerCase();
  return leaf && leaf.length > 0 ? leaf : null;
}

export function renderCompactionReport(report: CompactionReport): string {
  const expired = report.expired.map(
    (d) => `- \`${d.sourcePath}\` (${d.docType}${d.repo ? `, ${d.repo}` : ""}, ${d.eventTime.slice(0, 10)})`,
  );
  const conflicts = report.conflicts.flatMap((c) => [
    `### ${c.repo ?? "(repo 없음)"} — ${c.topic}`,
    ...c.entries.map((e) => `- ${e.eventTime.slice(0, 10)} \`${e.sourcePath}\` › ${e.headerPath}\n  > ${e.excerpt.replace(/\s+/g, " ")}`),
    "",
  ]);
  return [
    `# compaction 리포트 — team ${report.team}`,
    "",
    `기준 시각: event_time < ${report.cutoff}`,
    "",
    ...(report.truncated
      ? [`> ⚠️ 청크가 ${MAX_CHUNKS}개를 넘어 최신 ${MAX_CHUNKS}개만 봤습니다. 오래된 문서가 빠졌을 수 있으니 repo별로 나눠 다시 확인하세요.`, ""]
      : []),
    `## 1. 수명 확인 — 경과 문서 ${report.expired.length}건`,
    ...(expired.length > 0 ? expired : ["- 없음"]),
    "",
    `## 2. 충돌 후보 — 같은 repo·같은 주제를 다룬 문서 묶음 ${report.conflicts.length}개`,
    "",
    ...(conflicts.length > 0 ? conflicts : ["- 없음", ""]),
    "## 다음 단계 (에이전트)",
    "3. 분리: 후보마다 정의가 실제로 다른지 원문으로 확인하고, 유효하지 않은 정의는 deprecated 문서로 떼어 낸다.",
    "4. 다시 쓰기: 살아남은 사실을 오늘 날짜 frontmatter로 `docs/knowledge/`(또는 원래 디렉토리)에 다시 쓴다. 추론 없이, 출처 경로를 남긴다.",
    "5. 티어 이동: `pnpm memory tier`(ISM이 못 잡은 늦게 적재된 인덱스 포함)",
  ].join("\n");
}
