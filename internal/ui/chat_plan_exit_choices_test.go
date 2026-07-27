package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTakePlanExitResolutionPreservesApprovedArgumentsBeforeClosing(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "plan"})
	page.OpenExitPlanModePermissionModal("perm_exit", "plan-1", "Plan", "body", "", `{"title":"Plan","document":{"checkpoints":[{"id":"cp-1","title":"One"}]}}`)

	permissionID, _, approvedArguments := page.takePlanExitResolution(true)
	if permissionID != "perm_exit" {
		t.Fatalf("permission ID = %q", permissionID)
	}
	for _, want := range []string{`"title":"Plan"`, `"document"`, `"execution_granularity":"checkpointed"`} {
		if !strings.Contains(approvedArguments, want) {
			t.Fatalf("approved arguments %s missing %s", approvedArguments, want)
		}
	}
	if page.planExitModalActive() {
		t.Fatal("modal remained active after taking resolution")
	}
}

func TestExitPlanReviewToggleWritesCanonicalApprovedArguments(t *testing.T) {
	for _, tt := range []struct {
		name          string
		toggle        bool
		wantPolicy    string
		wantAutomatic string
	}{{"automatic by default", false, "automatic", `"continue_automatically":true`}, {"manual review", true, "review_each_checkpoint", `"continue_automatically":false`}} {
		t.Run(tt.name, func(t *testing.T) {
			page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "plan"})
			page.OpenExitPlanModePermissionModal("perm_exit", "plan-1", "Plan", "body", "", `{"keep":"value"}`)
			if tt.toggle {
				page.HandleKey(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
			}
			got := page.planExitApprovedArguments()
			for _, want := range []string{`"execution_granularity":"checkpointed"`, `"continuation_policy":"` + tt.wantPolicy + `"`, tt.wantAutomatic, `"keep":"value"`} {
				if !strings.Contains(got, want) {
					t.Fatalf("args %s missing %s", got, want)
				}
			}
		})
	}
}
