import { open, readdir, stat } from "node:fs/promises";
import path from "node:path";
import { isIndexablePath } from "../ingest/paths.js";
import { isInScope } from "../ingest/scope.js";

export interface ScannedFile {
  readonly rel: string;
  readonly abs: string;
  readonly mtimeMs: number;
  readonly size: number;
}

export interface SkippedFile {
  readonly rel: string;
  readonly reason: string;
}

export interface ScanResult {
  readonly files: readonly ScannedFile[];
  readonly skipped: readonly SkippedFile[];
}

/** hot 스캔 — 색인 대상 파일과 stat. 내용은 읽지 않는다(평상시 비용 = stat-walk) */
export async function scanHot(root: string, maxFileBytes: number): Promise<ScanResult> {
  const files: ScannedFile[] = [];
  const skipped: SkippedFile[] = [];

  async function walk(dirRel: string): Promise<void> {
    const entries = await readdir(path.join(root, dirRel), { withFileTypes: true }).catch(() => []);
    for (const entry of entries) {
      const rel = dirRel ? `${dirRel}/${entry.name}` : entry.name;
      if (entry.isDirectory()) {
        if (isInScope(rel, false)) await walk(rel);
        continue;
      }
      if (!entry.isFile() || !isIndexablePath(rel)) continue;
      const abs = path.join(root, rel);
      const info = await stat(abs);
      if (info.size > maxFileBytes) {
        skipped.push({ rel, reason: `size ${info.size} > ${maxFileBytes}` });
        continue;
      }
      files.push({ rel, abs, mtimeMs: info.mtimeMs, size: info.size });
    }
  }

  await walk("");
  return { files: files.sort((a, b) => (a.rel < b.rel ? -1 : a.rel > b.rel ? 1 : 0)), skipped };
}

const BINARY_SNIFF_BYTES = 8000;

/** 앞부분에 NUL 바이트가 있으면 바이너리로 본다 */
export async function isProbablyBinary(abs: string): Promise<boolean> {
  const handle = await open(abs, "r");
  try {
    const buffer = Buffer.alloc(BINARY_SNIFF_BYTES);
    const { bytesRead } = await handle.read(buffer, 0, BINARY_SNIFF_BYTES, 0);
    return buffer.subarray(0, bytesRead).includes(0);
  } finally {
    await handle.close();
  }
}
