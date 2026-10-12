import path from "node:path";

const DOC_EXTENSIONS = new Set([".md", ".markdown", ".mdx"]);
/** 색인도 watch 도 하지 않는 디렉토리. .worktrees 는 작업 중 사본이라 원본과 중복된다 */
export const IGNORED_SEGMENTS: ReadonlySet<string> = new Set([
  "node_modules", ".git", ".worktrees", "worktrees", ".data", "dist", "build", ".next", "coverage", ".claude",
]);
/** 메타레포 루트에서 색인하는 단일 파일 */
const ROOT_FILES = new Set(["README.md", "INDEX.md", "CLAUDE.md"]);
/** main/{repo}/docs/... — 관리 대상 repository 의 docs */
const MANAGED_REPO_DOCS = /^main\/[^/]+\/docs\//;

/** 색인 대상인가: 메타레포 루트 문서, docs/ 아래, 관리 대상 repository(main/{repo})의 docs/ 아래 마크다운 */
export function isIndexablePath(relativePath: string): boolean {
  const rel = relativePath.split(path.sep).join("/");
  if (!DOC_EXTENSIONS.has(path.posix.extname(rel).toLowerCase())) return false;
  if (rel.split("/").some((segment) => IGNORED_SEGMENTS.has(segment))) return false;
  return ROOT_FILES.has(rel) || rel.startsWith("docs/") || MANAGED_REPO_DOCS.test(rel);
}

/** 사용자 입력 경로를 meta-repo 안의 절대/상대 경로로 바꾼다. 밖으로 나가면 실패한다. */
export function resolveInsideRoot(root: string, input: string): { absolute: string; relative: string } {
  const absolute = path.resolve(root, input);
  const relative = path.relative(root, absolute);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new Error(`meta-repository 밖의 경로는 적재할 수 없습니다: ${input}`);
  }
  return { absolute, relative: relative.split(path.sep).join("/") };
}
