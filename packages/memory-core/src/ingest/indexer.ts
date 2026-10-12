import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";
import type { MemoryConfig } from "../config.js";
import { buildChunks, type DistillFacts } from "../document/build-chunks.js";
import { lastCommitOf } from "../document/git.js";
import { monthlyIndex, INDEX_PREFIX } from "../naming.js";
import { ensureMonthlyIndex } from "../opensearch/bootstrap.js";
import type { OpenSearchPort } from "../opensearch/port.js";
import type { DocType, MemoryChunk } from "../types.js";
import { coldKeys, type ColdStore } from "../cold/cold-store.js";
import { isIndexablePath, resolveInsideRoot } from "./paths.js";

/** 파생 상태 복원용 조회. 데몬이 판정 원장·증류 캐시를 꽂는다 */
export interface IndexLookups {
  distilled(team: string, bodySha: string): DistillFacts | null;
  supersededBy(team: string, sourcePath: string): string | null;
}

export interface IndexerDeps {
  readonly os: OpenSearchPort;
  readonly coldStore: ColdStore;
  readonly config: MemoryConfig;
  readonly now?: () => Date;
  readonly lookups?: IndexLookups;
}

export interface IngestResult {
  readonly sourcePath: string;
  readonly team: string;
  readonly index: string;
  readonly chunks: number;
  readonly docType: DocType;
  readonly bodySha: string;
  readonly contentSha: string;
  readonly eventTime: string;
  readonly title: string;
  readonly body: string;
  readonly supersedes: string | null;
}

export interface Indexer {
  ingestFile(inputPath: string, teamOverride?: string): Promise<IngestResult | null>;
  ingestPath(inputPath: string, teamOverride?: string): Promise<IngestResult[]>;
  /** indices 를 주면 그 인덱스에서만 지운다 (데몬은 복원된 cold 인덱스를 건드리지 않는다) */
  removeFile(inputPath: string, indices?: readonly string[]): Promise<number>;
  bulkChunks(chunks: readonly MemoryChunk[]): Promise<number>;
}

const ALL_MEMORY_INDICES = [`${INDEX_PREFIX}*`];
const LENIENT = { ignore_unavailable: "true", allow_no_indices: "true" } as const;

export function createIndexer(deps: IndexerDeps): Indexer {
  const { os, coldStore, config, lookups } = deps;
  const now = deps.now ?? (() => new Date());

  async function bulkChunks(chunks: readonly MemoryChunk[]): Promise<number> {
    if (chunks.length === 0) return 0;
    const byIndex = new Map<string, MemoryChunk[]>();
    for (const chunk of chunks) {
      const index = monthlyIndex(chunk.team, new Date(chunk.event_time));
      byIndex.set(index, [...(byIndex.get(index) ?? []), chunk]);
    }
    for (const [index, group] of byIndex) {
      await ensureMonthlyIndex(os, index, group[0]?.team ?? config.defaultTeam);
    }
    const lines = [...byIndex].flatMap(([index, group]) =>
      group.flatMap(({ id, ...source }) => [{ index: { _index: index, _id: id } }, source]),
    );
    const response = await os.bulk(lines, { refresh: "true" });
    if (response.errors) {
      const first = response.items.map((i) => Object.values(i)[0]).find((r) => r?.error);
      throw new Error(`_bulk 일부 실패: ${first?.error?.type}: ${first?.error?.reason}`);
    }
    return chunks.length;
  }

  /** source_path의 청크를 지운다. keepIds가 있으면 그 id는 남긴다(방금 쓴 새 청크). */
  async function deleteBySource(
    sourcePath: string,
    keepIds: readonly string[] = [],
    indices: readonly string[] = ALL_MEMORY_INDICES,
  ): Promise<number> {
    if (indices.length === 0) return 0;
    const out = await os.request<{ deleted: number }>(
      "POST",
      `/${indices.join(",")}/_delete_by_query`,
      {
        query: {
          bool: {
            filter: [{ term: { source_path: sourcePath } }],
            ...(keepIds.length > 0 && { must_not: [{ ids: { values: keepIds } }] }),
          },
        },
      },
      { refresh: "true", conflicts: "proceed", ...LENIENT },
    );
    return out.deleted;
  }

  async function teamsOf(sourcePath: string): Promise<string[]> {
    const out = await os.request<{ aggregations?: { teams: { buckets: Array<{ key: string }> } } }>(
      "POST",
      `/${INDEX_PREFIX}*/_search`,
      { size: 0, query: { term: { source_path: sourcePath } }, aggs: { teams: { terms: { field: "team", size: 100 } } } },
      LENIENT,
    );
    return (out.aggregations?.teams.buckets ?? []).map((b) => b.key);
  }

  async function ingestFile(inputPath: string, teamOverride?: string): Promise<IngestResult | null> {
    const { absolute, relative } = resolveInsideRoot(config.metaRepoRoot, inputPath);
    if (!isIndexablePath(relative)) return null;
    const [content, info, git] = await Promise.all([readFile(absolute, "utf8"), stat(absolute), lastCommitOf(absolute)]);
    const built = buildChunks({
      sourcePath: relative,
      content,
      defaultTeam: config.defaultTeam,
      fallbackTime: git.committedAt ?? info.mtime,
      gitSha: git.sha,
      now: now(),
      mtimeMs: info.mtimeMs,
      size: info.size,
      teamOverride,
      distilled: lookups?.distilled,
      supersededBy: lookups ? (team) => lookups.supersededBy(team, relative) : undefined,
    });
    // 새 청크를 먼저 쓰고(id가 결정적이라 덮어쓰기) 남은 낡은 청크만 지운다.
    // _bulk가 실패해도 이전 내용은 검색에 남고, 검색이 0건이 되는 틈도 없다.
    await bulkChunks(built.chunks);
    await deleteBySource(relative, built.chunks.map((c) => c.id));
    await coldStore.put(coldKeys.raw(built.team, relative), content);
    await coldStore.put(
      coldKeys.chunks(built.team, relative),
      built.chunks.map((c) => JSON.stringify(c)).join("\n") + "\n",
    );
    return {
      sourcePath: relative,
      team: built.team,
      index: monthlyIndex(built.team, built.eventTime),
      chunks: built.chunks.length,
      docType: built.docType,
      bodySha: built.bodySha,
      contentSha: built.contentSha,
      eventTime: built.eventTime.toISOString(),
      title: built.title,
      body: built.body,
      supersedes: built.supersedes,
    };
  }

  async function ingestPath(inputPath: string, teamOverride?: string): Promise<IngestResult[]> {
    const { absolute } = resolveInsideRoot(config.metaRepoRoot, inputPath);
    const info = await stat(absolute);
    if (!info.isDirectory()) {
      const one = await ingestFile(absolute, teamOverride);
      return one ? [one] : [];
    }
    const entries = await readdir(absolute, { recursive: true, withFileTypes: true });
    const files = entries
      .filter((e) => e.isFile())
      .map((e) => path.join(e.parentPath, e.name))
      .filter((file) => isIndexablePath(path.relative(config.metaRepoRoot, file)))
      .sort();
    const results: IngestResult[] = [];
    for (const file of files) {
      const result = await ingestFile(file, teamOverride);
      if (result) results.push(result);
    }
    return results;
  }

  async function removeFile(inputPath: string, indices?: readonly string[]): Promise<number> {
    const { relative } = resolveInsideRoot(config.metaRepoRoot, inputPath);
    if (!isIndexablePath(relative)) return 0;
    const teams = await teamsOf(relative);
    const deleted = await deleteBySource(relative, [], indices ?? ALL_MEMORY_INDICES);
    await Promise.all(teams.map((team) => coldStore.remove(coldKeys.chunks(team, relative))));
    return deleted;
  }

  return { ingestFile, ingestPath, removeFile, bulkChunks };
}
