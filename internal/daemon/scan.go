package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/drakejin/workspace/internal/ingest"
)

// ScannedFile 은 색인 대상 파일과 stat 이다.
type ScannedFile struct {
	Rel     string
	Abs     string
	MtimeMs int64
	Size    int64
}

// Skipped 는 게이트에 걸린 파일과 사유다.
type Skipped struct {
	Rel    string
	Reason string
}

// ErrPartialScan 은 일부 디렉토리를 읽지 못했다는 뜻이다. 결과는 쓰되, 없는 파일을 지워진 것으로 보면 안 된다.
var ErrPartialScan = errors.New("일부 디렉토리를 읽지 못했습니다")

// ScanHot 은 hot(working tree)을 훑는다. 내용은 읽지 않는다 — 평상시 비용은 stat-walk 뿐이다.
// 하위 디렉토리를 읽지 못하면 그 디렉토리를 Skipped 에 남기고 ErrPartialScan 을 함께 돌려준다.
func ScanHot(root string, maxFileBytes int64) ([]ScannedFile, []Skipped, error) {
	var (
		files   []ScannedFile
		skipped []Skipped
		partial bool
	)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			if p == root {
				return walkErr
			}
			partial = true
			skipped = append(skipped, Skipped{Rel: rel, Reason: "읽기 실패: " + walkErr.Error()})
			return nil
		}
		if d.IsDir() {
			if p != root && !ingest.InScope(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !ingest.IsIndexable(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			skipped = append(skipped, Skipped{Rel: rel, Reason: fmt.Sprintf("size %d > %d", info.Size(), maxFileBytes)})
			return nil
		}
		files = append(files, ScannedFile{Rel: rel, Abs: p, MtimeMs: info.ModTime().UnixMilli(), Size: info.Size()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	if err == nil && partial {
		err = ErrPartialScan
	}
	return files, skipped, err
}

const binarySniffBytes = 8000

// IsProbablyBinary 는 앞부분에 NUL 바이트가 있으면 바이너리로 본다.
func IsProbablyBinary(abs string) (bool, error) {
	f, err := os.Open(abs)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, binarySniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

var titlePattern = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
