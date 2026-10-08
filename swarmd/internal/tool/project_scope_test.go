package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

// Purpose: WithWorkspaceScope/workspaceScopeFromContext must not turn a rootless
// project scope into the caller's working directory. The runtime boundary is the
// narrowest layer proving a real file remains unreadable despite its existence.
func TestProjectScopeDoesNotInheritCallerFilesystem(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private.txt")
	if err := os.WriteFile(path, []byte("not project authority"), 0600); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	scope := WorkspaceScope{SessionID: "conversation", Principal: p, RejectScopeExpansion: true}
	bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx := WithWorkspaceScope(bounded, scope)
	got := workspaceScopeFromContext(ctx, root)
	if got.PrimaryPath != "" || len(got.Roots) != 0 || !got.RejectScopeExpansion || got.SessionID != scope.SessionID || got.Principal != p {
		t.Fatalf("caller scope leaked into project: %+v", got)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	results := NewRuntime(1).ExecuteBatch(ctx, root, []Call{{CallID: "read", Name: "read", Arguments: string(args)}})
	if len(results) != 1 || results[0].Error == "" || strings.Contains(results[0].Output, "not project authority") {
		t.Fatalf("project read escaped: %+v", results)
	}
	if len(resolveAllowedRoots(got)) != 0 || len(resolveMutableRoots(got)) != 0 {
		t.Fatal("rootless project acquired filesystem roots")
	}
}
