#!/usr/bin/env bash
# Claude Code 에게 시나리오 meta-repo 의 drakejin-workspace-memory-mcp 를 붙여 프롬프트 하나를 시키고,
# 무엇을 불렀고(도구 이름·입력) 무엇을 받았고(도구 결과) 뭐라고 답했는지를 그대로 보여 준다.
# 사용법: source <root>/.scenario.env && scenarios/claude-mcp.sh "<프롬프트>"
#   CLAUDE_MODEL (기본 haiku), CLAUDE_MAX_TURNS (기본 8)
# 사용자 설정(훅·플러그인)은 끄고(--setting-sources ""), 시나리오의 CLAUDE.md 를 규칙으로 붙인다. 내장 도구는 Read 만.
set -euo pipefail

PROMPT="${1:-}"
if [[ -z "$PROMPT" || -z "${META_REPO_ROOT:-}" ]]; then
    echo "usage: source <root>/.scenario.env && $0 \"<프롬프트>\"" >&2
    exit 2
fi
CONFIG="${META_REPO_ROOT}/.scenario-mcp.json"
[[ -f "$CONFIG" ]] || { echo "[claude-mcp] ${CONFIG} 가 없습니다 — scenarios/setup.sh 먼저" >&2; exit 1; }
SERVER="drakejin-workspace-memory-mcp"
TOOLS=""
for t in search_memory search_filters related_docs docs_by_entity list_cold restore_cold ingest_path; do
    TOOLS="${TOOLS:+${TOOLS},}mcp__${SERVER}__${t}"
done

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT
(cd "$META_REPO_ROOT" && claude -p "$PROMPT" \
    --model "${CLAUDE_MODEL:-haiku}" --max-turns "${CLAUDE_MAX_TURNS:-8}" \
    --output-format stream-json --verbose \
    --setting-sources "" --strict-mcp-config --mcp-config "$CONFIG" \
    --append-system-prompt-file "${META_REPO_ROOT}/CLAUDE.md" \
    --tools "Read" --allowedTools "Read,${TOOLS}" \
    --disable-slash-commands --no-session-persistence < /dev/null > "$OUT" 2>&1) || true

python3 - "$OUT" <<'PY'
import json, sys
servers, calls = [], 0
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        e = json.loads(line)
    except ValueError:
        if line.strip():
            print("== raw", line.rstrip())
        continue
    t = e.get("type")
    if t == "system" and e.get("subtype") == "init":
        servers = e.get("mcp_servers", [])
        print("== mcp", ", ".join(f"{s.get('name')}:{s.get('status')}" for s in servers))
        tools = [x for x in e.get("tools", []) if x.startswith("mcp__")]
        print("== tools", ", ".join(sorted(x.split("__")[-1] for x in tools)))
    elif t == "assistant":
        for c in e["message"]["content"]:
            if c.get("type") == "tool_use":
                calls += 1
                print(f"== tool_use {c['name'].split('__')[-1]} {json.dumps(c.get('input', {}), ensure_ascii=False)}")
    elif t == "user":
        for c in e.get("message", {}).get("content", []):
            if isinstance(c, dict) and c.get("type") == "tool_result":
                content = c.get("content")
                if isinstance(content, list):
                    content = "\n".join(x.get("text", "") for x in content if isinstance(x, dict))
                mark = " (error)" if c.get("is_error") else ""
                print(f"== tool_result{mark}\n{content}")
    elif t == "result":
        print("== answer")
        print(e.get("result", ""))
        print(f"== stats tool_calls={calls} turns={e.get('num_turns')} cost=${e.get('total_cost_usd', 0):.4f} error={e.get('is_error')}")
PY
