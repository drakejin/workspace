package ops

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
)

// helpCLI 는 OpenSearch·그래프·claude 없이 도는 CLI 다 — 도움말과 사용 오류는 어느 것도 부르지 않아야 한다.
func helpCLI(t *testing.T, out *bytes.Buffer) *CLI {
	t.Helper()
	root := t.TempDir()
	return &CLI{
		Config: config.Config{MetaRepoRoot: root, DefaultTeam: "ads", LLM: config.LLM{ClaudeBin: filepath.Join(root, "no-claude")}},
		Out:    out, Now: time.Now,
	}
}

// flagLine 은 도움말의 플래그 줄이다: 들여쓴 "-이름 [값 종류]" 다음에 설명.
var flagLine = regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)\b[^\n]*$`)

// DEAD-14: 플래그 설명은 '<command> -h' 로 보인다 — 예전에는 FlagSet 출력을 버려서 설명 문자열을 아무도 볼 수 없었다.
// 도움말은 OpenSearch 가 꺼져 있어도 나오고(사용 오류가 아니라 성공), 하위 명령을 먼저 받는 archive·graph 도 같다.
func TestCommandHelpPrintsFlagsWithDescriptions(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"search", []string{"-h"}, []string{"search [-project P]", "-superseded", "밀린·deprecate 된 문서도 본다", "-team string", "(기본 ads)"}},
		{"bench", []string{"-help"}, []string{"-max-turns int", "질문당 최대 턴", "(기본 15)", "-mcp-script string", "scripts/mcp/run.sh 절대 경로"}},
		{"archive", []string{"-h"}, []string{"archive <deprecate|undeprecate|purge|edit>", "-reason string", "왜"}},
		{"graph", []string{"-h"}, []string{"graph <neighbors", "-depth int", "(기본 2)"}},
		{"graph", []string{"neighbors", "-h"}, []string{"-depth int"}},
		{"restore", []string{"--help"}, []string{"restore <index>"}},
	}
	for _, c := range cases {
		t.Run(c.name+" "+strings.Join(c.args, " "), func(t *testing.T) {
			var out bytes.Buffer

			err := helpCLI(t, &out).Run(context.Background(), c.name, c.args)

			if err != nil {
				t.Fatalf("도움말은 성공이다: %v", err)
			}
			for _, want := range c.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("도움말에 %q 가 없다:\n%s", want, out.String())
				}
			}
		})
	}
}

// DEAD-14: 사용 오류(인자 부족·모르는 플래그)도 그 명령의 플래그 표를 보인다.
func TestUsageErrorListsCommandFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"-bogus"}} {
		err := helpCLI(t, &bytes.Buffer{}).Run(context.Background(), "bench", args)

		if !errors.Is(err, ErrUsage) {
			t.Fatalf("bench %v = %v", args, err)
		}
		for _, want := range []string{"bench -questions F", "-runs int", "조건마다 회차", "-max-turns int"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("bench %v 사용 오류에 %q 가 없다:\n%v", args, want, err)
			}
		}
	}
}

// DEAD-14: 사용법 표(memory-mcp help)는 명령마다 그 명령이 받는 플래그를 모두 적는다 — bench 의 -max-turns·-mcp-script 처럼
// 정의만 있고 표에 없는 숨은 플래그가 없다.
func TestUsageTableNamesEveryFlag(t *testing.T) {
	for name, cmd := range commands {
		var out bytes.Buffer
		if err := helpCLI(t, &out).Run(context.Background(), name, []string{"-h"}); err != nil {
			t.Errorf("%s -h = %v", name, err)
			continue
		}
		for _, m := range flagLine.FindAllStringSubmatch(out.String(), -1) {
			mention := regexp.MustCompile(`(^|[\s\[|])-` + regexp.QuoteMeta(m[1]) + `($|[\s\]])`)
			if !mention.MatchString(cmd.usage) {
				t.Errorf("%s 의 사용법 줄에 -%s 가 없다: %s", name, m[1], cmd.usage)
			}
		}
	}
}
