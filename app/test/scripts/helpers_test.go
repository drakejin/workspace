// Package scripts 는 scripts/·scenarios/ 의 bash 스크립트를 실제로 돌려 보는 테스트다.
// 각 테스트는 임시 디렉토리(가짜 HOME·TMPDIR·LaunchAgents)에 스크립트를 복사해 돌린다 —
// launchctl·docker 는 가짜 명령으로 바꾸고, 실제 스택·실제 워크스페이스에는 닿지 않는다.
package scripts

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// systemPath 는 가짜 PATH 의 뒤쪽 — 기본 유틸(sed, awk, sort, mktemp ...)이 있는 곳이다.
const systemPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// repoRoot 는 이 레포 루트다 (app/test/scripts 에서 세 단계 위).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

// sandbox 는 테스트 하나의 격리된 실행 환경이다.
type sandbox struct {
	t     *testing.T
	dir   string // 실제 경로(심볼릭 링크를 푼) 임시 디렉토리
	home  string
	stubs string // PATH 맨 앞 — 가짜 명령(launchctl, docker)
	extra []string
}

// newSandbox 는 빈 HOME·TMPDIR 과 가짜 명령 디렉토리를 만든다. tools 는 PATH 에 있어야 하는 명령이다 (없으면 건너뛴다).
func newSandbox(t *testing.T, tools ...string) *sandbox {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb := &sandbox{t: t, dir: dir, home: filepath.Join(dir, "home"), stubs: filepath.Join(dir, "stubs")}
	for _, d := range []string{sb.home, sb.stubs, filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sb.extra = toolDirs(t, append([]string{"bash"}, tools...))
	return sb
}

// toolDirs 는 명령들이 있는 디렉토리다. 하나라도 없으면 테스트를 건너뛴다.
func toolDirs(t *testing.T, tools []string) []string {
	t.Helper()
	var dirs []string
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s 가 없어 건너뜀", tool)
		}
		dirs = append(dirs, filepath.Dir(p))
	}
	return dirs
}

// requireDarwin 은 launchd·plutil 이 있는 macOS 에서만 도는 테스트다.
func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("launchd·plutil 은 macOS 에만 있다")
	}
}

// env 는 스크립트에 줄 환경 전부다 — 이 프로세스의 환경은 물려주지 않는다.
func (sb *sandbox) env(vars map[string]string) []string {
	path := strings.Join(append(append([]string{sb.stubs}, sb.extra...), systemPath), ":")
	out := []string{"HOME=" + sb.home, "TMPDIR=" + filepath.Join(sb.dir, "tmp"), "PATH=" + path}
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

// result 는 스크립트 한 번의 결과다.
type result struct {
	stdout, stderr string
	err            error
}

func (r result) output() string { return r.stdout + r.stderr }

// bash 는 bash 로 args 를 돌린다 (args[0] 은 스크립트 경로 또는 -c).
func (sb *sandbox) bash(vars map[string]string, args ...string) result {
	sb.t.Helper()
	cmd := exec.Command("bash", args...)
	cmd.Dir = sb.dir
	cmd.Env = sb.env(vars)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return result{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// mustBash 는 성공해야 하는 bash 실행이다.
func (sb *sandbox) mustBash(vars map[string]string, args ...string) result {
	sb.t.Helper()
	r := sb.bash(vars, args...)
	if r.err != nil {
		sb.t.Fatalf("bash %v: %v\n%s", args, r.err, r.output())
	}
	return r
}

// copyTo 는 레포의 파일·디렉토리(rel)를 dst 아래 같은 상대 경로로 복사한다 (실행 권한 유지).
// os 로 읽으므로 go test 캐시도 스크립트가 바뀌면 다시 돈다.
func (sb *sandbox) copyTo(dst string, rels ...string) {
	sb.t.Helper()
	src := repoRoot(sb.t)
	for _, rel := range rels {
		from := filepath.Join(src, rel)
		err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			to := filepath.Join(dst, rel, strings.TrimPrefix(p, from))
			if d.IsDir() {
				return os.MkdirAll(to, 0o755)
			}
			return copyFile(p, to)
		})
		if err != nil {
			sb.t.Fatalf("copy %s: %v", rel, err)
		}
	}
}

func copyFile(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, data, info.Mode().Perm())
}

// stub 은 PATH 맨 앞에 가짜 명령을 둔다. 부를 때마다 인자를 <dir>/<name>.log 에 한 줄로 적고 body 를 돈다.
func (sb *sandbox) stub(name, body string) string {
	sb.t.Helper()
	log := filepath.Join(sb.dir, name+".log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(sb.stubs, name), []byte(script), 0o755); err != nil {
		sb.t.Fatal(err)
	}
	return log
}

// calls 는 가짜 명령이 받은 호출들이다 (log 는 stub 의 반환값).
func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// plistJSON 은 plist 파일을 plutil 로 읽어 map 으로 돌려준다 (plutil -lint 도 겸한다).
func plistJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", file).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil %s: %v\n%s", file, err, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("plist json: %v\n%s", err, out)
	}
	return m
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
