// memory-mcp 는 MCP 서버(stdio)이자 운영 CLI 다.
//
//	memory-mcp            MCP 서버 (stdio) — .mcp.json 이 scripts/mcp/run.sh 로 부른다
//	memory-mcp <command>  운영 명령 (search, tier, audit, graph, archive, bench ...). 'memory-mcp help', 플래그는 'memory-mcp <command> -h'
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/ops"
	"github.com/drakejin/workspace/app/internal/state"
)

// startupProbeTimeout 은 MCP 서버가 stdio 를 열기 전에 OpenSearch 에 닿는지 기다리는 상한이다.
const startupProbeTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		if errors.Is(err, ops.ErrUsage) {
			fmt.Fprintf(os.Stderr, "%v\n\n%s\n", err, ops.Usage())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Println(ops.Usage())
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	// stdout 은 MCP 프로토콜 전용이라 로그는 stderr 로만 쓴다
	logger := log.New(os.Stderr, "", 0)
	osClient := opensearch.New(cfg.OpenSearchURL)
	store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
	if err != nil {
		return err
	}
	var g graph.Store
	if cfg.Graph.URI != "" {
		if g, err = graph.New(cfg.Graph); err != nil {
			return err
		}
		defer g.Close(context.Background())
	}
	// 데몬 밖의 재색인(ingest_path, audit -apply)도 증류 결과를 붙이고, superseded_by·deprecated 는 색인에 있던 값을 이어받는다
	ix := ingest.New(ingest.Deps{OS: osClient, Cold: store, Config: cfg, Distilled: state.LookupDistilled(cfg.MetaRepoRoot)})
	handlers := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: osClient, Cold: store, Indexer: ix, Graph: g, Log: logger.Printf})

	if len(args) == 0 || args[0] == "serve" {
		logger.Printf("[memory-mcp] root=%s workspace=%s opensearch=%s graph=%s team=%s",
			cfg.MetaRepoRoot, cfg.Workspace, cfg.OpenSearchURL, orOff(cfg.Graph.URI), cfg.DefaultTeam)
		// 닿는지만 짧게 본다 — 쓰기 쪽 bootstrap 은 적재·복원 도구가 처음 불릴 때 한다 (세션을 열 때마다 클러스터에 쓰지 않게)
		probe, cancel := context.WithTimeout(ctx, startupProbeTimeout)
		if err := handlers.ReadReady(probe); err != nil {
			logger.Printf("[memory-mcp] OpenSearch 에 닿지 않음, 첫 도구 호출 때 다시 본다: %v", err)
		}
		cancel()
		return mcpserver.NewServer(handlers).Run(ctx, &mcp.StdioTransport{})
	}
	cli := &ops.CLI{Config: cfg, OS: osClient, Cold: store, Indexer: ix, Handlers: handlers, Graph: g, Out: os.Stdout, In: os.Stdin, Now: time.Now}
	return cli.Run(ctx, args[0], args[1:])
}

func orOff(s string) string {
	if s == "" {
		return "off"
	}
	return s
}
