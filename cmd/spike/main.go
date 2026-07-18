// Spike: prove embedded-postgres + pgvector on macOS arm64.
// Success = CREATE EXTENSION vector works and cosine search returns expected order.
// This file is throwaway — it will be replaced by internal/pg once the approach is validated.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

const (
	pgvectorLibSrc     = "/opt/homebrew/Cellar/pgvector/0.8.5/lib/postgresql@18/vector.dylib"
	pgvectorControlDir = "/opt/homebrew/Cellar/pgvector/0.8.5/share/postgresql@18/extension"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("spike failed: %v", err)
	}
	fmt.Println("\nSPIKE OK: embedded-postgres + pgvector search works end-to-end.")
}

func run() error {
	tmp, err := os.MkdirTemp("", "chatmem-spike-*")
	if err != nil {
		return err
	}
	runtimePath := filepath.Join(tmp, "runtime")
	dataPath := filepath.Join(tmp, "data")
	fmt.Printf("scratch dir: %s\n", tmp)

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		RuntimePath(runtimePath).
		DataPath(dataPath).
		Port(54329).
		StartTimeout(60 * time.Second))

	if err := pg.Start(); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	defer func() {
		if err := pg.Stop(); err != nil {
			log.Printf("stop postgres: %v", err)
		}
	}()
	fmt.Println("postgres started on :54329")

	if err := installPgvectorFiles(runtimePath); err != nil {
		return fmt.Errorf("install pgvector: %w", err)
	}
	fmt.Println("pgvector files installed into embedded runtime")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, "postgres://postgres:postgres@localhost:54329/postgres?sslmode=disable")
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("CREATE EXTENSION vector: %w", err)
	}
	fmt.Println("CREATE EXTENSION vector — ok")

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS items (
			id   INT PRIMARY KEY,
			name TEXT,
			embedding vector(3)
		)`); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	rows := []struct {
		id   int
		name string
		vec  []float32
	}{
		{1, "apples-oranges", []float32{1, 2, 3}},
		{2, "bananas", []float32{4, 5, 6}},
		{3, "grapes", []float32{1, 1, 1}},
	}
	for _, r := range rows {
		if _, err := conn.Exec(ctx,
			"INSERT INTO items (id, name, embedding) VALUES ($1,$2,$3) ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding",
			r.id, r.name, pgvector.NewVector(r.vec)); err != nil {
			return fmt.Errorf("insert %d: %w", r.id, err)
		}
	}
	fmt.Println("inserted 3 rows")

	query := pgvector.NewVector([]float32{3, 1, 2})
	cur, err := conn.Query(ctx,
		"SELECT id, name, embedding <=> $1 AS distance FROM items ORDER BY distance LIMIT 3",
		query)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer cur.Close()

	fmt.Println("\ncosine-distance results (nearest first):")
	fmt.Println("  id | name              | distance")
	fmt.Println("  ---+-------------------+----------")
	for cur.Next() {
		var id int
		var name string
		var dist float64
		if err := cur.Scan(&id, &name, &dist); err != nil {
			return err
		}
		fmt.Printf("  %-3d| %-18s| %f\n", id, name, dist)
	}
	return cur.Err()
}

func installPgvectorFiles(runtimePath string) error {
	extLibDir := filepath.Join(runtimePath, "lib", "postgresql")
	extShareDir := filepath.Join(runtimePath, "share", "postgresql", "extension")

	if err := copyFile(pgvectorLibSrc, filepath.Join(extLibDir, "vector.dylib")); err != nil {
		return fmt.Errorf("copy dylib: %w", err)
	}

	entries, err := os.ReadDir(pgvectorControlDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(pgvectorControlDir, e.Name())
		dst := filepath.Join(extShareDir, e.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", e.Name(), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
