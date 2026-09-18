package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
