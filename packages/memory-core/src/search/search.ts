import { hotAlias } from "../naming.js";
import { isStatus, type OpenSearchPort } from "../opensearch/port.js";
import type { DocType, TimeRange } from "../types.js";

export interface SearchRequest {
  readonly team: string;
  readonly query: string;
  readonly timeRange?: TimeRange;
  readonly repo?: string;
  readonly docType?: DocType;
  readonly size?: number;
  /** 판정·선언으로 밀린 문서도 본다 (기본: 숨김) */
  readonly includeSuperseded?: boolean;
}

export interface SearchHit {
  readonly score: number;
  readonly index: string;
  readonly source_path: string;
  readonly header_path: string;
  readonly repo: string | null;
  readonly doc_type: DocType;
  readonly event_time: string;
  readonly git_sha: string | null;
  readonly text: string;
  readonly summary: string | null;
  readonly superseded_by: string | null;
}

const DEFAULT_SIZE = 8;
const MAX_SIZE = 50;

export function buildSearchQuery(req: SearchRequest) {
  const filter: object[] = [];
  if (req.timeRange?.from || req.timeRange?.to) {
    filter.push({
      range: { event_time: { ...(req.timeRange.from && { gte: req.timeRange.from }), ...(req.timeRange.to && { lte: req.timeRange.to }) } },
    });
  }
  if (req.repo) filter.push({ term: { repo: req.repo } });
  if (req.docType) filter.push({ term: { doc_type: req.docType } });
  return {
    size: Math.min(Math.max(req.size ?? DEFAULT_SIZE, 1), MAX_SIZE),
    query: {
      bool: {
        must: [{ multi_match: { query: req.query, fields: ["text", "header_path^2", "source_path.text", "title^2", "summary"] } }],
        filter,
        ...(req.includeSuperseded ? {} : { must_not: [{ exists: { field: "superseded_by" } }] }),
      },
    },
  };
}

interface RawHits {
  readonly hits: { readonly hits: ReadonlyArray<{ _index: string; _score: number; _source: Omit<SearchHit, "score" | "index"> }> };
}

/** 팀의 hot alias에 BM25 검색. alias가 아직 없으면(문서 0건) 빈 결과. */
export async function searchMemory(os: OpenSearchPort, req: SearchRequest): Promise<SearchHit[]> {
  if (req.query.trim().length === 0) throw new Error("query가 비어 있습니다");
  try {
    const out = await os.request<RawHits>("POST", `/${hotAlias(req.team)}/_search`, buildSearchQuery(req));
    return out.hits.hits.map((h) => ({
      score: h._score,
      index: h._index,
      source_path: h._source.source_path,
      header_path: h._source.header_path,
      repo: h._source.repo,
      doc_type: h._source.doc_type,
      event_time: h._source.event_time,
      git_sha: h._source.git_sha,
      text: h._source.text,
      summary: h._source.summary ?? null,
      superseded_by: h._source.superseded_by ?? null,
    }));
  } catch (error) {
    if (isStatus(error, 404)) return [];
    throw error;
  }
}
