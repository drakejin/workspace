#!/usr/bin/env node
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { createContext } from "./context.js";
import { createMemoryServer } from "./server.js";

async function main(): Promise<void> {
  const ctx = createContext();
  // stdout은 MCP 프로토콜 전용. 로그는 stderr로만.
  console.error(`[memory-mcp] root=${ctx.config.metaRepoRoot} opensearch=${ctx.config.opensearchUrl} team=${ctx.config.defaultTeam}`);
  ctx.ready().then(
    () => console.error("[memory-mcp] OpenSearch 준비 완료 (template, ISM, snapshot repository)"),
    (error: unknown) => console.error(`[memory-mcp] OpenSearch 준비 실패, 첫 도구 호출 때 다시 시도: ${String(error)}`),
  );
  await createMemoryServer(ctx).connect(new StdioServerTransport());
}

main().catch((error: unknown) => {
  console.error(`[memory-mcp] 시작 실패: ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
});
