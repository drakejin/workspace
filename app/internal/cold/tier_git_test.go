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

	removed, kept, byGit, err := removeHotOriginals(ctx, store, root, docs)

	if err != nil {
		t.Fatal(err)
	}
	if !byGit {
		t.Errorf("git 레포에서는 git rm 으로 지웠다고 알린다 (커밋해야 한다)")
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

// R4-daemon-b-6: tier 는 hot 원본을 디스크의 철자로 열고 git 에 넘긴다 — source_path(NFC)와 디스크 이름이 다른 원본(Finder 가
// 만든 NFD 한글 이름, 대소문자만 다른 이름)도 확인하고 지운다. 정규화·대소문자를 구분하는 파일 시스템(Linux)에서는 source_path
// 로 열면 파일이 없고, git 은 다른 철자의 경로를 모른다 — 예전에는 그런 원본을 건너뛰거나 커밋하지 않은 것으로 보고 남겼다.
func TestRemoveHotOriginalsUsesTheOnDiskSpelling(t *testing.T) {
	const body = "# 원본\n"
	cases := map[string]struct{ disk, sourcePath string }{
		"NFD 한글 이름": {"docs/specs/각/a.md", "docs/specs/각/a.md"},
		"대소문자":      {"docs/specs/Upper.md", "docs/specs/upper.md"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store := NewLocalStore(t.TempDir())
			gitIn(t, root, "init", "-q")
			writeHot(t, root, c.disk, body)
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "fixture")
			mustDo(t, store.Put(ctx, RawKey("t", c.sourcePath), body))
			doc := sourceDoc{team: "t", sourcePath: c.sourcePath, contentSHA: document.SHA256(body)}

			if !hotOriginalExists(root, c.sourcePath) {
				t.Errorf("디스크의 철자로 원본을 찾아야 한다")
			}
			removed, kept, byGit, err := removeHotOriginals(ctx, store, root, []sourceDoc{doc})

			if err != nil || strings.Join(removed, ",") != c.sourcePath || len(kept) != 0 || !byGit {
				t.Errorf("removed=%v kept=%v byGit=%v err=%v (커밋된 그대로인 원본은 git rm 으로 지운다)", removed, kept, byGit, err)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.disk))); !os.IsNotExist(err) {
				t.Errorf("디스크의 원본이 남았다: %v", err)
			}
		})
	}
}

// git 레포가 아닌 루트(테스트·임시 meta-repo)는 예전처럼 cold store raw 확인만으로 지운다 — 되살릴 git 히스토리가 처음부터 없다.
func TestRemoveHotOriginalsOutsideGitRemovesVerifiedCopies(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocalStore(t.TempDir())
	writeHot(t, root, "docs/specs/a.md", "# a\n")
	mustDo(t, store.Put(ctx, RawKey("t", "docs/specs/a.md"), "# a\n"))

	removed, kept, byGit, err := removeHotOriginals(ctx, store, root, []sourceDoc{{team: "t", sourcePath: "docs/specs/a.md", contentSHA: document.SHA256("# a\n")}})

	if err != nil || strings.Join(removed, ",") != "docs/specs/a.md" || len(kept) != 0 || byGit {
		t.Errorf("removed=%v kept=%v byGit=%v err=%v (git 이 다루지 않으므로 파일만 지운다 — git rm 이 아니다)", removed, kept, byGit, err)
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
	if len(decisions) == 1 && !decisions[0].GitRemoved {
		t.Errorf("git 레포에서 치운 원본은 git rm 으로 지웠다고 결정에 남긴다 (커밋해야 한다): %+v", decisions[0])
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

// verifiedDocs 는 root 의 rels 를 cold store raw 에 지금 내용 그대로 넣고 tier 가 넘기는 문서 목록으로 돌려준다.
func verifiedDocs(t *testing.T, store Store, root string, rels ...string) []sourceDoc {
	t.Helper()
	var docs []sourceDoc
	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		mustDo(t, err)
		mustDo(t, store.Put(context.Background(), RawKey("t", rel), string(body)))
		docs = append(docs, sourceDoc{team: "t", sourcePath: rel, contentSHA: document.SHA256(string(body))})
	}
	return docs
}

// OPS-3: git 이 레포를 거부하면(소유자가 달라 "detected dubious ownership" 등) git 레포가 아니라고 보고 지우지 않는다 —
// 커밋 여부를 확인하지 못한 채 지우면 커밋 안 된 원본이 cold store 에만 남는다. 지우지 않고 까닭을 에러로 알린다.
func TestRemoveHotOriginalsKeepsFilesWhenGitRefusesTheRepo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocalStore(t.TempDir())
	gitIn(t, root, "init", "-q")
	writeHot(t, root, "docs/specs/untracked.md", "# 커밋한 적 없다\n")
	docs := verifiedDocs(t, store, root, "docs/specs/untracked.md")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1") // git 이 이 레포의 소유자를 믿지 않는다

	removed, kept, byGit, err := removeHotOriginals(ctx, store, root, docs)

	if err == nil || len(removed) != 0 || len(kept) != 0 || byGit {
		t.Errorf("git 이 레포를 거부하면 지우지 않고 알린다: removed=%v kept=%v byGit=%v err=%v", removed, kept, byGit, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "docs/specs/untracked.md")); statErr != nil {
		t.Errorf("커밋 안 된 원본을 지웠다: %v", statErr)
	}
}

// OPS-3: meta-repo 가 이 meta-repo 를 추적하지 않는 바깥 git 레포(홈 디렉토리의 dotfiles 레포 등) 안에 있으면 git 레포가 아닌
// meta-repo 처럼 다룬다 — 바깥 레포에 커밋하라는 안내는 맞지 않고, 원본은 영영 남는다. 바깥 레포가 meta-repo 의 파일을
// 추적하면(모노레포의 한 디렉토리) git 레포로 다룬다.
func TestRemoveHotOriginalsInsideAnEnclosingRepo(t *testing.T) {
	ctx := context.Background()
	t.Run("바깥 레포가 meta-repo 를 추적하지 않는다", func(t *testing.T) {
		outer := t.TempDir()
		gitIn(t, outer, "init", "-q")
		writeHot(t, outer, ".zshrc", "# dotfiles\n")
		gitIn(t, outer, "add", ".zshrc")
		gitIn(t, outer, "commit", "-q", "-m", "dotfiles")
		root := filepath.Join(outer, "work", "meta")
		writeHot(t, root, "docs/specs/a.md", "# a\n")
		store := NewLocalStore(t.TempDir())

		removed, kept, byGit, err := removeHotOriginals(ctx, store, root, verifiedDocs(t, store, root, "docs/specs/a.md"))

		if err != nil || strings.Join(removed, ",") != "docs/specs/a.md" || len(kept) != 0 || byGit {
			t.Errorf("git 레포가 아닌 meta-repo 처럼 지운다: removed=%v kept=%v byGit=%v err=%v", removed, kept, byGit, err)
		}
	})
	t.Run("바깥 레포가 meta-repo 를 추적한다", func(t *testing.T) {
		outer := t.TempDir()
		gitIn(t, outer, "init", "-q")
		root := filepath.Join(outer, "meta")
		writeHot(t, root, "docs/specs/committed.md", "# 커밋됨\n")
		gitIn(t, outer, "add", "-A")
		gitIn(t, outer, "commit", "-q", "-m", "meta")
		writeHot(t, root, "docs/specs/untracked.md", "# 커밋한 적 없다\n")
		store := NewLocalStore(t.TempDir())

		removed, kept, byGit, err := removeHotOriginals(ctx, store, root, verifiedDocs(t, store, root, "docs/specs/committed.md", "docs/specs/untracked.md"))

		if err != nil || strings.Join(removed, ",") != "docs/specs/committed.md" || strings.Join(kept, ",") != "docs/specs/untracked.md" || !byGit {
			t.Errorf("git 이 되살릴 수 있는 원본만 git rm 으로 지운다: removed=%v kept=%v byGit=%v err=%v", removed, kept, byGit, err)
		}
	})
}

// R5-final-daemon-5: GitState 는 KeptByGit 의 답을 바꾸는 일(커밋, add·rm --cached 로 추적이 바뀜)이 있을 때만 바뀐다 — 데몬은 이 값이
// 그대로면 남긴 원본을 git 으로 다시 보지 않는다. git 레포가 아니면 그렇다는 값이다.
func TestGitStateChangesOnlyWhenKeptByGitCan(t *testing.T) {
	ctx := context.Background()
	outside, err := GitState(ctx, t.TempDir())
	if err != nil || outside != gitStateNone {
		t.Errorf("git 레포가 아니다: %q %v", outside, err)
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	writeHot(t, root, "docs/specs/a.md", "# a\n")
	state := func() string {
		t.Helper()
		s, err := GitState(ctx, root)
		mustDo(t, err)
		return s
	}

	empty := state()
	if again := state(); again != empty {
		t.Errorf("바뀐 것이 없는데 다르다: %q → %q", empty, again)
	}
	gitIn(t, root, "add", "docs/specs/a.md")
	staged := state()
	gitIn(t, root, "commit", "-q", "-m", "a")
	committed := state()
	gitIn(t, root, "rm", "-q", "--cached", "docs/specs/a.md")
	untracked := state()

	seen := map[string]bool{}
	for _, s := range []string{empty, staged, committed, untracked} {
		if seen[s] {
			t.Errorf("add·commit·rm --cached 뒤에도 같은 값이 나왔다: %q", []string{empty, staged, committed, untracked})
		}
		seen[s] = true
	}
}
