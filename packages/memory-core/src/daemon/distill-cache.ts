import path from "node:path";
import type { DistillFacts } from "../document/build-chunks.js";
import { jsonlLog, type JsonlLog } from "./jsonl-log.js";

export interface DistillRecord extends DistillFacts {
  readonly bodySha: string;
  readonly sourcePath: string;
  readonly at: string;
}

/** 증류 캐시 — 키는 본문 sha. hit 면 LLM 0회. 메모리에 올려 두고 append 할 때 같이 갱신한다 */
export interface DistillCache {
  readonly log: JsonlLog<DistillRecord>;
  load(): Promise<void>;
  get(team: string, bodySha: string): DistillFacts | null;
  put(team: string, record: DistillRecord): Promise<void>;
  size(team?: string): number;
}

export function createDistillCache(root: string): DistillCache {
  const log = jsonlLog<DistillRecord>(path.join(root, "knowledge-cache"));
  const byTeam = new Map<string, Map<string, DistillRecord>>();
  const teamMap = (team: string) => {
    const existing = byTeam.get(team);
    if (existing) return existing;
    const created = new Map<string, DistillRecord>();
    byTeam.set(team, created);
    return created;
  };
  return {
    log,
    async load() {
      byTeam.clear();
      for (const team of await log.teams()) {
        for (const r of await log.readAll(team)) teamMap(team).set(r.bodySha, r);
      }
    },
    get(team, bodySha) {
      const r = byTeam.get(team)?.get(bodySha);
      return r ? { summary: r.summary, entities: r.entities } : null;
    },
    async put(team, record) {
      await log.append(team, record);
      teamMap(team).set(record.bodySha, record);
    },
    size(team) {
      if (team) return byTeam.get(team)?.size ?? 0;
      return [...byTeam.values()].reduce((n, m) => n + m.size, 0);
    },
  };
}
