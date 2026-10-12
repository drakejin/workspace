package state

import (
	"path/filepath"
	"sync"

	"github.com/drakejin/workspace/app/internal/model"
)

// DistillRecord 는 증류 캐시 한 줄이다.
type DistillRecord struct {
	BodySHA    string   `json:"bodySha"`
	SourcePath string   `json:"sourcePath"`
	Summary    string   `json:"summary"`
	Entities   []string `json:"entities"`
	At         string   `json:"at"`
}

// DistillCache 는 .cache/distill/{team}.jsonl 이다. 키는 본문 sha — hit 면 LLM 0회.
type DistillCache struct {
	Ledger *Ledger[DistillRecord]
	mu     sync.RWMutex
	byTeam map[string]map[string]DistillRecord
}

// NewDistillCache 는 root 의 증류 캐시다. Load 전에는 비어 있다.
func NewDistillCache(root string) *DistillCache {
	return &DistillCache{Ledger: NewLedger[DistillRecord](filepath.Join(CacheDir(root), "distill")), byTeam: map[string]map[string]DistillRecord{}}
}

// Load 는 원장을 메모리에 올린다.
func (c *DistillCache) Load() error {
	teams, err := c.Ledger.Teams()
	if err != nil {
		return err
	}
	loaded := map[string]map[string]DistillRecord{}
	for _, team := range teams {
		records, err := c.Ledger.ReadAll(team)
		if err != nil {
			return err
		}
		m := map[string]DistillRecord{}
		for _, r := range records {
			m[r.BodySHA] = r
		}
		loaded[team] = m
	}
	c.mu.Lock()
	c.byTeam = loaded
	c.mu.Unlock()
	return nil
}

// Get 은 캐시 조회다.
func (c *DistillCache) Get(team, bodySHA string) (model.Distilled, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.byTeam[team][bodySHA]
	if !ok {
		return model.Distilled{}, false
	}
	return model.Distilled{Summary: r.Summary, Entities: r.Entities}, true
}

// Put 은 원장에 append 하고 메모리도 갱신한다.
func (c *DistillCache) Put(team string, r DistillRecord) error {
	if err := c.Ledger.Append(team, r); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byTeam[team] == nil {
		c.byTeam[team] = map[string]DistillRecord{}
	}
	c.byTeam[team][r.BodySHA] = r
	return nil
}

// Size 는 캐시 건수다. team 이 비면 전체.
func (c *DistillCache) Size(team string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if team != "" {
		return len(c.byTeam[team])
	}
	n := 0
	for _, m := range c.byTeam {
		n += len(m)
	}
	return n
}

// LookupDistilled 는 데몬 밖(MCP 서버, 운영 CLI)에서 쓰는 증류 캐시 조회다. 호출마다 원장을 다시 읽어
// 그 사이 데몬이 증류한 결과도 붙인다 — 데몬 밖의 재색인이 요약·entity 를 지우지 않게.
func LookupDistilled(root string) func(team, bodySHA string) (model.Distilled, bool) {
	return func(team, bodySHA string) (model.Distilled, bool) {
		c := NewDistillCache(root)
		if err := c.Load(); err != nil {
			return model.Distilled{}, false
		}
		return c.Get(team, bodySHA)
	}
}
