import { describe, expect, test } from "vitest";
import { isInScope } from "../src/ingest/scope.js";

describe("isInScope", () => {
  test.each([
    ["", undefined, true],
    ["docs", false, true],
    ["docs/a.md", true, true],
    ["docs/a.png", true, false],
    ["main", false, true],
    ["main/svc", false, true],
    ["main/svc/docs", false, true],
    ["main/svc/docs/adr/x.md", true, true],
    ["main/svc/src", false, false],
    ["main/svc/.worktrees", false, false],
    ["main/svc/node_modules", false, false],
    ["app", false, false],
    ["scripts", false, false],
    [".git", false, false],
    ["README.md", true, true],
    ["README.md", undefined, true],
    ["package.json", undefined, false],
  ])("%s (file=%s) → %s", (p, isFile, expected) => {
    expect(isInScope(p, isFile)).toBe(expected);
  });
});
