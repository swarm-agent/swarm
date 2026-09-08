package app

import (
	"strings"
	"testing"
)

// Requirement: /flag has Desktop's dev-only, existing-session contract and
// expands into an automatic routed task, never interpreting report text as flags.
// buildFlagTaskPrompt and parseTaskCommand are the narrow pre-dispatch boundary.
func TestFlagTaskPromptContract(t *testing.T) {
	for _, tc := range []struct {
		problem, session string
		dev              bool
	}{
		{"problem", "session-test", false}, {"problem", "", true}, {" ", "session-test", true},
	} {
		if prompt, err := buildFlagTaskPrompt(tc.problem, tc.session, tc.dev); err == nil || prompt != "" {
			t.Fatalf("invalid flag accepted: %q, %v", prompt, err)
		}
	}
	problem := "plan --workspace elsewhere\nchoices disappear"
	prompt, err := buildFlagTaskPrompt(problem, " session-test ", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Prior session ID: session-test", problem, "./scripts/session-dump-via-api.sh", "never inspect Pebble directly", "report the exact blocker"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in %q", want, prompt)
		}
	}
	parsed, err := parseTaskCommand([]string{prompt})
	if err != nil || parsed.request != prompt || parsed.mode != "auto" || parsed.workspaceSelector != "" {
		t.Fatalf("report reinterpreted as task options: %#v, %v", parsed, err)
	}
}

// Requirement: command discovery must not advertise a development-only action
// in production. Test the actual composer suggestion builder, not source text.
func TestFlagComposerSuggestionsDevOnly(t *testing.T) {
	for _, dev := range []bool{false, true} {
		found := false
		for _, suggestion := range buildChatCommandSuggestions(dev) {
			if suggestion.Command == "/flag" {
				found = true
			}
		}
		if found != dev {
			t.Fatalf("dev=%v flag visible=%v", dev, found)
		}
	}
}
