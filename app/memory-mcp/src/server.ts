import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { DOC_TYPES } from "@meta/memory-core";
import { z } from "zod";
import type { AppContext } from "./context.js";
import { handleIngest, handleListCold, handleRestoreCold, handleSearch } from "./handlers.js";

type ToolResult = { content: Array<{ type: "text"; text: string }>; isError?: boolean };

async function respond(run: () => Promise<string>): Promise<ToolResult> {
  try {
    return { content: [{ type: "text", text: await run() }] };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.error(`[memory-mcp] tool error: ${message}`);
    return { content: [{ type: "text", text: `실패: ${message}` }], isError: true };
  }
}

const team = z.string().regex(/^[a-z0-9][a-z0-9-]*$/).optional().describe("팀/실 이름. 생략하면 기본 팀");

export function createMemoryServer(ctx: AppContext): McpServer {
  const server = new McpServer({ name: "memory-mcp", version: "0.1.0" });

  server.registerTool(
    "search_memory",
    {
      title: "meta-repository 문서 검색",
      description:
        "meta-repository에 쌓인 spec·ADR·PRD·plan 문서를 BM25로 검색한다 (최근 60일 hot 영역). " +
        "작업을 시작하면 파일 트리를 뒤지기 전에 먼저 부른다. 질의에는 repository 이름, 서비스명, 함수명, 에러 코드 같은 식별자를 그대로 넣고, " +
        "결과가 부족하면 단어를 바꿔 다시 부른다. 결과의 source_path를 Read 해서 원문을 근거로 일한다 (스니펫을 답으로 쓰지 않는다). " +
        "더 새 문서에 밀린 문서는 기본으로 숨긴다.",
      inputSchema: {
        team,
        query: z.string().min(1).describe("자연어 문장 또는 식별자"),
        time_range: z
          .object({
            from: z.string().optional().describe("ISO 날짜 또는 date math (예: now-30d)"),
            to: z.string().optional(),
          })
          .optional(),
        repo: z.string().optional().describe("관리 대상 repository로 좁히기"),
        doc_type: z.enum(DOC_TYPES).optional(),
        size: z.number().int().min(1).max(50).optional(),
        include_superseded: z.boolean().optional().describe("판정·개정으로 밀린 문서도 본다 (기본: 숨김)"),
      },
      annotations: { readOnlyHint: true },
    },
    (args) => respond(() => handleSearch(ctx, args)),
  );

  server.registerTool(
    "ingest_path",
    {
      title: "문서 수동 적재",
      description: "파일 워처가 놓친 문서나 디렉토리를 수동으로 색인한다. 경로는 meta-repository 기준 상대 경로.",
      inputSchema: { path: z.string().min(1), team },
    },
    (args) => respond(() => handleIngest(ctx, args)),
  );

  server.registerTool(
    "list_cold",
    {
      title: "cold 인덱스 목록",
      description: "60일이 지나 스냅샷(S3)으로 내려간 팀의 cold 인덱스 목록. 오래된 맥락이 필요할 때 확인한다.",
      inputSchema: { team },
      annotations: { readOnlyHint: true },
    },
    (args) => respond(() => handleListCold(ctx, args)),
  );

  server.registerTool(
    "restore_cold",
    {
      title: "cold 인덱스 복원",
      description:
        "스냅샷에서 cold 인덱스 하나를 hot으로 복원한다. 반드시 사용자에게 \"cold 인덱스 X가 있는데 복원할까요\"를 먼저 묻고, " +
        "동의를 받은 경우에만 confirmed=true로 호출한다.",
      inputSchema: {
        index: z.string().regex(/^mem-[a-z0-9-]+-\d{4}\.\d{2}$/).describe("list_cold가 돌려준 인덱스 이름"),
        confirmed: z.boolean().optional().describe("사용자가 복원에 동의했으면 true"),
      },
    },
    (args) => respond(() => handleRestoreCold(ctx, args)),
  );

  return server;
}
