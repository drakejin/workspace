import { mkdtemp, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, test } from "vitest";
import { createDistillCache } from "../src/daemon/distill-cache.js";
import { jsonlLog } from "../src/daemon/jsonl-log.js";

describe("jsonlLog", () => {
  test("깨진 줄은 건너뛰고 알린다, 없는 파일은 빈 목록", async () => {
    const dir = await mkdtemp(path.join(os.tmpdir(), "jsonl-"));
    const bad: number[] = [];
    const log = jsonlLog<{ n: number }>(dir, (_f, line) => bad.push(line));
    expect(await log.readAll("t")).toEqual([]);
    await log.append("t", { n: 1 });
    await writeFile(path.join(dir, "t.jsonl"), '{"n":1}\n{broken\n{"n":2}\n');
    expect(await log.readAll("t")).toEqual([{ n: 1 }, { n: 2 }]);
    expect(bad).toEqual([2]);
  });
});

describe("distill cache", () => {
  test("본문 sha 로 찾고, 다시 load 해도 남는다", async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cache-"));
    const cache = createDistillCache(root);
    await cache.load();
    expect(cache.get("t", "sha")).toBeNull();
    await cache.put("t", { bodySha: "sha", sourcePath: "docs/a.md", summary: "요약", entities: ["concept:x"], at: "now" });
    expect(cache.get("t", "sha")).toEqual({ summary: "요약", entities: ["concept:x"] });
    const reloaded = createDistillCache(root);
    await reloaded.load();
    expect(reloaded.get("t", "sha")?.summary).toBe("요약");
    expect(reloaded.size()).toBe(1);
    expect(reloaded.size("t")).toBe(1);
    expect(reloaded.size("other")).toBe(0);
  });
});
