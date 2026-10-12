import path from "node:path";
import { IGNORED_SEGMENTS, isIndexablePath } from "./paths.js";

/**
 * 스캔·watch 가 내려가도 되는 경로인가. 색인 대상이 있을 수 있는 길만 연다:
 * 루트, docs/**, main/, main/{repo}/, main/{repo}/docs/**. 관리 대상 repository 의 코드 트리는 보지 않는다.
 * (대형 트리를 통째로 watch 하면 macOS 에서 EMFILE 로 죽는다)
 */
export function isInScope(relativePath: string, isFile: boolean | undefined): boolean {
  const rel = relativePath.split(path.sep).join("/");
  if (rel === "" || rel === ".") return true;
  const parts = rel.split("/");
  if (parts.some((p) => IGNORED_SEGMENTS.has(p))) return false;
  if (isFile === true) return isIndexablePath(rel);
  if (parts.length === 1 && isFile === undefined && rel.includes(".")) return isIndexablePath(rel);
  const top = parts[0];
  if (top === "docs") return true;
  if (top !== "main") return false;
  return parts.length <= 2 || parts[2] === "docs";
}
