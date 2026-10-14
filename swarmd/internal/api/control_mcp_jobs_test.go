package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: when a job settles for review with nothing committed (a
// finder's report, for example), get_job and give_job's wait say there is
// nothing to merge and the report is the result. Threat: the hint told the
// delegating AI to call swarm_deliver_job, which can only fail on a job with
// no commits. An unknown result keeps the merge hint so committed work is
// never hidden. controlMCPResultCommitted and controlMCPJobNext own the hint
// that jobView returns; a pure-function test is the narrowest layer.
func TestControlMCPJobNextForReportOnlyJobs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    map[string]any
		committed bool
	}{
		{"no commits by delivery", map[string]any{"delivery_state": "empty", "commits": []string{"abc fix"}}, false},
		{"no net change by delivery", map[string]any{"delivery_state": "empty"}, false},
		{"candidate work", map[string]any{"delivery_state": "candidate_work", "commits": []string{}}, true},
		{"already merged", map[string]any{"delivery_state": "integrated"}, true},
		{"empty branch log", map[string]any{"commits": []string{}}, false},
		{"branch log with commits", map[string]any{"commits": []string{"abc add script"}}, true},
		{"unknown", map[string]any{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed := controlMCPResultCommitted(tc.result)
			if committed != tc.committed {
				t.Fatalf("committed = %v, want %v", committed, tc.committed)
			}
			next := controlMCPJobNext("needs_review", committed)
			if merges := strings.Contains(next, "swarm_deliver_job"); merges != tc.committed {
				t.Fatalf("next %q suggests merging = %v, want %v", next, merges, tc.committed)
			}
			if !tc.committed && (!strings.Contains(next, "nothing to merge") || !strings.Contains(next, "swarm_get_job")) {
				t.Fatalf("report-only next %q does not say there is nothing to merge", next)
			}
		})
	}
	if next := controlMCPJobNext("completed", false); next != "the job is finished and merged" {
		t.Fatalf("other statuses changed: %q", next)
	}
}

func TestControlMCPFillWorktreeHead(t *testing.T) {
	head := strings.Repeat("c", 40)
	assessed := strings.Repeat("d", 40)
	cases := []struct {
		name    string
		out     map[string]any
		changes map[string]any
		want    any
	}{
		{"assessment head wins", map[string]any{"head": assessed}, map[string]any{"head": head}, assessed},
		{"worktree head when the assessment has none (sync)", map[string]any{}, map[string]any{"head": head, "base": strings.Repeat("e", 40)}, head},
		{"no worktree: no head", map[string]any{}, map[string]any{"head": head, "note": "works directly in its folder"}, nil},
		{"not a commit id", map[string]any{}, map[string]any{"head": "HEAD"}, nil},
	}
	for _, tc := range cases {
		controlMCPFillWorktreeHead(tc.out, tc.changes)
		if tc.out["head"] != tc.want {
			t.Errorf("%s: head = %v, want %v", tc.name, tc.out["head"], tc.want)
		}
	}
}

// Requirement: swarm_deliver_job merges a job whose first delivery assessment
// could not observe the task's Git state (a slow sandboxed Git read or heads
// moving under it): the integrate route refuses that with a 409 before any
// receipt or Git change, so the tool repeats it once. If it repeats, the
// refusal tells the AI to call swarm_deliver_job again and read swarm_get_job,
// never an HTTP route an AI key cannot call. An observed refusal (uncommitted
// changes, for example) is not retried. Threat: a transient Git timeout
// strands finished work, or the AI is sent to a route it cannot reach.
// Owners: controlMCPDeliverJob, controlMCPIntegrate and
// controlMCPUnobservedDelivery; the integrate route is a stub handler (its
// assessment is covered in worktree), so this is the narrowest layer for the
// tool's retry and wording. The 409 body uses the route's real message builder.
func TestControlMCPDeliverJobRetriesUnobservedDelivery(t *testing.T) {
	unobserved := &pebblestore.TaskDeliveryAssessment{State: "unavailable", ReasonCode: "inspection_timeout", Reason: "Git inspection exceeded its time budget", Freshness: "unknown", AllowedActions: []string{}}
	dirty := &pebblestore.TaskDeliveryAssessment{State: "candidate_work", ReasonCode: "candidate_delta", Freshness: "observed", SourceDirty: true, AllowedActions: []string{}}
	for _, tc := range []struct {
		name      string
		refusals  []*pebblestore.TaskDeliveryAssessment
		wantPosts int
		wantErr   []string
	}{
		{"unobserved once then merged", []*pebblestore.TaskDeliveryAssessment{unobserved}, 2, nil},
		{"unobserved twice", []*pebblestore.TaskDeliveryAssessment{unobserved, unobserved}, 2, []string{"nothing was merged", "inspection_timeout", "Call swarm_deliver_job again", "read swarm_get_job"}},
		{"observed refusal is not retried", []*pebblestore.TaskDeliveryAssessment{dirty}, 1, []string{"HTTP 409", "candidate_work"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			task := map[string]any{"id": "t1", "project_id": "p1", "status": "needs_review", "session_id": "s1", "worktree_branch": "agent/t1", "base_branch": "dev"}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/p1/tasks/t1":
					_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
				case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/p1/tasks":
					_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []any{task}})
				case r.Method == http.MethodPost && r.URL.Path == "/v3/projects/p1/tasks/t1/integrate":
					posts++
					if posts <= len(tc.refusals) {
						a := tc.refusals[posts-1]
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "code": "409", "error": taskDeliveryNotActionableMessage("p1", "t1", a), "delivery": a})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "integrated", "task": task, "integration": map[string]any{"target_branch": "dev", "resulting_target_head": "abc123"}})
				default:
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "unexpected " + r.Method + " " + r.URL.Path})
				}
			})
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r = r.WithContext(context.WithValue(r.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read", "sessions:write"}}))
			result, err := controlMCPDeliverJob(&controlMCPCall{request: r, next: h}, map[string]any{"job_id": "media/p1/t1"})
			if posts != tc.wantPosts {
				t.Fatalf("integrate requests: got %d, want %d (err %v)", posts, tc.wantPosts, err)
			}
			if tc.wantErr == nil {
				out, _ := result.(map[string]any)
				if err != nil || out["status"] != "integrated" || out["job_id"] != "media/p1/t1" {
					t.Fatalf("delivery not merged after one unobserved refusal: %+v %v", result, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("refused delivery reported success: %+v", result)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("%q missing %q", err.Error(), want)
				}
			}
			if posts == 2 && strings.Contains(err.Error(), "/v3/") {
				t.Fatalf("refusal sends the AI to an HTTP route: %q", err.Error())
			}
		})
	}
}
