package pebblestore

import (
	"strings"
	"testing"
)

func TestPutWorkspaceBindingForAccountRequiresStrictFields(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	_, err := topology.PutWorkspaceBindingForAccount("account-a", TopologyWorkspaceBindingRecord{
		BindingID:           "binding-missing-strict-fields",
		UserID:              "user-a",
		AccountScopeID:      "account-a",
		SourceWorkspacePath: "/workspace-a",
	})
	if err == nil || !strings.Contains(err.Error(), "topology source workspace id is required") {
		t.Fatalf("expected strict source workspace id validation, got %v", err)
	}
}

func TestPutWorkspaceBindingForAccountValidatesPlacementAndIndexesActiveBinding(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	binding := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	binding.BindingID = "binding-a"
	if _, err := topology.PutWorkspaceBindingForAccount("account-a", binding); err != nil {
		t.Fatalf("put strict binding: %v", err)
	}

	var indexedBindingID string
	ok, err := topology.store.GetJSON(KeyTopologyWorkspaceBindingActiveForAccount("account-a", "ws-a", "local-swarm"), &indexedBindingID)
	if err != nil || !ok || indexedBindingID != binding.BindingID {
		t.Fatalf("active binding index ok=%t id=%q err=%v", ok, indexedBindingID, err)
	}

	got, ok, err := topology.GetWorkspaceBindingForAccount("account-a", binding.BindingID)
	if err != nil || !ok || got.SourceWorkspaceID != "ws-a" || got.PlacementGeneration != 1 {
		t.Fatalf("stored strict binding mismatch ok=%t err=%v record=%+v", ok, err, got)
	}
}

func TestPutWorkspaceBindingForAccountValidatesAccessMode(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	binding := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	binding.BindingID = "binding-invalid-access"
	binding.AccessMode = "local"
	_, err := topology.PutWorkspaceBindingForAccount("account-a", binding)
	if err == nil || !strings.Contains(err.Error(), "access mode must be read_only or read_write") {
		t.Fatalf("expected access mode validation, got %v", err)
	}
}

func TestPutWorkspaceBindingForAccountRejectsPlacementMismatch(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	binding := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 2)
	binding.BindingID = "binding-stale-placement"
	_, err := topology.PutWorkspaceBindingForAccount("account-a", binding)
	if err == nil || !strings.Contains(err.Error(), "placement generation does not match placement") {
		t.Fatalf("expected placement mismatch, got %v", err)
	}
}

func TestDeleteWorkspaceBindingForAccountClearsActiveIndex(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	binding := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	binding.BindingID = "binding-a"
	if _, err := topology.PutWorkspaceBindingForAccount("account-a", binding); err != nil {
		t.Fatalf("put strict binding: %v", err)
	}
	if err := topology.DeleteWorkspaceBindingForAccount("account-a", binding.BindingID); err != nil {
		t.Fatalf("delete binding: %v", err)
	}
	var indexedBindingID string
	ok, err := topology.store.GetJSON(KeyTopologyWorkspaceBindingActiveForAccount("account-a", "ws-a", "local-swarm"), &indexedBindingID)
	if err != nil || ok {
		t.Fatalf("active binding index remained ok=%t id=%q err=%v", ok, indexedBindingID, err)
	}
}

func TestPutWorkspaceBindingRejectsLegacyGlobalWrites(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	_, err := topology.PutWorkspaceBinding(TopologyWorkspaceBindingRecord{
		BindingID:           "legacy-global-binding",
		UserID:              "user-a",
		AccountScopeID:      "account-a",
		SourceWorkspacePath: "/workspace-a",
	})
	if err == nil || !strings.Contains(err.Error(), "legacy global topology workspace binding writes are forbidden") {
		t.Fatalf("expected forbidden legacy global write, got %v", err)
	}
}
