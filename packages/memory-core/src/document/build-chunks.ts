import { createHash } from "node:crypto";
import path from "node:path";
import { chunkMarkdown } from "../markdown/chunker.js";
import { splitFrontmatter } from "../markdown/frontmatter.js";
import type { DocType, MemoryChunk } from "../types.js";
import { resolveMetadata } from "./metadata.js";

/** 증류 산출. knowledge-cache 에서 body_sha 로 찾는다 */
export interface DistillFacts {
  readonly summary: string;
  readonly entities: readonly string[];
}

export interface DocumentInput {
  /** meta-repository 기준 상대 경로 (posix 구분자) */
  readonly sourcePath: string;
  readonly content: string;
  readonly defaultTeam: string;
  /** frontmatter에 날짜가 없을 때 쓸 시간 (git 커밋 시각 → 파일 mtime 순) */
  readonly fallbackTime: Date;
  readonly gitSha: string | null;
  readonly now: Date;
  readonly mtimeMs: number;
  readonly size: number;
  readonly teamOverride?: string;
  /** 판정·선언이 정한 superseded_by. 색인이 매번 여기서 복원한다 */
  readonly supersededBy?: (team: string) => string | null;
  /** 증류 캐시 조회. hit 면 LLM 없이 summary/entities 가 붙는다 */
  readonly distilled?: (team: string, bodySha: string) => DistillFacts | null;
}

export interface BuiltDocument {
  readonly team: string;
  readonly docType: DocType;
  readonly eventTime: Date;
  readonly contentSha: string;
  readonly bodySha: string;
  readonly title: string;
  readonly body: string;
  readonly supersedes: string | null;
  readonly chunks: readonly MemoryChunk[];
}

export const sha256 = (text: string): string => createHash("sha256").update(text).digest("hex");

/** 파일 하나를 OpenSearch 청크 목록으로 바꾼다. 같은 입력이면 같은 id가 나온다(재색인 시 덮어쓰기). */
export function buildChunks(input: DocumentInput): BuiltDocument {
  const { data, body } = splitFrontmatter(input.content);
  const meta = resolveMetadata(
    input.teamOverride ? { ...data, team: input.teamOverride } : data,
    input.sourcePath,
    input.defaultTeam,
  );
  const eventTime = meta.declaredTime ?? input.fallbackTime;
  const rootTitle = path.posix.basename(input.sourcePath, path.posix.extname(input.sourcePath));
  const contentSha = sha256(input.content);
  const bodySha = sha256(body);
  const title = /^#\s+(.+)$/m.exec(body)?.[1]?.trim() ?? rootTitle;
  const facts = input.distilled?.(meta.team, bodySha) ?? null;
  const supersededBy = asString(data.superseded_by) ?? input.supersededBy?.(meta.team) ?? null;
  const chunks = chunkMarkdown(body, { rootTitle }).map((chunk, index): MemoryChunk => ({
    id: chunkId(meta.team, input.sourcePath, index),
    team: meta.team,
    repo: meta.repo,
    doc_type: meta.docType,
    event_time: eventTime.toISOString(),
    source_path: input.sourcePath,
    git_sha: input.gitSha,
    header_path: chunk.headerPath,
    chunk_index: index,
    text: chunk.text,
    ingested_at: input.now.toISOString(),
    content_sha: contentSha,
    body_sha: bodySha,
    mtime_ms: input.mtimeMs,
    size: input.size,
    title,
    supersedes: meta.supersedes,
    superseded_by: supersededBy,
    summary: facts?.summary ?? null,
    entities: facts?.entities ?? null,
  }));
  return {
    team: meta.team,
    docType: meta.docType,
    eventTime,
    contentSha,
    bodySha,
    title,
    body,
    supersedes: meta.supersedes,
    chunks,
  };
}

function asString(value: unknown): string | null {
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : null;
}

function chunkId(team: string, sourcePath: string, index: number): string {
  return createHash("sha1").update(`${team}\0${sourcePath}\0${index}`).digest("hex");
}
