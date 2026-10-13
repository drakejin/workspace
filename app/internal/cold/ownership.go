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
// 문서가 하나도 없던 스냅샷도 표시 줄({"snapshot": 이름})을 남긴다 — 줄이 없으면 "목록 없는(ISM) 스냅샷" 으로 읽혀
// 그 달 전체를 cold 가 가진 것이 된다. 읽기-고치기-쓰기는 키 잠금 안에서 한다 (tier·archive 가 겹쳐도 줄을 잃지 않게).
func AppendManifest(ctx context.Context, store Store, team, index, snapshot string, entries []ManifestEntry) error {
	if len(entries) == 0 {
		entries = []ManifestEntry{{}}
	}
	err := updateKey(ctx, store, ManifestKey(team, index), func(body string, _ bool) (string, bool) {
		var sb strings.Builder
		sb.WriteString(body)
		for _, e := range entries {
			e.Snapshot = snapshot
			writeManifestLine(&sb, e)
		}
		return sb.String(), true
	})
	if err != nil {
		return fmt.Errorf("manifest %s: %w", index, err)
	}
	return nil
}

// DropFromManifest 는 문서를 목록에서 뺀다 (archive purge). 그 줄이 덮던 스냅샷은 여전히 목록이 덮는 것으로 남긴다
// (경로 없는 줄) — 지우면 그 스냅샷이 "목록 없는 스냅샷" 으로 보여 달 단위 판단으로 물러난다.
func DropFromManifest(ctx context.Context, store Store, team, index, sourcePath string) error {
	return rewriteManifest(ctx, store, team, index, func(e ManifestEntry) (ManifestEntry, bool) {
		if e.SourcePath == sourcePath {
			return ManifestEntry{Snapshot: e.Snapshot}, true
		}
		return e, true
	})
}

// DropSnapshotsFromManifest 는 지운 스냅샷들의 줄을 목록에서 뺀다 (archive 가 다시 쓴 뒤) — 목록이 지금 있는 스냅샷의 내용만 말하게.
func DropSnapshotsFromManifest(ctx context.Context, store Store, team, index string, snapshots []string) error {
	gone := make(map[string]bool, len(snapshots))
	for _, s := range snapshots {
		gone[s] = true
	}
	return rewriteManifest(ctx, store, team, index, func(e ManifestEntry) (ManifestEntry, bool) {
		return e, !gone[e.Snapshot]
	})
}

// rewriteManifest 는 목록의 줄마다 keep 을 적용해 다시 쓴다 (키 잠금 안에서). 목록이 없으면 아무것도 하지 않는다.
func rewriteManifest(ctx context.Context, store Store, team, index string, keep func(ManifestEntry) (ManifestEntry, bool)) error {
	err := updateKey(ctx, store, ManifestKey(team, index), func(body string, ok bool) (string, bool) {
		if !ok {
			return "", false
		}
		var sb strings.Builder
		for _, e := range decodeManifest(body) {
			if next, kept := keep(e); kept {
				writeManifestLine(&sb, next)
			}
		}
		return sb.String(), true
	})
	if err != nil {
		return fmt.Errorf("manifest %s: %w", index, err)
	}
	return nil
}

func writeManifestLine(sb *strings.Builder, e ManifestEntry) {
	line, _ := json.Marshal(e)
	sb.Write(line)
	sb.WriteByte('\n')
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
// (ISM 의 스냅샷과 삭제 사이에 그 달로 쓴 내용은 놓칠 수 있다). tier·archive 가 뜬 스냅샷(이름 cold-)은 목록 줄이 없어도
// ISM 스냅샷으로 보지 않는다 — 목록을 쓰기 전에 끊긴 스냅샷이 그 달 전체를 가진 것으로 읽히지 않게 (모르면 색인하는 쪽이 안전하다).
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
	for _, snap := range snaps {
		if !m.covers(snap) {
			return true, nil // 목록 없는 스냅샷(ISM)이 있다 — 달 단위로 본다
		}
	}
	return m != nil && m.entries[sourcePath+"\x00"+contentSHA], nil
}

// covers 는 목록이 snap 의 내용을 말하는가다. tier·archive 의 스냅샷은 줄이 없어도 (빈 스냅샷, 목록 쓰기 전에 끊김) 그렇다.
func (m *manifest) covers(snap string) bool {
	return isTierSnapshot(snap) || (m != nil && m.snapshots[snap])
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
