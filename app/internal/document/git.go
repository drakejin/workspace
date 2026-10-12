package document

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitFacts 는 파일을 마지막으로 바꾼 커밋이다. git 밖이거나 커밋 전 파일이면 비어 있다.
type GitFacts struct {
	SHA         string
	CommittedAt time.Time
}

const gitTimeout = 5 * time.Second

// LastCommit 는 파일의 마지막 커밋을 읽는다. 실패하면 빈 값(에러 아님).
func LastCommit(ctx context.Context, abs string) GitFacts {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%H %cI", "--", filepath.Base(abs))
	cmd.Dir = filepath.Dir(abs)
	out, err := cmd.Output()
	if err != nil {
		return GitFacts{}
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return GitFacts{}
	}
	t, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return GitFacts{}
	}
	return GitFacts{SHA: fields[0], CommittedAt: t.UTC()}
}
