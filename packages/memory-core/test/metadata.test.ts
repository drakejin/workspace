import { describe, expect, test } from "vitest";
import { factsFromPath, resolveMetadata } from "../src/document/metadata.js";

describe("factsFromPath", () => {
  test.each([
    ["docs/repository/attribution/adr/0001-x.md", "attribution", "adr"],
    ["main/serving/docs/prd/launch.md", "serving", "prd"],
    ["main/serving/docs/plans/x.md", "serving", "plan"],
    ["docs/main/serving/notes.md", "serving", "note"],
    ["docs/specs/memory-mcp/design.md", null, "spec"],
    ["docs/specs/memory-mcp/INDEX.md", null, "guide"],
    ["README.md", null, "guide"],
    ["docs/guides/howto.md", null, "guide"],
    ["docs/knowledge/active-ad.md", null, "knowledge"],
    ["docs/feature/attribution/notes.md", null, "note"],
    ["docs/feature/x/ADR-3.md", null, "adr"],
  ])("%s → repo=%s, type=%s", (p, repo, type) => {
    expect(factsFromPath(p)).toEqual({ repo, docType: type });
  });
});

describe("resolveMetadata", () => {
  test("frontmatter가 경로 추론보다 우선한다", () => {
    const meta = resolveMetadata(
      { team: "Ads-Data", repo: "x", doc_type: "PRD", date: "2026-08-01" },
      "docs/repository/attribution/adr/a.md",
      "default",
    );
    expect(meta).toEqual({ team: "ads-data", repo: "x", docType: "prd", declaredTime: new Date("2026-08-01"), supersedes: null });
  });

  test("값이 없거나 잘못되면 기본값/경로 추론", () => {
    const meta = resolveMetadata({ doc_type: "weird", date: "not a date" }, "docs/specs/a.md", "default");
    expect(meta).toEqual({ team: "default", repo: null, docType: "spec", declaredTime: null, supersedes: null });
  });

  test("YAML이 Date로 읽은 값도 받는다", () => {
    const d = new Date("2026-01-02");
    expect(resolveMetadata({ event_time: d }, "docs/a.md", "t").declaredTime).toEqual(d);
  });

  test("잘못된 team은 거부한다", () => {
    expect(() => resolveMetadata({ team: "광고 팀" }, "docs/a.md", "t")).toThrow(/team/);
  });
});

describe("memoryKindOf", () => {
  test("knowledge·guide 만 knowledge, 나머지는 episodic", async () => {
    const { memoryKindOf } = await import("../src/document/metadata.js");
    expect(memoryKindOf("knowledge")).toBe("knowledge");
    expect(memoryKindOf("guide")).toBe("knowledge");
    expect(memoryKindOf("adr")).toBe("episodic");
    expect(memoryKindOf("note")).toBe("episodic");
  });

  test("supersedes 선언을 읽는다", () => {
    expect(resolveMetadata({ supersedes: "docs/knowledge/old.md" }, "docs/knowledge/new.md", "t").supersedes).toBe("docs/knowledge/old.md");
  });
});
