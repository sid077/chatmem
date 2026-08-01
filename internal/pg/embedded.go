package pg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

func New(cfg Config) *Embedded {
	if cfg.LogWriter == nil {
		cfg.LogWriter = os.Stderr
	}
	if cfg.CacheDir == "" {
		// Default beside the runtime dir so all "regeneratable" state
		// lives under one predictable root.
		cfg.CacheDir = filepath.Join(filepath.Dir(cfg.RuntimeDir), "binary-cache")
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		RuntimePath(cfg.RuntimeDir).
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
	// (common when an MCP client restarts the server). Serialize with a
	// file lock on the runtime dir + drop a half-extracted runtime if we
	// detect one. Only extraction is locked; the actual PG boot happens
	// after the lock is released so second callers can attach.
	if err := e.bootstrapUnderLock(ctx); err != nil {
		return err
	}
	if err := e.pg.Start(); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	if err := e.installPgvector(); err != nil {
		return fmt.Errorf("install pgvector: %w", err)
	}
	pool, err := pgxpool.New(ctx, e.dsn)
	if err != nil {
		return fmt.Errorf("connect pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
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

// bootstrapUnderLock serializes the extract-postgres-tarball step so two
// concurrent chatmem processes (typical when an MCP client restarts the
// server) don't race and leave a half-extracted pg-runtime dir. Also
// detects a previously-crashed extraction (temp_*/ dirs sitting next to
// pg-runtime, or pg-runtime missing bin/postgres) and cleans up so
// embedded-postgres can re-extract cleanly.
func (e *Embedded) bootstrapUnderLock(ctx context.Context) error {
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

	// If pg-runtime exists but lacks bin/postgres, extraction was interrupted.
	// Wipe it so embedded-postgres re-extracts from scratch.
	pgBin := filepath.Join(e.cfg.RuntimeDir, "bin", "postgres")
	if _, err := os.Stat(e.cfg.RuntimeDir); err == nil {
		if _, err := os.Stat(pgBin); errors.Is(err, os.ErrNotExist) {
			if err := os.RemoveAll(e.cfg.RuntimeDir); err != nil {
				return fmt.Errorf("clean partial pg-runtime: %w", err)
			}
		}
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

func writeEmbedFile(embedPath, dst string) error {
	data, err := assetsFS.ReadFile(embedPath)
	if err != nil {
		return fmt.Errorf("read embed %s: %w", embedPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
