import { execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  createDistillCache,
  createJudgeState,
  INDEX_PREFIX,
  readStatus,
  runningDaemonPid,
  type MemoryConfig,
  type OpenSearchPort,
} from "@meta/memory-core";

const run = promisify(execFile);

export interface StatusProbe {
  dockerState(): Promise<string | null>;
  clusterHealth(): Promise<string | null>;
  indexStats(): Promise<{ indices: number; documents: number; chunks: number; lastIndexed: string | null } | null>;
}

/** 실제 환경을 읽는 probe. 테스트는 가짜로 바꿔 끼운다 */
export function liveProbe(config: MemoryConfig, os: OpenSearchPort): StatusProbe {
  return {
    async dockerState() {
      try {
        const { stdout } = await run("docker", ["compose", "ps", "--format", "{{.Service}} {{.State}}"], {
          cwd: config.metaRepoRoot,
          timeout: 10_000,
        });
        return stdout.split("\n").find((l) => l.startsWith("opensearch "))?.split(" ")[1] ?? "not started";
      } catch {
        return null;
      }
    },
    async clusterHealth() {
      try {
        return (await os.request<{ status: string }>("GET", "/_cluster/health")).status;
      } catch {
        return null;
      }
    },
    async indexStats() {
      try {
        const lenient = { ignore_unavailable: "true", allow_no_indices: "true" };
        const indices = await os.request<Array<{ index: string }>>("GET", `/_cat/indices/${INDEX_PREFIX}*`, undefined, {
          format: "json",
          h: "index",
        });
        const docs = await os.request<{ count: number }>("POST", `/${INDEX_PREFIX}*/_count`, { query: { term: { chunk_index: 0 } } }, lenient);
        const chunks = await os.request<{ count: number }>("POST", `/${INDEX_PREFIX}*/_count`, { query: { match_all: {} } }, lenient);
        const last = await os.request<{ aggregations?: { last: { value_as_string?: string } } }>(
          "POST",
          `/${INDEX_PREFIX}*/_search`,
          { size: 0, aggs: { last: { max: { field: "ingested_at" } } } },
          lenient,
        );
        return {
          indices: indices.length,
          documents: docs.count,
          chunks: chunks.count,
          lastIndexed: last.aggregations?.last.value_as_string ?? null,
        };
      } catch {
        return null;
      }
    },
  };
}

/** 데몬 상태 — 컨테이너·OpenSearch·색인·데몬·판정 질문·증류를 한 화면에. 읽기만 한다. 꺼진 것은 할 일로 안내한다 */
export async function renderStatus(config: MemoryConfig, probe: StatusProbe, now: Date = new Date()): Promise<string> {
  const lines: string[] = [];
  const hints: string[] = [];
  const root = config.metaRepoRoot;

  const docker = await probe.dockerState();
  if (docker === null) {
    lines.push("[status] docker: not running");
    hints.push("Docker(OrbStack) 실행 후 'make memory-up'");
  } else {
    lines.push(`[status] container opensearch: ${docker}`);
    if (docker !== "running") hints.push("'make memory-up' 으로 스택 기동");
  }

  const health = await probe.clusterHealth();
  if (health === null) {
    lines.push(`[status] opensearch: unreachable (${config.opensearchUrl})`);
    hints.push("'make memory-up' 으로 스택 기동");
  } else {
    lines.push(`[status] opensearch: ${health} (${config.opensearchUrl})`);
    const stats = await probe.indexStats();
    if (stats) {
      lines.push(`[status] index: ${stats.indices} indices, ${stats.documents} docs, ${stats.chunks} chunks`);
      if (stats.lastIndexed) lines.push(`[status] last indexed: ${stats.lastIndexed}`);
      if (stats.documents === 0) hints.push("'make reconcile' 로 1회 색인 (또는 'make daemon')");
    }
  }

  const pid = await runningDaemonPid(root);
  const status = await readStatus(root);
  if (pid === null) {
    lines.push("[status] daemon: stopped");
    hints.push("'make daemon' 으로 데몬 실행 (터미널 하나를 계속 점유한다)");
  } else {
    lines.push(`[status] daemon: running pid=${pid}${status ? ` since ${status.startedAt}, cycles=${status.cycles}` : ""}`);
    if (status) {
      const ageSec = Math.round((now.getTime() - Date.parse(status.heartbeatAt)) / 1000);
      lines.push(`[status] heartbeat: ${ageSec}s ago (interval ${status.intervalSeconds}s)`);
      if (ageSec > status.intervalSeconds * 3) hints.push("데몬 heartbeat 가 멈췄다 — 데몬 터미널 로그 확인 후 재시작");
    }
  }
  const last = status?.lastCycle;
  if (last) {
    if (last.error) {
      lines.push(`[status] last cycle: ${last.at} trigger=${last.trigger} FAILED — ${last.error}`);
    } else if (last.summary) {
      const s = last.summary;
      lines.push(
        `[status] last cycle: ${last.at} trigger=${last.trigger} scanned=${s.scanned} indexed=${s.indexed} ` +
          `stat_updated=${s.statUpdated} removed=${s.removed} skipped=${s.skipped} took=${s.tookMs}ms${s.refused ? " REFUSED(sanityMaxFiles)" : ""}`,
      );
    }
  }

  const judgeState = createJudgeState(root);
  const teams = await judgeState.log.teams();
  const questions = (await Promise.all(teams.map((t) => judgeState.pendingQuestions(t)))).flat();
  lines.push(`[status] judge: ${config.judge.enabled ? "on" : "off"}, 답을 기다리는 질문 ${questions.length}건`);
  if (questions.length > 0) hints.push("'pnpm memory questions' 로 질문 확인 후 'pnpm memory answer <pairKey> latest|old|keep'");

  const cache = createDistillCache(root);
  await cache.load();
  lines.push(
    `[status] distill: ${config.distill.enabled ? "on" : "off"}, cache ${cache.size()}건` +
      (status ? `, 대기 ${status.distillPending}건` : ""),
  );

  const unique = [...new Set(hints)];
  return [...lines, ...(unique.length > 0 ? ["", ...unique.map((h) => `[status] 할 일: ${h}`)] : [])].join("\n");
}
