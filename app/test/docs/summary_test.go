package docs

import (
	"regexp"
	"strings"
	"testing"
)

// fencedLines 는 문서의 펜스 코드 블록 안 줄들이다 (앞뒤 공백을 지우고, 빈 줄은 뺀다).
func fencedLines(content string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		if fence.MatchString(line) {
			inFence = !inFence
			continue
		}
		if s := strings.TrimSpace(line); inFence && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// guideSection 은 GUIDE 에서 heading 줄부터 다음 같은 단계 이상의 제목 전까지다.
func guideSection(t *testing.T, heading string) string {
	t.Helper()
	guide := readDoc(t, "GUIDE.md")
	i := strings.Index(guide, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("GUIDE 에 %q 절이 없다", heading)
	}
	rest := guide[i+len(heading)+2:]
	level := heading[:strings.Index(heading, " ")+1]
	for _, end := range []string{"\n" + level, "\n## ", "\n---\n"} {
		if j := strings.Index(rest, end); j >= 0 {
			rest = rest[:j]
		}
	}
	return rest
}

// 설치 출력 예가 있는 GUIDE §2 의 절 — README 는 이 앵커로 링크한다.
const (
	guideInit   = "### 2.1 `make memory-init` — 인프라 올리기 (멱등)"
	guideStart  = "### 2.2 `make memory-daemon-start` — 상주"
	guideStatus = "### 2.3 `scripts/memory-daemon/run.sh status`"
)

// DOC-7: README 는 GUIDE 의 요약이다 — 프로그램 출력 예([memory-init]·[memory-daemon]·[status] … 줄)는 GUIDE 에만 두고 README 는
// 그 절로 링크한다. 두 문서의 코드 블록이 같은 출력 줄을 나눠 가지면 숫자(docs·chunks·pid)가 바뀔 때 한쪽만 고쳐져 어긋난다.
func TestReadmeLinksGuideOutputInsteadOfCopying(t *testing.T) {
	guide := map[string]bool{}
	for _, line := range fencedLines(readDoc(t, "GUIDE.md")) {
		guide[line] = true
	}
	readme := readDoc(t, "README.md")
	for _, line := range fencedLines(readme) {
		if strings.HasPrefix(line, "[") && guide[line] {
			t.Errorf("README 가 GUIDE 의 출력 줄을 복사했다: %q", line)
		}
	}
	for _, heading := range []string{guideInit, guideStart, guideStatus} {
		anchor := githubSlug(headingText(strings.TrimLeft(heading, "# ")))
		if !strings.Contains(readme, "](./GUIDE.md#"+anchor+")") {
			t.Errorf("README 가 GUIDE 의 출력 예 %q (#%s) 로 링크하지 않는다", heading, anchor)
		}
	}
}

var (
	startedPID  = regexp.MustCompile(`\[memory-daemon\] started \(pid (\d+)\)`)
	runningPID  = regexp.MustCompile(`\[status\] daemon: running pid=(\d+) `)
	startLabel  = regexp.MustCompile(`\[memory-daemon\] launchd: com\.drakejin\.memory\.([a-z0-9-]+)\.daemon \(workspace=([a-z0-9-]+),`)
	loadedLabel = regexp.MustCompile(`\[memory-daemon\] launchd: loaded \(com\.drakejin\.memory\.([a-z0-9-]+)\.daemon\)`)
	indexLine   = regexp.MustCompile(`\[status\] index: workspace=([a-z0-9-]+) \d+ docs, \d+ chunks \(memory indices \d+\)`)
)

// submatch 는 re 의 첫 그룹들이다 (맞지 않으면 실패).
func submatch(t *testing.T, re *regexp.Regexp, where, text string) []string {
	t.Helper()
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("%s 에 %s 꼴의 줄이 없다", where, re)
	}
	return m[1:]
}

// R4-query-b-2: status 출력 예는 GUIDE §2.3 한 곳에만 있고, 'memory indices' 가 무엇을 세는지(이 workspace 의 팀 인덱스) 그 옆에서 밝힌다.
// §2.2·§2.3 의 출력은 한 번의 실행에서 찍은 것이다 — start 의 pid 와 status 의 pid, launchd 잡의 workspace 와 index 줄의 workspace 가 같다.
func TestStatusSampleLivesInGuideAndIsOneRun(t *testing.T) {
	if strings.Contains(readDoc(t, "README.md"), "(memory indices ") {
		t.Error("README 에 status 출력 예가 남았다 — GUIDE §2.3 에만 둔다")
	}
	status := guideSection(t, guideStatus)
	if !strings.Contains(status, "`memory indices` 는 이 workspace 의 팀") {
		t.Errorf("GUIDE §2.3 이 memory indices 가 이 workspace 의 팀 인덱스 수라고 밝히지 않는다:\n%s", status)
	}
	start := guideSection(t, guideStart)
	if pid, want := submatch(t, runningPID, "§2.3", status)[0], submatch(t, startedPID, "§2.2", start)[0]; pid != want {
		t.Errorf("§2.3 status 의 pid %s 가 §2.2 start 의 pid %s 와 다르다 — 한 번의 실행에서 찍는다", pid, want)
	}
	label := submatch(t, startLabel, "§2.2", start)
	loaded := submatch(t, loadedLabel, "§2.3", status)[0]
	index := submatch(t, indexLine, "§2.3", status)[0]
	if label[0] != label[1] || loaded != label[0] || index != label[0] {
		t.Errorf("출력 예의 workspace 가 서로 다르다: start 잡 %s (workspace=%s), status 잡 %s, index 줄 %s", label[0], label[1], loaded, index)
	}
}

// R4-ops-cold-2 · R4-ops-scripts-3: 문서가 동작보다 많이 약속하지 않는다.
func TestDocsDoNotOverclaim(t *testing.T) {
	checkFacts(t, []fact{
		// tier 가 지운 원본이 git 히스토리에 남는 것은 git 이 meta-repo 아래 파일을 다룰 때뿐이다 (cold/tier.go gitWorkTree)
		{id: "R4-ops-cold-2 (1)", file: "README.md",
			must:    []string{"git 이 다루지 않는 meta-repo"},
			mustNot: []string{"git 히스토리에도 커밋된 판이 있다"}},
		{id: "R4-ops-cold-2 (1)", file: "GUIDE.md", must: []string{"git 이 다루지 않는 meta-repo"}},
		// git 이 레포를 거부하면(dubious ownership) tier 는 원본을 지우지 않고 멈춘다
		{id: "R4-ops-cold-2 (3)", file: "GUIDE.md", must: []string{"safe.directory", "를 확인하지 못해 hot 원본을 지우지 않았습니다"}},
	})
	hotDays := lineWith(t, readDoc(t, "GUIDE.md"), "| `MEMORY_HOT_DAYS` |")
	if !strings.Contains(hotDays, "`scripts/memory/run.sh bootstrap`") {
		t.Errorf("R4-ops-cold-2 (2): MEMORY_HOT_DAYS 줄이 ISM 정책을 고치는 것을 scripts/memory/run.sh bootstrap 으로 밝히지 않는다: %q", hotDays)
	}
	readme := readDoc(t, "README.md")
	// schedule/ 의 launchd.label 은 uninstall·status 가 깐 잡을 찾는 기록이다 — 지워도 되는 것으로 묶지 않는다
	schedule := lineWith(t, readme, "├── schedule/")
	if !strings.Contains(schedule, "launchd.label") || strings.Contains(schedule, "지워도 된다") {
		t.Errorf("R4-ops-scripts-3 (1): .cache 트리의 schedule/ 줄: %q", schedule)
	}
	// make init 은 sync 를 부르므로 pull 에 실패한 repo 가 있으면 exit 1 로 끝난다 (clone 에 실패하면 그 전에)
	initRow := lineWith(t, readme, "| `make init` |")
	if !strings.Contains(initRow, "exit 1") {
		t.Errorf("R4-ops-scripts-3 (2): make init 줄이 실패 시 exit 1 을 밝히지 않는다: %q", initRow)
	}
}

// R5-final-query-1 · R5-final-query-2 · R5-me-1 · R5-final-daemon-1 · R5-final-daemon-2 · R5-final-docs-1 · R5-final-docs-2 ·
// R5-final-docs-4: 마지막 검토에서 나온, 문서가 동작보다 많이 (또는 다르게) 약속하던 문장.
func TestDocsMatchBehaviourAfterFinalReview(t *testing.T) {
	checkFacts(t, []fact{
		// 겹침에서 +00:00 쪽을 고르는 규칙은 적힌(시간대 없는) 벽시계에만 맞다 — date math 의 더하기·반올림이 겹침에 닿으면 계산하던
		// 시각의 오프셋을 잇는다. 틈은 둘 다 틈 길이만큼 늦게 읽는다 (런던 `2026-03-30T01:30||-1d` 는 BST 를 잇지 않고 02:30 BST)
		{id: "R5-final-query-1", file: "GUIDE.md",
			must: []string{"겹침에 닿으면 계산하던 시각의 오프셋을 잇는다", "`2026-10-24T01:30||+1d`",
				"date math 의 더하기·반올림이 닿은 시각이든 틈 길이만큼 늦게", "`2026-03-30T01:30||-1d`"},
			mustNot: []string{"시간대 없는 시각과 date math 는 서머타임이 바뀌는 날도 OpenSearch(java.time)처럼 읽는다 — 없는 시각(틈)은",
				"틈·겹침에 닿으면 이 규칙이 아니라"}},
		// 시간대 규칙은 이 머신(Go)의 tzdata 로 읽는다 — OpenSearch 의 JDK 와 판이 다른 시간대는 바뀌는 날 근처에서 다를 수 있다
		{id: "R5-final-query-2 · R5-me-1", file: "GUIDE.md", must: []string{"tzdata", "Europe/Chisinau", "Africa/Casablanca"}},
		// 이 판 전에 복원해 둔 달은 데몬이 고치지 않는다 — restore 를 다시 해야 빈 text 가 지워진다
		{id: "R5-final-daemon-2", file: "GUIDE.md", must: []string{"이 판 전에 복원해 둔 달"}},
		// 예전 tier 잡의 checkout 이 없어졌으면 어느 install·uninstall 에서든 내린다
		// 닿지 못할 뿐인 checkout(빠진 볼륨·권한)은 지워진 것인지 모르므로 내리지 않고 직접 내리는 명령을 알린다
		{id: "R5-final-docs-1", file: "README.md", must: []string{"checkout 이 없어졌으면", "닿지 못할 뿐이면 그대로 두고"}},
		// tier 는 git 이 다루는 meta-repo 에서만 git rm 한다 (커밋하지 않은 원본은 남기고, git 이 다루지 않으면 파일을 지운다)
		{id: "R5-final-docs-4", file: ".claude/commands/compaction.md",
			must:    []string{"커밋된 그대로인 원본만 `git rm`"},
			mustNot: []string{"hot 원본 제거(`git rm`)."}},
		// `(git rm)` 요약 뒤에 `hot 에 남김` 요약 줄이 올 수 있다 — `(git rm)` 은 끝줄이 아닐 수 있다
		{id: "R5-final-docs-4", file: ".claude/commands/compaction.md",
			must:    []string{"`(git rm)` 줄이 있으면"},
			mustNot: []string{"끝줄이 `(git rm)`"}},
		{id: "R5-final-docs-4", file: "GUIDE.md", must: []string{"(파일 삭제 — git 이 다루지 않는 meta-repo)"}},
	})
	// §2.2 의 출력 출처: 이 레포에서 부르면 이름·경로만 바뀌는 것이 아니다 — 수(docs·chunks·memory indices)·pid·시각도 다르다
	start := guideSection(t, guideStart)
	if !strings.Contains(start, "수(docs·chunks·`memory indices`)·pid·시각은") || strings.Contains(start, "경로 자리에 이 레포의 경로가 나온다.") {
		t.Errorf("R5-final-docs-2: §2.2 의 출력 출처가 이름·경로만 바뀐다고 한다:\n%s", start)
	}
	// status 의 마지막 사이클 줄은 cold_kept 도 보인다 (데몬 로그 줄과 같은 칸)
	if line := lineWith(t, guideSection(t, guideStatus), "[status] last cycle:"); !strings.Contains(line, "cold_owned=0 cold_kept=0 ") {
		t.Errorf("R5-final-daemon-1: §2.3 status 의 last cycle 줄에 cold_kept 가 없다: %q", line)
	}
}
