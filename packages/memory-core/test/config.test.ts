import { describe, expect, test } from "vitest";
import { loadConfig } from "../src/config.js";

describe("loadConfig", () => {
  test("기본값", () => {
    const c = loadConfig({ META_REPO_ROOT: "/meta" });
    expect(c).toMatchObject({
      opensearchUrl: "http://localhost:9202",
      metaRepoRoot: "/meta",
      defaultTeam: "default",
      hotDays: 60,
      snapshot: { repository: "mem-cold", type: "fs" },
      coldStore: { dir: "/meta/.data/cold-store", s3Bucket: undefined },
    });
  });

  test("빈 문자열은 기본값으로 본다", () => {
    expect(loadConfig({ META_REPO_ROOT: "/m", MEMORY_HOT_DAYS: "", COLD_STORE_S3_BUCKET: "" }).hotDays).toBe(60);
  });

  test("잘못된 값은 바로 실패한다", () => {
    expect(() => loadConfig({ MEMORY_HOT_DAYS: "-1" })).toThrow(/hotDays/);
    expect(() => loadConfig({ MEMORY_DEFAULT_TEAM: "Ads" })).toThrow(/defaultTeam/);
    expect(() => loadConfig({ COLD_SNAPSHOT_TYPE: "s3" })).toThrow(/COLD_SNAPSHOT_S3_BUCKET/);
  });
});

describe("loadConfig root", () => {
  test("META_REPO_ROOT가 없으면 INIT_CWD", () => {
    expect(loadConfig({ INIT_CWD: "/from-pnpm" }).metaRepoRoot).toBe("/from-pnpm");
    expect(loadConfig({ INIT_CWD: "/from-pnpm", META_REPO_ROOT: "/explicit" }).metaRepoRoot).toBe("/explicit");
  });
});
