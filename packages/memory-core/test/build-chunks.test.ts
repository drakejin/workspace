import { describe, expect, test } from "vitest";
import { buildChunks } from "../src/document/build-chunks.js";

const base = {
  sourcePath: "docs/repository/attribution/adr/0007-dedup.md",
  defaultTeam: "ads",
  fallbackTime: new Date("2026-09-01T00:00:00Z"),
  gitSha: "abc",
  now: new Date("2026-10-12T00:00:00Z"),
  mtimeMs: 1000,
  size: 42,
};

describe("buildChunks", () => {
  test("frontmatter 날짜가 event_time이 되고 필드가 다 채워진다", () => {
    const out = buildChunks({ ...base, content: "---\ndate: 2026-10-01\n---\n# 중복 제거\n클릭 기준" });
    expect(out.team).toBe("ads");
    expect(out.eventTime.toISOString()).toBe("2026-10-01T00:00:00.000Z");
    expect(out.chunks[0]).toMatchObject({
      team: "ads",
      repo: "attribution",
      doc_type: "adr",
      source_path: base.sourcePath,
      git_sha: "abc",
      header_path: "중복 제거",
      chunk_index: 0,
      ingested_at: "2026-10-12T00:00:00.000Z",
    });
  });

  test("날짜가 없으면 fallbackTime, id는 결정적", () => {
    const a = buildChunks({ ...base, content: "# A\nx" });
    const b = buildChunks({ ...base, content: "# A\ny" });
    expect(a.chunks[0]?.event_time).toBe("2026-09-01T00:00:00.000Z");
    expect(a.chunks[0]?.id).toBe(b.chunks[0]?.id);
  });

  test("teamOverride가 frontmatter보다 우선", () => {
    const out = buildChunks({ ...base, content: "---\nteam: other\n---\nbody", teamOverride: "forced" });
    expect(out.team).toBe("forced");
    expect(out.chunks[0]?.header_path).toBe("0007-dedup");
  });
});

describe("buildChunks daemon fields", () => {
  test("sha·stat·title, 증류 캐시와 판정 플래그를 복원한다", () => {
    const out = buildChunks({
      ...base,
      content: "---\nsupersedes: docs/old.md\n---\n# 제목\n본문",
      distilled: (team, sha) => (team === "ads" && sha.length === 64 ? { summary: "요약", entities: ["service:x"] } : null),
      supersededBy: () => "docs/newer.md",
    });
    const c = out.chunks[0]!;
    expect(c).toMatchObject({ mtime_ms: 1000, size: 42, title: "제목", supersedes: "docs/old.md", superseded_by: "docs/newer.md", summary: "요약", entities: ["service:x"] });
    expect(c.content_sha).toHaveLength(64);
    expect(c.body_sha).not.toBe(c.content_sha);
  });

  test("frontmatter 만 바뀌면 body_sha 는 그대로", () => {
    const a = buildChunks({ ...base, content: "---\ndate: 2026-01-01\n---\n# A\nx" });
    const b = buildChunks({ ...base, content: "---\ndate: 2026-02-01\n---\n# A\nx" });
    expect(a.bodySha).toBe(b.bodySha);
    expect(a.contentSha).not.toBe(b.contentSha);
  });
});
