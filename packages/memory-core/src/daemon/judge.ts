import { readFile } from "node:fs/promises";
import path from "node:path";
import type { MemoryConfig } from "../config.js";
import { splitFrontmatter } from "../markdown/frontmatter.js";
import { memoryKindOf } from "../document/metadata.js";
import type { AskLlm } from "../llm/local-claude.js";
import { hotAlias } from "../naming.js";
import { isStatus, type OpenSearchPort } from "../opensearch/port.js";
import { pairKeyOf, type JudgeRecord, type JudgeState } from "./judge-state.js";
import type { DiffEntry } from "./reconcile.js";

export interface JudgeSummary {
  readonly candidates: number;
  readonly latestWins: number;
  readonly llmJudged: number;
  readonly questions: number;
  readonly skippedIdempotent: number;
}

export interface Neighbor {
  readonly sourcePath: string;
  readonly bodySha: string;
  readonly eventTime: string;
  readonly text: string;
  /** 자기 자신 점수 대비 비율 (0~1). BM25 점수는 문서마다 스케일이 달라 정규화한다 */
  readonly relativeScore: number;
}

export interface LlmVerdict {
  readonly contradicts: boolean;
  readonly winner: "old" | "new";
  readonly confident: boolean;
}

const LIKE_CHARS = 6000;
const JUDGE_SYSTEM =
  "두 팀 문서가 같은 주제를 다룬다. contradicts=두 문서가 모순되는 주장인가. winner=어느 쪽이 현재 사실인가. confident=확신하는가(애매하면 false).";
const JUDGE_SCHEMA = {
  type: "object",
  properties: {
    contradicts: { type: "boolean" },
    winner: { type: "string", enum: ["old", "new"] },
    confident: { type: "boolean" },
  },
  required: ["contradicts", "winner", "confident"],
  additionalProperties: false,
};

export interface JudgeDeps {
  readonly os: OpenSearchPort;
  readonly config: MemoryConfig;
  readonly judgeState: JudgeState;
  readonly ask: AskLlm;
  readonly log: (line: string) => void;
  readonly now?: () => Date;
}

/**
 * 판정 게이트 (사이클 D). 같은 팀 안에서 episodic 끼리만 유사도로 판정한다.
 * knowledge 는 유사도로 판정하지 않는다 — 개정판의 supersedes 선언만 믿는다(오판정 0).
 * 1단 latest-wins(유효 시각 차 > windowDays) → 2단 로컬 claude → 3단 질문(저확신·LLM 불가).
 */
export async function judgeGate(deps: JudgeDeps, diff: readonly DiffEntry[]): Promise<JudgeSummary> {
  const sum = { candidates: 0, latestWins: 0, llmJudged: 0, questions: 0, skippedIdempotent: 0 };
  if (!deps.config.judge.enabled) return sum;
  const seen = new Set<string>();

  for (const d of diff) {
    if (d.change === "removed" || d.supersedes || memoryKindOf(d.docType) !== "episodic") continue;
    const neighbor = await bestNeighbor(deps.os, d, deps.config.judge.minScore);
    if (!neighbor) continue;
    sum.candidates++;
    const pairKey = pairKeyOf(d.bodySha, neighbor.bodySha);
    if (seen.has(pairKey) || (await deps.judgeState.latest(d.team)).has(pairKey)) {
      sum.skippedIdempotent++;
      continue;
    }
    seen.add(pairKey);

    const mine = { path: d.sourcePath, bodySha: d.bodySha, eventTime: d.eventTime, text: d.body };
    const theirsText = await readBody(deps.config.metaRepoRoot, neighbor.sourcePath);
    const theirs = { path: neighbor.sourcePath, bodySha: neighbor.bodySha, eventTime: neighbor.eventTime, text: theirsText ?? neighbor.text };
    const [older, newer] = Date.parse(mine.eventTime) <= Date.parse(theirs.eventTime) ? [mine, theirs] : [theirs, mine];
    const record = await decide(deps, pairKey, older, newer);
    await deps.judgeState.log.append(d.team, record);
    deps.log(`[judge] ${record.decision} ${older.path} ↔ ${newer.path} (${record.by}: ${record.reason})`);
    if (record.by === "latest-wins") sum.latestWins++;
    else if (record.decision === "question") sum.questions++;
    else sum.llmJudged++;
  }
  return sum;
}

/** 판정은 청크가 아니라 원문 전체로 한다. 검색은 경로를 돌려주고 본문은 원문을 읽는다 */
async function readBody(root: string, sourcePath: string): Promise<string | null> {
  try {
    return splitFrontmatter(await readFile(path.join(root, sourcePath), "utf8")).body;
  } catch {
    return null;
  }
}

interface Side {
  readonly path: string;
  readonly bodySha: string;
  readonly eventTime: string;
  readonly text: string;
}

export async function decide(deps: JudgeDeps, pairKey: string, older: Side, newer: Side): Promise<JudgeRecord> {
  const at = (deps.now?.() ?? new Date()).toISOString();
  const ref = (s: Side) => ({ path: s.path, bodySha: s.bodySha, eventTime: s.eventTime });
  const gapDays = (Date.parse(newer.eventTime) - Date.parse(older.eventTime)) / 86_400_000;
  const base = { pairKey, old: ref(older), new: ref(newer), at };

  if (gapDays > deps.config.judge.windowDays) {
    return { ...base, decision: "superseded", by: "latest-wins", reason: `유효 시각 차 ${gapDays.toFixed(1)}일 > ${deps.config.judge.windowDays}일` };
  }
  const verdict = await deps.ask<LlmVerdict>({
    system: JUDGE_SYSTEM,
    user: `--- old (${older.path}) ---\n${older.text.slice(0, 30_000)}\n\n--- new (${newer.path}) ---\n${newer.text.slice(0, 30_000)}`,
    schema: JUDGE_SCHEMA,
    tag: "judge",
  });
  if (!isVerdict(verdict)) {
    return { ...base, decision: "question", by: "fallback", reason: "LLM 판정 불가 — 사람이 답한다" };
  }
  if (!verdict.contradicts) return { ...base, decision: "keep", by: "llm", reason: "모순 아님 — 공존" };
  if (!verdict.confident) return { ...base, decision: "question", by: "llm", reason: `모순, 저확신(winner=${verdict.winner})` };
  // winner=old 면 새 문서가 틀렸다는 뜻 — 방향을 뒤집어 기록한다
  return verdict.winner === "new"
    ? { ...base, decision: "superseded", by: "llm", reason: "모순, 고확신 — 신판이 사실" }
    : { ...base, old: ref(newer), new: ref(older), decision: "superseded", by: "llm", reason: "모순, 고확신 — 구판이 사실" };
}

function isVerdict(v: unknown): v is LlmVerdict {
  const o = v as Partial<LlmVerdict> | null;
  return !!o && typeof o.contradicts === "boolean" && typeof o.confident === "boolean" && (o.winner === "old" || o.winner === "new");
}

interface MltHit {
  _score: number;
  _source: { source_path: string; body_sha?: string; event_time: string; doc_type: string; text: string; superseded_by?: string | null };
}

/** 같은 팀 hot 영역에서 가장 비슷한 다른 episodic 문서. 점수는 자기 자신과의 점수로 나눠 0~1 로 맞춘다 */
export async function bestNeighbor(os: OpenSearchPort, d: DiffEntry, minScore: number): Promise<Neighbor | null> {
  let hits: MltHit[];
  try {
    const out = await os.request<{ hits: { hits: MltHit[] } }>("POST", `/${hotAlias(d.team)}/_search`, {
      size: 30,
      query: {
        bool: {
          must: [{ more_like_this: { fields: ["text", "title"], like: d.body.slice(0, LIKE_CHARS), min_term_freq: 1, min_doc_freq: 1, max_query_terms: 40 } }],
          must_not: [{ terms: { doc_type: ["knowledge", "guide"] } }, { exists: { field: "superseded_by" } }],
        },
      },
      _source: ["source_path", "body_sha", "event_time", "doc_type", "text"],
    });
    hits = out.hits.hits;
  } catch (error) {
    if (isStatus(error, 404)) return null;
    throw error;
  }
  const self = Math.max(0, ...hits.filter((h) => h._source.source_path === d.sourcePath).map((h) => h._score));
  if (self === 0) return null;
  const best = new Map<string, MltHit>();
  for (const h of hits) {
    if (h._source.source_path === d.sourcePath || !h._source.body_sha) continue;
    const prev = best.get(h._source.source_path);
    if (!prev || prev._score < h._score) best.set(h._source.source_path, h);
  }
  const top = [...best.values()].sort((a, b) => b._score - a._score)[0];
  if (!top || top._score / self < minScore) return null;
  return {
    sourcePath: top._source.source_path,
    bodySha: top._source.body_sha ?? "",
    eventTime: top._source.event_time,
    text: top._source.text,
    relativeScore: top._score / self,
  };
}
