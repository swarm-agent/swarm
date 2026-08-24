package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCreateUsesOnlyGitRepositoryAndSurvivesWorkspaceMove(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("STATE_DIRECTORY", state)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil { t.Fatal(err) }
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{{ID:"session-1", AccountScopeID:"account-1", UserID:"user-1", WorkspacePath:workspace}}}
	metadata := &authorityMetadata{}
	authority := NewAuthority(NewRegistry(resolver, Limits{}), metadata)
	principal := Principal{SessionID:"session-1", AccountScopeID:"account-1", UserID:"user-1"}
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID:"git-only-create", CollectionID:"collection", CollectionName:"Git", VariantID:"variant", Filename:"note.txt", MediaType:"text/plain", Body:[]byte("git only")})
	if err != nil { t.Fatal(err) }
	if _, err := os.Stat(filepath.Join(state, "workspaces")); !os.IsNotExist(err) { t.Fatalf("former workspace artifact tree exists: %v", err) }
	moved := filepath.Join(t.TempDir(), "moved-workspace"); if err := os.Rename(workspace, moved); err != nil { t.Fatal(err) }
	resolver.sessions[0].WorkspacePath = moved
	reopened := NewAuthority(NewRegistry(resolver, Limits{}), metadata)
	body, err := reopened.ReadVariant(context.Background(), principal, created, 1024)
	if err != nil || string(body)!="git only" { t.Fatalf("body=%q err=%v", body, err) }
}
