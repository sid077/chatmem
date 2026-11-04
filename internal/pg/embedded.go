package pg

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:assets
var assetsFS embed.FS

type Config struct {
	DataDir    string
	RuntimeDir string
	// CacheDir is where the Postgres binary tarball is cached across runs.
	// Defaults to $CACHE_HOME/chatmem/binary-cache. Overriding this stops
	// embedded-postgres from writing .embedded-postgres-go/ into $HOME or
	// the working directory (which can fail on read-only or shared homes).
	CacheDir string
	Port     uint32
	// LogWriter receives Postgres subprocess output (initdb + server logs).
	// Defaults to os.Stderr. Set to io.Discard to silence.
	// IMPORTANT: never leave this as os.Stdout in an MCP-over-stdio context.
	LogWriter io.Writer
}

type Embedded struct {
	cfg  Config
	pg   *embeddedpostgres.EmbeddedPostgres
	pool *pgxpool.Pool
	dsn  string
}

// pgVersion is the Postgres release we run. It is also written to
// <RuntimeDir>/.chatmem-pg-version so a binary upgrade re-extracts.
const pgVersion = embeddedpostgres.V18

const versionStamp = ".chatmem-pg-version"

func New(cfg Config) *Embedded {
	if cfg.LogWriter == nil {
		cfg.LogWriter = os.Stderr
	}
	if cfg.CacheDir == "" {
		// Default beside the runtime dir so all "regeneratable" state
		// lives under one predictable root.
		cfg.CacheDir = filepath.Join(filepath.Dir(cfg.RuntimeDir), "binary-cache")
	}
	// embedded-postgres RemoveAll()s RuntimePath on every Start, then
	// extracts into BinariesPath only when bin/pg_ctl is missing. Pointing
	// RuntimePath at a throwaway scratch dir and BinariesPath at RuntimeDir
	// means binaries are extracted once and never deleted from under a
	// running server.
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(pgVersion).
		RuntimePath(cfg.RuntimeDir + ".scratch").
		BinariesPath(cfg.RuntimeDir).
		DataPath(cfg.DataDir).
		CachePath(cfg.CacheDir).
		Port(cfg.Port).
		Logger(cfg.LogWriter).
		StartTimeout(90 * time.Second))
	return &Embedded{
		cfg: cfg,
		pg:  pg,
		dsn: fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", cfg.Port),
	}
}

func (e *Embedded) Start(ctx context.Context) error {
	// Bootstrap is racy when two chatmem processes launch simultaneously
	// (common when an MCP client restarts the server). The lock must cover
	// e.pg.Start() itself: that is where embedded-postgres extracts the
	// binaries, so releasing the lock before it protects nothing.
	if err := e.startUnderLock(ctx); err != nil {
		return err
	}
	// The server is running from here on; any failure must stop it, or the
	// caller is left with an orphaned postgres it doesn't know it owns.
	pool, err := pgxpool.New(ctx, e.dsn)
	if err != nil {
		_ = e.pg.Stop()
		return fmt.Errorf("connect pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = e.pg.Stop()
		return fmt.Errorf("ping: %w", err)
	}
	e.pool = pool
	return nil
}

func (e *Embedded) Stop() error {
	if e.pool != nil {
		e.pool.Close()
		e.pool = nil
	}
	return e.pg.Stop()
}

// startUnderLock serializes extraction + pgvector install + server boot so
// two concurrent chatmem processes (typical when an MCP client restarts
// the server) don't race on the shared pg-runtime dir. Also detects a
// previously-crashed extraction (temp_*/ dirs sitting next to pg-runtime,
// pg-runtime missing bin/postgres, or a stale version stamp) and cleans up
// so embedded-postgres can re-extract cleanly.
func (e *Embedded) startUnderLock(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(e.cfg.RuntimeDir), 0o755); err != nil {
		return fmt.Errorf("mkdir cache parent: %w", err)
	}
	lockPath := e.cfg.RuntimeDir + ".lock"
	lock := flock.New(lockPath)

	// Bounded wait: MCP clients can spawn a new chatmem before the previous
	// one finishes shutting down; give the previous bootstrap ~90s to finish
	// its extraction (embedded-postgres tarball extract on cold cache is
	// typically <30s).
	lockCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 250*time.Millisecond)
	if err != nil {
		return fmt.Errorf("acquire pg bootstrap lock (%s): %w", lockPath, err)
	}
	if !locked {
		return fmt.Errorf("another chatmem process is bootstrapping postgres (holding %s); retry in a moment", lockPath)
	}
	defer func() { _ = lock.Unlock() }()

	// Clean up any leftover temp_*/ dirs from a previously-crashed extract.
	// embedded-postgres creates temp_<rand>/ next to pg-runtime while it
	// unpacks; a crash mid-extract leaves them stranded and a subsequent
	// extraction can rename over their partials.
	entries, _ := os.ReadDir(filepath.Dir(e.cfg.RuntimeDir))
	for _, ent := range entries {
		if ent.IsDir() && strings.HasPrefix(ent.Name(), "temp_") {
			_ = os.RemoveAll(filepath.Join(filepath.Dir(e.cfg.RuntimeDir), ent.Name()))
		}
	}

	// If pg-runtime exists but lacks bin/postgres, extraction was interrupted;
	// if its version stamp doesn't match, it's from an older chatmem. Either
	// way wipe it so embedded-postgres re-extracts from scratch. Never wipe
	// while a server we can see is using it (its port is in use).
	if err := portFree(e.cfg.Port); err != nil {
		return err
	}
	pgBin := filepath.Join(e.cfg.RuntimeDir, "bin", "postgres")
	stamp := filepath.Join(e.cfg.RuntimeDir, versionStamp)
	if _, err := os.Stat(e.cfg.RuntimeDir); err == nil {
		_, binErr := os.Stat(pgBin)
		got, _ := os.ReadFile(stamp)
		if errors.Is(binErr, os.ErrNotExist) || strings.TrimSpace(string(got)) != string(pgVersion) {
			if err := os.RemoveAll(e.cfg.RuntimeDir); err != nil {
				return fmt.Errorf("clean stale pg-runtime: %w", err)
			}
		}
	}

	if err := e.pg.Start(); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	if err := os.WriteFile(stamp, []byte(pgVersion), 0o644); err != nil {
		_ = e.pg.Stop()
		return fmt.Errorf("write pg version stamp: %w", err)
	}
	if err := e.installPgvector(); err != nil {
		_ = e.pg.Stop()
		return fmt.Errorf("install pgvector: %w", err)
	}
	return nil
}

func (e *Embedded) Pool() *pgxpool.Pool { return e.pool }

func (e *Embedded) DSN() string { return e.dsn }

func (e *Embedded) installPgvector() error {
	platformDir := fmt.Sprintf("assets/%s_%s", runtime.GOOS, runtime.GOARCH)
	if _, err := fs.Stat(assetsFS, platformDir); err != nil {
		return fmt.Errorf("no embedded pgvector assets for %s/%s (build with assets for this platform)", runtime.GOOS, runtime.GOARCH)
	}

	libName := map[string]string{
		"darwin":  "vector.dylib",
		"linux":   "vector.so",
		"windows": "vector.dll",
	}[runtime.GOOS]
	if libName == "" {
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}

	if err := writeEmbedFile(
		platformDir+"/"+libName,
		filepath.Join(e.cfg.RuntimeDir, "lib", "postgresql", libName),
	); err != nil {
		return err
	}

	extSrc := platformDir + "/extension"
	extDst := filepath.Join(e.cfg.RuntimeDir, "share", "postgresql", "extension")
	entries, err := fs.ReadDir(assetsFS, extSrc)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if err := writeEmbedFile(extSrc+"/"+ent.Name(), filepath.Join(extDst, ent.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ErrPortInUse is returned by Start when something already listens on the
// configured port. Callers can treat it as "attach instead".
var ErrPortInUse = errors.New("postgres port already in use")

func portFree(port uint32) error {
	l, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return fmt.Errorf("%w: %d", ErrPortInUse, port)
	}
	return l.Close()
}

// writeEmbedFile installs an embedded asset. Identical files are left
// alone, and changed ones are swapped in via rename: truncating a shared
// library in place while a running postgres has it mapped can crash that
// server (on macOS, the kernel kills it for an invalid code signature).
func writeEmbedFile(embedPath, dst string) error {
	data, err := assetsFS.ReadFile(embedPath)
	if err != nil {
		return fmt.Errorf("read embed %s: %w", embedPath, err)
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
