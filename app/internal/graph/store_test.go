package graph

import (
	"strings"
	"testing"
)

// endpointLabels 는 관계 종류마다 두 끝의 노드 라벨이다 (Build 가 만드는 그대로).
var endpointLabels = map[string][2]string{
	"SUPERSEDES": {LabelDoc, LabelDoc}, "DERIVED_FROM": {LabelDoc, LabelDoc}, "RELATES": {LabelDoc, LabelDoc}, "LINKS_TO": {LabelDoc, LabelDoc},
	RelMentions: {LabelDoc, LabelEntity}, RelInRepo: {LabelDoc, LabelRepo},
}

// OPS-11: 관계를 적재할 때 끝점을 라벨로 MATCH 해야 라벨별 key 유일 제약(인덱스)을 쓴다 — 라벨이 없으면 행마다 클러스터의
// 모든 노드(다른 workspace 포함)를 두 번 훑는다. workspace 를 비우는 질의도 라벨로 좁힌다.
func TestGraphWritesMatchNodesByLabel(t *testing.T) {
	for _, typ := range edgeTypes {
		labels, ok := endpointLabels[typ]
		if !ok {
			t.Fatalf("%s 의 끝점 라벨을 모른다", typ)
		}
		q := relationshipQuery(typ)
		if !strings.Contains(q, "(a:"+labels[0]+" {key: r.from})") || !strings.Contains(q, "(b:"+labels[1]+" {key: r.to})") {
			t.Errorf("%s: 끝점을 라벨(%v)로 찾지 않는다: %s", typ, labels, q)
		}
	}
	for _, q := range workspaceDeleteQueries() {
		if strings.Contains(q, "(n {") {
			t.Errorf("라벨 없는 workspace 삭제: %s", q)
		}
	}
	if got := len(workspaceDeleteQueries()); got != 3 {
		t.Errorf("Doc·Entity·Repo 를 하나씩 비운다: %d개", got)
	}
}
