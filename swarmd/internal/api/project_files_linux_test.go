//go:build linux

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func projectOutputCall(t *testing.T, folder string) *controlMCPCall {
	t.Helper()
	server, _ := newRunOwnerServer(t)
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "full")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "workspaces": []any{map[string]any{"path": folder}}}}})
	})
	wireWorkflowRoutes(c)
	if _, err := controlMCPSetAutomation(c, map[string]any{"name": "review", "scope": map[string]any{"folder": folder}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	return c
}

// Requirement: project outputs inherit ACL access or umask, not daemon-imposed
// masks. controlMCPExportWorkflow (including replacement), saveMediaFiles and
// installDeliverable own these writes. Real filesystem calls are the narrowest
// proof of inherited access; no chmod of existing user files is permitted.
func TestProjectOutputPermissions(t *testing.T) {
	for _, acl := range []bool{false, true} {
		t.Run(fmt.Sprintf("acl=%v", acl), func(t *testing.T) {
			folder := t.TempDir()
			if acl {
				for _, name := range []string{"setfacl", "getfacl"} {
					if _, err := exec.LookPath(name); err != nil {
						t.Skip(name + " unavailable")
					}
				}
				if out, err := exec.Command("setfacl", "-m", "d:u::rwx,d:u:65534:rwx,d:g::rwx,d:m::rwx,d:o::---", folder).CombinedOutput(); err != nil {
					t.Skipf("default ACL unavailable: %s: %v", out, err)
				}
			}
			// Read Linux's current umask without changing process-global state.
			status, err := exec.Command("sh", "-c", "printf 'Umask: '; umask").Output()
			if err != nil {
				t.Fatal(err)
			}
			var mask uint32
			found := false
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "Umask:") {
					_, err = fmt.Sscanf(line, "Umask: %o", &mask)
					found = err == nil
				}
			}
			if !found {
				t.Fatal("current umask unavailable")
			}
			c := projectOutputCall(t, folder)
			if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": ""}); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(folder)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			saved, err := saveMediaFiles(&pebblestore.ProjectTaskRecord{ID: "media", Title: "image"}, []controlMCPMediaFile{{mediaType: "image/png", data: []byte("image bytes")}}, "media/nested", root)
			if err != nil {
				t.Fatal(err)
			}
			workflow := filepath.Join(folder, ".swarm/workflows/review.json")
			delivered := filepath.Join(folder, "delivered.json")
			if err := installDeliverable(workflow, delivered); err != nil {
				t.Fatal(err)
			}
			check := func(path string, dir bool) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				expected := os.FileMode(0o666)
				aclMask := "rw-"
				if dir {
					expected = 0o777
					aclMask = "rwx"
				}
				if acl {
					out, err := exec.Command("getfacl", "-cpn", path).CombinedOutput()
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(out), "mask::"+aclMask+"\n") || !strings.Contains(string(out), "user:65534:rwx") {
						t.Fatalf("ACL cut access on %s: %s", path, out)
					}
				} else if info.Mode().Perm() != expected&^os.FileMode(mask) {
					t.Fatalf("mode %o, want %o", info.Mode().Perm(), expected&^os.FileMode(mask))
				}
			}
			for _, dir := range []string{".swarm", ".swarm/workflows", "media", "media/nested"} {
				check(filepath.Join(folder, dir), true)
			}
			check(workflow, false)
			check(filepath.Join(folder, saved[0]["path"].(string)), false)
			check(delivered, false)
			if err := os.WriteFile(workflow, []byte("different"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": "", "overwrite": true}); err != nil {
				t.Fatal(err)
			}
			check(workflow, false)
		})
	}
}

// Requirement: MCP refusals expose actionable safe text, but unexpected I/O
// stays generic. The real transfer handlers plus controlMCPToolResult are the
// narrowest MCP result boundary; rejected calls must not write project files.
func TestWorkflowMCPRefusalMessages(t *testing.T) {
	folder := t.TempDir()
	c := projectOutputCall(t, folder)
	for _, tc := range []struct{ path, want string }{
		{".git/x.json", "workflow export paths must not contain .git"},
		{"x.txt", "workflow export path must end in .json"},
		{"../x.json", "workflow path must be relative and inside the project folder"},
	} {
		value, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": tc.path})
		result := controlMCPToolResult(value, err)
		if result["isError"] != true || result["content"].([]map[string]any)[0]["text"] != tc.want {
			t.Fatal(result)
		}
	}
	value, err := controlMCPImportWorkflow(c, map[string]any{"definition": map[string]any{}})
	result := controlMCPToolResult(value, err)
	if result["isError"] != true || result["content"].([]map[string]any)[0]["text"] != "swarm_workflow must be version 1" {
		t.Fatal(result)
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal mutated folder: %v %v", entries, err)
	}
	result = controlMCPToolResult(nil, &os.PathError{Op: "open", Path: filepath.Join(folder, "private"), Err: os.ErrPermission})
	if result["isError"] != true || result["content"].([]map[string]any)[0]["text"] != "tool failed" {
		t.Fatal(result)
	}
}
