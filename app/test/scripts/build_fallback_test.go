package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenBuildRoot 는 go build 가 컴파일 오류로 실패하는 meta-repo 다 (가짜 go). withBinary 면 .cache/bin 에 예전 바이너리가 있다.
func brokenBuildRoot(t *testing.T, withBinary bool) (*sandbox, string) {
	t.Helper()
	sb := newSandbox(t)
	root := filepath.Join(sb.dir, "meta")
	sb.copyTo(root, "scripts/memory-common.sh", "scripts/memory-daemon", "scripts/mcp")
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb.stub("go", `echo "./main.go:3:1: syntax error: unexpected }" >&2; exit 1`)
	if withBinary {
		for _, bin := range []string{"memory-daemon", "memory-mcp"} {
			writeExec(t, filepath.Join(root, ".cache", "bin", bin), "#!/bin/sh\necho \"old-"+bin+" $*\"\n")
		}
	}
	return sb, root
}

// OPS-8: 상주 진입점(launchd 가 부르는 run, .mcp.json 이 부르는 mcp)은 작업 트리가 컴파일되지 않아도 기존 바이너리로 뜬다 —
// 빌드 실패로 끝나면 launchd 는 30초마다 재시작을 반복하고 Claude Code 의 메모리 MCP 가 붙지 않는다.
func TestResidentEntriesFallBackToExistingBinaryWhenBuildFails(t *testing.T) {
	cases := []struct {
		name, script, arg, want string
	}{
		{"daemon run", "scripts/memory-daemon/run.sh", "run", "old-memory-daemon \n"},
		{"mcp", "scripts/mcp/run.sh", "", "old-memory-mcp serve\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb, root := brokenBuildRoot(t, true)
			args := []string{filepath.Join(root, tc.script)}
			if tc.arg != "" {
				args = append(args, tc.arg)
			}

			r := sb.bash(nil, args...)

			if r.err != nil {
				t.Fatalf("빌드가 실패해도 기존 바이너리로 떠야 한다: %v\n%s", r.err, r.output())
			}
			if !strings.HasSuffix(r.stdout, tc.want) {
				t.Errorf("기존 바이너리를 실행하지 않았다 — stdout = %q, want suffix %q", r.stdout, tc.want)
			}
			if !strings.Contains(r.stderr, "syntax error") || !strings.Contains(r.stderr, "기존") {
				t.Errorf("빌드 오류와 기존 바이너리로 계속한다는 경고를 stderr 에 남겨야 한다:\n%s", r.stderr)
			}
		})
	}
}

// OPS-8: MCP 의 stdout 은 프로토콜 전용이다 — 빌드 실패 경고가 stdout 에 섞이지 않는다.
func TestMCPBuildFallbackKeepsStdoutClean(t *testing.T) {
	sb, root := brokenBuildRoot(t, true)
	r := sb.mustBash(nil, filepath.Join(root, "scripts", "mcp", "run.sh"))
	if r.stdout != "old-memory-mcp serve\n" {
		t.Errorf("stdout 에 바이너리 출력 말고 다른 것이 섞였다: %q", r.stdout)
	}
}

// OPS-8: 바이너리가 아직 없으면(처음 빌드가 실패) 기댈 것이 없으니 실패한다.
func TestResidentEntriesFailWithoutBinaryWhenBuildFails(t *testing.T) {
	for _, entry := range [][]string{{"scripts/memory-daemon/run.sh", "run"}, {"scripts/mcp/run.sh"}} {
		t.Run(entry[0], func(t *testing.T) {
			sb, root := brokenBuildRoot(t, false)
			args := append([]string{filepath.Join(root, entry[0])}, entry[1:]...)

			r := sb.bash(nil, args...)

			if r.err == nil {
				t.Fatalf("빌드도 바이너리도 없는데 성공(exit 0)했다:\n%s", r.output())
			}
			if !strings.Contains(r.stderr, "syntax error") {
				t.Errorf("빌드 오류를 보여 줘야 한다:\n%s", r.stderr)
			}
		})
	}
}

// writeExec 는 실행 파일을 쓴다.
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
