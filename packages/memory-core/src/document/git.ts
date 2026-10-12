import { execFile } from "node:child_process";
import path from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);

export interface GitFacts {
  readonly sha: string | null;
  readonly committedAt: Date | null;
}

const NONE: GitFacts = { sha: null, committedAt: null };

/** 파일을 마지막으로 바꾼 커밋. git 밖이거나 커밋 전 파일이면 둘 다 null. */
export async function lastCommitOf(absolutePath: string): Promise<GitFacts> {
  try {
    const { stdout } = await run("git", ["log", "-1", "--format=%H %cI", "--", path.basename(absolutePath)], {
      cwd: path.dirname(absolutePath),
      timeout: 5000,
    });
    const [sha, iso] = stdout.trim().split(" ");
    if (!sha || !iso) return NONE;
    return { sha, committedAt: new Date(iso) };
  } catch {
    return NONE;
  }
}
