package run

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: accountMemoryPromptBlock and the control-plane dispatcher must give
// primary Orchestrator (including reserved aliases), never Swarm/custom agents
// or workers, account context even without a checkout. A temp MemoryStore is the
// narrowest layer proving actual prompt/inspect contents and rejected-call
// postconditions; principal, CAS, intent and bypass approval gates remain intact.
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
	for _, name := range []string{"system-orchestrator", "orchestrator", "@orchestrator", "swarm-orchestrator", "system/swarm-orchestrator", " SYSTEM-ORCHESTRATOR "} {
		profile := store.AgentProfile{Name: name, Mode: "primary"}
		block := s.accountMemoryPromptBlock(tool.WorkspaceScope{Principal: p}, profile)
		if !strings.Contains(block, "use concise responses") || !strings.Contains(block, "never grants") {
			t.Fatalf("missing primary Orchestrator memory for %s: %s", name, block)
		}
		handled, result, err := s.executeControlPlaneTool(ctx, "", "auto+bypass_permissions", profile, 0, tool.Call{Name: "manage_memory", Arguments: `{"action":"inspect"}`}, "", nil)
		if !handled || err != nil || !strings.Contains(result.Output, "use concise responses") {
			t.Fatalf("Orchestrator inspect failed: %v %s", err, result.Output)
		}
	}
	before, err := ms.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	// Exact reserved identity is required; display labels and similarly named
	// custom profiles are not authority. A forged enabled contract is insufficient.
	for _, name := range []string{"swarm", "coder", "system-coder", "finder", "designer", "router", "compact", "system-orchestrator", "orchestrator", "@orchestrator", "swarm-orchestrator", "system/swarm-orchestrator", "Swarm Orchestrator", "my-orchestrator", "system-orchestrator-custom"} {
		for _, mode := range []string{"primary", "subagent"} {
			if mode == "primary" && (name == "system-orchestrator" || name == "orchestrator" || name == "@orchestrator" || name == "swarm-orchestrator" || name == "system/swarm-orchestrator") {
				continue
			}
			profile := store.AgentProfile{Name: name, Mode: mode, ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{"manage_memory": {Enabled: store.BoolPtr(true)}}}}
			if s.accountMemoryPromptBlock(tool.WorkspaceScope{Principal: p}, profile) != "" {
				t.Fatalf("injected into restricted %s/%s", name, mode)
			}
			for _, args := range []string{`{"action":"inspect"}`, `{"action":"forget","expected_revision":2,"intent":"requested","entry_id":"rule"}`} {
				handled, result, err := s.executeControlPlaneTool(ctx, "", "auto+bypass_permissions", profile, 0, tool.Call{Name: "manage_memory", Arguments: args}, "", nil)
				if !handled || err == nil || result.Output != "" {
					t.Fatalf("restricted call accepted for %s/%s: %v %s", name, mode, err, result.Output)
				}
			}
		}
	}
	after, err := ms.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restricted calls changed memory", err)
	}
	other := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "other", UserID: "other"}
	_, result, inspectErr := s.executeControlPlaneTool(identity.ContextWithPrincipal(context.Background(), other), "", "auto", store.AgentProfile{Name: "system-orchestrator", Mode: "primary"}, 0, tool.Call{Name: "manage_memory", Arguments: `{"action":"inspect"}`}, "", nil)
	if inspectErr != nil || strings.Contains(result.Output, "use concise responses") {
		t.Fatal("cross-account inspect leaked memory", inspectErr)
	}
	if strings.Contains(s.accountMemoryPromptBlock(tool.WorkspaceScope{Principal: other}, store.AgentProfile{Name: "system-orchestrator", Mode: "primary"}), "use concise responses") {
		t.Fatal("cross-account guidance leaked")
	}
	if _, required := permissionRequirement("auto+bypass_permissions", "manage_memory", `{"action":"forget"}`); !required {
		t.Fatal("mutation permission bypassed")
	}
	// Dispatcher execution is post-admission; ExplainPolicy tests separately
	// prove this mutation cannot reach dispatch without consent under bypass.
	_, _, err = s.executeControlPlaneTool(ctx, "", "auto", store.AgentProfile{Name: "system-orchestrator", Mode: "primary"}, 0, tool.Call{Name: "manage_memory", Arguments: `{"action":"forget","expected_revision":2,"intent":"user asked to forget","entry_id":"rule"}`}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := ms.GetForAccount("a")
	if len(d.Entries) != 0 {
		t.Fatal("forget failed")
	}
}

// Purpose: the runtime must keep SelectionForSession as scope/settings authority
// after the Orchestrator cutover. Checkout-free project sessions may receive
// account guidance, never another session's/workspace's data. A real temp store
// proves selection contents, settings/CAS gates and no mutation on failed reads.
func TestMemoryOrchestratorSelectionPolicy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := store.NewMemoryStore(db)
	s := &Service{memoryStore: ms}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "a", UserID: "u"}
	ctx := identity.ContextWithPrincipal(context.Background(), p)
	profile := store.AgentProfile{Name: "system-orchestrator", Mode: "primary"}
	if err := store.NewSessionStore(db).CreateSessionForAccount(store.SessionSnapshot{ID: "project-chat", Metadata: map[string]any{}}, "u", "a"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"action":"remember","expected_revision":1,"intent":"requested","entry_id":"account","kind":"rule","content":"account-only guidance"}`,
		`{"action":"remember","expected_revision":2,"intent":"requested","entry_id":"workspace","kind":"rule","content":"workspace-only guidance","workspace_id":"other-workspace"}`,
		`{"action":"remember","expected_revision":3,"intent":"requested","entry_id":"session","kind":"rule","content":"session-only guidance","session_id":"other-session"}`,
	} {
		if _, err := s.executeMemoryTool(ctx, "", args); err != nil {
			t.Fatal(err)
		}
	}
	scope := tool.WorkspaceScope{Principal: p, SessionID: "project-chat"}
	block := s.accountMemoryPromptBlock(scope, profile)
	if !strings.Contains(block, "account-only guidance") || strings.Contains(block, "workspace-only guidance") || strings.Contains(block, "session-only guidance") {
		t.Fatal("checkout-free selection scope violated", block)
	}
	if !strings.Contains(s.composeInstructionsForScopeWithDiscoveryRoots(scope, nil, profile, ""), "account-only guidance") {
		t.Fatal("primary Orchestrator composition lost memory")
	}
	// Prompt composition must not re-materialize a subagent as primary before
	// deciding memory eligibility.
	worker := store.AgentProfile{Name: "system-orchestrator", Mode: "subagent"}
	if strings.Contains(s.composeInstructionsForScopeWithDiscoveryRoots(scope, nil, worker, ""), "account-only guidance") {
		t.Fatal("profile reconciliation injected memory into worker")
	}
	before, err := ms.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	foreign := scope
	foreign.Principal.UserID = "other-user"
	if got := s.accountMemoryPromptBlock(foreign, profile); !strings.Contains(got, "unavailable") || strings.Contains(got, "account-only guidance") {
		t.Fatal("foreign session principal accepted", got)
	}
	_, result, err := s.executeControlPlaneTool(ctx, "missing-session", "auto", profile, 0, tool.Call{Name: "manage_memory", Arguments: `{"action":"inspect"}`}, "", nil)
	if err == nil || result.Output != "" {
		t.Fatal("unknown session read accepted")
	}
	after, err := ms.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed reads mutated memory", err)
	}
	applySettings := func(settings store.MemorySettings) {
		t.Helper()
		d, err := ms.GetForAccount("a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ms.MutateForAccount("a", store.MemoryMutation{ExpectedRevision: d.Revision, Actor: store.MemoryActor{Kind: "user", ID: "u"}, Reason: "Explicit settings choice", Operation: "settings", Settings: &settings}); err != nil {
			t.Fatal(err)
		}
	}
	settings := before.Settings
	settings.ReadEnabled = false
	applySettings(settings)
	if got := s.accountMemoryPromptBlock(scope, profile); got != "" {
		t.Fatal("disabled read injected memory", got)
	}
	settings.ReadEnabled = true
	settings.InjectionTokens = 1
	applySettings(settings)
	if got := s.accountMemoryPromptBlock(scope, profile); got != "" {
		t.Fatal("budget bypassed", got)
	}
	settings = before.Settings
	applySettings(settings)
	if err := store.NewSessionStore(db).CreateSessionForAccount(store.SessionSnapshot{ID: "other-session", Metadata: map[string]any{}}, "u", "a"); err != nil {
		t.Fatal(err)
	}
	selectedSession := tool.WorkspaceScope{Principal: p, SessionID: "other-session"}
	if got := s.accountMemoryPromptBlock(selectedSession, profile); !strings.Contains(got, "session-only guidance") {
		t.Fatal("owned session memory missing", got)
	}
	settings.ExcludedSessions = []string{"other-session"}
	applySettings(settings)
	if got := s.accountMemoryPromptBlock(selectedSession, profile); !strings.Contains(got, "account-only guidance") || strings.Contains(got, "session-only guidance") {
		t.Fatal("exclusion policy violated", got)
	}
	settings.RememberEnabled = false
	applySettings(settings)
	d, err := ms.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	args := fmt.Sprintf(`{"action":"remember","expected_revision":%d,"intent":"requested","entry_id":"disabled","kind":"rule","content":"not stored"}`, d.Revision)
	if _, err := s.executeMemoryTool(ctx, "project-chat", args); err == nil {
		t.Fatal("disabled remember accepted")
	}
	after, err = ms.GetForAccount("a")
	if err != nil || !reflect.DeepEqual(d, after) {
		t.Fatal("disabled remember changed state", err)
	}
}

// Purpose: resolveAgentToolStates must not let saved/custom contracts or presets
// resurrect manage_memory outside primary Orchestrator. This pure resolver test
// is the narrowest layer proving provider visibility before tool dispatch.
func TestMemoryToolVisibilityBoundary(t *testing.T) {
	for _, name := range []string{"swarm", "system-coder", "finder", "custom", "custom-orchestrator", "system-orchestrator", "orchestrator"} {
		for _, mode := range []string{"primary", "subagent"} {
			profile := store.AgentProfile{Name: name, Mode: mode, ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{"manage_memory": {Enabled: store.BoolPtr(true)}}}}
			resolved, err := resolveAgentToolStates(profile, map[string]struct{}{"manage_memory": {}})
			if err != nil {
				t.Fatal(err)
			}
			want := mode == "primary" && (name == "system-orchestrator" || name == "orchestrator")
			if resolved.Tools["manage_memory"].Enabled != want {
				t.Fatalf("wrong memory visibility for %s/%s: %+v", name, mode, resolved.Tools["manage_memory"])
			}
		}
	}
}
