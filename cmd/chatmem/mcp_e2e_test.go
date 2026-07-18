package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Spawns the built chatmem binary as a subprocess and drives it over stdio
// using the SDK's client — the same code path Claude Code will use.
func TestBinaryStdioMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short mode")
	}
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "chatmem")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	// Isolate this test's PG data + runtime from any real install.
	dataDir := filepath.Join(tmp, "state")
	cacheDir := filepath.Join(tmp, "cache")
	cmd := exec.Command(binPath, "mcp", "--port", "54336")
	cmd.Env = append(os.Environ(),
		"CHATMEM_HOME="+dataDir,
		"CHATMEM_CACHE="+cacheDir,
	)
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	transport := &sdk.CommandTransport{Command: cmd}
	client := sdk.NewClient(&sdk.Implementation{Name: "e2e-test"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	found := map[string]bool{}
	for _, tool := range tools.Tools {
		found[tool.Name] = true
	}
	for _, want := range []string{"record_message", "get_conversation"} {
		if !found[want] {
			t.Fatalf("missing tool %s (got %+v)", want, found)
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "record_message",
		Arguments: map[string]any{
			"role":      "user",
			"content":   "e2e stdio test",
			"model":     "claude-opus-4-7",
			"provider":  "anthropic",
			"client_id": "e2e-test",
		},
	})
	if err != nil {
		t.Fatalf("call record_message: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	data, _ := json.Marshal(res.StructuredContent)
	if len(data) == 0 || string(data) == "null" {
		t.Fatalf("empty structured content: %+v", res.StructuredContent)
	}
}
