import { readFile } from "node:fs/promises";
import type { MemoryConfig } from "../config.js";
import { sha256 } from "../document/build-chunks.js";
import type { Indexer } from "../ingest/indexer.js";
import type { OpenSearchPort } from "../opensearch/port.js";
import type { DocType } from "../types.js";
import { fetchIndexedDocs, managedIndices, updateBySource, type IndexedDoc } from "./index-docs.js";
import type { JudgeState } from "./judge-state.js";
import { isProbablyBinary, scanHot } from "./scan.js";

/** 이번 사이클에 내용이 바뀐 문서. 판정·증류의 입력이다 (메모리 전달만, 영속 없음) */
export interface DiffEntry {
  readonly sourcePath: string;
  readonly team: string;
  readonly docType: DocType;
  readonly change: "new" | "modified" | "removed";
  readonly bodySha: string;
  readonly eventTime: string;
  readonly title: string;
  readonly body: string;
  readonly supersedes: string | null;
}

export interface CycleSummary {
  readonly scanned: number;
  readonly indexed: number;
  readonly statUpdated: number;
  readonly removed: number;
  readonly skipped: number;
  readonly flagsConverged: number;
  readonly refused: boolean;
  readonly tookMs: number;
}

export interface ReconcileDeps {
  readonly os: OpenSearchPort;
  readonly config: MemoryConfig;
  readonly indexer: Indexer;
  readonly judgeState: JudgeState;
  readonly log: (line: string) => void;
}

/**
 * 수렴 사이클 A — hot(git working tree)과 인덱스를 맞춘다. 매 사이클이 곧 풀 싱크라 첫 기동 분기가 없다.
 * 1) stat 이 같으면 건너뛴다  2) stat 이 달라도 sha 가 같으면 stat 만 갱신  3) 내용이 바뀌면 재색인
 * 4) 디스크에 없는 문서는 인덱스에서 지운다(tombstone)  5) superseded_by 를 원장·선언대로 수렴
 */
export async function reconcileOnce(deps: ReconcileDeps): Promise<{ summary: CycleSummary; diff: DiffEntry[] }> {
  const { os, config, indexer, log } = deps;
  const start = Date.now();
  const { files, skipped } = await scanHot(config.metaRepoRoot, config.reconcile.maxFileBytes);

  if (files.length > config.reconcile.sanityMaxFiles) {
    log(`[reconcile] matched ${files.length} files > sanityMaxFiles(${config.reconcile.sanityMaxFiles}) — 설정 오류로 판단, 색인 거부`);
    return { summary: emptySummary(files.length, skipped.length, true, start), diff: [] };
  }

  const indices = await managedIndices(os);
  const existing = await fetchIndexedDocs(os, indices);
  const diff: DiffEntry[] = [];
  const binary: string[] = [];
  let statUpdated = 0;

  for (const f of files) {
    const ex = existing.get(f.rel);
    existing.delete(f.rel);
    if (ex && ex.mtimeMs === f.mtimeMs && ex.size === f.size) continue;
    if (await isProbablyBinary(f.abs)) {
      binary.push(f.rel);
      continue;
    }
    const content = await readFile(f.abs, "utf8");
    if (ex && ex.contentSha === sha256(content)) {
      await updateBySource(os, indices, f.rel, { mtime_ms: f.mtimeMs, size: f.size });
      statUpdated++;
      continue;
    }
    const result = await indexer.ingestFile(f.abs);
    if (!result) continue;
    diff.push({
      sourcePath: result.sourcePath,
      team: result.team,
      docType: result.docType,
      change: ex ? "modified" : "new",
      bodySha: result.bodySha,
      eventTime: result.eventTime,
      title: result.title,
      body: result.body,
      supersedes: result.supersedes,
    });
  }

  // tombstone — 사람이 hot 에서 지운 문서. 원본 이력은 git 히스토리와 cold store raw 가 갖는다
  for (const gone of existing.values()) {
    await indexer.removeFile(gone.sourcePath, indices);
    diff.push({
      sourcePath: gone.sourcePath,
      team: gone.team,
      docType: gone.docType,
      change: "removed",
      bodySha: gone.bodySha ?? "",
      eventTime: gone.eventTime,
      title: "",
      body: "",
      supersedes: null,
    });
  }

  // 재색인으로 월별 인덱스가 새로 생겼을 수 있다 (인덱스를 통째로 지운 뒤의 재구축) — 목록을 다시 읽는다
  const flagsConverged = await convergeSupersededBy(deps, await managedIndices(os));
  for (const rel of binary) log(`[reconcile] skip ${rel} (binary content)`);
  for (const s of skipped) log(`[reconcile] skip ${s.rel} (${s.reason})`);

  const indexed = diff.filter((d) => d.change !== "removed").length;
  return {
    summary: {
      scanned: files.length,
      indexed,
      statUpdated,
      removed: diff.length - indexed,
      skipped: skipped.length + binary.length,
      flagsConverged,
      refused: false,
      tookMs: Date.now() - start,
    },
    diff,
  };
}

/**
 * superseded_by 의 정본은 둘이다: knowledge 개정판의 `supersedes:` 선언, 그리고 judge-state 원장.
 * 인덱스 값은 파생물이라 매 사이클 여기서 다시 맞춘다 (인덱스를 지워도 같은 결과로 재구축된다).
 */
export async function convergeSupersededBy(deps: ReconcileDeps, indices: readonly string[]): Promise<number> {
  const docs = await fetchIndexedDocs(deps.os, indices);
  const desired = await desiredSupersededBy(docs, deps.judgeState);
  let changed = 0;
  for (const doc of docs.values()) {
    const want = desired.get(doc.sourcePath) ?? null;
    if (doc.supersededBy === want) continue;
    await updateBySource(deps.os, indices, doc.sourcePath, { superseded_by: want });
    deps.log(`[judge] superseded_by ${doc.sourcePath} → ${want ?? "(해제)"}`);
    changed++;
  }
  return changed;
}

export async function desiredSupersededBy(
  docs: ReadonlyMap<string, IndexedDoc>,
  judgeState: JudgeState,
): Promise<Map<string, string>> {
  const desired = new Map<string, string>();
  const bodyShaOf = (p: string) => docs.get(p)?.bodySha ?? undefined;
  for (const team of new Set([...docs.values()].map((d) => d.team))) {
    for (const [oldPath, newPath] of await judgeState.supersededMap(team, bodyShaOf)) desired.set(oldPath, newPath);
  }
  // 사람의 선언이 판정보다 우선한다
  for (const doc of docs.values()) {
    if (doc.supersedes && docs.has(doc.supersedes)) desired.set(doc.supersedes, doc.sourcePath);
  }
  return desired;
}

function emptySummary(scanned: number, skipped: number, refused: boolean, start: number): CycleSummary {
  return { scanned, indexed: 0, statUpdated: 0, removed: 0, skipped, flagsConverged: 0, refused, tookMs: Date.now() - start };
}
