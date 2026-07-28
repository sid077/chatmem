package pg

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
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
