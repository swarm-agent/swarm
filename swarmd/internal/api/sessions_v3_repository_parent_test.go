package api

import (
	"path/filepath"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: a shared Finder/program lane is owned by the immediate parent,
// not the child's allocator ID. Threat: unusable retained rows or foreign-parent
// source substitution. Temp-store provenance resolution asserts exact linkage,
// leaves unknown paths untouched and cannot grant mutation authority.
func TestSessionRepositoryParentIdentityIsExactAndOwned(t *testing.T) {
	server, principal, _ := newSessionRouterTestServer(t, &sessionRouterRecordingRunner{id: "recording"}, nil)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := pebblestore.NewSessionStore(store)
	server.sessions = sessionruntime.NewService(sessions, nil)
	source := filepath.Join(t.TempDir(), "source")
	lane := filepath.Join(t.TempDir(), "lane")
	put := func(row pebblestore.SessionSnapshot) {
		t.Helper()
		_, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: row.ID, AccountScopeID: row.AccountScopeID, UserID: row.UserID, Kind: pebblestore.V3SessionMutationCreateSession, Session: &row, IdempotencyKey: row.ID, RequestHash: row.ID, NowUnixMs: 100})
		if err != nil {
			t.Fatal(err)
		}
	}
	put(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: source, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/parent", Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "base_commit": "base"}})
	put(pebblestore.SessionSnapshot{ID: "child", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: lane, Metadata: map[string]any{"parent_session_id": "parent", "task_program_id": "program", "task_program_job_id": "job"}})
	program := pebblestore.TaskProgramRecord{ParentSessionID: "parent", ProgramID: "program", DefinitionHash: "hash", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "stage", DependencyEvidence: "fixture"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "stage", AgentType: "coder", Title: "Fixture", MetaPrompt: "Fixture", Deliverable: "Fixture", OwnedScope: []string{"file"}, DependencyEvidence: "fixture", AcceptanceCriteria: []string{"fixture"}}}}}
	program.Jobs = []pebblestore.TaskProgramJobRecord{{JobID: "job", StageID: "stage", State: pebblestore.TaskProgramJobDeclared}}
	created, _, err := server.sessions.CreateTaskProgram(program)
	if err != nil {
		t.Fatal(err)
	}
	// Newly declared state remains unchanged by the inventory-only reader.
	if inspected, found, err := server.sessions.InspectTaskProgram("parent", "program"); err != nil || !found || inspected.Revision != created.Revision {
		t.Fatalf("inspect: %v %v", found, err)
	}
	item := sessionRepositoryItem{SessionID: "child", Kind: "worker", SourcePath: lane, WorkspacePath: lane}
	got := server.resolveRepositoryParentIdentity(principal, item)
	if got.SourcePath != source || got.laneOwnerID != "parent" || got.BaseCommit != "base" || got.currentAuthority {
		t.Fatalf("wrong provenance: %+v", got)
	}
	foreign := principal
	foreign.AccountScopeID = "foreign"
	if got := server.resolveRepositoryParentIdentity(foreign, item); got.SourcePath != lane || got.laneOwnerID != "" {
		t.Fatal("foreign parent resolved")
	}
	unknown := item
	unknown.SourcePath = filepath.Join(source, "nested")
	unknown.WorkspacePath = unknown.SourcePath
	if got := server.resolveRepositoryParentIdentity(principal, unknown); got.SourcePath != unknown.SourcePath || got.laneOwnerID != "" {
		t.Fatal("path ancestry substituted identity")
	}
	if stored, _, _ := server.sessions.GetSession("child"); stored.WorkspacePath != lane || stored.Metadata["base_commit"] != nil {
		t.Fatal("read resolver mutated session")
	}
}
