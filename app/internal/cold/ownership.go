package cold

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ManifestEntry 는 tier 가 스냅샷에 넣은 문서 하나다.
type ManifestEntry struct {
	SourcePath string `json:"source_path,omitempty"`
	ContentSHA string `json:"content_sha,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
}

// ManifestKey 는 월별 인덱스의 스냅샷 목록 키다.
func ManifestKey(team, index string) string { return team + "/manifests/" + index + ".jsonl" }

// AppendManifest 는 스냅샷 하나에 들어간 문서들을 목록에 더한다 (같은 달을 다시 tier 하면 줄이 쌓인다).
func AppendManifest(ctx context.Context, store Store, team, index, snapshot string, entries []ManifestEntry) error {
	key := ManifestKey(team, index)
	body, _, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("manifest %s: %w", index, err)
	}
	var sb strings.Builder
	sb.WriteString(body)
	for _, e := range entries {
		e.Snapshot = snapshot
		line, _ := json.Marshal(e)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := store.Put(ctx, key, sb.String()); err != nil {
		return fmt.Errorf("manifest %s: %w", index, err)
	}
	return nil
}

// DropFromManifest 는 문서를 목록에서 뺀다 (archive purge). 그 줄이 덮던 스냅샷은 여전히 목록이 덮는 것으로 남긴다
// (경로 없는 줄) — 지우면 그 스냅샷이 "목록 없는 스냅샷" 으로 보여 달 단위 판단으로 물러난다.
func DropFromManifest(ctx context.Context, store Store, team, index, sourcePath string) error {
	key := ManifestKey(team, index)
	body, ok, err := store.Get(ctx, key)
	if err != nil || !ok {
		return err
	}
	var sb strings.Builder
	for _, e := range decodeManifest(body) {
		if e.SourcePath == sourcePath {
			e = ManifestEntry{Snapshot: e.Snapshot}
		}
		line, _ := json.Marshal(e)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return store.Put(ctx, key, sb.String())
}

func decodeManifest(body string) []ManifestEntry {
	var out []ManifestEntry
	for _, line := range strings.Split(body, "\n") {
		var e ManifestEntry
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Ownership 은 "이 내용은 cold 가 갖고 있다" 판단이다. 데몬은 cold 가 가진 내용의 hot 파일을 다시 색인하지 않고,
// tier 는 그런 hot 원본을 치운다. tier·archive 는 쓰기를 막은(또는 잠깐 복원한) 인덱스를 스냅샷하고 그 문서 목록(manifest)을
// 남기므로 정확히 안다. 목록이 덮지 못하는 스냅샷(ISM 이 만든 것)이 그 달에 하나라도 있으면, 그 달이 스냅샷에 있다는 것만으로
// 가진 것으로 본다 — 호출하는 쪽이 cold store 청크와 파일의 내용 sha 가 같은지 먼저 확인한다
// (ISM 의 스냅샷과 삭제 사이에 그 달로 쓴 내용은 놓칠 수 있다).
type Ownership struct {
	archive Archive
	store   Store

	mu        sync.Mutex
	manifests map[string]*manifest // ManifestKey → 목록, nil = 목록 없음
}

type manifest struct {
	entries   map[string]bool // "path\x00sha"
	snapshots map[string]bool // 목록이 덮는 스냅샷 이름
}

// NewOwnership 은 사이클 하나 동안 쓰는 판단기다 (목록은 처음 볼 때 읽어 둔다).
func NewOwnership(archive Archive, store Store) *Ownership {
	return &Ownership{archive: archive, store: store, manifests: map[string]*manifest{}}
}

// Archive 는 판단의 바탕이 된 스냅샷 달 목록이다.
func (o *Ownership) Archive() Archive { return o.archive }

// Owns 는 team 의 index 로 갔던 sourcePath 의 내용(contentSHA)을 cold 가 갖고 있는가다.
func (o *Ownership) Owns(ctx context.Context, team, index, sourcePath, contentSHA string) (bool, error) {
	snaps := o.archive[index]
	if len(snaps) == 0 {
		return false, nil
	}
	m, err := o.load(ctx, team, index)
	if err != nil {
		return false, err
	}
	if m == nil {
		return true, nil
	}
	for _, snap := range snaps {
		if !m.snapshots[snap] {
			return true, nil // 목록 없는 스냅샷(ISM)이 있다 — 달 단위로 본다
		}
	}
	return m.entries[sourcePath+"\x00"+contentSHA], nil
}

func (o *Ownership) load(ctx context.Context, team, index string) (*manifest, error) {
	key := ManifestKey(team, index)
	o.mu.Lock()
	m, seen := o.manifests[key]
	o.mu.Unlock()
	if seen {
		return m, nil
	}
	body, ok, err := o.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", index, err)
	}
	if ok {
		m = &manifest{entries: map[string]bool{}, snapshots: map[string]bool{}}
		for _, e := range decodeManifest(body) {
			m.snapshots[e.Snapshot] = true
			if e.SourcePath != "" {
				m.entries[e.SourcePath+"\x00"+e.ContentSHA] = true
			}
		}
	}
	o.mu.Lock()
	o.manifests[key] = m
	o.mu.Unlock()
	return m, nil
}
