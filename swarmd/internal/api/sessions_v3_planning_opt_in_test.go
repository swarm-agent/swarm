package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: explicit public planning entry points must enable checkpoint tools
// while ordinary creation must not. Exercise HTTP creation/enter and MCP plan
// dispatch plus executor capability resolution, the boundaries that previously
// disagreed about opt-in; no provider calls are needed to prove this contract.
func TestSessionsV3ExplicitPlanningCapabilities(t *testing.T) {
	for _, entry := range []string{"default", "mode=plan", "enter", "swarm_run_plan"} {
		t.Run(entry, func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			setupSessionsV3ProviderManagedPlanTest(t, server, sessions, &sessionsV3RecordingProviderRunner{})
			server.v3SessionExecutor.startDelay = time.Hour
			workspace := t.TempDir()
			binding := seedSessionsV3PrimaryAuthority(t, server, workspace)
			mode := "auto"
			if entry == "mode=plan" {
				mode = "plan"
			}
			body := fmt.Sprintf(`{"client_request_id":"opt-in","workspace_path":%q,"swarm_id":"host-swarm-id","workspace_binding_id":%q,"target_kind":"host","target_relationship":"self","agent_name":"swarm","mode":%q}`, workspace, binding, mode)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))))
			if rec.Code != http.StatusOK {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			var response struct {
				Session pebblestore.SessionSnapshot `json:"session"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			id := response.Session.ID
			if entry == "enter" {
				postSessionsV3PlanModeTestJSON(t, server, id, "/plan-mode/enter", `{}`)
			}
			if entry == "swarm_run_plan" {
				// Stop only execution enqueueing: the real MCP handler and durable plan
				// lifecycle still run, and capabilities are inspected before any provider.
				call := controlMCPDirectCall(context.Background(), testPrincipal(), server.Handler(), server, "*")
				_, err := controlMCPRunPlanTool(call, map[string]any{"session_id": id, "plan": map[string]any{"title": "Opt in", "goal": "Complete explicit work", "checkpoints": []any{map[string]any{"title": "Work", "tasks": []any{"Perform work"}, "acceptance_criteria": []any{"Work complete"}}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			stored, ok, err := sessions.GetSession(id)
			if err != nil || !ok {
				t.Fatalf("load: %v %v", ok, err)
			}
			if entry != "swarm_run_plan" {
				profile, err := sessionV3AgentProfileFromMetadata(stored.Metadata)
				if err != nil {
					t.Fatal(err)
				}
				assertPlanningTools(t, profile, entry != "default")
			}
			executor := server.v3SessionExecutor
			runtime, err := executor.resolveSessionV3Capabilities(sessionV3ExecutorJob{SessionID: id, Principal: testPrincipal()})
			if err != nil {
				t.Fatal(err)
			}
			assertPlanningTools(t, runtime.AgentProfile, entry != "default")
		})
	}
}

func assertPlanningTools(t *testing.T, profile pebblestore.AgentProfile, want bool) {
	t.Helper()
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		config := profile.ToolContract.Tools[name]
		got := config.Enabled != nil && *config.Enabled
		if got != want {
			t.Errorf("%s enabled=%v want %v", name, got, want)
		}
	}
}
