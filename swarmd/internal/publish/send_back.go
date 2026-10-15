package publish

import (
	"fmt"
	"strings"
)

// RebaseConflictSendBack tells the job how to repeat its aborted landing rebase.
func RebaseConflictSendBack(base Base, files string) *SendBackError {
	header := fmt.Sprintf("Rebase conflict: rebase onto refs/swarm/publish/%s (commit %s), resolve the files below, and keep the worktree clean:\n", base.Branch, base.Commit)
	paths := strings.Split(strings.TrimSpace(files), "\n")
	for i, path := range paths {
		// Reserve space for the omitted-file count; never split a filename.
		if i >= 20 || len(header)+len(path)+64 > 4096 {
			header += fmt.Sprintf("and %d more\n", len(paths)-i)
			break
		}
		header += path + "\n"
	}
	return &SendBackError{Reason: header}
}

// ChecksSendBack tells the job how to reproduce the executed checks in order.
// Commands come from the bounded, secret-free environment definition (at most
// ten commands of 4000 bytes each); output is the already-redacted sandbox tail.
func ChecksSendBack(base Base, commands []string, timedOut bool, output string) *SendBackError {
	reason := "Checks failed"
	if timedOut {
		reason = "Checks timed out"
	}
	header := fmt.Sprintf("%s: checks ran after rebasing onto refs/swarm/publish/%s (commit %s). Reproduce from the job's worktree after rebasing onto that commit; run these commands in order with bash -lc, then keep the worktree clean:\n%s\nOutput tail:\n", reason, base.Branch, base.Commit, strings.Join(commands, "\n"))
	// Definitions may contain up to 40KB of commands. Preserve those in full
	// even when they exceed the normal message budget; only output is lossy.
	budget := 4096 - len(header)
	if budget <= 0 {
		return &SendBackError{Reason: header}
	}
	return &SendBackError{Reason: header + tail(output, budget)}
}
