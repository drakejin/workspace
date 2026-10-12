import { describe, expect, test } from "vitest";
import { findConflicts, findExpired, renderCompactionReport } from "../src/compaction/report.js";

const chunk = (source_path: string, header_path: string, event_time: string, repo: string | null = "svc", text = "t") => ({
  source_path, header_path, event_time, repo, doc_type: "adr", text,
});

describe("findExpired", () => {
  test("문서의 가장 최신 청크가 기준보다 오래된 것만", () => {
    const out = findExpired(
      [chunk("old.md", "A", "2026-07-01T00:00:00Z"), chunk("new.md", "B", "2026-10-01T00:00:00Z")],
      new Date("2026-08-13T00:00:00Z"),
    );
    expect(out.map((d) => d.sourcePath)).toEqual(["old.md"]);
  });
});

describe("findConflicts", () => {
  test("같은 repo·같은 주제를 다른 문서가 다루면 후보, 일반 제목은 제외", () => {
    const out = findConflicts([
      chunk("a.md", "ADR-1 > 활성 광고 정의", "2026-07-01T00:00:00Z", "svc", "노출 중인 광고"),
      chunk("b.md", "PRD > 활성 광고 정의", "2026-09-01T00:00:00Z", "svc", "예산이 남은 광고"),
      chunk("c.md", "X > 활성 광고 정의", "2026-09-01T00:00:00Z", "other"),
      chunk("a.md", "ADR-1 > 배경", "2026-07-01T00:00:00Z"),
      chunk("b.md", "PRD > 배경", "2026-09-01T00:00:00Z"),
    ]);
    expect(out).toHaveLength(1);
    expect(out[0]?.topic).toBe("활성 광고 정의");
    expect(out[0]?.entries.map((e) => e.sourcePath)).toEqual(["b.md", "a.md"]);
  });
});

describe("renderCompactionReport", () => {
  test("다섯 단계 중 1·2를 채우고 3~5를 안내한다", () => {
    const md = renderCompactionReport({
      team: "ads",
      cutoff: "2026-08-13T00:00:00.000Z",
      expired: [{ sourcePath: "old.md", repo: "svc", docType: "adr", eventTime: "2026-07-01T00:00:00Z" }],
      conflicts: [],
      truncated: false,
    });
    expect(md).toContain("`old.md` (adr, svc, 2026-07-01)");
    expect(md).toContain("충돌 후보");
    expect(md).toContain("다시 쓰기");
  });
});

describe("renderCompactionReport truncated", () => {
  test("잘렸으면 경고를 넣는다", () => {
    const md = renderCompactionReport({ team: "a", cutoff: "x", expired: [], conflicts: [], truncated: true });
    expect(md).toContain("만 봤습니다");
  });
});
