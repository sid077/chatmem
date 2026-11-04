package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	chatpg "github.com/sid077/chatmem/internal/pg"
)

// pgSupervisor keeps Postgres reachable for the lifetime of one
// `chatmem mcp` process.
//
// Several MCP clients (Claude Desktop, each Claude Code session, Cursor…)
// share one Postgres: the first process starts it and the rest attach. When
// the starting process exits it stops that server, which used to leave every
// attached process failing with "connection refused" until restarted. The
// supervisor pings the pool and, when the server is gone, starts it again
// under the bootstrap lock; if another process wins that race, the pool
// simply reconnects to theirs.
type pgSupervisor struct {
	cfg  chatpg.Config
	pool *pgxpool.Pool // the pool the MCP handlers use; reconnects on its own
	log  *slog.Logger

	mu      sync.Mutex
	owned   []*chatpg.Embedded // servers this process started; stopped on exit
	stopped bool
}

func newPGSupervisor(cfg chatpg.Config, pool *pgxpool.Pool, owned *chatpg.Embedded, log *slog.Logger) *pgSupervisor {
	s := &pgSupervisor{cfg: cfg, pool: pool, log: log}
	if owned != nil {
		s.owned = append(s.owned, owned)
	}
	return s
}

// watch pings every interval until ctx is done.
func (s *pgSupervisor) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.pool.Ping(pctx)
		cancel()
		if err == nil || ctx.Err() != nil {
			continue
		}
		s.restart(ctx, err)
	}
}

func (s *pgSupervisor) restart(ctx context.Context, pingErr error) {
	pg := chatpg.New(s.cfg)
	if err := pg.Start(ctx); err != nil {
		if !errors.Is(err, chatpg.ErrPortInUse) {
			s.log.Warn("postgres unreachable and restart failed", "ping_err", pingErr, "err", err)
		}
		// Port in use: another chatmem restarted it first; our pool reconnects.
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		_ = pg.Stop()
		return
	}
	s.owned = append(s.owned, pg)
	s.log.Info("postgres had stopped (its owning process exited); restarted it", "port", s.cfg.Port)
}

// stop closes the pool and stops any server this process started.
func (s *pgSupervisor) stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.pool.Close()
	var errs []error
	for _, pg := range s.owned {
		if err := pg.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
