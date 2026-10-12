import { readFile } from "node:fs/promises";
import path from "node:path";
import {
  acquireDaemonLock,
  bootstrap,
  createColdStore,
  createDistillCache,
  createDistillQueue,
  createIndexer,
  createJudgeState,
  createLocalClaude,
  createOpenSearchPort,
  judgeGate,
  missingDistillJobs,
  reconcileOnce,
  releaseDaemonLock,
  splitFrontmatter,
  writeStatus,
  type AskLlm,
  type CycleRecord,
  type JudgeSummary,
  type MemoryConfig,
  type OpenSearchPort,
} from "@meta/memory-core";
import { createSerialRunner } from "./serial.js";

export interface DaemonDeps {
  readonly config: MemoryConfig;
  readonly os?: OpenSearchPort;
  readonly ask?: AskLlm;
  readonly log?: (line: string) => void;
  /** 부팅 bootstrap 재시도 간격. 마지막 값으로 계속 기다린다 */
  readonly retryDelaysMs?: readonly number[];
}

export interface Daemon {
  /** 부팅: 락 → bootstrap(재시도) → boot 사이클 → 증류 백필 */
  boot(options: { once: boolean }): Promise<void>;
  /** 트리거(boot/timer/watch)가 겹쳐도 사이클은 직렬 1개 + 최대 1회 재실행 */
  trigger(reason: string): Promise<void>;
  /** 증류 큐가 빌 때까지 */
  settle(): Promise<void>;
  heartbeat(): Promise<void>;
  shutdown(): Promise<void>;
  readonly cycles: () => number;
}

export class DaemonAlreadyRunning extends Error {
  constructor(readonly pid: number) {
    super(`데몬이 이미 돌고 있습니다 (pid ${pid}). 파생물의 writer 는 하나여야 합니다 — 'make daemon-status' 로 확인하세요`);
    this.name = "DaemonAlreadyRunning";
  }
}

const DEFAULT_RETRY_MS = [5_000, 15_000, 60_000];

/**
 * reconcile 데몬 — 모든 파생물(인덱스·판정 플래그·증류 반영)의 유일한 writer.
 * 매 사이클이 곧 풀 싱크라 증분/풀 구분도, 첫 기동 특수 분기도 없다. 죽어 있던 사이의 변경은 다음 boot 가 흡수한다.
 */
export function createDaemon(deps: DaemonDeps): Daemon {
  const { config } = deps;
  const root = config.metaRepoRoot;
  const log = deps.log ?? ((line: string) => console.log(line));
  const os = deps.os ?? createOpenSearchPort(config.opensearchUrl);
  const ask = deps.ask ?? createLocalClaude(config.llm, log);
  const judgeState = createJudgeState(root);
  const cache = createDistillCache(root);
  const indexer = createIndexer({
    os,
    coldStore: createColdStore(config),
    config,
    // 증류 캐시 hit 는 재색인 때 바로 붙는다 (LLM 0회). superseded_by 는 사이클 끝의 수렴 단계가 맞춘다
    lookups: { distilled: (team, sha) => cache.get(team, sha), supersededBy: () => null },
  });
  const distill = createDistillQueue({ os, config, cache, ask, log });
  const startedAt = new Date().toISOString();
  let cycles = 0;
  let lastCycle: CycleRecord | null = null;
  let locked = false;

  const heartbeat = () =>
    writeStatus(root, {
      pid: process.pid,
      startedAt,
      heartbeatAt: new Date().toISOString(),
      intervalSeconds: config.reconcile.intervalSeconds,
      cycles,
      lastCycle,
      distillPending: distill.pending(),
    });

  async function judge(diff: Parameters<typeof judgeGate>[1]): Promise<JudgeSummary | null> {
    try {
      const summary = await judgeGate({ os, config, judgeState, ask, log }, diff);
      if (summary.candidates > 0) {
        log(
          `[judge] candidates=${summary.candidates} latestWins=${summary.latestWins} llm=${summary.llmJudged} ` +
            `questions=${summary.questions} idempotentSkip=${summary.skippedIdempotent}`,
        );
      }
      return summary;
    } catch (error) {
      log(`[judge] 게이트 실패 — 사이클 계속: ${message(error)}`);
      return null;
    }
  }

  async function cycle(trigger: string): Promise<void> {
    const at = new Date().toISOString();
    try {
      // A. hot 동기화 — 변경분(diff)이 나온다. 끝에서 superseded_by 를 원장대로 수렴시킨다
      const { summary, diff } = await reconcileOnce({ os, config, indexer, judgeState, log });
      log(
        `[reconcile] scanned=${summary.scanned} indexed=${summary.indexed} stat_updated=${summary.statUpdated} ` +
          `removed=${summary.removed} skipped=${summary.skipped} flags=${summary.flagsConverged} trigger=${trigger} took=${summary.tookMs}ms`,
      );
      // D. 판정 게이트 — 실패해도 색인 사이클은 산다
      const judged = await judge(diff);
      if (judged && judged.latestWins + judged.llmJudged > 0) {
        // 방금 내린 판정을 같은 사이클 안에서 인덱스에 반영한다 (변경분이 없으니 stat-walk 비용뿐)
        await reconcileOnce({ os, config, indexer, judgeState, log });
      }
      // E. 증류 큐 — 동기 반환, 소비는 백그라운드
      distill.enqueue(diff);
      lastCycle = { trigger, at, summary, judge: judged, error: null };
    } catch (error) {
      log(`[reconcile] failed: ${message(error)}`);
      lastCycle = { trigger, at, summary: null, judge: null, error: message(error) };
    } finally {
      cycles++;
      await heartbeat().catch((error: unknown) => log(`[daemon] 상태 기록 실패: ${message(error)}`));
    }
  }

  const runner = createSerialRunner(cycle);

  async function bootstrapWithRetry(once: boolean): Promise<void> {
    const delays = deps.retryDelaysMs ?? DEFAULT_RETRY_MS;
    for (let attempt = 0; ; attempt++) {
      try {
        await bootstrap(os, config);
        return;
      } catch (error) {
        // --once 는 무한 대기하지 않는다
        if (once && attempt >= delays.length - 1) throw error;
        const wait = delays[Math.min(attempt, delays.length - 1)] ?? 5_000;
        log(`[daemon] OpenSearch 준비 실패 (attempt ${attempt + 1}): ${message(error)} — ${wait / 1000}s 후 재시도`);
        await new Promise((resolve) => setTimeout(resolve, wait));
      }
    }
  }

  async function readBody(sourcePath: string): Promise<{ title: string; body: string } | null> {
    try {
      const { body } = splitFrontmatter(await readFile(path.join(root, sourcePath), "utf8"));
      return { title: /^#\s+(.+)$/m.exec(body)?.[1]?.trim() ?? path.basename(sourcePath), body };
    } catch {
      return null;
    }
  }

  return {
    async boot({ once }) {
      const lock = await acquireDaemonLock(root);
      if (!lock.acquired) throw new DaemonAlreadyRunning(lock.pid);
      locked = true;
      await cache.load();
      await bootstrapWithRetry(once);
      await runner.run("boot");
      // 증류 공백 백필 — 지난 실패나 데몬 정지 중 변경으로 summary 가 빈 문서
      if (config.distill.enabled) distill.enqueueJobs(await missingDistillJobs(os, readBody));
    },
    trigger: (reason) => runner.run(reason),
    settle: () => distill.settle(),
    heartbeat,
    async shutdown() {
      await runner.idle();
      await distill.settle();
      if (locked) await releaseDaemonLock(root);
      locked = false;
    },
    cycles: () => cycles,
  };
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
