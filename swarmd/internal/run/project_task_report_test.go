package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: initial Coder/Finder and Swarm task sessions must receive only their
// task-report capability, and real provider invocation must reach authenticated
// V3 persistence. Foreign targets, caller authority, and project mutation remain
// rejected. This joined inventory/invoker test proves wiring without a provider.
func TestProjectTaskReportProviderAccess(t *testing.T) {
	for _, base := range []pebblestore.AgentProfile{agent.CoderAgentProfileForParent(pebblestore.AgentProfile{}), agent.FinderAgentProfileForParent(pebblestore.AgentProfile{}), agent.SwarmAgentProfileForContext(pebblestore.AgentProfile{})} {
		t.Run(base.Name, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := pebblestore.NewSessionStore(db)
			events, err := pebblestore.NewEventLog(db)
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.NewService(ss, events)
			rt := tool.NewRuntime(1)
			rt.SetManageSessionService(sessions)
			rt.SetManageProjectStore(ss)
			permissions := permission.NewService(pebblestore.NewPermissionStore(db), events, nil)
			permissions.SetBypassPermissions(true)
			svc := NewService(sessions, nil, nil, rt, permissions, nil, nil, events)
			if err := ss.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}); err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			for _, snapshot := range []pebblestore.SessionSnapshot{
				{ID: "parent", Metadata: map[string]any{"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}},
				{ID: "child", Mode: "auto", WorkspacePath: workspace, Metadata: map[string]any{"project_id": "project", "task_id": "task", "resolved_agent_name": base.Name}},
			} {
				_, err := ss.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: snapshot.ID, UserID: "user", AccountScopeID: "account", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &snapshot})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := ss.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: strings.TrimPrefix(base.Name, "system-"), Status: "in_progress", SessionID: "child"}); err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{pebblestore.V3RunIntentPendingExecutor, pebblestore.V3RunIntentRunning} {
				_, err := ss.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run", ParentSessionID: "parent", Status: status}})
				if err != nil {
					t.Fatal(err)
				}
			}
			principal := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
			scope := tool.WorkspaceScope{SessionID: "child", Principal: principal}
			profile, defs, err := svc.ResolveTaskHistoryTools(scope, base, nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range defs {
				if d.Name == "manage_projects" {
					found = true
					actions := d.Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
					if len(actions) != 1 || actions[0] != "report_task" {
						t.Fatalf("excess authority: %v", actions)
					}
				}
			}
			if !found {
				t.Fatal("report tool missing")
			}
			_, policy, _, err := svc.compileResolvedAgentToolContract("account", profile)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = identity.ContextWithPrincipal(ctx, principal)
			invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{sessionID: "child", runID: "run", sessionMode: "auto", workspacePath: workspace, principal: principal, agentProfile: profile, policy: policy})
			for i, args := range []string{
				`{"action":"report_task","project_id":"project","task_id":"other","update_kind":"progress","summary":"x","client_request_id":"foreign"}`,
				`{"action":"report_task","project_id":"project","task_id":"task","parent_session_id":"forged","update_kind":"wake_request","summary":"x","client_request_id":"forged"}`,
				`{"action":"delete_task","project_id":"project","task_id":"task","expected_revision":1}`,
				`{"action":"get_task","project_id":"project","task_id":"task"}`,
				`{"action":"report_task","project_id":"project","task_id":"task","update_kind":"attention","summary":"Input needed","client_request_id":"valid"}`,
			} {
				result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "report", Name: "manage_projects", Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				if i < 4 {
					if result.Error == "" {
						t.Fatalf("forbidden call accepted: %+v", result)
					}
				} else {
					if result.Error != "" {
						t.Fatal(result.Error)
					}
					var receipt map[string]any
					if err := json.Unmarshal([]byte(result.Output), &receipt); err != nil {
						t.Fatal(err)
					}
					if receipt["status"] != "recorded" || receipt["delivery"] != "pending" {
						t.Fatal(receipt)
					}
				}
			}
			task, _, _ := ss.GetProjectTask("account", "project", "task")
			if task.Status != "in_progress" || task.Revision != 1 {
				t.Fatal("report changed task lifecycle")
			}
		})
	}
}
