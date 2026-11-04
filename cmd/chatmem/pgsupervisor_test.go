package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	chatpg "github.com/sid077/chatmem/internal/pg"
)

// TestSupervisorRestartsPostgresWhenOwnerExits reproduces the shared-PG
// failure: process A starts Postgres, process B attaches, A exits and stops
// the server. B must get a working database back without a restart.
func TestSupervisorRestartsPostgresWhenOwnerExits(t *testing.T) {
	tmp := t.TempDir()
	cfg := chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54344,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	owner := chatpg.New(cfg)
	if err := owner.Start(ctx); err != nil {
		t.Fatalf("owner start: %v", err)
	}
	pool, ok := tryAttachPostgres(ctx, cfg.Port)
	if !ok {
		_ = owner.Stop()
		t.Fatal("attach failed")
	}
	sup := newPGSupervisor(cfg, pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, wcancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); sup.watch(wctx, 200*time.Millisecond) }()
	// Mirror runMCP's shutdown order: stop watching (letting an in-flight
	// restart finish), then stop whatever this process owns.
	t.Cleanup(func() {
		wcancel()
		<-done
		if err := sup.stop(); err != nil {
			t.Errorf("supervisor stop: %v", err)
		}
	})

	if err := owner.Stop(); err != nil {
		t.Fatalf("owner stop: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		var one int
		err := pool.QueryRow(ctx, "SELECT 1").Scan(&one)
		if err == nil && one == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attached pool never recovered after owner exit: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
