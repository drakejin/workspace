import { describe, expect, test } from "vitest";
import { isIndexablePath, resolveInsideRoot } from "../src/ingest/paths.js";

describe("isIndexablePath", () => {
  test.each([
    ["docs/a.md", true],
    ["docs/x/y.mdx", true],
    ["main/svc/docs/a.md", true],
    ["main/svc/.worktrees/feat/docs/a.md", false],
    ["main/svc/src/a.md", false],
    ["README.md", true],
    ["CLAUDE.md", true],
    ["app/README.md", false],
    [".claude/commands/x.md", false],
    ["docs/a.txt", false],
    ["docs/node_modules/a.md", false],
  ])("%s → %s", (p, expected) => {
    expect(isIndexablePath(p)).toBe(expected);
  });
});

describe("resolveInsideRoot", () => {
  test("상대 경로를 루트 기준으로 푼다", () => {
    expect(resolveInsideRoot("/meta", "docs/a.md")).toEqual({ absolute: "/meta/docs/a.md", relative: "docs/a.md" });
  });
  test("루트 밖은 거부한다", () => {
    expect(() => resolveInsideRoot("/meta", "../etc/passwd")).toThrow(/밖의 경로/);
    expect(() => resolveInsideRoot("/meta", "/etc/passwd")).toThrow(/밖의 경로/);
  });
});
