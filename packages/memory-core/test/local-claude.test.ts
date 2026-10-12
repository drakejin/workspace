import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, test } from "vitest";
import { createLocalClaude, parseEnvelope } from "../src/llm/local-claude.js";

async function fakeClaude(script: string): Promise<string> {
  const dir = await mkdtemp(path.join(os.tmpdir(), "claude-"));
  const bin = path.join(dir, "claude");
  await writeFile(bin, `#!/usr/bin/env bash\n${script}\n`);
  await chmod(bin, 0o755);
  return bin;
}

const opts = (claudeBin: string) => ({ claudeBin, model: "opus", timeoutMs: 5000 });
const input = { system: "s", user: "본문", schema: {}, tag: "t" };

describe("createLocalClaude", () => {
  test("structured_output 을 돌려주고, 격리 플래그·stdin 본문·API 키 제거를 지킨다", async () => {
    const dir = await mkdtemp(path.join(os.tmpdir(), "args-"));
    const bin = await fakeClaude(
      `printf '%s\\n' "$@" > ${dir}/args; cat > ${dir}/stdin; echo "\${ANTHROPIC_API_KEY:-none}" > ${dir}/key\n` +
        `echo 'warning line'\necho '{"subtype":"success","structured_output":{"ok":true}}'`,
    );
    process.env.ANTHROPIC_API_KEY = "secret";
    try {
      expect(await createLocalClaude(opts(bin))<{ ok: boolean }>(input)).toEqual({ ok: true });
    } finally {
      delete process.env.ANTHROPIC_API_KEY;
    }
    const args = await readFile(path.join(dir, "args"), "utf8");
    expect(args).toContain("--strict-mcp-config");
    expect(args).toContain("--setting-sources");
    expect(await readFile(path.join(dir, "stdin"), "utf8")).toBe("본문");
    expect((await readFile(path.join(dir, "key"), "utf8")).trim()).toBe("none");
  });

  test("structured_output 이 없으면 result 텍스트의 JSON", async () => {
    const bin = await fakeClaude(`echo '{"subtype":"success","result":"답: {\\"a\\":1}"}'`);
    expect(await createLocalClaude(opts(bin))(input)).toEqual({ a: 1 });
  });

  test("실패·미설치·이상 출력은 던지지 않고 null", async () => {
    const logs: string[] = [];
    const log = (l: string) => logs.push(l);
    expect(await createLocalClaude(opts(await fakeClaude(`echo '{"subtype":"error_max_turns","is_error":true}'`)), log)(input)).toBeNull();
    expect(await createLocalClaude(opts(await fakeClaude("exit 3")), log)(input)).toBeNull();
    expect(await createLocalClaude(opts("/nonexistent/claude"), log)(input)).toBeNull();
    expect(await createLocalClaude(opts(await fakeClaude("echo not json")), log)(input)).toBeNull();
    expect(logs.some((l) => l.includes("미설치"))).toBe(true);
  });
});

describe("parseEnvelope", () => {
  test("마지막 JSON 줄", () => {
    expect(parseEnvelope('{"a":1}\nnoise\n{"b":2}')).toEqual({ b: 2 });
    expect(parseEnvelope("nothing")).toBeNull();
  });
});
