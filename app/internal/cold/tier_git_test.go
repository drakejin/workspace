package cold

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
)

// gitIn 은 root 에서 git 을 돌린다 (테스트용 이름·메일).
func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeHot(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	mustDo(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	mustDo(t, os.WriteFile(abs, []byte(body), 0o644))
}

// OPS-3: tier 는 git 이 되살릴 수 없는 hot 원본 — 추적하지 않는 파일, 커밋 뒤 고친(또는 add 만 한) 파일 — 을 지우지 않는다.
// 그 내용은 HEAD 에 없어 지우면 .gitignore 된 cold store 에만 남는다. 남긴 파일은 결정에 따로 알린다.
func TestRemoveHotOriginalsKeepsFilesGitCannotRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocalStore(t.TempDir())
	gitIn(t, root, "init", "-q")
	writeHot(t, root, "docs/specs/committed.md", "# 커밋됨\n")
	writeHot(t, root, "docs/specs/edited.md", "# 커밋한 판\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "fixture")
	writeHot(t, root, "docs/specs/edited.md", "# 커밋 뒤 고친 판\n")
	writeHot(t, root, "docs/specs/staged.md", "# add 만 했다\n")
	gitIn(t, root, "add", "docs/specs/staged.md")
	writeHot(t, root, "docs/specs/untracked.md", "# 커밋한 적 없다\n")

	var docs []sourceDoc
	for _, rel := range []string{"docs/specs/committed.md", "docs/specs/edited.md", "docs/specs/staged.md", "docs/specs/untracked.md"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		mustDo(t, err)
		mustDo(t, store.Put(ctx, RawKey("t", rel), string(body))) // cold store 에는 지금 내용이 있다
		docs = append(docs, sourceDoc{team: "t", sourcePath: rel, contentSHA: document.SHA256(string(body))})
	}

	removed, kept, err := removeHotOriginals(ctx, store, root, docs)

	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "docs/specs/committed.md" {
		t.Errorf("지운 원본 = %v (HEAD 와 같은 파일만)", removed)
	}
	sort.Strings(kept)
	if want := "docs/specs/edited.md,docs/specs/staged.md,docs/specs/untracked.md"; strings.Join(kept, ",") != want {
		t.Errorf("남긴 원본 = %v, want %s", kept, want)
	}
	for _, rel := range kept {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s 는 git 이 되살릴 수 없어 남긴다: %v", rel, err)
		}
	}
	if status := gitIn(t, root, "status", "--porcelain", "--", "docs/specs/committed.md"); !strings.HasPrefix(status, "D ") {
		t.Errorf("커밋된 원본은 git rm 으로 지워 다음 커밋에 들어간다: %q", status)
	}
}

// git 레포가 아닌 루트(테스트·임시 meta-repo)는 예전처럼 cold store raw 확인만으로 지운다 — 되살릴 git 히스토리가 처음부터 없다.
func TestRemoveHotOriginalsOutsideGitRemovesVerifiedCopies(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocalStore(t.TempDir())
	writeHot(t, root, "docs/specs/a.md", "# a\n")
	mustDo(t, store.Put(ctx, RawKey("t", "docs/specs/a.md"), "# a\n"))

	removed, kept, err := removeHotOriginals(ctx, store, root, []sourceDoc{{team: "t", sourcePath: "docs/specs/a.md", contentSHA: document.SHA256("# a\n")}})

	if err != nil || strings.Join(removed, ",") != "docs/specs/a.md" || len(kept) != 0 {
		t.Errorf("removed=%v kept=%v err=%v", removed, kept, err)
	}
}

// OPS-3: prune 도 git 이 되살릴 수 없는 원본은 남기고, 그 달의 결정(kept-uncommitted)으로 알린다 — 커밋하면 다음 tier 가 치운다.
func TestPruneReportsOriginalsKeptForGit(t *testing.T) {
	cfg := tierConfig(t)
	gitIn(t, cfg.MetaRepoRoot, "init", "-q")
	store := NewLocalStore(t.TempDir())
	fake, hot := coldMonthFixture(t, store, cfg.MetaRepoRoot, 1, 2) // hot 원본을 커밋하지 않았다

	decisions, err := pruneArchived(context.Background(), fake, store, cfg, "t", map[string]bool{})

	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %+v %v", decisions, err)
	}
	d := decisions[0]
	if d.Action != TierKeptUncommitted || len(d.RemovedFiles) != 0 || strings.Join(d.KeptFiles, ",") != strings.Join(hot, ",") {
		t.Errorf("decision = %+v, want %s 로 %v", d, TierKeptUncommitted, hot)
	}
	gitIn(t, cfg.MetaRepoRoot, "add", "-A")
	gitIn(t, cfg.MetaRepoRoot, "commit", "-q", "-m", "hot 원본")
	decisions, err = pruneArchived(context.Background(), fake, store, cfg, "t", map[string]bool{})
	if err != nil || len(decisions) != 1 || decisions[0].Action != TierPruned || len(decisions[0].RemovedFiles) != len(hot) {
		t.Errorf("커밋한 뒤에는 치운다: %+v %v", decisions, err)
	}
}

// tierOnceCluster 는 팀 t 의 지난달 인덱스 하나(문서 rel 하나)를 가진 클러스터다. tier 가 스냅샷을 뜨고 인덱스를 지우면
// 그 스냅샷이 목록(_all)에 나온다.
func tierOnceCluster(index, rel, sha string) *fakeOS {
	var mu sync.Mutex
	snapshot, deleted := "", false
	return &fakeOS{route: func(method, path string, body map[string]any) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case method == http.MethodGet && strings.HasPrefix(path, "/_cat/indices/"):
			if deleted {
				return []map[string]string{}, nil
			}
			return []map[string]string{{"index": index}}, nil
		case method == http.MethodPost && path == "/mem-*/_search": // WorkspaceTeams
			return map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": []any{map[string]any{"key": "t"}}}}}, nil
		case method == http.MethodGet && path == "/"+index+"/_settings":
			return map[string]any{index: map[string]any{"settings": map[string]any{}}}, nil
		case method == http.MethodPost && path == "/"+index+"/_search" && body["aggs"] != nil:
			return map[string]any{"aggregations": map[string]any{"newest": map[string]any{"value_as_string": "2026-01-10T00:00:00.000Z"}}}, nil
		case method == http.MethodPost && path == "/"+index+"/_search": // 문서 목록 (scan)
			return map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_source": map[string]any{"team": "t", "source_path": rel, "content_sha": sha}}}}}, nil
		case method == http.MethodPut && strings.HasPrefix(path, "/_snapshot/mem-cold/"):
			snapshot = strings.TrimPrefix(path, "/_snapshot/mem-cold/")
			return map[string]any{"snapshot": map[string]any{"state": snapshotSuccess}}, nil
		case method == http.MethodDelete && path == "/"+index:
			deleted = true
		case method == http.MethodGet && path == "/_snapshot/mem-cold/_all" && snapshot != "":
			return map[string]any{"snapshots": []any{map[string]any{"snapshot": snapshot, "indices": []string{index}, "state": snapshotSuccess, "start_time": "2026-10-13T00:00:00.000Z"}}}, nil
		}
		return nil, nil
	}}
}

// OPS-3: tier 가 방금 옮긴 달의 남긴 원본을, 뒤이은 prune 이 같은 달에서 다시 알리지 않는다 (한 번만, 개수도 한 번만).
func TestTierReportsAKeptOriginalOnce(t *testing.T) {
	ctx := context.Background()
	cfg := tierConfig(t)
	gitIn(t, cfg.MetaRepoRoot, "init", "-q")
	store := NewLocalStore(t.TempDir())
	const rel, index = "docs/specs/old.md", "mem-t-2026.01"
	body := "# 커밋하지 않은 옛 문서\n"
	sha := document.SHA256(body)
	writeHot(t, cfg.MetaRepoRoot, rel, body)
	mustDo(t, store.Put(ctx, RawKey("t", rel), body))
	chunk := model.Chunk{ID: rel + "#0", Team: "t", Kind: model.KindEpisodic, SourcePath: rel, ContentSHA: sha, EventTime: "2026-01-10T00:00:00Z", IngestedAt: "2026-01-10T00:00:00Z"}
	mustDo(t, store.Put(ctx, ChunksKey("t", rel), EncodeChunks([]model.Chunk{chunk})))

	decisions, err := Tier(ctx, tierOnceCluster(index, rel, sha), store, cfg, TierOptions{Now: tierNow, Team: "t"})

	if err != nil {
		t.Fatal(err)
	}
	mentions := 0
	for _, d := range decisions {
		mentions += len(d.KeptFiles)
	}
	if mentions != 1 || decisionOfIndex(decisions, index).Action != TierMoved {
		t.Errorf("남긴 원본은 한 번만 알린다: %+v", decisions)
	}
}

func decisionOfIndex(decisions []TierDecision, index string) TierDecision {
	for _, d := range decisions {
		if d.Index == index {
			return d
		}
	}
	return TierDecision{}
}
