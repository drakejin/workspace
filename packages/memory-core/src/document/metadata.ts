import { DOC_TYPES, type DocType } from "../types.js";
import { assertTeam } from "../naming.js";

export interface PathFacts {
  readonly repo: string | null;
  readonly docType: DocType;
}

// docs/repository/{repo}/... (발표 구조) 또는 docs/main/{repo}/... (워크스페이스 init 구조)
const REPO_DOCS = /^docs\/(?:repository|main)\/([^/]+)\//;
// main/{repo}/docs/... — 관리 대상 repository 안의 docs
const MANAGED_REPO_DOCS = /^main\/([^/]+)\/docs\//;

const DOC_TYPE_HINTS: ReadonlyArray<readonly [RegExp, DocType]> = [
  // 안내·목차 문서 — 시간이 지나도 낡지 않는 정본. 유사도 판정·노화 대상이 아니다
  [/^(README|INDEX|CLAUDE)\.md$|(^|\/)INDEX\.md$|^docs\/guides\//, "guide"],
  [/(^|\/)(adr|adrs|decisions?)(\/|$)|(^|\/)adr[-_]/i, "adr"],
  [/(^|\/)prds?(\/|$)|(^|\/)prd[-_]/i, "prd"],
  [/(^|\/)plans?(\/|$)|(^|\/)plan[-_]|[-_]plan\.md$/i, "plan"],
  [/(^|\/)knowledge\//, "knowledge"],
  [/(^|\/)specs?(\/|$)|(^|\/)spec[-_]|[-_]spec\.md$/i, "spec"],
];

/** meta-repository 기준 상대 경로에서 repo와 문서 종류를 추론한다. */
export function factsFromPath(relativePath: string): PathFacts {
  const normalized = relativePath.split("\\").join("/");
  const repo = REPO_DOCS.exec(normalized)?.[1] ?? MANAGED_REPO_DOCS.exec(normalized)?.[1] ?? null;
  const docType = DOC_TYPE_HINTS.find(([pattern]) => pattern.test(normalized))?.[1] ?? "note";
  return { repo, docType };
}

/** 시간이 지나면 낡는 기록(episodic)인가, 누가 바꾸기 전까지 유효한 사실(knowledge)인가 */
export type MemoryKind = "episodic" | "knowledge";

export function memoryKindOf(docType: DocType): MemoryKind {
  return docType === "knowledge" || docType === "guide" ? "knowledge" : "episodic";
}

export interface ResolvedMetadata {
  readonly team: string;
  readonly repo: string | null;
  readonly docType: DocType;
  /** frontmatter에 날짜가 있으면 그 값, 없으면 null (호출자가 git/mtime으로 채움) */
  readonly declaredTime: Date | null;
  /** knowledge 개정판이 밀어내는 구판 경로 (frontmatter supersedes) */
  readonly supersedes: string | null;
}

/** frontmatter가 경로 추론보다 우선한다. */
export function resolveMetadata(
  frontmatter: Readonly<Record<string, unknown>>,
  relativePath: string,
  defaultTeam: string,
): ResolvedMetadata {
  const facts = factsFromPath(relativePath);
  const team = asString(frontmatter.team) ?? defaultTeam;
  return {
    team: assertTeam(team.toLowerCase()),
    repo: asString(frontmatter.repo) ?? facts.repo,
    docType: asDocType(frontmatter.doc_type ?? frontmatter.type) ?? facts.docType,
    declaredTime: asDate(frontmatter.event_time ?? frontmatter.date),
    supersedes: asString(frontmatter.supersedes),
  };
}

function asString(value: unknown): string | null {
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : null;
}

function asDocType(value: unknown): DocType | null {
  const s = asString(value)?.toLowerCase();
  return s && (DOC_TYPES as readonly string[]).includes(s) ? (s as DocType) : null;
}

function asDate(value: unknown): Date | null {
  if (value instanceof Date && !Number.isNaN(value.getTime())) return value;
  const s = asString(value);
  if (!s) return null;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
}
