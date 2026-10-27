package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: the shared task Git projection used by get_job/wait_task must
// expose transient inspection failures as notes, not action_needed. Threat:
// asking users to repair Git that was merely slow. taskAssessmentAction is the
// narrow projection boundary; real mismatches must remain actionable.
func TestTaskInspectionStatusNote(t *testing.T) {
	for _, code := range []string{"inspection_timeout", "inspection_cancelled", "lane_branch_mismatch"} {
		a := pebblestore.TaskDeliveryAssessment{State: "unavailable", ReasonCode: code, Reason: "original reason"}
		action := taskAssessmentAction(&a)
		if code == "lane_branch_mismatch" {
			if action != "original reason" {
				t.Fatal(action)
			}
		} else if action != "" || a.Reason != "Git state not read in time; will retry" {
			t.Fatalf("action %q assessment %+v", action, a)
		}
		if a.State != "unavailable" || len(a.AllowedActions) != 0 {
			t.Fatal("unobserved state became actionable")
		}
	}
}

// Requirement: retain machine-readable inspection failures through the MCP
// adapter, without immediate hidden retries. Threat: string matching retries
// real mismatches, or multiple assessments hide the durable attempt count.
// The local HTTP dispatch seam proves this translation without live services.
func TestIntegrateInspectionCode(t *testing.T) {
	for _, code := range []string{"inspection_timeout", "inspection_cancelled", "lane_branch_mismatch"} {
		calls := 0
		c := &controlMCPCall{request: httptest.NewRequest(http.MethodPost, "/mcp", nil)}
		c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"tasks":[{"id":"task","session_id":"session","worktree_branch":"agent/task","base_branch":"dev"}]}`)
				return
			}
			calls++
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"error":"unobserved","delivery":{"freshness":"unknown","reason_code":%q,"reason":"Git state not read"}}`, code)
		})
		_, err := controlMCPIntegrate(c, map[string]any{"project_id": "project", "task_id": "task"}, "merge", "get")
		var typed controlMCPToolError
		if !errors.As(err, &typed) || typed.inspectionCode != code || calls != 1 {
			t.Fatalf("code %s calls %d error %#v", code, calls, err)
		}
	}
}
