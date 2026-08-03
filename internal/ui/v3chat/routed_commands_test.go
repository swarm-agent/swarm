package v3chat

import "testing"

func TestParseNewCommandForms(t *testing.T) {
	tests := []struct {
		input string
		want  NewCommand
	}{
		{"/new", NewCommand{}},
		{" /NEW  ", NewCommand{}},
		{"/new\u00a0fix the bug", NewCommand{Prompt: "fix the bug"}},
		{"/new fix the bug", NewCommand{Prompt: "fix the bug"}},
		{"/new worktree", NewCommand{ManagedWorktreeRequested: true}},
		{"/new worktree fix it", NewCommand{Prompt: "fix it", ManagedWorktreeRequested: true}},
		{"/new plan", NewCommand{PlanModeRequested: true}},
		{"/new plan map it", NewCommand{Prompt: "map it", PlanModeRequested: true}},
		{"/new wp", NewCommand{ManagedWorktreeRequested: true, PlanModeRequested: true}},
		{"/new wp build it", NewCommand{Prompt: "build it", ManagedWorktreeRequested: true, PlanModeRequested: true}},
	}
	for _, test := range tests {
		got, ok := ParseNewCommand(test.input)
		if !ok || got != test.want {
			t.Errorf("ParseNewCommand(%q) = %#v, %v; want %#v, true", test.input, got, ok, test.want)
		}
	}
	for _, input := range []string{"/newer", "/worktrees", "new"} {
		if _, ok := ParseNewCommand(input); ok {
			t.Errorf("ParseNewCommand(%q) unexpectedly matched", input)
		}
	}
}

func TestParseCommitCommand(t *testing.T) {
	tests := []struct {
		input string
		want  CommitCommand
	}{
		{"/commit ship the TUI command", CommitCommand{Message: "ship the TUI command"}},
		{" /COMMIT  keep ai in this longer message ", CommitCommand{Message: "keep ai in this longer message"}},
		{"/commit ai", CommitCommand{AI: true}},
		{"/commit AI", CommitCommand{AI: true}},
	}
	for _, test := range tests {
		got, matched, err := ParseCommitCommand(test.input)
		if err != nil || !matched || got != test.want {
			t.Errorf("ParseCommitCommand(%q) = %#v, %v, %v; want %#v, true, nil", test.input, got, matched, err, test.want)
		}
	}
	for _, input := range []string{"/commit", "/commit   "} {
		if _, matched, err := ParseCommitCommand(input); !matched || err == nil || err.Error() != "usage: /commit <message>|ai" {
			t.Errorf("ParseCommitCommand(%q) = matched=%v err=%v", input, matched, err)
		}
	}
	for _, input := range []string{"/committed message", "/compact", "commit message"} {
		if _, matched, err := ParseCommitCommand(input); matched || err != nil {
			t.Errorf("ParseCommitCommand(%q) unexpectedly matched: err=%v", input, err)
		}
	}
}

func TestParseWorktreeCommandIsLocalAndDoesNotCaptureWorktrees(t *testing.T) {
	for _, input := range []string{"/worktree on", "/wt on"} {
		got, matched, err := ParseWorktreeCommand(input)
		if err != nil || !matched || !got.Enabled {
			t.Errorf("ParseWorktreeCommand(%q) = %#v, %v, %v", input, got, matched, err)
		}
	}
	for _, input := range []string{"/worktree off", "/wt off"} {
		got, matched, err := ParseWorktreeCommand(input)
		if err != nil || !matched || got.Enabled {
			t.Errorf("ParseWorktreeCommand(%q) = %#v, %v, %v", input, got, matched, err)
		}
	}
	if _, matched, err := ParseWorktreeCommand("/worktrees"); matched || err != nil {
		t.Fatalf("/worktrees was captured: matched=%v err=%v", matched, err)
	}
	if _, matched, err := ParseWorktreeCommand("/wt"); !matched || err == nil {
		t.Fatalf("bare /wt = matched=%v err=%v", matched, err)
	}
}
