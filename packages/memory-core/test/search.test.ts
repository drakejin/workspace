import { describe, expect, test } from "vitest";
import { buildSearchQuery, searchMemory } from "../src/search/search.js";
import { OpenSearchError, type OpenSearchPort } from "../src/opensearch/port.js";

describe("buildSearchQuery", () => {
  test("BM25 multi_match + 시간/repo/종류 필터", () => {
    const q = buildSearchQuery({
      team: "ads",
      query: "dedup",
      timeRange: { from: "now-30d" },
      repo: "attribution",
      docType: "adr",
      size: 500,
    });
    expect(q.size).toBe(50);
    expect(q.query.bool.must[0]).toEqual({
      multi_match: { query: "dedup", fields: ["text", "header_path^2", "source_path.text", "title^2", "summary"] },
    });
    expect(q.query.bool.filter).toEqual([
      { range: { event_time: { gte: "now-30d" } } },
      { term: { repo: "attribution" } },
      { term: { doc_type: "adr" } },
    ]);
  });

  test("필터가 없으면 빈 filter, 밀린 문서는 기본으로 숨긴다", () => {
    const q = buildSearchQuery({ team: "a", query: "x" });
    expect(q.query.bool.filter).toEqual([]);
    expect(q.query.bool).toMatchObject({ must_not: [{ exists: { field: "superseded_by" } }] });
    expect(buildSearchQuery({ team: "a", query: "x", includeSuperseded: true }).query.bool).not.toHaveProperty("must_not");
  });
});

describe("searchMemory", () => {
  const port = (impl: OpenSearchPort["request"]): OpenSearchPort => ({ request: impl, bulk: async () => ({ errors: false, items: [] }) });

  test("hot alias로 묻고 결과를 펼친다", async () => {
    const calls: string[] = [];
    const os = port(async (_m, path) => {
      calls.push(path);
      return {
        hits: { hits: [{ _index: "mem-ads-2026.10", _score: 3.2, _source: { source_path: "docs/a.md", header_path: "A", repo: null, doc_type: "note", event_time: "2026-10-01", git_sha: null, text: "t" } }] },
      } as never;
    });
    const hits = await searchMemory(os, { team: "ads", query: "x" });
    expect(calls).toEqual(["/mem-ads-hot/_search"]);
    expect(hits[0]).toMatchObject({ score: 3.2, index: "mem-ads-2026.10", source_path: "docs/a.md" });
  });

  test("alias가 없으면 빈 결과, 다른 에러는 그대로", async () => {
    await expect(searchMemory(port(async () => { throw new OpenSearchError("x", 404, "index_not_found_exception"); }), { team: "a", query: "q" })).resolves.toEqual([]);
    await expect(searchMemory(port(async () => { throw new OpenSearchError("boom", 500, null); }), { team: "a", query: "q" })).rejects.toThrow("boom");
  });

  test("빈 질의는 거부", async () => {
    await expect(searchMemory(port(async () => ({}) as never), { team: "a", query: "  " })).rejects.toThrow(/비어/);
  });
});
