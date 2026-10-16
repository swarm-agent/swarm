package publish

import (
	"fmt"
	"regexp"
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
// ten commands of 4000 bytes each); output is already redacted by the sandbox.
func ChecksSendBack(base Base, commands []string, timedOut bool, output string) *SendBackError {
	reason := "Checks failed"
	if timedOut {
		reason = "Checks timed out"
	}
	header := fmt.Sprintf("%s: checks ran after rebasing onto refs/swarm/publish/%s (commit %s). Reproduce from the job's worktree after rebasing onto that commit; run these commands in order with bash -lc, then keep the worktree clean:\n%s\nOutput excerpt:\n", reason, base.Branch, base.Commit, strings.Join(commands, "\n"))
	// Preserve reproduction commands in full; bound only the diagnostic output.
	return &SendBackError{Reason: header + CheckOutputExcerpt(output)}
}

// CheckOutputLimit bounds retained check output while the command runs.
const CheckOutputLimit = 2 * 1024 * 1024

const checkExcerptLimit = 16 * 1024

var checkFailureLine = regexp.MustCompile(`--- FAIL|FAIL\t|panic:|✖|not ok|Error:|error:|FAILED|(?i:exit(?:ed)?(?: with)?(?: status| code)?[:= ]+[1-9][0-9]*\b|non[- ]zero exit)`)

// CheckOutputExcerpt prioritizes failure windows over passing output. Short
// excerpts are preserved verbatim so storage and send-back use identical text.
func CheckOutputExcerpt(output string) string {
	if len(output) <= checkExcerptLimit {
		return output
	}
	lines := strings.SplitAfter(output, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	selected := make([]bool, len(lines))
	matched := false
	for i, line := range lines {
		if !checkFailureLine.MatchString(line) {
			continue
		}
		matched = true
		for j := max(0, i-40); j < min(len(lines), i+21); j++ {
			selected[j] = true
		}
	}
	if !matched {
		return tail(output, checkExcerptLimit)
	}
	// Reserve room for the final twenty lines, bounded to leave room for the
	// failure even when the last line alone is enormous.
	end := strings.Join(lines[max(0, len(lines)-20):], "")
	end = tail(end, checkExcerptLimit/2)
	const separator = "\n...\n"
	budget := checkExcerptLimit - len(end) - len(separator)
	var excerpt strings.Builder
	for i, line := range lines[:max(0, len(lines)-20)] {
		if !selected[i] {
			continue
		}
		if len(line) > budget-excerpt.Len() {
			line = line[:budget-excerpt.Len()]
		}
		excerpt.WriteString(line)
		if excerpt.Len() == budget {
			break
		}
	}
	return excerpt.String() + separator + end
}
