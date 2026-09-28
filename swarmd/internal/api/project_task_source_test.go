package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: submission identity is scoped to the project and exact source binding;
// threat: retrying the same task ID with changed payload or a different repository
// must not be treated as the original dispatch. projectTaskSubmissionHash is the
// narrowest pre-persistence boundary for this assertion.
func TestProjectTaskSubmissionHashBindsPayloadAndSource(t *testing.T) {
	input := tool.ProjectTaskCreateInput{ID: "task-1", ClientRequestID: "request-1", Title: "Change", Prompt: "Implement"}
	source := pebblestore.ProjectTaskSource{WorkspaceID: "workspace-1", WorkspaceGeneration: 2, Path: "/repo/one", Provenance: "explicit"}
	first, err := projectTaskSubmissionHash("project", input, source)
	if err != nil { t.Fatal(err) }
	input.ClientRequestID = "request-2"
	again, err := projectTaskSubmissionHash("project", input, source)
	if err != nil || first != again { t.Fatalf("request transport changed contract: %q %q %v", first, again, err) }
	input.Prompt = "Different"
	changed, err := projectTaskSubmissionHash("project", input, source)
	if err != nil || changed == first { t.Fatalf("changed prompt reused reservation: %q %v", changed, err) }
	input.Prompt = "Implement"
	source.Path = "/repo/two"
	changed, err = projectTaskSubmissionHash("project", input, source)
	if err != nil || changed == first { t.Fatalf("changed target reused reservation: %q %v", changed, err) }
}

// Purpose: an approved Coder can run only in the owned allocated worktree of
// the exact task and account, with its original source distinct from runtime.
// Threat: stale or forged session lineage could execute a task in another repo.
// verifyProjectTaskSession is the narrowest pre-run authority check.
func TestVerifyProjectTaskSessionRejectsMismatchedLineage(t *testing.T) {
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", SessionID: "session", Agent: "coder", WorkspacePath: "/runtime", SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "workspace", WorkspaceGeneration: 2, Path: "/source", Provenance: "explicit"}}
	session := pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", WorkspacePath: "/runtime", WorktreeRootPath: "/runtime", WorktreeEnabled: true, Metadata: map[string]any{"project_id": "project", "task_id": "task", "swarm_v3_source_workspace_path": "/source", "swarm_v3_source_workspace_id": "workspace", "swarm_v3_source_workspace_generation": float64(2), "swarm_v3_runtime_workspace_path": "/runtime", "swarm_v3_worktree_owner_session_id": "session"}}
	if err := verifyProjectTaskSession(task, session, "account"); err != nil { t.Fatal(err) }
	for name, corrupt := range map[string]func(*pebblestore.SessionSnapshot){
		"owner": func(s *pebblestore.SessionSnapshot){s.Metadata["swarm_v3_worktree_owner_session_id"] = "other"},
		"source": func(s *pebblestore.SessionSnapshot){s.Metadata["swarm_v3_source_workspace_path"] = "/other"},
		"runtime": func(s *pebblestore.SessionSnapshot){s.WorktreeRootPath = "/other"},
		"account": func(s *pebblestore.SessionSnapshot){s.AccountScopeID = "other"},
	} {
		t.Run(name, func(t *testing.T){
			copy := session
			copy.Metadata = make(map[string]any)
			for k,v := range session.Metadata { copy.Metadata[k] = v }
			corrupt(&copy)
			if err := verifyProjectTaskSession(task, copy, "account"); err == nil || !strings.Contains(err.Error(), "reservation") { t.Fatalf("accepted unrelated session: %v", err) }
		})
	}
}
