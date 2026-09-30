package api

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func seedTaskSessionBinding(t *testing.T, f *matrixTestFixture, source pebblestore.ProjectTaskSource) pebblestore.TopologyWorkspaceBindingRecord {
	t.Helper()
	f.server.swarmStore = pebblestore.NewSwarmStore(f.db)
	f.server.topology = topologyruntime.NewService(pebblestore.NewTopologyStore(f.db), f.server.swarmStore)
	if _, err := f.server.swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "task-host", Name: "Task host", Role: "primary"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "task-host", UserID: f.userID, AccountScopeID: f.accountID, Name: "Task host", Relationship: "self", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	placement, err := f.server.topology.EnsureLocalSelfPlacementForPrincipal(f.accountID, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: "task-binding", UserID: f.userID, AccountScopeID: f.accountID,
		SourceWorkspaceID: source.WorkspaceID, SourceWorkspaceGeneration: source.WorkspaceGeneration,
		SourceWorkspacePath: source.Path, SourceWorkspaceName: "Repo",
		DestinationRuntimeSwarmID: "task-host", DestinationAuthorityHostSwarmID: "task-host", DestinationHostSwarmID: "task-host",
		DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationWorkspacePath: source.Path,
		PlacementGeneration: placement.PlacementGeneration, BindingGeneration: 1,
		State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: "task-host", Writable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestProjectTaskSessionBindingRejectsUnauthorizedAndStale(t *testing.T) {
	// Purpose: projectTaskSessionBinding must cross the real Sessions V3 create
	// resolver before allocation/create. Threat: foreign user, stale generation,
	// mismatched path or placement becomes runnable. Real catalog/topology is the
	// narrow authority layer; rejection must leave the session store untouched.
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	repo := t.TempDir()
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	source := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	binding := seedTaskSessionBinding(t, f, source)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	task := &pebblestore.ProjectTaskRecord{SourceWorkspace: source}
	if got, err := f.server.projectTaskSessionBinding(p, task); err != nil || got.WorkspaceBindingID != binding.BindingID {
		t.Fatalf("valid binding: %+v %v", got, err)
	}
	for _, mutate := range []func(*identity.Principal, *pebblestore.ProjectTaskRecord){
		func(p *identity.Principal, task *pebblestore.ProjectTaskRecord) { p.AccountScopeID = "foreign" },
		func(p *identity.Principal, task *pebblestore.ProjectTaskRecord) { p.UserID = "foreign-user" },
		func(p *identity.Principal, task *pebblestore.ProjectTaskRecord) {
			task.SourceWorkspace.WorkspaceGeneration++
		},
		func(p *identity.Principal, task *pebblestore.ProjectTaskRecord) {
			task.SourceWorkspace.Path = t.TempDir()
		},
	} {
		principal, changed := p, *task
		mutate(&principal, &changed)
		if _, err := f.server.projectTaskSessionBinding(principal, &changed); err == nil {
			t.Fatal("unauthorized/stale binding accepted")
		}
	}
	binding.PlacementGeneration++
	if _, err := f.server.topology.PutWorkspaceBindingForAccount(f.accountID, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.projectTaskSessionBinding(p, task); err == nil {
		t.Fatal("mismatched placement accepted")
	}
	sessions, err := f.server.sessions.ListSessionsForAccount(f.accountID, 10)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("binding validation created sessions: %+v %v", sessions, err)
	}
}
