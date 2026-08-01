package workspace

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildRoutingContextForPrincipalZeroEligible(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()

	addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/pending", "Pending", "", false)
	failed := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/failed", "Failed", "", false)
	if _, current, err := store.FailDefinitionForAccount(principal.AccountScopeID, failed.Path, failed.DefinitionGeneration, "analysis failed", "", 1); err != nil || !current {
		t.Fatalf("fail workspace definition current=%v err=%v", current, err)
	}
	addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/empty", "Empty", "   ", true)

	context, err := service.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		t.Fatalf("build routing context: %v", err)
	}
	if context.WorkspaceSelectionRequired || len(context.Workspaces) != 0 || context.bound != nil {
		t.Fatalf("zero-eligible context = %+v", context)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, ""); !errors.Is(err, ErrNoRoutableWorkspaces) {
		t.Fatalf("resolve zero-eligible error = %v, want %v", err, ErrNoRoutableWorkspaces)
	}
}

func TestBuildRoutingContextForPrincipalBindsSoleEligibleWorkspace(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()

	entry := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/private/sole", " Sole ", " sole definition ", true)
	context, err := service.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		t.Fatalf("build routing context: %v", err)
	}
	if context.WorkspaceSelectionRequired || len(context.Workspaces) != 0 || context.bound == nil {
		t.Fatalf("sole-workspace context = %+v", context)
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("marshal routing context: %v", err)
	}
	if strings.Contains(string(encoded), "/host/private") || strings.Contains(string(encoded), entry.WorkspaceID) {
		t.Fatalf("sole-workspace context advertised server binding: %s", encoded)
	}

	selection, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, "")
	if err != nil {
		t.Fatalf("resolve server-bound workspace: %v", err)
	}
	if selection.WorkspaceID != entry.WorkspaceID || selection.WorkspacePath != "/host/private/sole" || selection.WorkspaceName != "Sole" || selection.Definition != "sole definition" {
		t.Fatalf("selection = %+v", selection)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, entry.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("model selection for server-bound context error = %v", err)
	}
}

func TestBuildRoutingContextForPrincipalOffersMultipleDeterministicallyWithoutPaths(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()

	zeta := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/private/zeta", "zeta", "definition z", true)
	alphaUpper := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/private/alpha-upper", "Alpha", "definition upper", true)
	alphaLower := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/private/alpha-lower", "alpha", "definition lower", true)
	addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/private/pending", "Pending", "ignored", false)
	addRoutingWorkspace(t, store, "account-other", "/host/private/other", "Other", "other definition", true)

	context, err := service.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		t.Fatalf("build routing context: %v", err)
	}
	if !context.WorkspaceSelectionRequired {
		t.Fatalf("multiple-workspace context did not require selection: %+v", context)
	}
	gotIDs := []string{context.Workspaces[0].WorkspaceID, context.Workspaces[1].WorkspaceID, context.Workspaces[2].WorkspaceID}
	wantIDs := []string{alphaUpper.WorkspaceID, alphaLower.WorkspaceID, zeta.WorkspaceID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("workspace order = %v, want %v", gotIDs, wantIDs)
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("marshal routing context: %v", err)
	}
	if strings.Contains(string(encoded), "/host/private") || strings.Contains(string(encoded), "account-") {
		t.Fatalf("routing context leaked host/account details: %s", encoded)
	}
	if len(context.Workspaces) != 3 || context.Workspaces[0].Definition != "definition upper" {
		t.Fatalf("routing offers = %+v", context.Workspaces)
	}

	selection, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, alphaLower.WorkspaceID)
	if err != nil {
		t.Fatalf("resolve offered workspace: %v", err)
	}
	if selection.WorkspacePath != "/host/private/alpha-lower" || selection.WorkspaceID != alphaLower.WorkspaceID {
		t.Fatalf("resolved selection = %+v", selection)
	}
}

func TestResolveRoutingWorkspaceRejectsMissingUnlistedAndCrossAccountSelections(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()

	first := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/first", "First", "first definition", true)
	addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/second", "Second", "second definition", true)
	other := addRoutingWorkspace(t, store, "account-other", "/host/other", "Other", "other definition", true)
	context, err := service.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		t.Fatalf("build routing context: %v", err)
	}

	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, ""); !errors.Is(err, ErrRoutingWorkspaceSelectionRequired) {
		t.Fatalf("missing selection error = %v", err)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, "workspace-not-offered"); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("unlisted selection error = %v", err)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, other.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("cross-account selection error = %v", err)
	}
	otherPrincipal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "other-user", AccountScopeID: "account-other"}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(otherPrincipal, context, first.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("cross-account context reuse error = %v", err)
	}
}

func TestResolveRoutingWorkspaceRejectsDefinitionChangedAfterOffer(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()

	first := addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/first", "First", "first definition", true)
	addRoutingWorkspace(t, store, principal.AccountScopeID, "/host/second", "Second", "second definition", true)
	context, err := service.BuildRoutingContextForPrincipal(principal)
	if err != nil {
		t.Fatalf("build routing context: %v", err)
	}
	pending, err := store.MarkDefinitionPendingForAccount(principal.AccountScopeID, first.Path)
	if err != nil {
		t.Fatalf("mark definition pending: %v", err)
	}
	if _, current, err := store.CompleteDefinitionForAccount(principal.AccountScopeID, first.Path, pending.DefinitionGeneration, "changed definition", 1); err != nil || !current {
		t.Fatalf("complete changed definition current=%v err=%v", current, err)
	}

	if _, err := service.ResolveRoutingWorkspaceForPrincipal(principal, context, first.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("changed definition selection error = %v", err)
	}
}

func addRoutingWorkspace(t *testing.T, store *pebblestore.WorkspaceStore, accountScopeID, path, name, definition string, completed bool) pebblestore.WorkspaceEntry {
	t.Helper()
	entry, err := store.AddForAccount(accountScopeID, path, name)
	if err != nil {
		t.Fatalf("add workspace %q: %v", path, err)
	}
	pending, err := store.MarkDefinitionPendingForAccount(accountScopeID, path)
	if err != nil {
		t.Fatalf("mark workspace %q definition pending: %v", path, err)
	}
	if !completed {
		return pending
	}
	entry, current, err := store.CompleteDefinitionForAccount(accountScopeID, path, pending.DefinitionGeneration, definition, 1)
	if err != nil || !current {
		t.Fatalf("complete workspace %q definition current=%v err=%v", path, current, err)
	}
	return entry
}
