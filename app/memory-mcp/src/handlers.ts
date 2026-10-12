import {
  listCold,
  restoreCold,
  searchMemory,
  type DocType,
  type IngestResult,
  type SearchHit,
  type TimeRange,
} from "@meta/memory-core";
import type { AppContext } from "./context.js";

export interface SearchArgs {
  readonly team?: string;
  readonly query: string;
  readonly time_range?: TimeRange;
  readonly repo?: string;
  readonly doc_type?: DocType;
  readonly size?: number;
  readonly include_superseded?: boolean;
}

export async function handleSearch(ctx: AppContext, args: SearchArgs): Promise<string> {
  await ctx.ready();
  const team = args.team ?? ctx.config.defaultTeam;
  const hits = await searchMemory(ctx.os, {
    team,
    query: args.query,
    timeRange: args.time_range,
    repo: args.repo,
    docType: args.doc_type,
    size: args.size,
    includeSuperseded: args.include_superseded,
  });
  if (hits.length === 0) {
    return [
      `team=${team} hot 영역에서 "${args.query}"에 맞는 문서가 없습니다.`,
      "단어(서비스명, 함수명, 에러 코드)를 바꿔 다시 검색하세요. 60일보다 오래된 맥락이면 list_cold를 확인하세요.",
    ].join("\n");
  }
  return [`team=${team} · ${hits.length}건 (BM25)`, "", ...hits.map(formatHit)].join("\n");
}

function formatHit(hit: SearchHit, i: number): string {
  const meta = [hit.doc_type, hit.repo, hit.event_time.slice(0, 10), hit.git_sha?.slice(0, 7)].filter(Boolean).join(", ");
  const flags = hit.superseded_by ? `  [superseded → ${hit.superseded_by}]` : "";
  return [
    `[${i + 1}] ${hit.source_path} › ${hit.header_path}  (${meta}; score ${hit.score.toFixed(2)})${flags}`,
    ...(hit.summary ? [`요약: ${hit.summary}`] : []),
    hit.text,
    "",
  ].join("\n");
}

export async function handleIngest(ctx: AppContext, args: { path: string; team?: string }): Promise<string> {
  await ctx.ready();
  const results = await ctx.indexer.ingestPath(args.path, args.team);
  if (results.length === 0) {
    return `${args.path}: 색인 대상 문서가 없습니다 (docs/ 또는 repos/{repo}/docs/ 아래 .md만 대상)`;
  }
  return [`${results.length}개 문서 적재`, ...results.map(formatIngest)].join("\n");
}

export function formatIngest(r: IngestResult): string {
  return `- ${r.sourcePath} → ${r.index} (${r.chunks} chunks)`;
}

export async function handleListCold(ctx: AppContext, args: { team?: string }): Promise<string> {
  await ctx.ready();
  const team = args.team ?? ctx.config.defaultTeam;
  const cold = await listCold(ctx.os, ctx.config, team);
  if (cold.length === 0) return `team=${team}: cold 인덱스가 없습니다.`;
  return [
    `team=${team} cold 인덱스 ${cold.length}개 (repository ${ctx.config.snapshot.repository})`,
    ...cold.map((c) => `- ${c.index}  snapshot=${c.snapshot}  at=${c.snapshotTime}${c.restored ? "  [현재 복원됨]" : ""}`),
    "",
    "복원이 필요하면 사용자에게 먼저 \"cold 인덱스 X가 있는데 복원할까요\"라고 묻고, 동의를 받은 뒤 restore_cold(index, confirmed=true).",
  ].join("\n");
}

export async function handleRestoreCold(ctx: AppContext, args: { index: string; confirmed?: boolean }): Promise<string> {
  if (args.confirmed !== true) {
    return `복원하지 않았습니다. 사용자에게 "cold 인덱스 ${args.index}가 있는데 복원할까요"라고 먼저 묻고, 동의하면 confirmed=true로 다시 호출하세요.`;
  }
  await ctx.ready();
  const restored = await restoreCold(ctx.os, ctx.config, args.index);
  return `${restored.index}을(를) snapshot ${restored.snapshot}에서 hot으로 복원했습니다. 이제 search_memory에 포함됩니다 (ISM 자동 관리 제외).`;
}
