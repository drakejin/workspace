import type { Indexer } from "../ingest/indexer.js";
import type { MemoryChunk } from "../types.js";
import { coldKeys, type ColdStore } from "./cold-store.js";

export interface ReloadReport {
  readonly chunks: number;
  /** 파싱하지 못한 줄이 있는 키와 그 줄 번호(1부터). 나머지 줄과 다른 키는 계속 적재한다. */
  readonly corrupted: ReadonlyArray<{ key: string; lines: number[] }>;
}

/**
 * cold store의 청크 JSONL을 그대로 _bulk로 다시 넣는다.
 * 매핑을 바꾸거나 나중에 임베딩을 붙일 때 원본 파싱 없이 전체 재적재하는 길.
 */
export async function reloadFromColdStore(
  coldStore: ColdStore,
  indexer: Indexer,
  team: string,
  filter: (chunk: MemoryChunk) => boolean = () => true,
): Promise<ReloadReport> {
  const keys = (await coldStore.list(coldKeys.chunksPrefix(team))).filter((k) => k.endsWith(".jsonl"));
  let chunks = 0;
  const corrupted: Array<{ key: string; lines: number[] }> = [];
  for (const key of keys) {
    const body = await coldStore.get(key);
    if (!body) continue;
    const { parsed, badLines } = parseJsonl(body);
    if (badLines.length > 0) corrupted.push({ key, lines: badLines });
    chunks += await indexer.bulkChunks(parsed.filter(filter));
  }
  return { chunks, corrupted };
}

export function parseJsonl(body: string): { parsed: MemoryChunk[]; badLines: number[] } {
  const parsed: MemoryChunk[] = [];
  const badLines: number[] = [];
  body.split("\n").forEach((line, i) => {
    if (line.trim().length === 0) return;
    try {
      parsed.push(JSON.parse(line) as MemoryChunk);
    } catch {
      badLines.push(i + 1);
    }
  });
  return { parsed, badLines };
}
