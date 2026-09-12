package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: executeMemoryTool must expose explicit object CRUD without silently
// clearing omitted fields or allowing stale/cross-account changes. A temporary
// store plus the runtime dispatcher is the narrowest layer proving both the
// argument contract and persisted postconditions; permission admission is
// independently asserted, including bypass mode.
func TestMemoryObjectOperations(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := store.NewMemoryStore(db)
	s := &Service{memoryStore: ms}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "a", UserID: "u"})
	other := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "b", UserID: "v"})
	get := func(account string) store.MemoryDocument {
		t.Helper()
		d, e := ms.GetForAccount(account)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	call := func(args string) {
		t.Helper()
		if _, e := s.executeMemoryTool(ctx, "", args); e != nil {
			t.Fatal(e)
		}
	}
	before := get("a")
	for _, args := range []string{
		// Reported failure shape: remember enabled, current revision, learned
		// content and explicit intent supplied, but entry_id omitted.
		`{"action":"remember","expected_revision":1,"intent":"user requested a discoverable memory interface","content":"Prefer a visible memory-management interface.","kind":"learned"}`,
		`{"action":"remember","expected_revision":1,"intent":"requested","entry_id":"one","content":"concise"}`,
	} {
		_, e := s.executeMemoryTool(ctx, "", args)
		if !errors.Is(e, store.ErrMemoryPolicy) || !strings.Contains(e.Error(), "requires") && !strings.Contains(e.Error(), "required") {
			t.Fatalf("missing actionable field error: %v", e)
		}
		if !strings.Contains(args, `"entry_id"`) && !strings.Contains(e.Error(), "entry_id") {
			t.Fatalf("missing-ID rejection did not identify entry_id: %v", e)
		}
		if !reflect.DeepEqual(before, get("a")) {
			t.Fatal("invalid write changed state")
		}
	}
	call(`{"action":"remember","expected_revision":1,"intent":"requested","entry_id":"one","kind":"rule","content":"concise","workspace_id":"scope","pinned":true,"purpose":"operational_context","subject":"Build setup"}`)
	original := get("a").Entries[0]
	call(`{"action":"edit","expected_revision":2,"intent":"requested edit","entry_id":"one","content":"very concise"}`)
	edited := get("a").Entries[0]
	if edited.Purpose != "operational_context" || edited.Subject != "Build setup" || edited.Origin != "user" || edited.Content != "very concise" || edited.Kind != original.Kind || edited.WorkspaceID != original.WorkspaceID || !edited.Pinned || edited.CreatedAt != original.CreatedAt || edited.Revision != original.Revision+1 {
		t.Fatalf("edit lost fields: %#v", edited)
	}
	before = get("a")
	for _, args := range []string{
		`{"action":"edit","expected_revision":2,"intent":"requested","entry_id":"one","content":"stale"}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"one","purpose":"permission"}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"one","origin":"learned"}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"missing","content":"bad"}`,
		`{"action":"edit","expected_revision":3,"entry_id":"one","content":"no intent"}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"one"}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"one","content":""}`,
		`{"action":"edit","expected_revision":3,"intent":"requested","entry_id":"one","account_scope_id":"b","content":"forged"}`,
		`{"action":"forget","expected_revision":2,"intent":"requested","entry_id":"one"}`,
	} {
		if _, e := s.executeMemoryTool(ctx, "", args); e == nil {
			t.Fatalf("accepted %s", args)
		}
		if !reflect.DeepEqual(before, get("a")) {
			t.Fatal("rejected mutation changed state")
		}
	}
	foreignBefore := get("b")
	for _, action := range []string{"edit", "forget"} {
		args := `{"action":"` + action + `","expected_revision":1,"intent":"requested","entry_id":"one","content":"foreign"}`
		if _, e := s.executeMemoryTool(other, "", args); e == nil {
			t.Fatal("foreign object accepted")
		}
	}
	if !reflect.DeepEqual(foreignBefore, get("b")) || !reflect.DeepEqual(before, get("a")) {
		t.Fatal("cross-account rejection changed state")
	}
	raw, e := s.executeMemoryTool(ctx, "", `{"action":"inspect"}`)
	if e != nil {
		t.Fatal(e)
	}
	var listed struct {
		Memory store.MemoryDocument `json:"memory"`
	}
	if e = json.Unmarshal([]byte(raw), &listed); e != nil || len(listed.Memory.Entries) != 1 {
		t.Fatalf("inspect: %v", e)
	}
	call(`{"action":"edit","expected_revision":3,"intent":"clear scope and pin","entry_id":"one","workspace_id":"","pinned":false}`)
	if e := get("a").Entries[0]; e.WorkspaceID != "" || e.Pinned {
		t.Fatal("explicit zero fields not applied")
	}
	for _, action := range []string{"remember", "edit", "forget"} {
		if _, required := permissionRequirement("auto+bypass_permissions", "manage_memory", `{"action":"`+action+`"}`); !required {
			t.Fatalf("%s bypassed permission", action)
		}
	}
	_, _, e = s.executeControlPlaneToolWithMutation(ctx, "", "auto", store.AgentProfile{Name: "coder", Mode: "subagent"}, 0, tool.Call{Name: "manage_memory", Arguments: `{"action":"forget","expected_revision":4,"intent":"requested","entry_id":"one"}`}, "", nil, nil)
	if e == nil || len(get("a").Entries) != 1 {
		t.Fatal("restricted agent modified memory")
	}
	call(`{"action":"forget","expected_revision":4,"intent":"requested forget","entry_id":"one"}`)
	d := get("a")
	if len(d.Entries) != 0 {
		t.Fatal("forget retained object")
	}
	for _, h := range d.History {
		if h.Before != nil || h.After != nil {
			t.Fatal("forget retained historical content")
		}
	}
	before = d
	if _, e = s.executeMemoryTool(ctx, "", `{"action":"remember","expected_revision":5,"intent":"requested","entry_id":"one","kind":"rule","content":"resurrect"}`); !errors.Is(e, store.ErrMemoryPolicy) {
		t.Fatal("forgotten ID resurrected")
	}
	if !reflect.DeepEqual(before, get("a")) {
		t.Fatal("resurrection changed state")
	}
}
