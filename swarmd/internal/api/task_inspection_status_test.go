package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"swarm/packages/swarmd/internal/jobautomation"
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
		} else if action != "" || a.Reason != "original reason" {
			t.Fatalf("action %q assessment %+v", action, a)
		}
		if code != "lane_branch_mismatch" {
			if taskStatusAssessment(&a).Reason != "Git state not read in time; will retry" || a.Reason != "original reason" {
				t.Fatal("status note changed mutation reason")
			}
		}
		if a.State != "unavailable" || len(a.AllowedActions) != 0 {
			t.Fatal("unobserved state became actionable")
		}
	}
}

// Requirement: retain machine-readable inspection failures through the MCP
// adapter. Interactive callers retry an unobserved state once in-call; the
// workflow path integrates once so its durable attempt count stays exact.
// The local HTTP dispatch seam proves this translation without live services.
func TestIntegrateInspectionCode(t *testing.T) {
	for _, workflow := range []bool{false, true} {
		for _, code := range []string{"inspection_timeout", "inspection_cancelled", "lane_branch_mismatch"} {
			calls := 0
			c := &controlMCPCall{request: httptest.NewRequest(http.MethodPost, "/mcp", nil), workflowDelivery: workflow}
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
			want := 2
			if workflow {
				want = 1
			}
			if !errors.As(err, &typed) || typed.inspectionCode != code || calls != want {
				t.Fatalf("workflow %v code %s calls %d error %#v", workflow, code, calls, err)
			}
		}
	}
}

// Purpose: workflowDeliveryError must preserve configured retries for ordinary
// operational errors, and refuse only known unsafe Git states. This adapter
// unit is the narrowest proof that HTTP status alone does not disable retries.
func TestWorkflowDeliveryErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		refused bool
	}{
		{"publish", errors.New("push failed"), false},
		{"store", controlMCPToolError{status: 500, message: "database not available"}, false},
		{"unknown conflict", controlMCPToolError{status: 409, message: "temporary failure"}, false},
		{"mismatch", controlMCPToolError{inspectionCode: "lane_branch_mismatch"}, true},
		{"dirty", controlMCPToolError{body: `{"delivery":{"source_dirty":true}}`}, true},
		{"conflict", controlMCPToolError{body: `{"delivery":{"state":"conflict"}}`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := workflowDeliveryError(tc.err)
			var refused *jobautomation.RefusedError
			if errors.As(got, &refused) != tc.refused {
				t.Fatalf("classification: %T %v", got, got)
			}
			if !tc.refused && got != tc.err {
				t.Fatal("ordinary retry error replaced")
			}
		})
	}
}

// Requirement: default workflows retry busy or changing repositories even with
// retries=0. The workflowDeliveryError boundary is the narrowest proof that
// these structured refusals enter the runner's bounded inspection retry policy.
func TestWorkflowUnreadStateRetries(t *testing.T) {
	for _, code := range []string{"capacity_exceeded", "heads_changed"} {
		for _, err := range []error{
			controlMCPToolError{inspectionCode: code, message: code},
			controlMCPToolError{body: fmt.Sprintf(`{"delivery":{"reason_code":%q}}`, code), message: code},
		} {
			got := workflowDeliveryError(err)
			var transient *jobautomation.TransientInspectionError
			if !errors.As(got, &transient) || transient.Reason != err.Error() {
				t.Fatalf("%s: %T %v", code, got, got)
			}
		}
	}
}
