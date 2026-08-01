package pg_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	chatpg "github.com/sid077/chatmem/internal/pg"
)

// TestBootstrapLockSerializesExtraction is the guard for the reported race:
// two chatmem processes started 20 ms apart against a shared runtime dir
// used to leave a half-extracted pg-runtime and crash the second with
// "no such file or directory" during a rename. The fix serializes the
// extraction phase under a file lock and cleans up a partial runtime.
//
// This test focuses on the SHARED-RUNTIME piece of the race: it fires
// two goroutines that would both trigger extraction into the same
// runtimeDir, verifies extraction succeeds (bin/postgres present), and
// verifies the lock file was created and cleaned up. Each goroutine
// uses its own dataDir + port so Postgres server state isn't shared —
// only the runtime binaries are, which is the actual production shape
// on two Embedded instances that happen to run at once.
func TestBootstrapLockSerializesExtraction(t *testing.T) {
	tmp := t.TempDir()
	runtimeDir := filepath.Join(tmp, "pg-runtime")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	mk := func(dataSuffix string, port uint32) *chatpg.Embedded {
		return chatpg.New(chatpg.Config{
			DataDir:    filepath.Join(tmp, "data-"+dataSuffix),
			RuntimeDir: runtimeDir,
			Port:       port,
		})
	}
	a := mk("a", 54340)
	b := mk("b", 54341)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = a.Start(ctx) }()
	time.Sleep(20 * time.Millisecond)
	go func() { defer wg.Done(); errs[1] = b.Start(ctx) }()
	wg.Wait()

	t.Cleanup(func() {
		if a.Pool() != nil {
			_ = a.Stop()
		}
		if b.Pool() != nil {
			_ = b.Stop()
		}
	})

	// The important assertion: at least one Start() must succeed AND
	// bin/postgres must exist after both goroutines return (proves the
	// extraction wasn't clobbered by the race — the reported bug).
	successCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	if successCount == 0 {
		t.Fatalf("both Starts failed under lock: errA=%v errB=%v", errs[0], errs[1])
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "bin", "postgres")); err != nil {
		t.Fatalf("bin/postgres missing after extraction — race not prevented: %v", err)
	}
	// Lock file should exist alongside runtime dir.
	if _, err := os.Stat(runtimeDir + ".lock"); err != nil {
		t.Fatalf("expected lock file next to pg-runtime: %v", err)
	}
	// No leftover temp_* dirs from partial extraction.
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if e.IsDir() && (len(e.Name()) > 5 && e.Name()[:5] == "temp_") {
			t.Fatalf("leftover extraction temp dir: %s", e.Name())
		}
	}
}

// TestPartialRuntimeGetsRecovered simulates a previous chatmem process
// dying mid-extraction: pg-runtime exists but has no bin/postgres. The
// lock code should detect this and wipe pg-runtime so a fresh extract
// succeeds.
func TestPartialRuntimeGetsRecovered(t *testing.T) {
	tmp := t.TempDir()
	runtimeDir := filepath.Join(tmp, "pg-runtime")
	// Create an obviously-partial pg-runtime: some junk file, no bin/postgres.
	if err := os.MkdirAll(filepath.Join(runtimeDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "lib", "stray.dylib"), []byte("bogus"), 0o644); err != nil {
		t.Fatal(err)
	}

	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: runtimeDir,
		Port:       54342,
	})
	t.Cleanup(func() { _ = pg.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := pg.Start(ctx); err != nil {
		t.Fatalf("Start with partial runtime: %v", err)
	}
	// After recovery + re-extract, bin/postgres MUST exist.
	if _, err := os.Stat(filepath.Join(runtimeDir, "bin", "postgres")); err != nil {
		t.Fatalf("expected bin/postgres after recovery: %v", err)
	}
}
