//go:build linux

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: workflow file operations must reject FIFOs without opening them.
// Threat: an untrusted *.json FIFO can indefinitely block import or overwrite.
// readWorkflowFile owns this boundary. Real-store MCP calls are the narrowest
// layer proving per-file refusal, single-file errors, and unchanged state.
func TestWorkflowFIFORefused(t *testing.T) {
	server, store := newRunOwnerServer(t)
	folder := t.TempDir()
	fifo := filepath.Join(folder, "x.json")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "name": "Site", "workspaces": []any{map[string]any{"path": folder, "workspace_id": "ws-1", "role": "primary_code"}}}}})
	})
	// Exercise the real workflow route; only project discovery is fixture-backed.
	wireWorkflowRoutes(c)
	if _, err := controlMCPSetAutomation(c, map[string]any{"name": "fifo-test", "scope": map[string]any{"folder": folder}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	before := storedAutomations(t, store)[0]
	for _, operation := range []string{"directory import", "single import", "export"} {
		t.Run(operation, func(t *testing.T) {
			type result struct {
				value any
				err   error
			}
			done := make(chan result, 1)
			go func() {
				var value any
				var err error
				switch operation {
				case "directory import":
					value, err = controlMCPImportWorkflow(c, map[string]any{"project": "Site", "path": "."})
				case "single import":
					value, err = controlMCPImportWorkflow(c, map[string]any{"project": "Site", "path": "x.json"})
				case "export":
					value, err = controlMCPExportWorkflow(c, map[string]any{"name": "fifo-test", "path": "x.json", "overwrite": true})
				}
				done <- result{value, err}
			}()
			select {
			case got := <-done:
				if operation == "directory import" {
					if got.err != nil {
						t.Fatal(got.err)
					}
					files := got.value.(map[string]any)["files"].([]any)
					if len(files) != 1 || files[0].(map[string]any)["path"] != "x.json" || files[0].(map[string]any)["status"] != "refused" || !strings.Contains(files[0].(map[string]any)["error"].(string), "regular file") {
						t.Fatalf("unexpected results: %v", files)
					}
				} else if got.err == nil || !strings.Contains(got.err.Error(), "regular file") {
					t.Fatalf("expected regular-file refusal: %v", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("workflow operation blocked on FIFO")
			}
		})
	}
	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO replaced: %v %v", info, err)
	}
	after := storedAutomations(t, store)
	if len(after) != 1 || after[0].Revision != before.Revision {
		t.Fatal("refused file mutated workflows")
	}
}
