package tool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: one rule decides what a person can do with a waiting task's plan,
// and the plan they read is the task's structured plan_document. Requirements:
// (1) a bound plan with its document is accept_plan; a missing document, a
// document of another plan or a binding without session or version is
// reload, never acceptable; an ordinary task is start; anything not waiting
// is none; (2) a decision matches only the exact current binding, an
// ordinary task's names none; (3) the markdown is the document's own fields,
// never plan_summary or full_plan_markdown; (4) get_task detail=plan (the
// Orchestrator's and Swarm Control's read, home and linked) carries the same
// decision and the plan_document unchanged. Threat: the generic router
// summary shown as the plan, or a plan accepted at a version nobody read.
// Owners: ProjectTaskPlanReviewDecision, BuildProjectTaskPlanReview,
// ProjectTaskPlanReviewMatches, ProjectTaskPlanMarkdown,
// CompactProjectTaskAnswer. Narrowest layer: the pure builders.
func TestProjectTaskPlanReviewRuleAndMarkdown(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{ID: "plan-a", Title: "Exact plan", Info: pebblestore.SessionPlanInfo{Goal: "The goal", Constraints: []string{"No shortcuts"}},
		Checkpoints:  []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Step one", Tasks: []string{"Write it"}, AcceptanceCriteria: []string{"It is written."}}},
		Requirements: []pebblestore.SessionPlanRequirement{{ID: "req-1", CheckpointID: "cp-1", Text: "It is written."}}}
	binding := &pebblestore.ProjectTaskPlanBinding{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 3, Receipt: "r"}
	bound := pebblestore.ProjectTaskRecord{ID: "t", ProjectID: "p", Title: "Plan task", Status: "pending_approval", SessionID: "s", PlanBinding: binding, PlanDocument: doc,
		PlanSummary: "Task Mission canned", FullPlanMarkdown: "# Task Mission\nSwarm Execution"}

	review := BuildProjectTaskPlanReview("forge", bound)
	if review.Decision != ProjectTaskReviewAcceptPlan || review.Machine != "forge" || review.PlanBinding.DefinitionRevision != 3 || !reflect.DeepEqual(review.PlanDocument, doc) {
		t.Fatalf("bound review: %+v", review)
	}
	for _, want := range []string{"# Exact plan", "## Objective\n\nThe goal", "## Constraints\n\n- No shortcuts", "**req-1** It is written. (checkpoint cp-1)", "### 1. Step one", "- Write it", "Acceptance criteria:"} {
		if !strings.Contains(review.PlanMarkdown, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, review.PlanMarkdown)
		}
	}
	if strings.Contains(review.PlanMarkdown, "Task Mission") || strings.Contains(review.PlanMarkdown, "Swarm Execution") {
		t.Fatalf("router text in the plan:\n%s", review.PlanMarkdown)
	}
	raw, _ := json.Marshal(review)
	if strings.Contains(string(raw), `"receipt"`) || strings.Contains(string(raw), "full_plan_markdown") {
		t.Fatalf("review leaks the receipt or the router markdown: %s", raw)
	}

	// (2) Exact binding only.
	if err := ProjectTaskPlanReviewMatches(review, ProjectTaskApprovalGuards{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 3}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []ProjectTaskApprovalGuards{{}, {SessionID: "s", PlanID: "plan-a", DefinitionRevision: 2}, {SessionID: "s", PlanID: "plan-b", DefinitionRevision: 3}, {SessionID: "x", PlanID: "plan-a", DefinitionRevision: 3}} {
		if ProjectTaskPlanReviewMatches(review, g) == nil {
			t.Fatalf("inexact binding matched: %+v", g)
		}
	}

	// (1) Reload, start and none.
	missing := bound
	missing.PlanDocument = nil
	other := bound
	other.PlanDocument = &pebblestore.SessionPlanDocument{ID: "plan-b", Title: "Other"}
	noVersion := bound
	noVersion.PlanBinding = &pebblestore.ProjectTaskPlanBinding{SessionID: "s", PlanID: "plan-a"}
	planAgent := pebblestore.ProjectTaskRecord{ID: "t2", Status: "pending_approval", Agent: "plan"}
	for label, task := range map[string]pebblestore.ProjectTaskRecord{"missing document": missing, "other plan's document": other, "no version": noVersion, "plan agent without binding": planAgent} {
		r := BuildProjectTaskPlanReview("", task)
		if r.Decision != ProjectTaskReviewReload || r.Problem == "" || r.PlanDocument != nil || r.PlanMarkdown != "" {
			t.Fatalf("%s: %+v", label, r)
		}
		if ProjectTaskPlanReviewMatches(r, ProjectTaskApprovalGuards{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 3}) == nil {
			t.Fatalf("%s accepted", label)
		}
	}
	plain := BuildProjectTaskPlanReview("", pebblestore.ProjectTaskRecord{ID: "t3", Status: "pending_approval", Agent: "coder"})
	if plain.Decision != ProjectTaskReviewStart || plain.PlanBound || ProjectTaskPlanReviewMatches(plain, ProjectTaskApprovalGuards{}) != nil {
		t.Fatalf("ordinary task: %+v", plain)
	}
	if ProjectTaskPlanReviewMatches(plain, ProjectTaskApprovalGuards{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 3}) == nil {
		t.Fatal("an ordinary task accepted a plan binding")
	}
	for _, status := range []string{"in_progress", "planning", "completed"} {
		started := bound
		started.Status = status
		if r := BuildProjectTaskPlanReview("", started); r.Decision != ProjectTaskReviewNone || ProjectTaskPlanReviewMatches(r, ProjectTaskApprovalGuards{SessionID: "s", PlanID: "plan-a", DefinitionRevision: 3}) == nil {
			t.Fatalf("%s task decided: %+v", status, r)
		}
	}

	// (4) The Orchestrator's get_task detail=plan: same decision, same document.
	answer, _ := json.Marshal(map[string]any{"task": bound})
	compact, err := CompactProjectTaskAnswer("get_task", ProjectTaskDetailPlan, answer)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Plan struct {
			PlanDocument   *pebblestore.SessionPlanDocument `json:"plan_document"`
			ReviewDecision string                           `json:"review_decision"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(compact, &view); err != nil || view.Plan.ReviewDecision != ProjectTaskReviewAcceptPlan || !reflect.DeepEqual(view.Plan.PlanDocument, doc) {
		t.Fatalf("get_task detail=plan: %s %v", compact, err)
	}
}
