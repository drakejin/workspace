package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoryInitComposeUp 는 graphURI 설정으로 make memory-init(scripts/memory-init/run.sh)을 돌리고 docker compose up 호출을 돌려준다.
// docker·go·curl 은 가짜다 — 스택은 띄우지 않고, 최초 색인은 가짜 .cache/bin/memory-daemon 이 받는다.
func memoryInitComposeUp(t *testing.T, graphURI string) string {
	t.Helper()
	sb := newSandbox(t)
	root := filepath.Join(sb.dir, "meta")
	sb.copyTo(root, "scripts/memory-common.sh", "scripts/memory-init")
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(root, ".cache", "bin", "memory-daemon"), "#!/bin/sh\necho \"[reconcile] fake $*\"\n")
	dockerLog := sb.stub("docker", "exit 0")
	sb.stub("go", `[ "$1" = version ] && echo "go version go1.25.6 darwin/arm64"; exit 0`)
	sb.stub("curl", "exit 0")

	r := sb.mustBash(map[string]string{"MEMORY_GRAPH_URI": graphURI}, filepath.Join(root, "scripts", "memory-init", "run.sh"))

	for _, c := range calls(t, dockerLog) {
		if strings.HasPrefix(c, "compose up") {
			return c
		}
	}
	t.Fatalf("docker compose up 을 부르지 않았다: %v\n%s", calls(t, dockerLog), r.output())
	return ""
}

// FILE-13: 그래프를 끄면(MEMORY_GRAPH_URI=off) memory-init 은 Neo4j 를 띄우지 않는다 — OpenSearch 만 이름으로 올린다.
// (이름으로 올리므로 다른 meta-repo 가 쓰려고 이미 떠 있는 Neo4j 는 건드리지 않는다)
func TestMemoryInitSkipsNeo4jWhenGraphOff(t *testing.T) {
	up := memoryInitComposeUp(t, "off")
	if up != "compose up -d opensearch" {
		t.Errorf("그래프가 off 인데 docker %s — 'compose up -d opensearch' 여야 한다 (서비스를 안 적으면 Neo4j 도 뜬다)", up)
	}
}

// FILE-13: 그래프가 켜져 있으면 OpenSearch 와 Neo4j 를 둘 다 올린다.
func TestMemoryInitStartsNeo4jWhenGraphOn(t *testing.T) {
	up := memoryInitComposeUp(t, "bolt://localhost:7689")
	if up != "compose up -d opensearch neo4j" {
		t.Errorf("docker %s — 'compose up -d opensearch neo4j' 여야 한다", up)
	}
}
