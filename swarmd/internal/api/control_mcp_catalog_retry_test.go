package api

import (
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: a manage_projects call with no action is list, as the
//     permission gate reads it (a read that runs unapproved); only an
//     explicit update that names a project and changes nothing writes its
//     project.md again (swarm_retry_project_context's handler).
//   - Threat/regression: an Orchestrator call such as manage_projects
//     {"id": ...} passing the gate as a read and then regenerating
//     project.md (POST /v3/projects/{id}/context:retry) without approval.
//   - Boundary/authority: Runtime.executeManageProjects (the omitted
//     action written back as list), tool.ProjectContextRetryCall and
//     catalogProjectContextRetry, over the real Orchestrator tool path and
//     the project store's ContextGeneration record.
//   - Narrowest layer: the Orchestrator's tool call on the in-process
//     harness; the store record is the observable write.
func TestManageProjectsWithoutActionNeverRetriesProjectContext(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "retry-probe", AccountID: h.home.account, Name: "Retry probe", ContextGeneration: &pebblestore.ProjectContextGeneration{Status: "failed", Attempt: 1, Error: "earlier failure"}}); err != nil {
		t.Fatal(err)
	}
	attempt := func() (int, string) {
		t.Helper()
		record, ok, err := store.GetProject(h.home.account, "retry-probe")
		if err != nil || !ok || record.ContextGeneration == nil {
			t.Fatalf("project: %v %v %+v", ok, err, record)
		}
		return record.ContextGeneration.Attempt, record.ContextGeneration.Status
	}
	orchestrator := func(args map[string]any) (string, error) {
		return h.agentToolCall(t, agentruntime.SwarmOrchestratorAgentID, "home-orch", "manage_projects", args)
	}
	out, err := orchestrator(map[string]any{"id": "retry-probe"})
	if err != nil {
		t.Fatalf("no-action call: %v", err)
	}
	if envelope := parityDecode(t, out); envelope["action"] != "list" {
		t.Fatalf("no-action call answered as %v, want list: %s", envelope["action"], out)
	}
	if got, status := attempt(); got != 1 || status != "failed" {
		t.Fatalf("no-action call changed project.md generation: attempt %d status %s", got, status)
	}
	// The same call with an explicit, approved update is the retry.
	out, err = orchestrator(map[string]any{"action": "update", "id": "retry-probe"})
	if got, status := attempt(); got == 1 && status == "failed" {
		t.Fatalf("explicit update did not retry project.md generation (attempt %d, %s): %v %s", got, status, err, out)
	}
	if err != nil && !strings.Contains(err.Error(), "project") {
		t.Fatalf("explicit update: %v", err)
	}
}
