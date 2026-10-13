package cold

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
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
// 남기므로 정확히 안다. 목록의 줄은 그 스냅샷이 지금 있을 때만 믿는다 — 지워진 스냅샷의 줄은 문서를 붙잡지 않는다.
// 목록이 덮지 못하는 스냅샷(ISM 이 만든 것)은 그 스냅샷이 떠지기 전에 적재된 내용(cold store 청크의 ingested_at)만 가진 것으로
// 본다 — 스냅샷 뒤에 그 달로 늦게 쓴 문서는 스냅샷에 없다. 호출하는 쪽이 cold store 청크와 파일의 내용 sha 가 같은지 먼저 확인한다.
// tier·archive 가 뜬 스냅샷(이름 cold-)은 목록 줄이 없어도 ISM 스냅샷으로 보지 않는다 — 목록을 쓰기 전에 끊긴 스냅샷이
// 그 달 전체를 가진 것으로 읽히지 않게 (모르면 색인하는 쪽이 안전하다).
type Ownership struct {
	archive Archive
	store   Store

	mu        sync.Mutex
	manifests map[string]*manifest // ManifestKey → 목록, nil = 목록 없음
	ingested  map[string]time.Time // 청크 키 + sha → 그 내용의 적재 시각 (zero = 모른다)
}

type manifest struct {
	entries   map[string]bool // 지금 있는 스냅샷의 줄 "path\x00sha"
	gone      map[string]bool // 지워진 스냅샷에만 남은 줄 "path\x00sha"
	snapshots map[string]bool // 목록이 덮는(지금 있는) 스냅샷 이름
}

// verdict 는 내용 하나에 대한 판단이다.
type verdict int

const (
	verdictFree     verdict = iota // cold 가 갖지 않은 내용 — hot 이 맡는다
	verdictOwned                   // 지금 있는 스냅샷이 담았다
	verdictStranded                // cold 로 간 달의 내용인데 어느 스냅샷에도 있다고 볼 수 없다
)

// NewOwnership 은 사이클 하나 동안 쓰는 판단기다 (목록·적재 시각은 처음 볼 때 읽어 둔다).
func NewOwnership(archive Archive, store Store) *Ownership {
	return &Ownership{archive: archive, store: store, manifests: map[string]*manifest{}, ingested: map[string]time.Time{}}
}

// Owns 는 team 의 index 로 갔던 sourcePath 의 내용(contentSHA)을 지금 있는 스냅샷이 담았는가다.
func (o *Ownership) Owns(ctx context.Context, team, index, sourcePath, contentSHA string) (bool, error) {
	v, err := o.judge(ctx, team, index, sourcePath, contentSHA)
	return v == verdictOwned, err
}

// Stranded 는 cold 로 간 달의 내용인데 어느 스냅샷에도 있다고 볼 수 없는 것이다 — 목록이 지워진 스냅샷에만 올려 둔 내용
// (고치기 전의 archive 가 옛 스냅샷을 지우며 잃은 문서), 또는 ISM 스냅샷 뒤에(적재 시각을 모르면 그것도) 그 달로 적재된 내용.
// cold 소유가 아니므로 데몬은 hot 파일이 있으면 다시 색인하고 tier 는 hot 원본을 지우지 않는다. hot 파일이 없으면 cold store
// 사본(raw·청크)이 유일하다 — 감사가 알린다.
func (o *Ownership) Stranded(ctx context.Context, team, index, sourcePath, contentSHA string) (bool, error) {
	v, err := o.judge(ctx, team, index, sourcePath, contentSHA)
	return v == verdictStranded, err
}

// Reloadable 은 reload(cold store 청크를 hot 인덱스로 다시 싣기)가 ch 를 실어도 되는가다. 스냅샷이 가진 내용은 싣지 않고
// (필요하면 restore), 갇힌 내용은 hot 파일이 있을 때만 싣는다 — 없으면 데몬이 파일 없는 문서로 보고 cold store 청크(유일한
// 사본)까지 지운다. 판단하지 못하면 싣는다 (빠뜨리는 쪽보다 낫다).
func (o *Ownership) Reloadable(ctx context.Context, root string, ch model.Chunk) bool {
	v, err := o.judge(ctx, ch.Team, naming.IndexFor(ch), ch.SourcePath, ch.ContentSHA)
	switch {
	case err != nil:
		return true
	case v == verdictOwned:
		return false
	case v == verdictStranded:
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(ch.SourcePath)))
		return err == nil
	}
	return true
}

// judge 는 내용 하나를 판단한다. 그 달의 스냅샷이 하나도 없어도 목록을 본다 — 목록에 오른 내용은 한때 cold 로 간 것이라,
// 스냅샷이 모두 사라졌으면(손으로 지웠거나 repository 를 바꿨다) cold store 사본이 유일하다.
func (o *Ownership) judge(ctx context.Context, team, index, sourcePath, contentSHA string) (verdict, error) {
	m, err := o.load(ctx, team, index)
	if err != nil {
		return verdictFree, err
	}
	key := sourcePath + "\x00" + contentSHA
	if m != nil && m.entries[key] {
		return verdictOwned, nil
	}
	if ism := m.uncovered(o.archive[index]); len(ism) > 0 {
		return o.judgeISM(ctx, team, sourcePath, contentSHA, ism)
	}
	if m != nil && m.gone[key] {
		return verdictStranded, nil
	}
	return verdictFree, nil
}

// ismClockSkew 는 적재 시각(이 머신의 시계)과 스냅샷 시작 시각(OpenSearch 의 시계)을 견줄 때 넉넉히 보는 시계 차이다.
// 스냅샷 직전 이만큼 안에 적재된 내용은 스냅샷에 있다고 단정하지 않는다 — 다시 색인하는 쪽이 안전하다.
const ismClockSkew = 5 * time.Minute

// judgeISM 은 목록이 덮지 못하는 스냅샷(ISM)이 있는 달의 내용이다. 그 스냅샷 하나라도 이 내용이 적재된 뒤에 떠졌으면 가졌다.
func (o *Ownership) judgeISM(ctx context.Context, team, sourcePath, contentSHA string, ism []Snapshot) (verdict, error) {
	at, err := o.ingestedAt(ctx, team, sourcePath, contentSHA)
	if err != nil {
		return verdictFree, err
	}
	for _, s := range ism {
		if !at.IsZero() && at.Add(ismClockSkew).Before(model.ParseTime(s.StartTime)) {
			return verdictOwned, nil
		}
	}
	return verdictStranded, nil
}

// ingestedAt 은 cold store 청크에 남은 이 내용의 적재 시각이다. 사본이 없거나 다른 내용이면 zero (모른다).
func (o *Ownership) ingestedAt(ctx context.Context, team, sourcePath, contentSHA string) (time.Time, error) {
	key := ChunksKey(team, sourcePath) + "\x00" + contentSHA
	o.mu.Lock()
	at, seen := o.ingested[key]
	o.mu.Unlock()
	if seen {
		return at, nil
	}
	chunks, ok, err := Archived(ctx, o.store, team, sourcePath)
	if err != nil {
		return time.Time{}, fmt.Errorf("cold store %s: %w", sourcePath, err)
	}
	if ok && chunks[0].ContentSHA == contentSHA {
		at = model.ParseTime(chunks[0].IngestedAt)
	}
	o.mu.Lock()
	o.ingested[key] = at
	o.mu.Unlock()
	return at, nil
}

// covers 는 목록이 snap 의 내용을 말하는가다. tier·archive 의 스냅샷은 줄이 없어도 (빈 스냅샷, 목록 쓰기 전에 끊김) 그렇다.
func (m *manifest) covers(snap string) bool {
	return isTierSnapshot(snap) || (m != nil && m.snapshots[snap])
}

// uncovered 는 snaps 중 목록이 덮지 못하는 스냅샷(ISM)이다.
func (m *manifest) uncovered(snaps []Snapshot) []Snapshot {
	var out []Snapshot
	for _, s := range snaps {
		if !m.covers(s.Name) {
			out = append(out, s)
		}
	}
	return out
}

// load 는 index 의 목록을 읽는다 (처음 볼 때 한 번).
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
		m = o.parseManifest(index, body)
	}
	o.mu.Lock()
	o.manifests[key] = m
	o.mu.Unlock()
	return m, nil
}

// parseManifest 는 목록 본문을 지금 있는 스냅샷(o.archive[index])의 줄과 지워진 스냅샷의 줄로 나눈다.
func (o *Ownership) parseManifest(index, body string) *manifest {
	live := make(map[string]bool, len(o.archive[index]))
	for _, s := range o.archive[index] {
		live[s.Name] = true
	}
	m := &manifest{entries: map[string]bool{}, gone: map[string]bool{}, snapshots: map[string]bool{}}
	for _, e := range decodeManifest(body) {
		key := e.SourcePath + "\x00" + e.ContentSHA
		switch {
		case !live[e.Snapshot]:
			if e.SourcePath != "" {
				m.gone[key] = true
			}
		case e.SourcePath != "":
			m.snapshots[e.Snapshot] = true
			m.entries[key] = true
		default:
			m.snapshots[e.Snapshot] = true
		}
	}
	return m
}
