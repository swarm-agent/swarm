package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestPlanPermissionModalOpensBeforeApprovedArgumentsBackfill(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	record := ChatPermissionRecord{
		ID: "perm-plan", SessionID: "session-1", RunID: "run-1", ToolName: "plan_manage",
		Requirement: "plan_new_request", Status: "pending", ToolArguments: `{"title":"Plan","document":{"title":"Plan","checkpoints":[{"id":"cp-1","title":"Do it","status":"pending","order":1,"tasks":["Do it"],"acceptance_criteria":["Done"]}]}}`,
	}
	page.ApplyPermissionRecords([]ChatPermissionRecord{record})
	if !page.planPermissionModalActive() {
		t.Fatal("missing approved_arguments hid the pending plan permission")
	}
	if got := page.planPermissionApprovedArguments(); got != "" {
		t.Fatalf("approval unexpectedly available before canonical backfill: %q", got)
	}
	page.planPermissionManual = true
	record.UpdatedAt = 20
	record.ApprovedArguments = `{"action":"request_new_plan","document":{"title":"Plan"}}`
	page.ApplyPermissionRecords([]ChatPermissionRecord{record})
	if got := page.planPermissionApprovedArguments(); got == "" {
		t.Fatal("visible modal did not adopt later canonical approved arguments")
	}
	if !page.planPermissionManual {
		t.Fatal("canonical backfill reset the user's continuation choice")
	}
}

func TestPlanPermissionModalRoutesAllCanonicalPlanRequirements(t *testing.T) {
	for _, requirement := range []string{"plan_new_request", "plan_revision_request", "plan_amendment_request", "plan_followup_request"} {
		t.Run(requirement, func(t *testing.T) {
			page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
			record := planPermissionTestRecord("perm-"+requirement, "plan_manage", requirement)
			page.permissions = []ChatPermissionRecord{record}
			page.rebuildToolLifecycleViews()
			if !page.planPermissionModalActive() || page.planPermission != record.ID {
				t.Fatalf("plan modal state = active %v permission %q, want %q", page.planPermissionModalActive(), page.planPermission, record.ID)
			}
			if page.planUpdateModalActive() || page.planExitModalActive() || page.planEditorModalActive() {
				t.Fatal("V3 plan permission activated a legacy plan modal")
			}
		})
	}
}

func TestPlanPermissionModalRendersStructuredPlanFully(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "plan", AuthConfigured: true})
	record := planPermissionTestRecord("perm-exit", "exit_plan_mode", "tool")
	if !page.OpenPlanPermissionModal(record) {
		t.Fatal("OpenPlanPermissionModal returned false")
	}
	joined := renderLineTexts(page.planPermissionModalLines(120))
	for _, want := range []string{
		"Summary: Review the proposed plan",
		"Goal: Ship the focused change",
		"Checkpoint 1: Implement",
		"Objective: Build the component",
		"Tasks:",
		"Create the modal",
		"Acceptance criteria:",
		"All plan fields render",
		"Notes: Keep it isolated",
		"Checkpoint 2: Verify",
		"Inspect routing",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered content %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, `"checkpoints"`) || strings.Contains(joined, `{"`) {
		t.Fatalf("rendered content contains raw JSON: %q", joined)
	}
}

func TestPlanPermissionModalConsumesOnlyDecisionAndScrollingKeys(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "plan", AuthConfigured: true})
	page.OpenPlanPermissionModal(planPermissionTestRecord("perm-exit", "exit_plan_mode", "tool"))
	for _, event := range []*tcell.EventKey{
		tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone),
	} {
		page.HandleKey(event)
		if !page.planPermissionModalActive() {
			t.Fatal("non-decision legacy key closed the plan permission modal")
		}
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone))
	if page.planPermissionModalActive() {
		t.Fatal("d did not deny and close the plan permission modal")
	}
}

func TestPlanPermissionModalDefaultsAutomaticAndOverlaysOnlyExecutionPolicy(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "plan", AuthConfigured: true})
	record := planPermissionTestRecord("perm-exit", "exit_plan_mode", "tool")
	page.OpenPlanPermissionModal(record)
	if page.planPermissionManual {
		t.Fatal("new plan permission defaulted to manual review")
	}
	assertPlanPermissionArguments(t, page.planPermissionApprovedArguments(), true)

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	if !page.planPermissionManual {
		t.Fatal("m did not toggle manual checkpoint review")
	}
	assertPlanPermissionArguments(t, page.planPermissionApprovedArguments(), false)

	page.OpenPlanPermissionModal(record)
	if page.planPermissionManual {
		t.Fatal("newly opened plan permission did not reset automatic continuation")
	}
}

func TestPlanPermissionUpdatedClosesModalAndAdvancesQueue(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	first := planPermissionTestRecord("perm-first", "plan_manage", "plan_new_request")
	second := planPermissionTestRecord("perm-second", "plan_manage", "plan_revision_request")
	page.permissions = []ChatPermissionRecord{first, second}
	page.rebuildToolLifecycleViews()
	if page.planPermission != first.ID {
		t.Fatalf("opened %q, want first permission %q", page.planPermission, first.ID)
	}

	first.Status = "approved"
	first.UpdatedAt = 20
	page.permissions = mergePermissionHistory(page.permissions, []ChatPermissionRecord{first})
	page.rebuildToolLifecycleViews()
	if page.planPermission != second.ID {
		t.Fatalf("modal advanced to %q, want %q", page.planPermission, second.ID)
	}
}

func assertPlanPermissionArguments(t *testing.T, raw string, automatic bool) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("approved arguments are invalid: %v (%q)", err, raw)
	}
	if got["keep"] != "canonical" {
		t.Fatalf("canonical argument was not preserved: %#v", got)
	}
	if got["execution_granularity"] != "checkpointed" {
		t.Fatalf("execution_granularity = %#v", got["execution_granularity"])
	}
	wantPolicy := "review_each_checkpoint"
	if automatic {
		wantPolicy = "automatic"
	}
	if got["continuation_policy"] != wantPolicy || got["continue_automatically"] != automatic {
		t.Fatalf("execution policy = %#v, want policy %q automatic %v", got, wantPolicy, automatic)
	}
}

func planPermissionTestRecord(id, tool, requirement string) ChatPermissionRecord {
	return ChatPermissionRecord{
		ID:          id,
		SessionID:   "session-1",
		RunID:       "run-1",
		ToolName:    tool,
		Requirement: requirement,
		Status:      "pending",
		CreatedAt:   10,
		UpdatedAt:   10,
		ToolArguments: `{
			"title":"Focused plan",
			"summary":"Review the proposed plan",
			"document":{
				"title":"Focused plan",
				"info":{"goal":"Ship the focused change"},
				"checkpoints":[
					{"id":"cp-1","title":"Implement","order":1,"objective":"Build the component","tasks":["Create the modal"],"acceptance_criteria":["All plan fields render"],"notes":"Keep it isolated"},
					{"id":"cp-2","title":"Verify","order":2,"objective":"Inspect routing","tasks":["Check hydration and realtime"],"acceptance_criteria":["Both paths open the modal"],"notes":"No legacy modal"}
				]
			},
			"approved_arguments":{"keep":"canonical","document":{"title":"Focused plan","checkpoints":[{"id":"cp-1","title":"Implement"}]},"execution_granularity":"legacy","continuation_policy":"legacy","continue_automatically":false}
		}`,
	}
}
