#!/usr/bin/env node
import path from "node:path";
import { watch } from "chokidar";
import { isInScope, loadConfig } from "@meta/memory-core";
import { createDaemon, DaemonAlreadyRunning } from "./daemon.js";

/**
 * make daemon       — 상주. boot 1회 + 타이머(intervalSeconds) + 파일 watcher(debounce) 힌트
 * make reconcile    — --once. 사이클 1회 + 증류 큐 비우기 후 종료 (memory-up 의 최초 색인)
 */
async function main(): Promise<void> {
  const once = process.argv.includes("--once");
  const config = loadConfig();
  const daemon = createDaemon({ config });

  try {
    await daemon.boot({ once });
  } catch (error) {
    if (error instanceof DaemonAlreadyRunning && once) {
      // 상주 데몬이 이미 수렴시키고 있다 — 1회 색인은 할 일이 없다
      console.log(`[reconcile] 데몬(pid ${error.pid})이 돌고 있어 1회 색인을 건너뜁니다`);
      return;
    }
    throw error;
  }

  if (once) {
    await daemon.settle();
    await daemon.shutdown();
    return;
  }

  const root = config.metaRepoRoot;
  const rel = (p: string) => path.relative(root, p).split(path.sep).join("/");
  let debounce: NodeJS.Timeout | null = null;
  // 이벤트 내용은 쓰지 않는다 — 사이클을 앞당기는 힌트일 뿐. 색인 범위 밖 트리는 watch 도 열지 않는다
  const watcher = watch(root, {
    ignoreInitial: true,
    ignored: (p, stats) => !isInScope(rel(p), stats ? stats.isFile() : undefined),
  }).on("all", () => {
    if (debounce) clearTimeout(debounce);
    debounce = setTimeout(() => void daemon.trigger("watch"), config.reconcile.debounceMs);
  });
  const timer = setInterval(() => void daemon.trigger("timer"), config.reconcile.intervalSeconds * 1000);

  console.log(
    `[daemon] watching ${root} (docs/, main/*/docs/), interval=${config.reconcile.intervalSeconds}s, ` +
      `debounce=${config.reconcile.debounceMs}ms, judge=${config.judge.enabled ? "on" : "off"}, distill=${config.distill.enabled ? "on" : "off"}`,
  );

  const stop = async (signal: string) => {
    console.log(`[daemon] ${signal} — 진행 중인 사이클을 마치고 종료합니다`);
    clearInterval(timer);
    if (debounce) clearTimeout(debounce);
    await watcher.close();
    await daemon.shutdown();
    process.exit(0);
  };
  process.on("SIGINT", () => void stop("SIGINT"));
  process.on("SIGTERM", () => void stop("SIGTERM"));
}

main().catch((error: unknown) => {
  console.error(`[daemon] 시작 실패: ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
});
