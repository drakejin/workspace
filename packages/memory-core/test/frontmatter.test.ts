import { describe, expect, test } from "vitest";
import { splitFrontmatter } from "../src/markdown/frontmatter.js";

describe("splitFrontmatter", () => {
  test("YAML을 읽고 본문을 돌려준다", () => {
    const out = splitFrontmatter("---\nteam: ads\ndate: 2026-10-01\n---\n# 본문");
    expect(out.data.team).toBe("ads");
    expect(out.body).toBe("# 본문");
    expect(out.bodyLineOffset).toBe(4);
  });

  test("frontmatter가 없으면 원문 그대로", () => {
    expect(splitFrontmatter("# hi")).toEqual({ data: {}, body: "# hi", bodyLineOffset: 0 });
  });

  test("닫히지 않았거나 깨진 YAML은 frontmatter 없음으로 본다", () => {
    expect(splitFrontmatter("---\nteam: ads\n# x").data).toEqual({});
    expect(splitFrontmatter("---\n: : [\n---\nbody").data).toEqual({});
  });

  test("YAML이 객체가 아니면 빈 data", () => {
    expect(splitFrontmatter("---\n- a\n---\nbody").data).toEqual({});
  });
});
