// 로컬 Claude Code CLI 를 LLM 백엔드로 쓴다 — API 키 없이, 머신에 로그인된 구독 인증으로.
// 격리: cwd=tmpdir + --setting-sources "" + --strict-mcp-config + --tools "" + --disable-slash-commands
//   → 이 레포의 CLAUDE.md·MCP·훅이 판정 프롬프트에 새지 않는다.
// 실패(미설치·미인증·타임아웃·스키마 불일치)는 던지지 않고 null — 호출측이 폴백 경로를 갖는다.
import { execFile } from "node:child_process";
import { tmpdir } from "node:os";

export interface LlmOptions {
  readonly claudeBin: string;
  readonly model: string;
  readonly timeoutMs: number;
}

export interface AskInput {
  readonly system: string;
  /** stdin 으로 넘긴다 (argv 길이 제한 회피) */
  readonly user: string;
  readonly schema: Record<string, unknown>;
  readonly tag: string;
}

export type AskLlm = <T>(input: AskInput) => Promise<T | null>;

interface CliEnvelope {
  readonly is_error?: boolean;
  readonly subtype?: string;
  readonly result?: string;
  readonly structured_output?: unknown;
}

export function createLocalClaude(options: LlmOptions, log: (line: string) => void = console.warn): AskLlm {
  return async <T>(input: AskInput): Promise<T | null> => {
    const args = [
      "-p",
      "--output-format", "json",
      "--model", options.model,
      "--system-prompt", input.system,
      "--json-schema", JSON.stringify(input.schema),
      "--tools", "",
      "--strict-mcp-config",
      "--disable-slash-commands",
      "--no-session-persistence",
      "--setting-sources", "",
    ];
    try {
      const stdout = await run(options.claudeBin, args, input.user, options.timeoutMs);
      const envelope = parseEnvelope(stdout);
      if (!envelope || envelope.is_error || envelope.subtype !== "success") {
        log(`[${input.tag}] 로컬 claude 실패(subtype=${envelope?.subtype ?? "파싱 불가"}) — 폴백`);
        return null;
      }
      if (envelope.structured_output && typeof envelope.structured_output === "object") {
        return envelope.structured_output as T;
      }
      const json = /\{[\s\S]*\}/.exec(envelope.result ?? "")?.[0];
      return json ? (JSON.parse(json) as T) : null;
    } catch (error) {
      log(`[${input.tag}] 로컬 claude 호출 실패 — 폴백: ${error instanceof Error ? error.message : String(error)}`);
      return null;
    }
  };
}

function run(bin: string, args: string[], stdin: string, timeoutMs: number): Promise<string> {
  const env = { ...process.env };
  // 키가 있으면 CLI 가 구독 대신 API 과금 경로를 탄다
  delete env.ANTHROPIC_API_KEY;
  delete env.ANTHROPIC_AUTH_TOKEN;
  return new Promise((resolve, reject) => {
    const child = execFile(
      bin,
      args,
      { cwd: tmpdir(), env, timeout: timeoutMs, maxBuffer: 16 * 1024 * 1024, encoding: "utf8" },
      (error, stdout, stderr) => {
        if (!error) return resolve(stdout);
        const code = (error as NodeJS.ErrnoException).code;
        reject(new Error(code === "ENOENT" ? `\`${bin}\` 미설치/PATH 부재` : (stderr || error.message).split("\n")[0]));
      },
    );
    child.stdin?.end(stdin);
  });
}

/** 한 줄 JSON envelope. 앞에 경고 줄이 섞여도 마지막 JSON 줄을 집는다 */
export function parseEnvelope(stdout: string): CliEnvelope | null {
  const lines = stdout.trim().split("\n").filter((l) => l.trim().startsWith("{")).reverse();
  for (const line of lines) {
    try {
      return JSON.parse(line) as CliEnvelope;
    } catch {
      // 다음 후보
    }
  }
  return null;
}
