package pebblestore

import (
	"strconv"
	"strings"
)

// Reopen causes: why a workflow's reopen step sends (or sent) a task's work
// back to its agent. Task progress (each delivery's reopen_cause), the
// outcome word, the task state reason and wait_tasks milestones all read
// them through ProjectTaskReopen, so the local routes, the native tools, MCP
// and linked machines name a reopen the same way.
const (
	// ProjectTaskReopenReviewBlock: a review sent the work back (verdict
	// block, or a reopen without a failed-step condition); the agent
	// addresses its findings.
	ProjectTaskReopenReviewBlock = "review_block"
	// ProjectTaskReopenPushFailed: the review passed, then a handover step
	// after it (a push) failed; the agent rebases and retries.
	ProjectTaskReopenPushFailed = "push_failed"
	// ProjectTaskReopenCheckFailed: a step that is not a handover after a
	// passed review (a check) failed; the agent fixes the failure.
	ProjectTaskReopenCheckFailed = "check_failed"
)

// maxProjectTaskReopenFailure bounds the failed step's reason carried in an
// outcome, a state reason and a milestone (runes).
const maxProjectTaskReopenFailure = 80

const projectTaskSentBackOutcomePrefix = "sent_back (round "

// ProjectTaskReopen is how a reopen step sends (or sent) a task's work back:
// the follow-up's round (1 is the first), the cause and, for a failed step,
// its short failure ("push failed: exited with code 1: …").
type ProjectTaskReopen struct {
	Round   int
	Cause   string
	Failure string
}

// Normalized is r with a known cause (unknown or empty: review_block, the
// meaning sent back always had), a non-negative round and the failure on one
// line, capped rune-safely; a review block carries no failure.
func (r ProjectTaskReopen) Normalized() ProjectTaskReopen {
	r.Round = max(r.Round, 0)
	switch r.Cause {
	case ProjectTaskReopenPushFailed, ProjectTaskReopenCheckFailed:
	default:
		r.Cause = ProjectTaskReopenReviewBlock
	}
	if r.Cause == ProjectTaskReopenReviewBlock {
		r.Failure = ""
		return r
	}
	failure := strings.Join(strings.Fields(r.Failure), " ")
	if runes := []rune(failure); len(runes) > maxProjectTaskReopenFailure {
		failure = string(runes[:maxProjectTaskReopenFailure]) + "…"
	}
	r.Failure = failure
	return r
}

// Outcome is the task progress outcome for the reopen. It always starts with
// "sent_back (round N)", the word count buckets (and older readers) keep in
// progress; a push failure adds " rebase: <failure>", a check failure
// " fix: <failure>".
func (r ProjectTaskReopen) Outcome() string {
	r = r.Normalized()
	out := projectTaskSentBackOutcomePrefix + strconv.Itoa(r.Round) + ")"
	switch r.Cause {
	case ProjectTaskReopenPushFailed:
		out += " rebase: " + firstNonEmptyTrimmed(r.Failure, "push failed")
	case ProjectTaskReopenCheckFailed:
		out += " fix: " + firstNonEmptyTrimmed(r.Failure, "a step failed")
	}
	return out
}

// ParseProjectTaskReopenOutcome reads a reopen from an outcome Outcome wrote
// (including ones from before causes existed, which read as review_block).
// ok is false when the outcome is not a sent_back outcome.
func ParseProjectTaskReopenOutcome(outcome string) (ProjectTaskReopen, bool) {
	outcome = strings.TrimSpace(outcome)
	rest, found := strings.CutPrefix(outcome, projectTaskSentBackOutcomePrefix)
	if !found {
		return ProjectTaskReopen{}, false
	}
	number, rest, found := strings.Cut(rest, ")")
	if !found {
		return ProjectTaskReopen{}, false
	}
	round, err := strconv.Atoi(number)
	if err != nil {
		return ProjectTaskReopen{}, false
	}
	out := ProjectTaskReopen{Round: round, Cause: ProjectTaskReopenReviewBlock}
	rest = strings.TrimSpace(rest)
	if failure, ok := strings.CutPrefix(rest, "rebase:"); ok {
		out.Cause, out.Failure = ProjectTaskReopenPushFailed, strings.TrimSpace(failure)
	} else if failure, ok := strings.CutPrefix(rest, "fix:"); ok {
		out.Cause, out.Failure = ProjectTaskReopenCheckFailed, strings.TrimSpace(failure)
	}
	return out.Normalized(), true
}

// StateReason is the task state reason of a task its workflow reopened. It
// stays within the 120 runes a wait row keeps (the failure is capped).
func (r ProjectTaskReopen) StateReason() string {
	r = r.Normalized()
	round := strconv.Itoa(r.Round)
	switch r.Cause {
	case ProjectTaskReopenPushFailed:
		return "rebase (round " + round + "): review passed; " + firstNonEmptyTrimmed(r.Failure, "push failed")
	case ProjectTaskReopenCheckFailed:
		return "sent_back (round " + round + "): " + firstNonEmptyTrimmed(r.Failure, "a step failed")
	}
	return r.Outcome() + "; the agent addresses the findings"
}

// Milestone is the wait_tasks milestone label of the reopen: "sent back
// (round N)" only for a review block, "rebase needed (round N): <failure>"
// when the review passed and the push failed.
func (r ProjectTaskReopen) Milestone() string {
	r = r.Normalized()
	round := strconv.Itoa(r.Round)
	switch r.Cause {
	case ProjectTaskReopenPushFailed:
		return "rebase needed (round " + round + "): " + firstNonEmptyTrimmed(r.Failure, "push failed")
	case ProjectTaskReopenCheckFailed:
		return "sent back (round " + round + "): " + firstNonEmptyTrimmed(r.Failure, "a step failed")
	}
	return "sent back (round " + round + ")"
}
