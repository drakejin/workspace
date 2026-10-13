package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/markdown"
)

// badFrontmatterValue 는 Prepare 가 frontmatter 값(team 이름) 때문에 실패한 파일을 알린다. 그 판은 색인되지 않아
// 색인에는 고치기 전 판이 계속 남는다 — 데몬 로그 한 줄만으로는 놓친다. 다른 실패(게이트·읽기)는 보고하지 않는다.
func (r *runner) badFrontmatterValue(rel string, err error) {
	var fe *markdown.FrontmatterError
	if !errors.As(err, &fe) {
		return
	}
	r.add(badFrontmatter, rel, "frontmatter 값이 잘못됐다 — 이 판은 색인하지 않는다 (색인에는 고치기 전 판이 남는다): "+fe.Error(), false)
}

// noteFrontmatter 는 Prepare 한 파일의 frontmatter 를 본다: YAML 이 깨졌으면 알리고(메타데이터 없이 원문 전체가 본문으로
// 색인된다), supersedes 선언은 순환 검사(supersedesDeclarations)를 위해 모은다.
func (r *runner) noteFrontmatter(rel string, b document.Built) {
	if b.FrontmatterErr != nil {
		r.add(badFrontmatter, rel, "frontmatter YAML 이 깨졌다 — 메타데이터(team·date·supersedes …) 없이 원문 전체를 본문으로 색인한다: "+b.FrontmatterErr.Error(), false)
	}
	if b.Meta.Supersedes == "" {
		return
	}
	if r.declared == nil {
		r.declared = map[string]string{}
	}
	r.declared[rel] = b.Meta.Supersedes
}

// supersedesDeclarations 는 수렴(daemon 의 desiredSupersededBy)이 말없이 버리는 supersedes 선언을 알린다: 자기 자신을 가리키는 선언,
// 순환하는 선언(A↔B — 어느 쪽도 밀리지 않고 그래프에도 관계가 없다). 고리 하나는 한 번만, 가장 앞선 경로로 알린다.
func (r *runner) supersedesDeclarations(_ context.Context) error {
	starts := make([]string, 0, len(r.declared))
	for rel := range r.declared {
		starts = append(starts, rel)
	}
	sort.Strings(starts)
	reported := map[string]bool{}
	for _, start := range starts {
		if r.declared[start] == start {
			r.add(supersedesSelf, start, "frontmatter supersedes 가 자기 자신을 가리킨다 — 선언을 무시한다", false)
			continue
		}
		cycle := cycleFrom(r.declared, start)
		if cycle == nil || reported[start] {
			continue
		}
		for _, rel := range cycle {
			reported[rel] = true
		}
		loop := strings.Join(append(cycle, start), " → ")
		r.add(supersedesCycle, start, fmt.Sprintf("frontmatter supersedes 가 순환한다 (%s) — 순환한 선언은 모두 무시돼 어느 쪽도 밀리지 않는다", loop), false)
	}
	return nil
}

// cycleFrom 은 start 에서 선언을 따라가 start 로 돌아오면 그 고리(start 부터)다. 끝나거나 다른 고리로 들어가면 nil.
func cycleFrom(declared map[string]string, start string) []string {
	path := []string{start}
	seen := map[string]bool{start: true}
	for cur := declared[start]; ; cur = declared[cur] {
		if cur == start {
			return path
		}
		if _, ok := declared[cur]; !ok || seen[cur] {
			return nil
		}
		seen[cur] = true
		path = append(path, cur)
	}
}
