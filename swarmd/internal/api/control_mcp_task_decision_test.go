package api

import (
	"net/http"
	"net/http/httptest"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: wait_task must stop on a canonical pending decision, without
// declaring completion or rewriting durable execution state. Threat: an agent
// waits forever while its caller receives only in_progress; unauthorized session
// reads or old permissions must not mask a terminal failure. Owners: task,
// waitForTask and authenticated dispatch. This HTTP adapter fixture is the
// narrowest proof of read composition; GCP exercises real permission records.
func TestControlMCPTaskPendingDecision(t *testing.T) {
	for _, scenario := range []string{"question", "approval", "resolved", "failed", "forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			status := "in_progress"
			if scenario == "failed" {
				status = "failed"
			}
			c := &controlMCPCall{request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}}), next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatal("read mutated state")
				}
				switch r.URL.Path {
				case "/v3/projects/project/tasks/task":
					writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task", "session_id": "session", "status": status}})
				case "/v3/sessions/session":
					reads++
					if scenario == "forbidden" {
						writeJSON(w, 403, map[string]any{"error": "forbidden"})
						return
					}
					pending := []any{}
					if scenario != "resolved" {
						pending = append(pending, map[string]any{"id": "decision", "tool_name": scenario})
					}
					writeJSON(w, 200, map[string]any{"pending_permissions": pending})
				default:
					t.Fatalf("unexpected route: %s", r.URL.Path)
				}
			})}
			task, wait, err := c.waitForTask("project", "task", 0)
			if scenario == "forbidden" {
				if err == nil {
					t.Fatal("authorization failure swallowed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "question", "approval":
				if task["status"] != "needs_input" || wait["settled"] != true {
					t.Fatalf("decision hidden: %v %v", task, wait)
				}
			case "resolved":
				if task["status"] != "in_progress" || wait["settled"] != false {
					t.Fatalf("resolved wait persisted: %v", task)
				}
			case "failed":
				if task["status"] != "failed" || reads != 0 {
					t.Fatal("terminal outcome overridden")
				}
			}
		})
	}
}
