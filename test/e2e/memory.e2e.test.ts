/**
 * 실제 OpenSearch(docker compose up)에 붙는 블랙박스 e2e. MEMORY_E2E=1 일 때만 돈다.
 * 발표 §13 데모 순서 + hot→cold→restore 왕복 + compaction 리포트를 MCP 클라이언트로 검증한다.
 */
import { cp, mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import {
  buildCompactionReport,
  loadConfig,
  reloadFromColdStore,
  tierExpired,
} from "../../packages/memory-core/src/index.js";
import { createContext, type AppContext } from "../../app/memory-mcp/src/context.js";
import { createMemoryServer } from "../../app/memory-mcp/src/server.js";

const enabled = process.env.MEMORY_E2E === "1";
const TEAM = `e2e-${Date.now().toString(36)}`;
const NOW = new Date();
const daysAgo = (n: number) => new Date(NOW.getTime() - n * 86_400_000).toISOString().slice(0, 10);

describe.skipIf(!enabled)("memory-mcp e2e (real OpenSearch)", () => {
  let root: string;
  let ctx: AppContext;
  let client: Client;

  const call = async (name: string, args: Record<string, unknown>) => {
    const out = await client.callTool({ name, arguments: args });
    const text = (out.content as Array<{ text: string }>)[0]?.text ?? "";
    return { text, isError: out.isError === true };
  };

  beforeAll(async () => {
    root = await mkdtemp(path.join(os.tmpdir(), "meta-e2e-"));
    await mkdir(path.join(root, "docs/repository/attribution/adr"), { recursive: true });
    await mkdir(path.join(root, "main/serving/docs"), { recursive: true });
    ctx = createContext(loadConfig({ ...process.env, META_REPO_ROOT: root, MEMORY_DEFAULT_TEAM: TEAM }));
    await ctx.ready();
    const [clientSide, serverSide] = InMemoryTransport.createLinkedPair();
    await createMemoryServer(ctx).connect(serverSide);
    client = new Client({ name: "e2e", version: "0" });
    await client.connect(clientSide);
  });

  afterAll(async () => {
    await client?.close();
    await ctx?.os.request("DELETE", `/mem-${TEAM}-*`).catch(() => {});
  });

  test("MCP 도구는 발표의 네 개", async () => {
    const tools = (await client.listTools()).tools.map((t) => t.name).sort();
    expect(tools).toEqual(["ingest_path", "list_cold", "restore_cold", "search_memory"]);
  });

  test("데모 2·3: ADR을 적재하면 배경 설명 없이 search_memory로 찾는다", async () => {
    await writeFile(
      path.join(root, "docs/repository/attribution/adr/0007-click-dedup.md"),
      `---\nrepo: attribution\ndate: ${daysAgo(2)}\n---\n# ADR-0007 클릭 중복 제거\n## 결정\n\`ClickDeduplicator.window\` 는 30m.\n`,
    );
    await call("ingest_path", { path: "docs/repository/attribution/adr/0007-click-dedup.md" });
    const { text } = await call("search_memory", { query: "ClickDeduplicator window" });
    expect(text).toContain("docs/repository/attribution/adr/0007-click-dedup.md › ADR-0007 클릭 중복 제거 > 결정");
  });

  test("ingest_path: 관리 대상 repository의 docs, meta-repo 밖 경로는 거부", async () => {
    await writeFile(path.join(root, "main/serving/docs/prd-active-ad.md"), `---\ndate: ${daysAgo(1)}\n---\n# 활성 광고\n## 활성 광고 정의\n예산이 남은 광고`);
    const ok = await call("ingest_path", { path: "main/serving/docs" });
    expect(ok.text).toContain(`main/serving/docs/prd-active-ad.md → mem-${TEAM}-`);
    const bad = await call("ingest_path", { path: "../../etc" });
    expect(bad.isError).toBe(true);

    const { text } = await call("search_memory", { query: "활성 광고 정의", repo: "serving", doc_type: "prd" });
    expect(text).toContain("main/serving/docs/prd-active-ad.md");
  });

  test("파일을 지우면 색인과 청크 JSONL에서 빠진다", async () => {
    const file = path.join(root, "main/serving/docs/tmp-delete-me.md");
    await writeFile(file, "# 지울 문서\nEphemeralMarkerToken");
    await ctx.indexer.ingestFile(file);
    expect((await call("search_memory", { query: "EphemeralMarkerToken" })).text).toContain("tmp-delete-me.md");
    expect(await ctx.indexer.removeFile(file)).toBe(1);
    expect((await call("search_memory", { query: "EphemeralMarkerToken" })).text).toContain("문서가 없습니다");
    expect(await ctx.coldStore.get(`${TEAM}/chunks/main/serving/docs/tmp-delete-me.md.jsonl`)).toBeNull();
  });

  test("재색인: 섹션이 줄면 낡은 청크만 지워지고 남은 청크는 유지된다", async () => {
    const file = path.join(root, "main/serving/docs/shrinking.md");
    await writeFile(file, "# S\n## 하나\nAlphaSectionToken\n## 둘\nBetaSectionToken");
    await ctx.indexer.ingestFile(file);
    await writeFile(file, "# S\n## 하나\nAlphaSectionToken 수정");
    await ctx.indexer.ingestFile(file);
    expect((await call("search_memory", { query: "AlphaSectionToken" })).text).toContain("shrinking.md › S > 하나");
    expect((await call("search_memory", { query: "BetaSectionToken" })).text).toContain("문서가 없습니다");
    await ctx.indexer.removeFile(file);
  });

  test("time_range 필터", async () => {
    const recent = await call("search_memory", { query: "ClickDeduplicator", time_range: { from: "now-1d" } });
    expect(recent.text).toContain("문서가 없습니다");
  });

  test("hot 60일 → cold(스냅샷+삭제) → 묻고 나서 restore → 다시 검색됨", async () => {
    await writeFile(
      path.join(root, "docs/repository/attribution/adr/0001-legacy-window.md"),
      `---\nrepo: attribution\ndate: ${daysAgo(120)}\n---\n# ADR-0001 레거시 윈도우\n## 활성 광고 정의\n노출 중인 광고. LegacyWindowPolicy 사용.\n`,
    );
    await call("ingest_path", { path: "docs/repository/attribution/adr/0001-legacy-window.md" });
    expect((await call("search_memory", { query: "LegacyWindowPolicy" })).text).toContain("0001-legacy-window.md");

    // compaction 1·2단계: 수명 확인과 충돌 후보
    const report = await buildCompactionReport(ctx.os, ctx.config, TEAM, NOW);
    expect(report.expired.map((d) => d.sourcePath)).toEqual(["docs/repository/attribution/adr/0001-legacy-window.md"]);

    // 5단계: 티어 이동
    const decisions = await tierExpired(ctx.os, ctx.config, { now: NOW, team: TEAM });
    const oldIndex = decisions.find((d) => d.action === "tiered" && d.index.startsWith(`mem-${TEAM}-`))?.index;
    expect(oldIndex).toBeDefined();
    expect((await call("search_memory", { query: "LegacyWindowPolicy" })).text).toContain("문서가 없습니다");

    const cold = await call("list_cold", {});
    expect(cold.text).toContain(oldIndex!);

    const unconfirmed = await call("restore_cold", { index: oldIndex! });
    expect(unconfirmed.text).toContain("복원할까요");
    expect((await call("search_memory", { query: "LegacyWindowPolicy" })).text).toContain("문서가 없습니다");

    const restored = await call("restore_cold", { index: oldIndex!, confirmed: true });
    expect(restored.isError).toBe(false);
    expect((await call("search_memory", { query: "LegacyWindowPolicy" })).text).toContain("0001-legacy-window.md");

    // 복원본은 ISM/tier 대상에서 빠져 바로 cold로 돌아가지 않는다
    const again = await tierExpired(ctx.os, ctx.config, { now: NOW, dryRun: true, team: TEAM });
    expect(again.find((d) => d.index === oldIndex)?.action).toBe("skipped-restored");
  }, 60_000);

  test("S3 층(청크 JSONL)에서 _bulk 재적재", async () => {
    await ctx.os.request("DELETE", `/mem-${TEAM}-*`);
    const reloaded = await reloadFromColdStore(ctx.coldStore, ctx.indexer, TEAM, (c) => !c.source_path.includes("legacy"));
    expect(reloaded.chunks).toBeGreaterThan(0);
    expect(reloaded.corrupted).toEqual([]);
    expect((await call("search_memory", { query: "ClickDeduplicator" })).text).toContain("0007-click-dedup.md");
  });
});

describe.skipIf(!enabled)("team 접두어 충돌", () => {
  test("team X의 list_cold/tier/compaction은 team X-sub 인덱스를 잡지 않는다", async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), "meta-e2e-"));
    await mkdir(path.join(root, "docs"), { recursive: true });
    const base = `p${Date.now().toString(36)}`;
    const sub = `${base}-sub`;
    const ctx = createContext(loadConfig({ ...process.env, META_REPO_ROOT: root, MEMORY_DEFAULT_TEAM: base }));
    await ctx.ready();
    try {
      await writeFile(path.join(root, "docs/old.md"), `---\nteam: ${sub}\ndate: ${daysAgo(200)}\n---\n# old\nx`);
      await ctx.indexer.ingestPath("docs");
      const report = await buildCompactionReport(ctx.os, ctx.config, base, NOW);
      expect(report.expired).toEqual([]);
      const decisions = await tierExpired(ctx.os, ctx.config, { now: NOW, team: base, dryRun: true });
      expect(decisions).toEqual([]);
    } finally {
      await ctx.os.request("DELETE", `/mem-${base}-*`).catch(() => {});
    }
  });
});
