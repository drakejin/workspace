import { Client } from "@opensearch-project/opensearch";

export type HttpMethod = "GET" | "PUT" | "POST" | "DELETE" | "HEAD";

/** memory-core가 OpenSearch에 바라는 것의 전부. 테스트에서는 가짜로 바꿔 끼운다. */
export interface OpenSearchPort {
  request<T = unknown>(method: HttpMethod, path: string, body?: unknown, query?: Record<string, string>): Promise<T>;
  bulk(lines: readonly object[], query?: Record<string, string>): Promise<BulkResponse>;
}

export interface BulkResponse {
  readonly errors: boolean;
  readonly items: ReadonlyArray<Record<string, { status: number; error?: { type: string; reason: string } }>>;
}

export class OpenSearchError extends Error {
  constructor(
    message: string,
    readonly statusCode: number | null,
    readonly errorType: string | null,
  ) {
    super(message);
    this.name = "OpenSearchError";
  }
}

export function isStatus(error: unknown, statusCode: number): boolean {
  return error instanceof OpenSearchError && error.statusCode === statusCode;
}

export function hasErrorType(error: unknown, type: string): boolean {
  return error instanceof OpenSearchError && error.errorType === type;
}

/** 공식 클라이언트의 transport 위에 얇게 얹는다. API 시그니처가 버전마다 달라서 경로를 직접 쓴다. */
export function createOpenSearchPort(node: string): OpenSearchPort {
  const client = new Client({ node });
  const send = async <T>(params: Record<string, unknown>): Promise<T> => {
    try {
      const response = await client.transport.request(params as never);
      return (response as { body: T }).body;
    } catch (error) {
      throw toOpenSearchError(error, `${String(params.method)} ${String(params.path)}`);
    }
  };
  return {
    request: (method, path, body, query) => send({ method, path, body, querystring: query }),
    bulk: (lines, query) => send({ method: "POST", path: "/_bulk", bulkBody: lines, querystring: query }),
  };
}

function toOpenSearchError(error: unknown, operation: string): OpenSearchError {
  const meta = (error as { meta?: { statusCode?: number; body?: { error?: { type?: string; reason?: string } } } }).meta;
  const statusCode = meta?.statusCode ?? null;
  const type = meta?.body?.error?.type ?? null;
  const reason = meta?.body?.error?.reason ?? (error instanceof Error ? error.message : String(error));
  return new OpenSearchError(`OpenSearch ${operation} 실패 (${statusCode ?? "연결 불가"}): ${reason}`, statusCode, type);
}
