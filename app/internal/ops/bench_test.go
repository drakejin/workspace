package ops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeQuestions 는 meta-repo 밖에 질문 파일 하나를 둔다.
func writeQuestions(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bench.yaml")
	if err := os.WriteFile(p, []byte("questions:\n  - id: q\n    prompt: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// API-12: -modes 오타(wiht)나 끝 공백('with ')은 claude 를 --mcp-config "" 로 돌려 memory 도구 없이 잰 결과를 남겼다.
// 모르는 조건은 claude 를 찾기도 전에 사용 오류다.
func TestBenchRejectsUnknownModes(t *testing.T) {
	questions := writeQuestions(t)
	for _, modes := range []string{"wiht", "with,", "with,without,both"} {
		err := helpCLI(t, &bytes.Buffer{}).Run(context.Background(), "bench", []string{"-questions", questions, "-modes", modes})

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "조건") {
			t.Errorf("-modes %q = %v — 모르는 조건은 사용 오류", modes, err)
		}
	}
}

// R4-query-b-4: 사용 오류는 명령 이름을 한 번만 적는다 — bench.Options.Validate 의 에러가 이미 "bench:" 로 시작해
// "usage: bench: bench: 모르는 측정 조건" 처럼 겹쳤다. 이름이 없는 원인에는 그대로 붙인다.
func TestUsageErrorNamesTheCommandOnce(t *testing.T) {
	err := helpCLI(t, &bytes.Buffer{}).Run(context.Background(), "bench", []string{"-questions", writeQuestions(t), "-modes", "wiht"})
	if !errors.Is(err, ErrUsage) || strings.Count(err.Error(), "bench:") != 1 || !strings.Contains(err.Error(), `bench: 모르는 측정 조건 "wiht"`) {
		t.Errorf("bench 사용 오류 = %v", err)
	}

	err = helpCLI(t, &bytes.Buffer{}).Run(context.Background(), "search", []string{"질의어", "-topic", "t"})
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "search: 플래그 -topic") {
		t.Errorf("이름이 없는 원인에는 명령 이름을 붙인다: %v", err)
	}
}

// API-12: 공백이 붙은 조건 이름은 떼어 읽는다 — 'with ,without' 이 with 를 memory 도구 없이 재지 않게. 그 뒤 검사는 claude 찾기다.
func TestBenchTrimsModeNames(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := helpCLI(t, &bytes.Buffer{}).Run(context.Background(), "bench",
		[]string{"-questions", writeQuestions(t), "-modes", "with , without", "-mcp-script", script})

	if err == nil || errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "claude CLI") {
		t.Errorf("조건 검사는 통과하고 claude 찾기에서 실패해야 한다: %v", err)
	}
}

// API-12: with 조건이 부를 -mcp-script 가 없으면 MCP 서버가 뜨지 못한 채 with 를 잰다 — 시작 전에 거부한다.
// without 만 재면 스크립트는 필요 없다.
func TestBenchRequiresTheMCPScriptOnlyForWith(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.sh")
	cli := helpCLI(t, &bytes.Buffer{})

	err := cli.Run(context.Background(), "bench", []string{"-questions", writeQuestions(t), "-mcp-script", missing})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("없는 -mcp-script = %v", err)
	}

	err = cli.Run(context.Background(), "bench", []string{"-questions", writeQuestions(t), "-modes", "without", "-mcp-script", missing})
	if err == nil || strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "claude CLI") {
		t.Errorf("without 만이면 스크립트를 보지 않는다: %v", err)
	}
}
