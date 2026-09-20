package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/tool"
)

func TestPlanManageHelpAction(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	output, err := runSvc.executePlanManageTool(sessionID, `{"action":"help"}`, "")
	if err != nil {
		t.Fatalf("plan_manage help failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal help payload: %v", err)
	}
	if payload["action"] != "help" || payload["status"] != "ok" {
		t.Fatalf("unexpected help response: %v", payload)
	}
	instructions, _ := payload["instructions"].(string)
	for _, want := range []string{
		"Canonical SessionPlanDocument Schema",
		"Feedback Intent Routing Table",
		"Worker V2 Schedule Specification",
		"start_session_checkpoint",
		"request_new_plan",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("help instructions missing %q: %s", want, instructions)
		}
	}
}

func TestTaskHelpAction(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	for _, tc := range []struct {
		topic string
		want  string
	}{
		{"", "Task Tool Delegation & Swarm Contract"},
		{"program", "Task Program Specification"},
		{"swarm", "Iteration Swarm & Rapid Alternatives Specification"},
	} {
		args := `{"action":"help"}`
		if tc.topic != "" {
			args = `{"action":"help","topic":"` + tc.topic + `"}`
		}
		output, err := runSvc.executeTaskTool(context.Background(), sessionID, "auto", 1, tool.Call{CallID: "call-help", Name: "task", Arguments: args}, nil)
		if err != nil {
			t.Fatalf("task help failed: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatalf("unmarshal task help payload: %v", err)
		}
		if payload["action"] != "help" || payload["status"] != "ok" {
			t.Fatalf("unexpected task help response: %v", payload)
		}
		instructions, _ := payload["instructions"].(string)
		if !strings.Contains(instructions, tc.want) {
			t.Fatalf("task help instructions missing %q: %s", tc.want, instructions)
		}
	}
}

func TestProviderManagedToolInvokerTaskHelpAction(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test"}
	for i, tc := range []struct {
		name string
		args string
		want string
	}{
		{"default help with prompt", `{"action":"help","prompt":"inspect task help"}`, "Task Tool Delegation & Swarm Contract"},
		{"program help without prompt", `{"action":"help","topic":"program"}`, "Task Program Specification"},
		{"swarm help with prompt", `{"action":"help","topic":"swarm","prompt":"swarm help"}`, "Iteration Swarm & Rapid Alternatives Specification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoker := runSvc.NewProviderManagedToolInvoker(ProviderManagedToolInvokerConfig{
				SessionID: sessionID, PermissionSessionID: sessionID, RunID: "run-test-help", Step: i + 1,
				SessionMode: sessionruntime.ModeAuto, Principal: principal, ProviderManagedV3: true,
				ApplySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
					if input.UserID == "" {
						input.UserID = principal.UserID
					}
					if input.AccountScopeID == "" {
						input.AccountScopeID = principal.AccountScopeID
					}
					return sessionSvc.ApplySessionMutation(input)
				},
			})
			result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
				CallID:    fmt.Sprintf("call-task-help-%d", i+1),
				Name:      "task",
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("execute tool failed: %v", err)
			}
			if result.Error != "" {
				t.Fatalf("unexpected tool error: %s", result.Error)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
				t.Fatalf("decode output: %v, raw=%s", err, result.Output)
			}
			if payload["action"] != "help" || payload["status"] != "ok" {
				t.Fatalf("unexpected payload: %#v", payload)
			}
			instructions, _ := payload["instructions"].(string)
			if !strings.Contains(instructions, tc.want) {
				t.Fatalf("instructions missing %q: %s", tc.want, instructions)
			}
		})
	}
}
