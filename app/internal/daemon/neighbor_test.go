package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

type captureOS struct{ body string }

func (c *captureOS) Do(_ context.Context, _, _ string, body any, _ url.Values, out any) error {
	b, _ := json.Marshal(body)
	c.body = string(b)
	return json.Unmarshal([]byte(`{"hits":{"hits":[]}}`), out)
}

func (c *captureOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// 판정 이웃은 같은 프로젝트 안에서만 찾는다 (vms: "같은 project 안"). 프로젝트가 없는 문서는 프로젝트 없는 문서끼리.
func TestBestNeighborStaysInTheSameProject(t *testing.T) {
	os := &captureOS{}
	if _, err := BestNeighbor(context.Background(), os, DiffEntry{SourcePath: "a.md", Team: "t", Project: "alpha-api", DocType: model.DocADR, Body: "x"}, "ws", 0.3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(os.body, `{"term":{"repo":"alpha-api"}}`) {
		t.Errorf("프로젝트 필터가 없다: %s", os.body)
	}
	// 같은 종류의 기록끼리만 — ADR 이 PRD 에 밀리지 않게
	if !strings.Contains(os.body, `{"term":{"doc_type":"adr"}}`) {
		t.Errorf("문서 종류 필터가 없다: %s", os.body)
	}
	if _, err := BestNeighbor(context.Background(), os, DiffEntry{SourcePath: "b.md", Team: "t", Body: "x"}, "ws", 0.3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(os.body, `{"exists":{"field":"repo"}}`) {
		t.Errorf("프로젝트 없는 문서는 프로젝트 없는 문서끼리: %s", os.body)
	}
}
