package publish

import (
	"fmt"
	"strings"
)

// RebaseConflictSendBack tells the job how to repeat its aborted landing rebase.
func RebaseConflictSendBack(base Base, files string) *SendBackError {
	return &SendBackError{Reason: fmt.Sprintf("Rebase conflict: rebase onto refs/swarm/publish/%s (commit %s), resolve the files below, and keep the worktree clean:\n%s", base.Branch, base.Commit, tail(strings.TrimSpace(files), 4096))}
}

// ChecksSendBack tells the job how to reproduce the executed checks in order.
// Commands come from the bounded, secret-free environment definition (at most
// ten commands of 4000 bytes each); output is the already-redacted sandbox tail.
func ChecksSendBack(base Base, commands []string, timedOut bool, output string) *SendBackError {
	reason := "Checks failed"
	if timedOut {
		reason = "Checks timed out"
	}
	return &SendBackError{Reason: fmt.Sprintf("%s: checks ran after rebasing onto refs/swarm/publish/%s (commit %s). Reproduce from the job's worktree after rebasing onto that commit; run these commands in order with bash -lc, then keep the worktree clean:\n%s\nOutput tail:\n%s", reason, base.Branch, base.Commit, strings.Join(commands, "\n"), tail(output, 4096))}
}
