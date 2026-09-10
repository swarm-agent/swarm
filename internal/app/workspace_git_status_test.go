package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: saved-workspace readiness uses authenticated daemon Git state,
// even when the terminal user's Git cannot inspect the service-owned checkout.
// Threat: root onboarding falsely fails after successful admission, or accepts
// an unborn, wrong-root, or unavailable repository. This HTTP client/app boundary
// proves readiness and fail-closed mapping without ambient Git or root privileges.
func TestWorkspaceGitStatusAuthority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasGit     bool
		head, root string
		code       int
		want       model.GitReadiness
		wantErr    bool
	}{
		{"ready", true, "abc", "/workspace", 200, model.GitReadinessReady, false},
		{"unborn", true, "", "/workspace", 200, model.GitReadinessNeedsCommit, false},
		{"not-repository", false, "", "", 200, model.GitReadinessNotRepository, false},
		{"wrong-root", true, "abc", "/other", 200, model.GitReadinessCheckFailed, true},
		{"unavailable", false, "", "", 503, model.GitReadinessCheckFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/workspace/git/status" || r.URL.Query().Get("workspace_path") != "/workspace" || r.Header.Get("X-Swarm-Token") != "test-token" {
					t.Error("missing scoped authenticated Git request")
					http.Error(w, "invalid request", 400)
					return
				}
				if tc.code != 200 {
					http.Error(w, "unavailable", tc.code)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": client.GitSnapshot{WorkspacePath: "/workspace", RepoRoot: tc.root, HasGit: tc.hasGit, HeadOID: tc.head, Branch: "dev", DirtyCount: 2}})
			}))
			defer server.Close()
			a := &App{api: testAPIWithToken(server.URL)}
			got, err := a.workspaceGitStatus(context.Background(), "/workspace")
			if (err != nil) != tc.wantErr || got.Readiness != tc.want {
				t.Fatalf("status=%+v error=%v", got, err)
			}
			home := model.HomeModel{Workspaces: []model.Workspace{{Path: "/workspace", Active: true}}, Directories: []model.DirectoryItem{{ResolvedPath: "/workspace"}}}
			applyGitStatusToDirectory(&home.Directories[0], got)
			if homeModelHasActiveWorkspace(home, "/workspace") != (tc.want == model.GitReadinessReady) {
				t.Fatal("onboarding readiness mismatch")
			}
		})
	}
}
