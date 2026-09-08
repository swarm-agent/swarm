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

// Requirement: resolved ask-user cards must reconstruct the user's choice and
// text from the durable permission reason after editor state is reset/reloaded.
// specializedPermissionCardRows is the narrow rendering boundary; stale editor
// state, missing answers, and denial must never fabricate a first-option pick.
func TestAskUserResolvedResponse(t *testing.T) {
	for _, tc := range []struct {
		name, reason, decision, pick, response string
	}{
		{"suggestion", "Beta", "allow_once", "› 2 Beta", "Your response: Beta"},
		{"custom", "S3 café terminalword", "allow_once", "› 3 Custom response", "S3 café terminalword"},
		{"missing", "", "allow_once", "", "No response recorded"},
		{"denied", "not an answer", "deny_once", "", "No response recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := client.PermissionRecord{ID: "ask", ToolName: "ask-user", Status: "resolved", Decision: tc.decision, Reason: tc.reason, ToolArguments: `{"question":"Choose","options":["Alpha","Beta"]}`}
			for _, interaction := range []*permissionInteractionView{nil, {AskSelection: 0, AskAnswers: map[string]string{"q_1": "Alpha"}}} {
				text := renderRowsText(specializedPermissionCardRows(record, 0, 60, testPageStyles(), false, false, "", interaction))
				if !strings.Contains(text, tc.response) || (tc.pick != "" && !strings.Contains(text, tc.pick)) {
					t.Fatalf("missing saved response or pick:\n%s", text)
				}
				for _, unwanted := range []string{"› 1 Alpha", "Response required", "S Submit", "Enter Select"} {
					if strings.Contains(text, unwanted) {
						t.Fatalf("resolved card retained %q:\n%s", unwanted, text)
					}
				}
				if tc.pick == "" && strings.Contains(text, "›") {
					t.Fatalf("fabricated a selection:\n%s", text)
				}
			}
		})
	}
}

// Requirement: every structured question's saved answer remains visible, not
// only question one; labels may differ from submitted values. Use the existing
// submission encoder and resolved-card boundary to verify their shared format.
func TestAskUserResolvedMultipleResponses(t *testing.T) {
	record := client.PermissionRecord{ID: "ask", ToolName: "ask-user", Status: "resolved", Decision: "allow_once", ToolArguments: `{"questions":[{"id":"target","question":"Where?","options":[{"label":"Staging","value":"stage"},{"label":"Production","value":"prod"}]},{"id":"note","question":"Why?","options":["First","Second"]}]}`}
	intent, _ := parseAskUserIntent(record)
	record.Reason, _ = askUserResolutionReason(intent, map[string]string{"target": "prod", "note": "my own reason"})
	text := renderRowsText(specializedPermissionCardRows(record, 0, 60, testPageStyles(), false, false, "", nil))
	for _, want := range []string{"Where?", "› 2 Production", "Your response: prod", "Why?", "› 3 Custom response", "Your response: my own reason"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}
