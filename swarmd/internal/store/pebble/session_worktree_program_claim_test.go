package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Purpose: ApplyV3SessionMutation -> prepareWorktreeOwnership ->
// validateRetainedWorktreeProgramClaims must let a parent publish task lineage
// after a Finder borrowed its lane, including retained failed programs. A read
// path must not become an exclusive ownership claim. The store boundary is the
// narrowest layer proving durable metadata/event publication and unchanged lane
// ownership, while real writer/integration claims still reject atomically.
func TestWorktreeProgramClaimsFinderLineage(t *testing.T) {
	for _, scenario := range []string{"finder-running", "finder-completed", "finder-failed-program", "coder", "workspace-designer", "unknown-agent", "missing-definition", "foreign-integration-lane", "foreign-owner"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			path := filepath.Join(t.TempDir(), "lane")
			createRecoverySession(t, s, "parent", path)
			program := taskProgramStoreFixture("parent", "discovery", "definition")
			program.Definition.Jobs[0].AgentType = "finder"
			program.Jobs[0].WorkspacePath = path
			program.Jobs[0].ChildSessionID = "child"
			program.Jobs[0].CurrentSessionID = "child"
			program.Jobs[0].State = TaskProgramJobCompleted
			program.State = TaskProgramStateRunning
			allowed := strings.HasPrefix(scenario, "finder-")
			switch scenario {
			case "finder-running":
				program.Jobs[0].State = TaskProgramJobRunning
			case "finder-failed-program":
				program.State = TaskProgramStateFailed
			case "coder":
				program.Definition.Jobs[0].AgentType = "coder"
			case "workspace-designer":
				program.Definition.Jobs[0].AgentType = "designer"
				program.Definition.Jobs[0].OutputMode = "workspace"
			case "unknown-agent":
				program.Definition.Jobs[0].AgentType = ""
			case "missing-definition":
				program.Jobs[0].JobID = "unmatched"
			case "foreign-integration-lane":
				program.ParentSessionID = "other-parent"
				program.RepositoryLane = &TaskProgramRepositoryLane{SourcePath: filepath.Join(t.TempDir(), "source"), WorkspacePath: path, Branch: "agent/parent", BaseCommit: strings.Repeat("a", 40)}
			}
			if _, _, err := s.CreateTaskProgram(program); err != nil {
				t.Fatal(err)
			}
			id := "parent"
			if scenario == "foreign-owner" {
				createRecoverySession(t, s, "intruder", "")
				id = "intruder"
			}
			before, ok, err := s.GetSession(id)
			if err != nil || !ok {
				t.Fatalf("session: found=%v err=%v", ok, err)
			}
			claimsBefore, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil {
				t.Fatal(err)
			}
			next := before
			next.Metadata = map[string]interface{}{"task_launches": map[string]interface{}{"next-task": map[string]interface{}{"status": "spawned"}}}
			if scenario == "foreign-owner" {
				next.WorktreeEnabled, next.WorktreeRootPath, next.WorktreeBranch = true, path, "agent/parent"
			}
			seq := uint64(1)
			_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationUpdateMetadata, IdempotencyKey: "lineage", PayloadHash: "lineage", Session: &next, ExpectedLastEventSeq: &seq})
			if allowed && err != nil {
				t.Fatalf("Finder blocked parent lineage: %v", err)
			}
			if !allowed && !errors.Is(err, ErrWorktreeRecoveryConflict) {
				t.Fatalf("exclusive claim bypass: %v", err)
			}
			after, ok, err := s.GetSession(id)
			if err != nil || !ok {
				t.Fatalf("session after mutation: found=%v err=%v", ok, err)
			}
			wantEvents := 1
			if allowed {
				wantEvents = 2
				if !reflect.DeepEqual(after.Metadata, next.Metadata) || after.WorktreeRootPath != path {
					t.Fatalf("lineage not published: %+v", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected mutation changed session")
			}
			events, err := s.ListV3SessionEvents(id, 0, 10)
			if err != nil || len(events) != wantEvents {
				t.Fatalf("events=%d want=%d err=%v", len(events), wantEvents, err)
			}
			claimsAfter, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil || !reflect.DeepEqual(claimsBefore, claimsAfter) {
				t.Fatalf("ownership changed: %+v err=%v", claimsAfter, err)
			}
		})
	}
}
