import { describe, expect, test } from "vitest";
import { chunkMarkdown } from "../src/markdown/chunker.js";

describe("chunkMarkdown", () => {
  test("헤더 계층을 header_path로 이어 붙인다", () => {
    const md = ["# ADR-7", "intro", "## 결정", "A를 고른다", "### 대안", "B는 버린다", "## 결과", "비워둠"].join("\n");
    const chunks = chunkMarkdown(md, { rootTitle: "adr-7" });
    expect(chunks.map((c) => c.headerPath)).toEqual([
      "ADR-7",
      "ADR-7 > 결정",
      "ADR-7 > 결정 > 대안",
      "ADR-7 > 결과",
    ]);
    expect(chunks[2]?.text).toContain("B는 버린다");
  });

  test("헤더 앞 머리말은 rootTitle로 잡는다", () => {
    const chunks = chunkMarkdown("머리말\n\n# 제목\n본문", { rootTitle: "file" });
    expect(chunks[0]).toEqual({ headerPath: "file", text: "머리말" });
  });

  test("코드 블록 안의 # 줄은 헤더가 아니다", () => {
    const md = ["# 설치", "```bash", "# 주석", "make start", "```", "끝"].join("\n");
    const chunks = chunkMarkdown(md, { rootTitle: "x" });
    expect(chunks).toHaveLength(1);
    expect(chunks[0]?.text).toContain("# 주석");
  });

  test("레벨을 건너뛰는 헤더도 상위 경로를 유지한다", () => {
    const chunks = chunkMarkdown("# A\na\n### C\nc\n## B\nb", { rootTitle: "x" });
    expect(chunks.map((c) => c.headerPath)).toEqual(["A", "A > C", "A > B"]);
  });

  test("긴 섹션은 문단 경계에서 나누고 코드 블록은 자르지 않는다", () => {
    const code = ["```", "line1", "", "line2", "```"].join("\n");
    const md = ["# T", "a".repeat(30), "", code, "", "b".repeat(30)].join("\n");
    const chunks = chunkMarkdown(md, { rootTitle: "x", maxChars: 40 });
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.some((c) => c.text.includes("line1\n\nline2"))).toBe(true);
    expect(chunks.every((c) => c.headerPath === "T")).toBe(true);
  });

  test("빈 섹션은 버린다", () => {
    expect(chunkMarkdown("\n\n", { rootTitle: "x" })).toEqual([]);
  });
});
