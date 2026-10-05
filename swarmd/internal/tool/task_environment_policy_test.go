package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type taskEnvironmentRoutingFixture struct {
	ProjectTaskLifecycleService
	calls      int
	action     string
	connection string
}

func (f *taskEnvironmentRoutingFixture) ManageTaskEnvironment(_ context.Context, _ identity.Principal, _ string, req TaskEnvironmentRequest) (TaskEnvironmentResult, error) {
	f.calls++
	f.action = req.Action
	f.connection = req.ConnectionID
	return TaskEnvironmentResult{}, errors.New("receipt boundary rejection")
}

// Purpose: executeManageEnvironments must route task inspection and mutations to
// the receipt boundary before touching generic deployment services. A nil generic
// manager proves no provider effect; the fixture verifies exact routing and error
// propagation, including cancellation aliases and missing metadata.
func TestTaskEnvironmentToolRouting(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"project_id": "project", "task_id": "task", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}},
		{"project_id": "project", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}},
	} {
		f := &taskEnvironmentRoutingFixture{}
		r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: metadata}}, projectTaskLifecycle: f, environmentsStore: &inspectionEnvironmentStore{}}
		scope := WorkspaceScope{SessionID: "session", Principal: identity.Principal{Type: "user", AccountScopeID: "account", UserID: "user"}}
		for _, action := range []string{"exec", "release", "get_deployment", "get_operation", "cancel_operation", "cancel", "build", "ensure", "deploy", "attach_task", "acquire_attachment", "list_attachments"} {
			before := f.calls
			out, err := r.executeManageEnvironments(context.Background(), scope, "call", map[string]any{"action": action})
			if err == nil || !strings.Contains(err.Error(), "receipt boundary rejection") || out != "" || f.calls != before+1 {
				t.Fatalf("%s bypassed boundary: %s %v calls=%d", action, out, err, f.calls)
			}
		}
		for _, action := range []string{"summary", "history", "list_deployments", "stop", "destroy", "start", "cleanup_review"} {
			before := f.calls
			out, err := r.executeManageEnvironments(context.Background(), scope, "call", map[string]any{"action": action})
			if err == nil || out != "" || f.calls != before {
				t.Fatalf("generic %s accepted: %s %v", action, out, err)
			}
		}
	}
}

// Purpose: the registered definition must expose the receipt interface actually
// decoded by executeTaskEnvironment; unit schema assertions catch unreachable
// handler-only actions without claiming end-to-end authorization evidence.
func TestTaskEnvironmentSchema(t *testing.T) {
	def := manageEnvironmentsDefinition()
	props := def.Parameters["properties"].(map[string]any)
	for _, key := range []string{"project_id", "task_id", "attempt_id", "attachment_id", "expected_task_revision", "expected_attachment_revision", "workspace_id", "deployment_id", "operation_id", "lease_id", "expires_at", "ttl_millis"} {
		if props[key] == nil {
			t.Fatalf("missing parameter %s", key)
		}
	}
	actions := props["action"].(map[string]any)["enum"].([]string)
	for _, want := range []string{"list_attachments", "attach_task", "detach_task", "acquire_attachment", "get_deployment", "get_operation", "cancel_operation"} {
		found := false
		for _, got := range actions {
			found = found || want == got
		}
		if !found {
			t.Fatalf("missing action %s", want)
		}
	}
}

// Purpose: Orchestrator requests naming a task must cross the same authority
// boundary as Swarm even before an attachment exists. Nil generic services prove
// preparation and receipt actions cannot silently fall back to workspace scope.
func TestTaskEnvironmentOrchestratorRouting(t *testing.T) {
	f := &taskEnvironmentRoutingFixture{}
	metadata := map[string]any{"project_id": "project", "agent_profile": pebblestore.AgentProfile{Name: "orchestrator"}}
	r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: metadata}}, projectTaskLifecycle: f, environmentsStore: &inspectionEnvironmentStore{}}
	scope := WorkspaceScope{SessionID: "session", Principal: identity.Principal{Type: "user", AccountScopeID: "account", UserID: "user"}}
	for _, action := range []string{"build", "ensure", "deploy", "get_operation", "cancel", "release_preparation"} {
		before := f.calls
		_, err := r.executeManageEnvironments(context.Background(), scope, "call", map[string]any{"action": action, "project_id": "project", "task_id": "task"})
		if err == nil || !strings.Contains(err.Error(), "receipt boundary rejection") || f.calls != before+1 {
			t.Fatalf("%s bypassed task: %v", action, err)
		}
	}
	_, err := r.executeTaskEnvironment(context.Background(), scope, map[string]any{"action": "exec", "env": map[string]string{"MODE": "test"}, "reason": "done", "idempotency_key": "request"})
	if err == nil || !strings.Contains(err.Error(), "receipt boundary rejection") {
		t.Fatalf("runtime options not decoded: %v", err)
	}
	before := f.calls
	if _, err := r.executeTaskEnvironment(context.Background(), scope, map[string]any{"action": "exec", "project_result": map[string]any{}}); err == nil || f.calls != before {
		t.Fatal("alternate source authority silently accepted")
	}
}

// Purpose: executeManageEnvironments must pass the existing connection override
// through strict task decoding, not reject it or fall back to generic execution.
// The rejecting service proves both exact forwarding and boundary enforcement.
func TestTaskEnvironmentConnectionRouting(t *testing.T) {
	f := &taskEnvironmentRoutingFixture{}
	metadata := map[string]any{"project_id": "project", "task_id": "task", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}}
	r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: metadata}}, projectTaskLifecycle: f, environmentsStore: &inspectionEnvironmentStore{}}
	scope := WorkspaceScope{SessionID: "session", Principal: identity.Principal{Type: "user", AccountScopeID: "account", UserID: "user"}}
	for _, action := range []string{"build", "ensure", "deploy"} {
		for _, connection := range []string{"", "selected-connection"} {
			before := f.calls
			_, err := r.executeManageEnvironments(context.Background(), scope, "call", map[string]any{"action": action, "connection_id": connection})
			if err == nil || !strings.Contains(err.Error(), "receipt boundary rejection") || f.calls != before+1 || f.connection != connection {
				t.Fatalf("override not forwarded: %v calls=%d connection=%q", err, f.calls, f.connection)
			}
		}
	}
}
