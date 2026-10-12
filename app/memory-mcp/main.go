// memory-mcp 는 MCP 서버(stdio)이자 운영 CLI 다.
//
//	memory-mcp            MCP 서버 (stdio) — .mcp.json 이 부른다
//	memory-mcp <command>  운영 명령 (search, ingest, tier, status ...). 'memory-mcp help'
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

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/mcpserver"
	"github.com/drakejin/workspace/internal/opensearch"
	"github.com/drakejin/workspace/internal/ops"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, ops.ErrUsage) {
			fmt.Fprintf(os.Stderr, "%v\n\n%s\n", err, ops.Usage)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Println(ops.Usage)
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
	ix := ingest.New(ingest.Deps{OS: osClient, Cold: store, Config: cfg})
	handlers := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: osClient, Cold: store, Indexer: ix, Log: logger.Printf})

	if len(args) == 0 || args[0] == "serve" {
		logger.Printf("[memory-mcp] root=%s opensearch=%s team=%s", cfg.MetaRepoRoot, cfg.OpenSearchURL, cfg.DefaultTeam)
		if err := handlers.Ready(ctx); err != nil {
			logger.Printf("[memory-mcp] OpenSearch 준비 실패, 첫 도구 호출 때 다시 시도: %v", err)
		}
		return mcpserver.NewServer(handlers).Run(ctx, &mcp.StdioTransport{})
	}
	cli := &ops.CLI{Config: cfg, OS: osClient, Cold: store, Indexer: ix, Handlers: handlers, Out: os.Stdout, Now: time.Now}
	return cli.Run(ctx, args[0], args[1:])
}
