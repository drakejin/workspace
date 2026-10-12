import { appendFile, mkdir, readFile, readdir } from "node:fs/promises";
import path from "node:path";

/**
 * 팀별 append-only JSONL 로그. judge-state/ 와 knowledge-cache/ 가 쓴다.
 * git 에 커밋되는 원장이라 인덱스가 날아가도 판정·증류 결과를 LLM 재호출 없이 복원한다.
 */
export interface JsonlLog<T> {
  readAll(team: string): Promise<T[]>;
  append(team: string, entry: T): Promise<void>;
  teams(): Promise<string[]>;
}

export function jsonlLog<T>(dir: string, onCorrupt: (file: string, line: number) => void = () => {}): JsonlLog<T> {
  const file = (team: string) => path.join(dir, `${team}.jsonl`);
  return {
    async readAll(team) {
      let body: string;
      try {
        body = await readFile(file(team), "utf8");
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") return [];
        throw error;
      }
      const out: T[] = [];
      body.split("\n").forEach((line, i) => {
        if (line.trim().length === 0) return;
        try {
          out.push(JSON.parse(line) as T);
        } catch {
          onCorrupt(file(team), i + 1);
        }
      });
      return out;
    },
    async append(team, entry) {
      await mkdir(dir, { recursive: true });
      await appendFile(file(team), `${JSON.stringify(entry)}\n`, "utf8");
    },
    async teams() {
      const names = await readdir(dir).catch(() => [] as string[]);
      return names.filter((n) => n.endsWith(".jsonl")).map((n) => n.slice(0, -".jsonl".length)).sort();
    },
  };
}
