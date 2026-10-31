package run

import (
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the Orchestrator's model is shown a refused or failed catalog
// call as its envelope (state refused or error, the reason in data), the
// same answer Swarm Control's client gets, not as plain error text; any
// other tool's failure keeps the run's error payload. Threat: the parity
// defect where errors, from a linked machine above all, reached one surface
// as plain text outside the envelope. Authority: prepareToolOutputForModel,
// the one function that turns a tool result into the model's text.
func TestModelSeesCatalogFailuresAsEnvelopes(t *testing.T) {
	reason := "linked machine box refused get_task: the machine answered HTTP 404: task t9 not found on box"
	envelope := tool.FailureEnvelope("manage_projects", "get_task", "box", true, errors.New(reason), reason)
	got := prepareToolOutputForModel(tool.Call{CallID: "c1", Name: "manage_projects"}, tool.Result{CallID: "c1", Name: "manage_projects", Error: reason, Output: envelope})
	if got != envelope || tool.CheckEnvelope(got) != nil {
		t.Fatalf("catalog failure shown as %s", got)
	}
	// Another tool's failure: the run's error payload, unchanged.
	got = prepareToolOutputForModel(tool.Call{CallID: "c2", Name: "bash"}, tool.Result{CallID: "c2", Name: "bash", Error: "exit status 1", Output: "exit status 1"})
	if !strings.Contains(got, `"path_id":"run.tool-output.v2"`) || !strings.Contains(got, "exit status 1") {
		t.Fatalf("bash failure shown as %s", got)
	}
	// Plain error text in a catalog tool's output is not passed as an envelope.
	got = prepareToolOutputForModel(tool.Call{CallID: "c3", Name: "manage_projects"}, tool.Result{CallID: "c3", Name: "manage_projects", Error: reason, Output: reason})
	if !strings.Contains(got, `"path_id":"run.tool-output.v2"`) {
		t.Fatalf("plain failure shown as %s", got)
	}
}
