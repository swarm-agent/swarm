package v3chat

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

// Requirement: numeric custom selection must focus an empty editor, never reuse
// a suggested answer or consume typed S/digits as shortcuts. HandleKey is the
// narrow real event boundary; the existing transport test covers submission.
func TestAskUserNumericCustomEditor(t *testing.T) {
	permission := client.PermissionRecord{ID: "ask", SessionID: "session-test", ToolName: "ask-user", Status: "pending", ToolArguments: `{"question":"Choose","options":["Alpha","Beta"]}`}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: permission.SessionID}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone))
	if !page.permissionAskCustomMode || len(page.permissionAskCustomInput) != 0 {
		t.Fatalf("custom editor state: mode=%v input=%q", page.permissionAskCustomMode, string(page.permissionAskCustomInput))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !page.permissionAskCustomMode || page.permissionAskAnswers["q_1"] != "" {
		t.Fatal("empty custom answer accepted or retained suggestion")
	}
	for _, r := range "S3 café" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if string(page.permissionAskCustomInput) != "S3 café" {
		t.Fatalf("lost typed text: %q", string(page.permissionAskCustomInput))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone))
	if page.scroll != 8 || page.follow {
		t.Fatal("question cannot scroll while editing")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	if page.scroll != 0 || !page.follow {
		t.Fatal("question cannot return to bottom")
	}
}

// Requirement: every word of a long choice survives narrow-card rendering.
// specializedPermissionCardRows is the rendering boundary where single-line
// labels were clipped; assert late words too, not just a prefix/snapshot.
func TestAskUserChoiceWrapPreservesText(t *testing.T) {
	permission := client.PermissionRecord{ID: "ask", ToolName: "ask-user", Status: "pending", ToolArguments: `{"question":"Choose","options":["Alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima november terminalword","Beta"]}`}
	rows := specializedPermissionCardRows(permission, 1, 32, testPageStyles(), true, false, "", nil)
	text := renderRowsText(rows)
	for _, word := range strings.Fields("Alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima november terminalword") {
		if !strings.Contains(text, word) {
			t.Fatalf("choice lost %q:\n%s", word, text)
		}
	}
}
