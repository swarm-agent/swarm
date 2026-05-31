package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestTopologyWorkspaceBindingStore(t *testing.T) *TopologyStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace-binding.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewTopologyStore(store)
}

func putTestLocalSelfPlacement(t *testing.T, topology *TopologyStore, accountScopeID, userID, runtimeSwarmID string, generation int) {
	t.Helper()
	if _, err := topology.PutRuntimeForAccount(accountScopeID, TopologyRuntimeRecord{SwarmID: runtimeSwarmID, UserID: userID, AccountScopeID: accountScopeID, Name: "local"}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := topology.PutRuntimePlacementForAccount(accountScopeID, TopologyRuntimePlacementRecord{RuntimeSwarmID: runtimeSwarmID, AccountScopeID: accountScopeID, AuthorityHostSwarmID: runtimeSwarmID, RuntimeKind: TopologyRuntimeKindHost, PlacementGeneration: generation, State: TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put placement: %v", err)
	}
}

func testLocalSelfWorkspaceBinding(accountScopeID, userID, workspaceID, workspacePath, runtimeSwarmID string, generation int) TopologyWorkspaceBindingRecord {
	return TopologyWorkspaceBindingRecord{
		UserID:                          userID,
		AccountScopeID:                  accountScopeID,
		SourceWorkspaceID:               workspaceID,
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "workspace-a",
		DestinationRuntimeSwarmID:       runtimeSwarmID,
		DestinationAuthorityHostSwarmID: runtimeSwarmID,
		DestinationRuntimeKind:          TopologyRuntimeKindHost,
		DestinationHostSwarmID:          runtimeSwarmID,
		DestinationWorkspacePath:        workspacePath,
		AccessMode:                      TopologyWorkspaceBindingAccessModeLocal,
		MaterializationKind:             TopologyWorkspaceBindingMaterializationSource,
		PlacementGeneration:             generation,
		BindingGeneration:               1,
		State:                           TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           runtimeSwarmID,
		Writable:                        true,
	}
}

func TestPutWorkspaceBindingForAccountRejectsDuplicateActiveBinding(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	base := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	first := base
	first.BindingID = "binding-a"
	if _, err := topology.PutWorkspaceBindingForAccount("account-a", first); err != nil {
		t.Fatalf("put first binding: %v", err)
	}
	second := base
	second.BindingID = "binding-b"
	_, err := topology.PutWorkspaceBindingForAccount("account-a", second)
	if err == nil || !strings.Contains(err.Error(), "active workspace binding already exists") {
		t.Fatalf("expected duplicate active binding error, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsNonWritable(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	desired := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	desired.Writable = false

	_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err == nil || !strings.Contains(err.Error(), "must be writable") {
		t.Fatalf("expected writable validation error, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountForcesDeterministicID(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	desired := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	desired.BindingID = "/workspace-a|local-swarm|/workspace-a"

	got, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err != nil {
		t.Fatalf("ensure binding: %v", err)
	}
	want := deterministicTopologyWorkspaceSelfBindingIDForRecord("account-a", normalizeTopologyWorkspaceBindingRecord(got))
	if got.BindingID != want {
		t.Fatalf("binding id = %q, want deterministic %q", got.BindingID, want)
	}
	if strings.Contains(got.BindingID, "/workspace-a") {
		t.Fatalf("binding id must not contain mutable workspace path: %q", got.BindingID)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsExistingWrongID(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	existing := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	existing.BindingID = "wsb_legacy_path_derived"
	if _, err := topology.PutWorkspaceBindingForAccount("account-a", existing); err != nil {
		t.Fatalf("put legacy binding: %v", err)
	}

	_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1))
	if err == nil || !strings.Contains(err.Error(), "deterministic binding id mismatch") {
		t.Fatalf("expected deterministic binding id mismatch, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsMissingPlacement(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	desired := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)

	_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err == nil || !strings.Contains(err.Error(), "runtime placement is required") {
		t.Fatalf("expected missing placement error, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsStaleSourceGeneration(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	existing := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	created, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", existing)
	if err != nil {
		t.Fatalf("ensure existing binding: %v", err)
	}
	desired := existing
	desired.SourceWorkspaceGeneration = 2

	_, err = topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err == nil || !strings.Contains(err.Error(), "source workspace generation mismatch") {
		t.Fatalf("expected source generation mismatch for %q, got %v", created.BindingID, err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsAccessMaterializationAttestationMismatch(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	existing := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	if _, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", existing); err != nil {
		t.Fatalf("ensure existing binding: %v", err)
	}
	cases := []struct {
		name      string
		mutate    func(*TopologyWorkspaceBindingRecord)
		wantError string
	}{
		{name: "state", mutate: func(r *TopologyWorkspaceBindingRecord) { r.State = "inactive" }, wantError: "state must be bound"},
		{name: "access", mutate: func(r *TopologyWorkspaceBindingRecord) { r.AccessMode = "remote" }, wantError: "access mode must be local"},
		{name: "materialization", mutate: func(r *TopologyWorkspaceBindingRecord) { r.MaterializationKind = "copy" }, wantError: "materialization kind must be source"},
		{name: "attestation", mutate: func(r *TopologyWorkspaceBindingRecord) { r.AttestedByHostSwarmID = "other-swarm" }, wantError: "attesting host must equal destination runtime swarm id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := existing
			tc.mutate(&desired)
			_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsDestinationPathMismatch(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	existing := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	if _, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", existing); err != nil {
		t.Fatalf("ensure existing binding: %v", err)
	}
	desired := existing
	desired.DestinationWorkspacePath = "/workspace-b"

	_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err == nil || !strings.Contains(err.Error(), "destination workspace path mismatch") {
		t.Fatalf("expected destination path mismatch, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountRejectsPlacementGenerationMismatch(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	existing := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)
	if _, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", existing); err != nil {
		t.Fatalf("ensure existing binding: %v", err)
	}
	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: "account-a", AuthorityHostSwarmID: "local-swarm", RuntimeKind: TopologyRuntimeKindHost, PlacementGeneration: 2, State: TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("update placement: %v", err)
	}
	desired := existing
	desired.PlacementGeneration = 2

	_, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err == nil || !strings.Contains(err.Error(), "placement generation mismatch") {
		t.Fatalf("expected placement generation mismatch, got %v", err)
	}
}

func TestEnsureLocalWorkspaceSelfBindingForAccountReusesExactBinding(t *testing.T) {
	topology := newTestTopologyWorkspaceBindingStore(t)
	putTestLocalSelfPlacement(t, topology, "account-a", "user-a", "local-swarm", 1)
	desired := testLocalSelfWorkspaceBinding("account-a", "user-a", "ws-a", "/workspace-a", "local-swarm", 1)

	first, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err != nil {
		t.Fatalf("first ensure binding: %v", err)
	}
	second, err := topology.EnsureLocalWorkspaceSelfBindingForAccount("account-a", desired)
	if err != nil {
		t.Fatalf("second ensure binding: %v", err)
	}
	if second.BindingID != first.BindingID || second.CreatedAt != first.CreatedAt || second.UpdatedAt != first.UpdatedAt {
		t.Fatalf("expected exact binding reuse, first=%+v second=%+v", first, second)
	}
	records, err := topology.ListWorkspaceBindingsForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("binding count = %d, want 1", len(records))
	}
}
