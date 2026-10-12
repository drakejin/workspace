#!/usr/bin/env node
import {
  buildCompactionReport,
  listCold,
  reloadFromColdStore,
  renderCompactionReport,
  restoreCold,
  tierExpired,
} from "@meta/memory-core";
import { createContext, type AppContext } from "./context.js";
import { formatIngest, handleSearch } from "./handlers.js";
import { answerQuestion, renderQuestions, type Answer } from "./questions.js";
import { liveProbe, renderStatus } from "./status.js";

const ANSWERS: readonly Answer[] = ["latest", "old", "keep"];
/** OpenSearch 가 꺼져 있어도 돌아야 하는 명령 (bootstrap 을 건너뛴다) */
const OFFLINE_COMMANDS = new Set(["status", "questions", "answer"]);

const USAGE = `사용법: pnpm memory <command>
  bootstrap                         템플릿·ISM 정책·스냅샷 repository 생성/갱신
  ingest <path> [--team T]          파일/디렉토리 적재
  search <query> [--team T] [--repo R] [--include-superseded]   BM25 검색
  compaction-report [--team T]      주간 compaction 1·2단계 리포트 (markdown)
  tier [--dry-run] [--team T]       최신 문서도 hot 기간을 넘긴 인덱스를 스냅샷 후 삭제 (기본: 모든 팀)
  list-cold [--team T]              cold 인덱스 목록
  restore <index>                   cold 인덱스 복원
  reload [--team T]                 cold store의 청크 JSONL을 _bulk로 재적재
  status                            데몬 상태 (make daemon-status)
  questions                         판정이 사람에게 넘긴 질문 목록
  answer <pairKey> latest|old|keep  질문에 답한다`;

type Command = (ctx: AppContext, args: readonly string[], team: string) => Promise<string>;

const commands: Record<string, Command> = {
  bootstrap: async () => "bootstrap 완료",
  ingest: async (ctx, args, team) => {
    const target = required(positional(args)[0], "path");
    const results = await ctx.indexer.ingestPath(target, flag(args, "--team") ? team : undefined);
    return [`${results.length}개 문서 적재`, ...results.map(formatIngest)].join("\n");
  },
  search: async (ctx, args, team) =>
    handleSearch(ctx, {
      team,
      query: required(positional(args).join(" "), "query"),
      repo: flag(args, "--repo"),
      include_superseded: args.includes("--include-superseded"),
    }),
  "compaction-report": async (ctx, _args, team) =>
    renderCompactionReport(await buildCompactionReport(ctx.os, ctx.config, team)),
  tier: async (ctx, args) => {
    const decisions = await tierExpired(ctx.os, ctx.config, { dryRun: args.includes("--dry-run"), team: flag(args, "--team") });
    return decisions.map((d) => `${d.action.padEnd(16)} ${d.index}  newest=${d.newestEventTime ?? "-"}`).join("\n") || "인덱스 없음";
  },
  "list-cold": async (ctx, _args, team) =>
    (await listCold(ctx.os, ctx.config, team))
      .map((c) => `${c.index}  ${c.snapshot}  ${c.snapshotTime}${c.restored ? "  [restored]" : ""}`)
      .join("\n") || "cold 인덱스 없음",
  restore: async (ctx, args) => {
    const restored = await restoreCold(ctx.os, ctx.config, required(positional(args)[0], "index"));
    return `restored ${restored.index} from ${restored.snapshot}`;
  },
  status: async (ctx) => renderStatus(ctx.config, liveProbe(ctx.config, ctx.os)),
  questions: async (ctx) => renderQuestions(ctx.config),
  answer: async (ctx, args) => {
    const [pairKey, answer] = positional(args);
    if (!pairKey || !ANSWERS.includes(answer as Answer)) throw new Error(`사용법: answer <pairKey> latest|old|keep\n\n${USAGE}`);
    return answerQuestion(ctx.config, pairKey, answer as Answer);
  },
  reload: async (ctx, _args, team) => {
    const report = await reloadFromColdStore(ctx.coldStore, ctx.indexer, team);
    const corrupted = report.corrupted.map((c) => `  손상: ${c.key} (줄 ${c.lines.join(", ")})`);
    return [`${report.chunks} chunks 재적재 (from ${ctx.coldStore.location})`, ...corrupted].join("\n");
  },
};

function flag(args: readonly string[], name: string): string | undefined {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

const VALUE_FLAGS = new Set(["--team", "--repo"]);

function positional(args: readonly string[]): string[] {
  return args.filter((a, i) => !a.startsWith("--") && !VALUE_FLAGS.has(args[i - 1] ?? ""));
}

function required(value: string | undefined, name: string): string {
  if (!value) throw new Error(`${name} 인자가 필요합니다\n\n${USAGE}`);
  return value;
}

async function main(): Promise<void> {
  const [name, ...args] = process.argv.slice(2);
  const command = name ? commands[name] : undefined;
  if (!command) {
    console.log(USAGE);
    process.exit(name ? 1 : 0);
  }
  const ctx = createContext();
  if (!OFFLINE_COMMANDS.has(name!)) await ctx.ready();
  console.log(await command(ctx, args, flag(args, "--team") ?? ctx.config.defaultTeam));
}

main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
