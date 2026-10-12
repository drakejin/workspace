import type { MemoryConfig } from "../config.js";
import type { DistillFacts } from "../document/build-chunks.js";
import type { AskLlm } from "../llm/local-claude.js";
import type { OpenSearchPort } from "../opensearch/port.js";
import type { DistillCache } from "./distill-cache.js";
import { fetchIndexedDocs, managedIndices, updateBySource } from "./index-docs.js";
import type { DiffEntry } from "./reconcile.js";

const SYSTEM = `너는 팀 문서 코퍼스의 증류기다. 문서 하나를 받아 요약과 entity 를 산출한다.
규칙: entities 는 문서가 실제로 다루는 대상만(스쳐간 언급 제외), "type:name" 형식, type 은 service|repo|table|api|concept|person 중 하나.
summary 는 검색 결과 카드용 한두 문장, 한국어. 문서에 없는 내용을 추론하지 않는다.`;
const SCHEMA = {
  type: "object",
  properties: {
    summary: { type: "string" },
    entities: { type: "array", items: { type: "string" } },
  },
  required: ["summary", "entities"],
  additionalProperties: false,
};
const MAX_BODY_CHARS = 60_000;

export interface DistillJob {
  readonly team: string;
  readonly sourcePath: string;
  readonly bodySha: string;
  readonly title: string;
  readonly body: string;
}

export interface DistillDeps {
  readonly os: OpenSearchPort;
  readonly config: MemoryConfig;
  readonly cache: DistillCache;
  readonly ask: AskLlm;
  readonly log: (line: string) => void;
}

export interface DistillQueue {
  /** 변경분을 큐에 넣고 바로 돌아온다 (색인 사이클을 막지 않는다) */
  enqueue(diff: readonly DiffEntry[]): void;
  enqueueJobs(jobs: readonly DistillJob[]): void;
  /** 큐가 빌 때까지 기다린다 (--once 용) */
  settle(): Promise<void>;
  pending(): number;
}

/**
 * 증류 큐 (사이클 E). 캐시 키는 본문 sha — hit 면 LLM 0회, miss 면 로컬 claude 로 {summary, entities} 를 뽑아
 * knowledge-cache/{team}.jsonl 에 append 하고 인덱스에 반영한다. 실패는 그 문서만 건너뛰고 다음 사이클에 다시 본다.
 */
export function createDistillQueue(deps: DistillDeps): DistillQueue {
  const queue: DistillJob[] = [];
  const queued = new Set<string>();
  let active = 0;
  let idle: Array<() => void> = [];

  const key = (j: DistillJob) => `${j.team}\0${j.sourcePath}\0${j.bodySha}`;
  const notifyIdle = () => {
    if (active > 0 || queue.length > 0) return;
    const waiters = idle;
    idle = [];
    waiters.forEach((resolve) => resolve());
  };

  async function process(job: DistillJob): Promise<void> {
    let facts: DistillFacts | null = deps.cache.get(job.team, job.bodySha);
    if (!facts) {
      const raw = await deps.ask<{ summary?: string; entities?: string[] }>({
        system: SYSTEM,
        user: `title: ${job.title}\npath: ${job.sourcePath}\n\n--- 본문 ---\n${job.body.slice(0, MAX_BODY_CHARS)}`,
        schema: SCHEMA,
        tag: "distill",
      });
      if (!raw?.summary) {
        deps.log(`[distill] ${job.sourcePath}: 실패 — 다음 사이클 재시도`);
        return;
      }
      facts = { summary: raw.summary.trim(), entities: [...new Set((raw.entities ?? []).map((e) => e.trim()).filter(Boolean))] };
      await deps.cache.put(job.team, { ...facts, bodySha: job.bodySha, sourcePath: job.sourcePath, at: new Date().toISOString() });
      deps.log(`[distill] ${job.sourcePath}: summary ${facts.summary.length}자, entities ${facts.entities.length}`);
    }
    await updateBySource(deps.os, await managedIndices(deps.os), job.sourcePath, {
      summary: facts.summary,
      entities: facts.entities,
    });
  }

  function pump(): void {
    while (active < deps.config.distill.concurrency && queue.length > 0) {
      const job = queue.shift()!;
      active++;
      process(job)
        .catch((error: unknown) => deps.log(`[distill] ${job.sourcePath}: ${error instanceof Error ? error.message : String(error)}`))
        .finally(() => {
          active--;
          queued.delete(key(job));
          pump();
          notifyIdle();
        });
    }
  }

  const enqueueJobs = (jobs: readonly DistillJob[]) => {
    if (!deps.config.distill.enabled) return;
    for (const job of jobs) {
      if (queued.has(key(job))) continue;
      queued.add(key(job));
      queue.push(job);
    }
    pump();
  };

  return {
    enqueue(diff) {
      enqueueJobs(
        diff
          .filter((d) => d.change !== "removed")
          .map((d) => ({ team: d.team, sourcePath: d.sourcePath, bodySha: d.bodySha, title: d.title, body: d.body })),
      );
    },
    enqueueJobs,
    settle() {
      return new Promise((resolve) => {
        idle = [...idle, resolve];
        notifyIdle();
      });
    },
    pending: () => queue.length + active,
  };
}

/** 부팅 때 한 번 — summary 가 빈 문서를 다시 큐에 넣는다 (지난 실패·데몬 정지 중 변경 백필) */
export async function missingDistillJobs(
  os: OpenSearchPort,
  readBody: (sourcePath: string) => Promise<{ title: string; body: string } | null>,
): Promise<DistillJob[]> {
  const docs = await fetchIndexedDocs(os, await managedIndices(os));
  const jobs: DistillJob[] = [];
  for (const doc of docs.values()) {
    if (doc.summary || !doc.bodySha) continue;
    const content = await readBody(doc.sourcePath);
    if (content) jobs.push({ team: doc.team, sourcePath: doc.sourcePath, bodySha: doc.bodySha, ...content });
  }
  return jobs;
}
