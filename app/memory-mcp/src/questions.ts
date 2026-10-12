import { createJudgeState, type JudgeRecord, type MemoryConfig } from "@meta/memory-core";

export type Answer = "latest" | "old" | "keep";

/** 판정이 사람에게 넘긴 질문 목록 (같은 내용 쌍은 다시 묻지 않는다) */
export async function renderQuestions(config: MemoryConfig): Promise<string> {
  const state = createJudgeState(config.metaRepoRoot);
  const rows: string[] = [];
  for (const team of await state.log.teams()) {
    for (const q of await state.pendingQuestions(team)) {
      rows.push(
        `- [${team}] ${q.pairKey}\n  old: ${q.old.path} (${q.old.eventTime.slice(0, 10)})\n  new: ${q.new.path} (${q.new.eventTime.slice(0, 10)})\n  이유: ${q.reason}`,
      );
    }
  }
  if (rows.length === 0) return "답을 기다리는 질문이 없습니다.";
  return [
    `질문 ${rows.length}건 — 답: pnpm memory answer <pairKey> latest|old|keep`,
    "  latest = 최신을 사실로(구판 숨김) · old = 구판이 사실(신판 숨김) · keep = 둘 다 유지(공존)",
    "",
    ...rows,
  ].join("\n");
}

/** 질문에 답한다. 원장에 사람 판정을 append 하면 다음 데몬 사이클이 인덱스를 맞춘다 */
export async function answerQuestion(config: MemoryConfig, pairKey: string, answer: Answer, now = new Date()): Promise<string> {
  const state = createJudgeState(config.metaRepoRoot);
  for (const team of await state.log.teams()) {
    const q = (await state.latest(team)).get(pairKey);
    if (!q) continue;
    const base = { pairKey, by: "human" as const, at: now.toISOString() };
    const record: JudgeRecord =
      answer === "keep"
        ? { ...base, decision: "keep", old: q.old, new: q.new, reason: "사람: 공존" }
        : answer === "latest"
          ? { ...base, decision: "superseded", old: q.old, new: q.new, reason: "사람: 최신이 사실" }
          : { ...base, decision: "superseded", old: q.new, new: q.old, reason: "사람: 구판이 사실" };
    await state.log.append(team, record);
    return `${pairKey}: ${record.reason} — 다음 데몬 사이클(또는 'make reconcile')이 인덱스에 반영합니다`;
  }
  throw new Error(`질문을 찾지 못했습니다: ${pairKey} ('pnpm memory questions' 로 확인)`);
}
