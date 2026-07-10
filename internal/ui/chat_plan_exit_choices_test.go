package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestExitPlanRunChoicesWriteCanonicalApprovedArguments(t *testing.T) {
	for _, tt := range []struct {
		key             rune
		wantGranularity string
		wantPolicy      string
		wantAutomatic   string
	}{{'1', "checkpointed", "automatic", `"continue_automatically":true`}, {'2', "run_through", "automatic", `"continue_automatically":true`}, {'3', "checkpointed", "review_each_checkpoint", `"continue_automatically":false`}} {
		page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "plan"})
		page.OpenExitPlanModePermissionModal("perm_exit", "plan-1", "Plan", "body", "", `{"keep":"value"}`)
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, tt.key, tcell.ModNone))
		got := page.planExitApprovedArguments()
		for _, want := range []string{`"execution_granularity":"` + tt.wantGranularity + `"`, `"continuation_policy":"` + tt.wantPolicy + `"`, tt.wantAutomatic, `"keep":"value"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("choice %c args %s missing %s", tt.key, got, want)
			}
		}
	}
}
