package publish

import (
	"strings"
	"testing"
)

// Purpose: prepareLandWorktree uses RebaseConflictSendBack after aborting a
// conflicted rebase. The job must receive the fetched ref and exact commit, not
// its stale local branch. This formatter unit test is the narrowest layer that
// proves the recovery instructions and bounded file details without Git or a sandbox.
func TestRebaseConflictSendBackTarget(t *testing.T) {
	base := Base{Branch: "dev", Commit: strings.Repeat("a", 40)}
	for _, files := range []string{"CHANGELOG.md\n", strings.Repeat("file\n", 2000)} {
		message := RebaseConflictSendBack(base, files).Reason
		for _, want := range []string{"rebase onto refs/swarm/publish/dev (commit " + base.Commit + ")", "resolve", "keep the worktree clean", "file"} {
			if want == "file" && files == "CHANGELOG.md\n" {
				want = "CHANGELOG.md"
			}
			if !strings.Contains(message, want) {
				t.Errorf("send-back missing %q: %.300s", want, message)
			}
		}
		if len(message) > 4608 {
			t.Errorf("unbounded conflict send-back: %d bytes", len(message))
		}
	}
}

// Purpose: prepareLandWorktree uses ChecksSendBack for nonzero checks and
// deadline expiry. Recovery must identify the immutable base and commands in
// execution order, and retain only bounded, already-redacted output. A formatter
// unit test proves this message contract without executing owner check commands.
func TestChecksSendBackReproduction(t *testing.T) {
	base := Base{Branch: "release/dev", Commit: strings.Repeat("b", 40)}
	commands := []string{"setup-check", "go test ./internal/publish -run TestChecks"}
	for _, timedOut := range []bool{false, true} {
		output := "discarded-prefix" + strings.Repeat("x", 4096) + "[REDACTED] final failure"
		message := ChecksSendBack(base, commands, timedOut, output).Reason
		prefix := "Checks failed:"
		if timedOut {
			prefix = "Checks timed out:"
		}
		for _, want := range []string{prefix, "refs/swarm/publish/release/dev (commit " + base.Commit + ")", "job's worktree", "after rebasing", strings.Join(commands, "\n"), "[REDACTED] final failure"} {
			if !strings.Contains(message, want) {
				t.Errorf("send-back missing %q: %.300s", want, message)
			}
		}
		if strings.Contains(message, "discarded-prefix") || len(message) > 4608 {
			t.Errorf("unbounded check send-back: %d bytes", len(message))
		}
	}
}
