// memory-daemon 은 reconcile 데몬이다.
//
//	memory-daemon         상주 — boot 1회 + 타이머(MEMORY_RECONCILE_INTERVAL_SECONDS) + 파일 watcher(debounce) 힌트
//	memory-daemon -once   사이클 1회 + 증류 큐 비우기 후 종료 (make reconcile, memory-up 의 최초 색인)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/state"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "[daemon] 시작 실패: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	once := flag.Bool("once", false, "사이클 1회 후 종료")
	flag.Parse()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stdout, "", 0)

	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Log: logger.Printf})
	if err != nil {
		return err
	}
	if err := d.Boot(ctx, *once); err != nil {
		var running *state.AlreadyRunningError
		if errors.As(err, &running) && *once {
			// 상주 데몬이 이미 수렴시키고 있다 — 1회 색인은 할 일이 없다
			logger.Printf("[reconcile] 데몬(pid %d)이 돌고 있어 1회 색인을 건너뜁니다", running.PID)
			return nil
		}
		return err
	}
	if *once {
		d.Settle()
		return d.Shutdown()
	}

	logger.Printf("[daemon] watching %s (루트 문서, docs/, main/*/docs/), interval=%s, debounce=%s, judge=%s, distill=%s, graph=%s",
		cfg.MetaRepoRoot, cfg.Reconcile.Interval, cfg.Reconcile.Debounce, onOff(cfg.Judge.Enabled), onOff(cfg.Distill.Enabled), onOff(cfg.Graph.URI != ""))
	if err := d.Run(ctx); err != nil {
		return err
	}
	logger.Printf("[daemon] 종료 신호 — 진행 중인 증류를 마치고 종료합니다")
	return d.Shutdown()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
