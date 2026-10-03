package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the real manage_projects dispatch must normalize provider-encoded and
// native patches without bypassing EditProjectTaskRequirements/V3 publication.
// This fixture is the narrowest end-to-end boundary proving rejected inputs leave
// durable content intact and a targeted edit invalidates old approval guards.
func TestProjectRequirementPatchToolBoundary(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		t.Run(map[bool]string{false: "object", true: "encoded"}[encoded], func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer f.db.Close()
			f.server.v3SessionExecutor = nil
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
			project := f.createProject(t)
			if err := f.server.sessions.Store().CompleteRepositoryHistoryMaintenance(ctx); err != nil { t.Fatal(err) }
			doc := &pebblestore.SessionPlanDocument{ID: "requirements", Title: "Review changes", Info: pebblestore.SessionPlanInfo{Goal: "Review targeted edits"}, Requirements: []pebblestore.SessionPlanRequirement{{ID: "review-edits", Text: "Review edits.", CheckpointID: "review"}, {ID: "keep", Text: "Keep other requirements.", CheckpointID: "review"}}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "review", Order: 1, Title: "Review", Tasks: []string{"Preserve technical implementation details"}, AcceptanceCriteria: []string{"Review edits.", "Keep other requirements."}}}}
			task, err := f.server.CreateProjectTask(ctx, p, project, tool.ProjectTaskCreateInput{Title: doc.Title, Prompt: doc.Info.Goal, Agent: "swarm", FeatureSize: "big", Document: doc})
			if err != nil { t.Fatal(err) }
			before, found, err := f.server.sessions.GetPlan(task.SessionID, doc.ID)
			if err != nil || !found { t.Fatalf("plan: %v %v", found, err) }
			rt := tool.NewRuntime(1)
			rt.SetManageProjectStore(f.server.sessions.Store())
			rt.SetManageSessionService(f.server.sessions)
			rt.SetProjectTaskLifecycleService(f.server)
			scope := tool.WorkspaceScope{Principal: p, PrimaryPath: t.TempDir()}
			invoke := func(patch any) error {
				args, err := json.Marshal(map[string]any{"action": "edit_requirements", "project_id": project, "task_id": task.ID, "document_patch": patch})
				if err != nil { t.Fatal(err) }
				_, err = rt.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(args)})
				return err
			}
			for _, bad := range []any{"{", "null", "[]", "42", nil, []any{}, 42, map[string]any{"operations": "wrong type"}} {
				if err := invoke(bad); err == nil || !strings.Contains(err.Error(), "document_patch") { t.Fatalf("invalid patch %v: %v", bad, err) }
				after, _, err := f.server.sessions.GetPlan(task.SessionID, doc.ID)
				if err != nil || !reflect.DeepEqual(before, after) { t.Fatalf("invalid patch changed plan: %v", err) }
			}
			patch := map[string]any{"base_revision_id": before.Document.RevisionID, "operations": []any{map[string]any{"operation": "edit_requirement", "requirement_id": "review-edits", "requirement": map[string]any{"id": "review-edits", "text": "Let me change one requirement and see exactly what changed, without rewriting the plan.", "checkpoint_id": "review"}}}}
			var input any = patch
			if encoded { raw, err := json.Marshal(patch); if err != nil { t.Fatal(err) }; input = string(raw) }
			if err := invoke(input); err != nil { t.Fatal(err) }
			after, _, err := f.server.sessions.GetPlan(task.SessionID, doc.ID)
			if err != nil { t.Fatal(err) }
			if after.Document.Requirements[0].Text == before.Document.Requirements[0].Text || !reflect.DeepEqual(after.Document.Requirements[1], before.Document.Requirements[1]) || !reflect.DeepEqual(after.Document.Checkpoints[0].Tasks, before.Document.Checkpoints[0].Tasks) || after.Document.RevisionID == before.Document.RevisionID { t.Fatal("targeted edit lost content or revision") }
			if err := invoke(input); err == nil { t.Fatal("accepted stale patch") }
			if _, err := f.server.ApproveProjectTask(ctx, p, project, task.ID, tool.ProjectTaskApprovalGuards{SessionID: task.SessionID, PlanID: doc.ID, DefinitionRevision: task.PlanBinding.DefinitionRevision}); err == nil { t.Fatal("accepted old approval") }
			final, _, err := f.server.sessions.GetPlan(task.SessionID, doc.ID)
			if err != nil || !reflect.DeepEqual(after, final) { t.Fatalf("rejected operation changed plan: %v", err) }
			updated, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, project, task.ID)
			if err != nil || updated.Status != "pending_approval" || updated.PlanBinding.DefinitionRevision <= task.PlanBinding.DefinitionRevision { t.Fatalf("missing renewed review: %+v %v", updated, err) }
			if intent, found, err := f.server.sessions.Store().GetV3SessionActiveRunIntent(task.SessionID); err != nil || found { t.Fatalf("unapproved execution: %+v %v", intent, err) }
		})
	}
}
