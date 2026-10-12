import { mkdtemp } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, test } from "vitest";
import { loadConfig } from "../src/config.js";
import { decide, type LlmVerdict } from "../src/daemon/judge.js";
import { createJudgeState, pairKeyOf } from "../src/daemon/judge-state.js";
import type { AskLlm } from "../src/llm/local-claude.js";

const config = loadConfig({ META_REPO_ROOT: "/meta" });
const side = (p: string, day: string) => ({ path: p, bodySha: `${p}-sha`, eventTime: `2026-10-${day}T00:00:00.000Z`, text: p });
const askReturning = (v: LlmVerdict | null): AskLlm => (async () => v) as AskLlm;
const deps = (ask: AskLlm) => ({ os: {} as never, config, judgeState: {} as never, ask, log: () => {}, now: () => new Date("2026-10-13T00:00:00Z") });

describe("decide", () => {
  test("유효 시각 차가 window 보다 크면 LLM 없이 latest-wins", async () => {
    let called = 0;
    const ask = (async () => { called++; return null; }) as AskLlm;
    const r = await decide(deps(ask), "k", side("old.md", "01"), side("new.md", "12"));
    expect(r).toMatchObject({ decision: "superseded", by: "latest-wins", old: { path: "old.md" }, new: { path: "new.md" } });
    expect(called).toBe(0);
  });

  test.each([
    [null, "question", "fallback", "old.md"],
    [{ contradicts: false, winner: "new", confident: true }, "keep", "llm", "old.md"],
    [{ contradicts: true, winner: "new", confident: false }, "question", "llm", "old.md"],
    [{ contradicts: true, winner: "new", confident: true }, "superseded", "llm", "old.md"],
    [{ contradicts: true, winner: "old", confident: true }, "superseded", "llm", "new.md"],
  ] as const)("window 안: verdict %j → %s (%s), 밀리는 쪽 %s", async (verdict, decision, by, loser) => {
    const r = await decide(deps(askReturning(verdict as LlmVerdict | null)), "k", side("old.md", "10"), side("new.md", "12"));
    expect(r).toMatchObject({ decision, by, old: { path: loser } });
  });

  test("스키마에 안 맞는 LLM 응답은 질문으로", async () => {
    const r = await decide(deps(askReturning({ contradicts: "yes" } as never)), "k", side("a.md", "10"), side("b.md", "11"));
    expect(r.decision).toBe("question");
  });
});

describe("judge-state", () => {
  test("pairKey 는 방향 무관", () => {
    expect(pairKeyOf("aaaa", "bbbb")).toBe(pairKeyOf("bbbb", "aaaa"));
  });

  test("마지막 기록이 결론, 구판 본문이 바뀌면 결론 무효, 질문은 답이 붙으면 사라진다", async () => {
    const state = createJudgeState(await mkdtemp(path.join(os.tmpdir(), "judge-")));
    const old = { path: "a.md", bodySha: "sha-a", eventTime: "2026-10-01" };
    const neu = { path: "b.md", bodySha: "sha-b", eventTime: "2026-10-02" };
    await state.log.append("t", { pairKey: "k1", decision: "question", old, new: neu, reason: "?", by: "fallback", at: "1" });
    expect(await state.pendingQuestions("t")).toHaveLength(1);
    await state.log.append("t", { pairKey: "k1", decision: "superseded", old, new: neu, reason: "사람", by: "human", at: "2" });
    expect(await state.pendingQuestions("t")).toHaveLength(0);

    const shas: Record<string, string> = { "a.md": "sha-a", "b.md": "sha-b" };
    expect([...(await state.supersededMap("t", (p) => shas[p]))]).toEqual([["a.md", "b.md"]]);
    expect([...(await state.supersededMap("t", (p) => (p === "a.md" ? "edited" : shas[p])))]).toEqual([]);
    expect([...(await state.supersededMap("t", (p) => (p === "b.md" ? undefined : shas[p])))]).toEqual([]);
    expect(await state.log.teams()).toEqual(["t"]);
  });
});
