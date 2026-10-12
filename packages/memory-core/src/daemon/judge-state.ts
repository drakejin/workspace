import path from "node:path";
import { jsonlLog, type JsonlLog } from "./jsonl-log.js";

export type JudgeDecision = "superseded" | "keep" | "question";

/** 판정 한 건. 같은 pairKey 의 마지막 기록이 현재 결론이다 (질문 → 답이 뒤에 붙는다) */
export interface JudgeRecord {
  readonly pairKey: string;
  readonly decision: JudgeDecision;
  readonly old: { readonly path: string; readonly bodySha: string; readonly eventTime: string };
  readonly new: { readonly path: string; readonly bodySha: string; readonly eventTime: string };
  readonly reason: string;
  readonly by: "latest-wins" | "llm" | "human" | "fallback";
  readonly at: string;
}

/** 방향 무관 — 같은 내용 쌍이면 같은 키. 본문(frontmatter 제외) sha 라 판정 기록이 재판정을 부르지 않는다 */
export function pairKeyOf(bodyShaA: string, bodyShaB: string): string {
  return [bodyShaA.slice(0, 12), bodyShaB.slice(0, 12)].sort().join(":");
}

export interface JudgeState {
  readonly log: JsonlLog<JudgeRecord>;
  /** pairKey → 마지막 기록 */
  latest(team: string): Promise<Map<string, JudgeRecord>>;
  /** 현재 유효한 superseded 결론: 구판 경로 → 신판 경로. 구판 본문이 바뀌었으면 결론은 무효(재평가) */
  supersededMap(team: string, currentBodySha: (path: string) => string | undefined): Promise<Map<string, string>>;
  pendingQuestions(team: string): Promise<JudgeRecord[]>;
}

export function createJudgeState(root: string): JudgeState {
  const log = jsonlLog<JudgeRecord>(path.join(root, "judge-state"));
  const latest = async (team: string) => {
    const map = new Map<string, JudgeRecord>();
    for (const record of await log.readAll(team)) map.set(record.pairKey, record);
    return map;
  };
  return {
    log,
    latest,
    async supersededMap(team, currentBodySha) {
      const out = new Map<string, string>();
      for (const r of (await latest(team)).values()) {
        if (r.decision !== "superseded") continue;
        if (currentBodySha(r.old.path) !== r.old.bodySha) continue;
        if (currentBodySha(r.new.path) === undefined) continue;
        out.set(r.old.path, r.new.path);
      }
      return out;
    },
    async pendingQuestions(team) {
      return [...(await latest(team)).values()].filter((r) => r.decision === "question");
    },
  };
}
