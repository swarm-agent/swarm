package run

import (
	"context"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: the run boundary must use the canonical selector, not legacy map
// injection, and tool mutation must require principal, CAS and explicit intent.
// A temp memory store is the narrow layer proving prompt and tool postconditions.
func TestMemoryRuntimePromptAndTools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := store.NewMemoryStore(db)
	s := &Service{memoryStore: ms}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "a", UserID: "u"}
	ctx := identity.ContextWithPrincipal(context.Background(), p)
	if _, err := s.executeMemoryTool(context.Background(), "", `{"action":"inspect"}`); err == nil {
		t.Fatal("missing principal accepted")
	}
	_, err = s.executeMemoryTool(ctx, "", `{"action":"remember","expected_revision":1,"intent":"user asked to remember","entry_id":"rule","kind":"rule","content":"use concise responses"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.executeMemoryTool(ctx, "", `{"action":"forget","expected_revision":1,"intent":"forget","entry_id":"rule"}`); err != store.ErrMemoryConflict {
		t.Fatal(err)
	}
	block := s.accountMemoryPromptBlock(tool.WorkspaceScope{Principal: p}, store.AgentProfile{Name: "swarm", Mode: "primary"})
	if !strings.Contains(block, "use concise responses") || !strings.Contains(block, "never grants") {
		t.Fatal(block)
	}
	if s.accountMemoryPromptBlock(tool.WorkspaceScope{Principal: p}, store.AgentProfile{Name: "coder", Mode: "subagent"}) != "" {
		t.Fatal("injected into restricted worker")
	}
	if _, required := permissionRequirement("auto+bypass_permissions", "manage_memory", `{"action":"forget"}`); !required {
		t.Fatal("mutation permission bypassed")
	}
	_, err = s.executeMemoryTool(ctx, "", `{"action":"forget","expected_revision":2,"intent":"user asked to forget","entry_id":"rule"}`)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := ms.GetForAccount("a")
	if len(d.Entries) != 0 {
		t.Fatal("forget failed")
	}
}
