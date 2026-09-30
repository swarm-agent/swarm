package run

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// A deterministic provider emits the actual provider-managed invocation rather
// than calling the lifecycle service directly. No implementation provider runs.
type taskPlanPublicationProvider struct{ arguments string }

func (p taskPlanPublicationProvider) submit(ctx context.Context, invoker provideriface.ToolInvoker) (provideriface.ToolExecutionResult, error) {
	return invoker.ExecuteTool(ctx, toolInvocation("publish-plan", "exit_plan_mode", p.arguments))
}

// Purpose: publication is not acceptance. Authority: executeProviderManagedToolCall,
// authorizeTaskPlanPublication, SubmitProjectTaskStructuredPlan and V3 mutations.
// This invocation layer is the narrowest layer reproducing the former permission
// wait; durable reads prove the pending review rather than assistant prose.
func TestProviderManagedTaskPlanPublication(t *testing.T) {
	for _, scenario := range []string{"publish", "wrong-account", "wrong-session", "stale-run", "stale-attempt", "disabled", "deny"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			svc, sessionID, permissions, storePath, cleanup := newTaskPlanPublicationTestService(t, workspace)
			defer cleanup()
			current, _, err := svc.sessions.SetMode(sessionID, sessionruntime.ModePlan)
			if err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: current.AccountScopeID, Title: "Review plan", Agent: "plan", Status: "planning", SessionID: sessionID, WorkspacePath: workspace}
			if err := svc.sessions.Store().PutProjectTask(current.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			runID := task.ExecutionRunID()
			if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, AccountScopeID: current.AccountScopeID, UserID: current.UserID, ClientRequestID: "planning-run", IdempotencyKey: "planning-run", PayloadHash: "planning-run", RequestHash: "planning-run", Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: pebblestore.V3RunIntentRunning}}); err != nil {
				t.Fatal(err)
			}
			principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: sessionID, AccountScopeID: current.AccountScopeID, UserID: current.UserID}
			config := providerToolInvokerConfig{sessionID: sessionID, permissionSessionID: sessionID, runID: runID, step: 1, sessionMode: sessionruntime.ModePlan, principal: principal, providerManagedV3: true, applySessionMutation: svc.sessions.ApplySessionMutation, agentProfile: pebblestore.AgentProfile{Name: "swarm", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}}
			switch scenario {
			case "wrong-account":
				config.principal.AccountScopeID = "other-account"
			case "wrong-session":
				config.principal.SessionID = "other-session"
			case "stale-run":
				config.runID = "old-run"
			case "stale-attempt":
				if _, err := svc.sessions.Store().UpdateProjectTask(current.AccountScopeID, "project", "task", func(task *pebblestore.ProjectTaskRecord) error {
					task.ActiveAttemptID = "next"
					task.Attempts = append(task.Attempts, pebblestore.ProjectTaskAttempt{ID: "next", SessionID: sessionID})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				config.agentProfile.ExitPlanModeEnabled = pebblestore.BoolPtr(false)
			case "deny":
				config.policy = &permission.Policy{Version: 1, Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Tool: "exit_plan_mode", Decision: permission.PolicyDecisionDeny}}}
			}
			before, _, err := svc.sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
			if err != nil {
				t.Fatal(err)
			}
			provider := taskPlanPublicationProvider{arguments: `{"document":{"id":"review-plan","title":"Review plan","info":{"goal":"Durable review before implementation"},"checkpoints":[{"id":"cp-1","title":"Implement","order":1,"status":"pending","tasks":["Implement after acceptance"],"acceptance_criteria":["Reviewed"]}]}}`}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, err := provider.submit(ctx, svc.newProviderToolInvoker(config))
			if err != nil {
				t.Fatalf("provider invocation: %v", err)
			}
			pending, err := permissions.ListPending(sessionID, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("publication created legacy approval: %#v err=%v", pending, err)
			}
			after, found, err := svc.sessions.Store().GetProjectTask(current.AccountScopeID, "project", "task")
			if err != nil || !found {
				t.Fatalf("read task: found=%t err=%v", found, err)
			}
			plan, planFound, err := svc.sessions.Store().GetPlan(sessionID, "review-plan")
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "publish" {
				if result.Error == "" || planFound || after.PlanBinding != nil || after.Revision != before.Revision || after.Status != before.Status {
					t.Fatalf("rejected publication mutated state: result=%+v task=%+v plan=%+v", result, after, plan)
				}
				return
			}
			if result.Error != "" || !strings.Contains(result.Output, "plan_submitted_for_review") || !planFound || plan.Document == nil || plan.ApprovalState != "pending" || plan.Status != "pending_approval" {
				t.Fatalf("publication did not complete: result=%+v plan=%+v", result, plan)
			}
			binding := after.PlanBinding
			if after.Status != "pending_approval" || binding == nil || binding.SessionID != sessionID || binding.PlanID != plan.ID || binding.DefinitionRevision != plan.Version || binding.Receipt == "" || after.PlanDocument != nil {
				t.Fatalf("task must bind canonical plan, not a copied document: %+v", after)
			}
			reloaded, _, err := svc.sessions.GetSession(sessionID)
			if err != nil || reloaded.Mode != sessionruntime.ModePlan {
				t.Fatalf("publication started Auto: %+v err=%v", reloaded, err)
			}
			active, found, err := svc.sessions.Store().GetV3SessionActiveRunIntent(sessionID)
			if err != nil || !found || active.RunID != runID {
				t.Fatalf("publication replaced planning run: %+v err=%v", active, err)
			}
			intents, err := svc.sessions.Store().ListRunIntents(sessionID, 10)
			if err != nil || len(intents) != 1 || intents[0].RunID != runID {
				t.Fatalf("publication created implementation intent: %+v err=%v", intents, err)
			}
			// Retrying the provider submission does not create a new definition.
			result, err = provider.submit(ctx, svc.newProviderToolInvoker(config))
			if err != nil || result.Error != "" {
				t.Fatalf("duplicate publication: %+v err=%v", result, err)
			}
			again, _, err := svc.sessions.Store().GetPlan(sessionID, binding.PlanID)
			if err != nil || again.Version != binding.DefinitionRevision || again.Document.RevisionID != plan.Document.RevisionID {
				t.Fatalf("duplicate changed definition: %+v err=%v", again, err)
			}
			cleanup()
			reopened, err := pebblestore.Open(storePath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			durable := pebblestore.NewSessionStore(reopened)
			restoredTask, found, err := durable.GetProjectTask(current.AccountScopeID, "project", "task")
			if err != nil || !found || restoredTask.Status != "pending_approval" || restoredTask.PlanBinding == nil || *restoredTask.PlanBinding != *binding {
				t.Fatalf("review binding lost on reopen: %+v err=%v", restoredTask, err)
			}
			restoredPlan, found, err := durable.GetPlan(binding.SessionID, binding.PlanID)
			if err != nil || !found || restoredPlan.ApprovalState != "pending" || restoredPlan.Version != binding.DefinitionRevision || restoredPlan.Document == nil || restoredPlan.Document.RevisionID != plan.Document.RevisionID || restoredPlan.Document.Info.Goal != plan.Document.Info.Goal {
				t.Fatalf("canonical document lost on reopen: %+v err=%v", restoredPlan, err)
			}
		})
	}
}

// Purpose: the task publication exception must not authorize standalone plans.
// Authority: authorizeTaskPlanPublication and the unchanged permission service.
// A real provider-managed invocation must still persist a pending plan_acceptance
// permission and must not save or start a plan without the user's resolution.
func TestProviderManagedStandalonePlanStillRequiresAcceptance(t *testing.T) {
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, t.TempDir())
	defer cleanup()
	current, _, err := svc.sessions.SetMode(sessionID, sessionruntime.ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	config := providerToolInvokerConfig{sessionID: sessionID, runID: "standalone-plan", sessionMode: sessionruntime.ModePlan, providerManagedV3: true, applySessionMutation: svc.sessions.ApplySessionMutation, principal: identity.Principal{Type: identity.PrincipalTypeUser, SessionID: sessionID, UserID: current.UserID, AccountScopeID: current.AccountScopeID}, agentProfile: pebblestore.AgentProfile{Name: "swarm", ExitPlanModeEnabled: pebblestore.BoolPtr(true)}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = (taskPlanPublicationProvider{arguments: `{"document":{"id":"standalone","title":"Standalone","checkpoints":[{"id":"cp-1","title":"Implement","order":1,"status":"pending"}]}}`}).submit(ctx, svc.newProviderToolInvoker(config))
	pending, err := permissions.ListPending(sessionID, 10)
	if err != nil || len(pending) != 1 || pending[0].Requirement != "plan_acceptance" || pending[0].Status != pebblestore.PermissionStatusPending {
		t.Fatalf("standalone acceptance permission missing: %#v err=%v", pending, err)
	}
	if _, found, err := svc.sessions.Store().GetPlan(sessionID, "standalone"); err != nil || found {
		t.Fatalf("unapproved standalone plan persisted: found=%t err=%v", found, err)
	}
}

func newTaskPlanPublicationTestService(t *testing.T, workspace string) (*Service, string, *permission.Service, string, func()) {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "state.pebble")
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = store.Close() }) }
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	current, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: "review-user", AccountScopeID: "review-account", Title: "Task review", WorkspacePath: workspace, Mode: sessionruntime.ModePlan, Metadata: map[string]any{"project_id": "project", "task_id": "task", "task_attempt_id": "initial"}})
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	return svc, current.ID, permissions, storePath, cleanup
}
