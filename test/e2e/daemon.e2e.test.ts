/**
 * reconcile 데몬 블랙박스 e2e — 실제 OpenSearch(make memory-up)에 붙는다. MEMORY_E2E=1 일 때만 돈다.
 * LLM 은 가짜(ask 주입)라 결정적이다. 실제 claude CLI 경로는 local-claude 단위 테스트가 맡는다.
 */
import { mkdir, mkdtemp, rm, utimes, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import {
  createJudgeState,
  createOpenSearchPort,
  loadConfig,
  readStatus,
  searchMemory,
  type AskInput,
  type AskLlm,
  type MemoryConfig,
} from "../../packages/memory-core/src/index.js";
import { createDaemon, DaemonAlreadyRunning, type Daemon } from "../../app/daemon/src/daemon.js";
import { answerQuestion } from "../../app/memory-mcp/src/questions.js";

const enabled = process.env.MEMORY_E2E === "1";
const TEAM = `d${Date.now().toString(36)}`;
const daysAgo = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString().slice(0, 10);

/** 가짜 LLM — 판정은 큐에 넣은 답, 증류는 고정 요약. 호출 수를 센다 */
function fakeLlm() {
  const calls = { judge: 0, distill: 0 };
  const verdicts: Array<unknown> = [];
  const ask = (async (input: AskInput) => {
    if (input.tag === "judge") {
      calls.judge++;
      return verdicts.shift() ?? null;
    }
    calls.distill++;
    const title = /title: (.+)/.exec(input.user)?.[1] ?? "?";
    return { summary: `요약: ${title}`, entities: ["concept:demo"] };
  }) as AskLlm;
  return { ask, calls, verdicts };
}

describe.skipIf(!enabled)("reconcile 데몬 e2e (real OpenSearch)", () => {
  let root: string;
  let config: MemoryConfig;
  let daemon: Daemon;
  const llm = fakeLlm();
  const logs: string[] = [];
  const os_ = () => createOpenSearchPort(config.opensearchUrl);
  const write = async (rel: string, body: string) => {
    await mkdir(path.dirname(path.join(root, rel)), { recursive: true });
    await writeFile(path.join(root, rel), body);
  };
  const search = (query: string, includeSuperseded = false) =>
    searchMemory(os_(), { team: TEAM, query, includeSuperseded });

  beforeAll(async () => {
    root = await mkdtemp(path.join(os.tmpdir(), "daemon-e2e-"));
    config = loadConfig({
      ...process.env,
      META_REPO_ROOT: root,
      MEMORY_DEFAULT_TEAM: TEAM,
      MEMORY_DISTILL_ENABLED: "true",
      MEMORY_JUDGE_ENABLED: "true",
    });
    daemon = createDaemon({ config, ask: llm.ask, log: (l) => logs.push(l), retryDelaysMs: [100] });
    await write("docs/specs/orders/2026-08-retry.md", `---\ndate: ${daysAgo(40)}\n---\n# 주문 재시도 정책\n## 결정\nOrderRetryPolicy 는 결제 실패 시 3회 재시도한다. 간격은 1분이다.\n`);
    await write("main/shop-api/docs/adr/0001-idempotency.md", `---\ndate: ${daysAgo(3)}\n---\n# ADR-0001 멱등 키\nIdempotencyKeyHeader 를 모든 결제 요청에 붙인다.\n`);
    await write("main/shop-api/src/ignored.md", "# 코드 트리는 색인하지 않는다\nCodeTreeToken");
    await daemon.boot({ once: false });
  }, 60_000);

  afterAll(async () => {
    await daemon?.shutdown();
    await os_().request("DELETE", `/mem-${TEAM}-*`).catch(() => {});
  });

  test("boot 사이클: docs/ 와 main/{repo}/docs/ 를 색인하고 코드 트리는 건너뛴다", async () => {
    expect((await search("OrderRetryPolicy"))[0]?.source_path).toBe("docs/specs/orders/2026-08-retry.md");
    expect((await search("IdempotencyKeyHeader"))[0]?.repo).toBe("shop-api");
    expect(await search("CodeTreeToken")).toEqual([]);
    const status = await readStatus(root);
    expect(status).toMatchObject({ pid: process.pid, cycles: 1, lastCycle: { trigger: "boot", summary: { scanned: 2, indexed: 2 } } });
  });

  test("변경이 없으면 아무것도 다시 색인하지 않는다 (stat 게이트), mtime 만 바뀌면 stat 만 갱신", async () => {
    await daemon.trigger("timer");
    expect((await readStatus(root))?.lastCycle?.summary).toMatchObject({ indexed: 0, statUpdated: 0 });
    const future = new Date(Date.now() + 60_000);
    await utimes(path.join(root, "docs/specs/orders/2026-08-retry.md"), future, future);
    await daemon.trigger("watch");
    const last = (await readStatus(root))?.lastCycle;
    expect(last?.error ?? null).toBeNull();
    expect(last?.summary).toMatchObject({ indexed: 0, statUpdated: 1 });
  });

  test("판정 1단 latest-wins: 유효 시각이 7일 넘게 차이 나는 비슷한 episodic 문서는 최신이 이긴다 (LLM 0회)", async () => {
    await write("docs/specs/orders/2026-10-retry.md", `---\ndate: ${daysAgo(1)}\n---\n# 주문 재시도 정책\n## 결정\nOrderRetryPolicy 는 결제 실패 시 5회 재시도한다. 간격은 지수 백오프다.\n`);
    const before = llm.calls.judge;
    await daemon.trigger("watch");
    const visible = await search("OrderRetryPolicy");
    expect(visible.map((h) => h.source_path)).toEqual(["docs/specs/orders/2026-10-retry.md"]);
    const all = await search("OrderRetryPolicy", true);
    expect(all.find((h) => h.source_path === "docs/specs/orders/2026-08-retry.md")?.superseded_by).toBe("docs/specs/orders/2026-10-retry.md");
    expect(llm.calls.judge).toBe(before);
    expect(logs.some((l) => l.includes("latest-wins"))).toBe(true);
  });

  test("판정 3단 질문: 7일 이내 + LLM 불가면 질문으로 남기고, 사람이 답하면 다음 사이클이 반영한다", async () => {
    await write("docs/specs/payments/a.md", `---\ndate: ${daysAgo(3)}\n---\n# 환불 기한\nRefundWindowDays 는 7일이다. 환불 요청은 고객센터로 받는다.\n`);
    await daemon.trigger("watch");
    await write("docs/specs/payments/b.md", `---\ndate: ${daysAgo(2)}\n---\n# 환불 기한\nRefundWindowDays 는 14일이다. 환불 요청은 고객센터로 받는다.\n`);
    llm.verdicts.push(null);
    await daemon.trigger("watch");
    const questions = await createJudgeState(root).pendingQuestions(TEAM);
    expect(questions).toHaveLength(1);
    expect((await search("RefundWindowDays")).map((h) => h.source_path).sort()).toEqual(["docs/specs/payments/a.md", "docs/specs/payments/b.md"]);

    await answerQuestion(config, questions[0]!.pairKey, "latest");
    await daemon.trigger("timer");
    expect((await search("RefundWindowDays")).map((h) => h.source_path)).toEqual(["docs/specs/payments/b.md"]);
    expect(await createJudgeState(root).pendingQuestions(TEAM)).toEqual([]);
  });

  test("판정 2단 LLM: 모순이 아니면 공존, 같은 내용 쌍은 다시 묻지 않는다", async () => {
    await write("docs/specs/shipping/a.md", `---\ndate: ${daysAgo(2)}\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 둔다.\n`);
    await daemon.trigger("watch");
    await write("docs/specs/shipping/b.md", `---\ndate: ${daysAgo(1)}\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 두고 제주를 3구역으로 둔다.\n`);
    llm.verdicts.push({ contradicts: false, winner: "new", confident: true });
    const before = llm.calls.judge;
    await daemon.trigger("watch");
    expect(llm.calls.judge).toBe(before + 1);
    expect((await search("ShippingZoneTable")).map((h) => h.source_path).sort()).toEqual(["docs/specs/shipping/a.md", "docs/specs/shipping/b.md"]);
    // frontmatter 만 바꾸면 본문 sha 가 같아 재판정하지 않는다
    await write("docs/specs/shipping/b.md", `---\ndate: ${daysAgo(1)}\ntags: [x]\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 두고 제주를 3구역으로 둔다.\n`);
    await daemon.trigger("watch");
    expect(llm.calls.judge).toBe(before + 1);
  });

  test("knowledge 는 유사도로 판정하지 않고 supersedes 선언만 따른다", async () => {
    await write("docs/knowledge/order-status.md", `---\ndoc_type: knowledge\n---\n# 주문 상태\nOrderStatusEnum 은 CREATED, PAID 두 가지다.\n`);
    await daemon.trigger("watch");
    await write("docs/knowledge/order-status-v2.md", `---\ndoc_type: knowledge\nsupersedes: docs/knowledge/order-status.md\n---\n# 주문 상태\nOrderStatusEnum 은 CREATED, PAID, SHIPPED 세 가지다.\n`);
    await daemon.trigger("watch");
    expect((await search("OrderStatusEnum")).map((h) => h.source_path)).toEqual(["docs/knowledge/order-status-v2.md"]);
  });

  test("증류: 변경분은 요약이 붙고, 캐시 hit 는 LLM 을 부르지 않는다", async () => {
    await daemon.settle();
    const hit = (await search("IdempotencyKeyHeader"))[0];
    expect(hit?.summary).toBe("요약: ADR-0001 멱등 키");
    expect(logs.some((l) => l.startsWith("[distill]"))).toBe(true);
  });

  test("재구축 계약: 인덱스를 지워도 다음 사이클이 같은 결과로 되살린다 — 판정·증류는 원장에서, LLM 0회", async () => {
    await daemon.settle();
    const before = { ...llm.calls };
    const snapshot = async () =>
      (await search("OrderRetryPolicy RefundWindowDays OrderStatusEnum IdempotencyKeyHeader ShippingZoneTable", true))
        .map((h) => `${h.source_path}|${h.superseded_by ?? "-"}|${h.summary ?? "-"}`)
        .sort();
    const expected = await snapshot();
    await os_().request("DELETE", `/mem-${TEAM}-*`);
    await daemon.trigger("timer");
    await daemon.settle();
    expect(await snapshot()).toEqual(expected);
    expect(llm.calls).toEqual(before);
  });

  test("파일을 지우면 다음 사이클이 색인에서 뺀다 (tombstone)", async () => {
    await rm(path.join(root, "main/shop-api/docs/adr/0001-idempotency.md"));
    await daemon.trigger("watch");
    expect(await search("IdempotencyKeyHeader")).toEqual([]);
  });

  test("writer 는 하나 — 다른 프로세스의 1회 색인은 데몬이 떠 있으면 건너뛴다", async () => {
    const { execFile } = await import("node:child_process");
    const out = await new Promise<string>((resolve, reject) =>
      execFile(
        "npx",
        ["tsx", "app/daemon/src/main.ts", "--once"],
        { cwd: path.resolve(__dirname, "../.."), env: { ...process.env, META_REPO_ROOT: root, MEMORY_DEFAULT_TEAM: TEAM }, timeout: 60_000 },
        (error, stdout, stderr) => (error ? reject(new Error(stderr || error.message)) : resolve(stdout)),
      ),
    );
    expect(out).toContain(`데몬(pid ${process.pid})이 돌고 있어 1회 색인을 건너뜁니다`);
  }, 90_000);
});
