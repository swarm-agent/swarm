package api

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskDeliveryNotActionableMessage explains why direct integration refused a
// task's delivery: it names the observed delivery state and the action that
// resolves it, instead of an unexplained "not actionable". The same text
// reaches MCP callers, so it names actions (integrate again, re-read the
// task, follow up with the task's agent), never an HTTP route.
func taskDeliveryNotActionableMessage(a *pebblestore.TaskDeliveryAssessment) string {
	if a == nil {
		return "delivery is not actionable for direct integration: the task's Git lane was not assessed; re-read the task and integrate again"
	}
	state := a.State
	if state == "" {
		state = "unknown"
	}
	prefix := fmt.Sprintf("delivery is not actionable for direct integration: delivery state %q", state)
	if a.ReasonCode != "" {
		prefix += fmt.Sprintf(" (%s)", a.ReasonCode)
	}
	if reason := strings.TrimSpace(a.Reason); reason != "" {
		prefix += ": " + reason
	}
	var remedy string
	switch {
	case a.Freshness != "observed":
		// Usually transient (a slow Git read or heads moving during it);
		// nothing was integrated, so repeating the request is safe.
		remedy = "the task's Git state could not be observed (freshness " + fmt.Sprintf("%q", a.Freshness) + "); nothing was integrated; integrate again, and if it repeats re-read the task to see its delivery state"
	case state == "empty" && a.SourceDirty:
		remedy = "the task worktree has uncommitted changes but no commits since its base, and only commits are integrated; commit the changes on branch " + fmt.Sprintf("%q", a.SourceBranch) + " (for example ask the task's agent to commit them), then integrate again"
	case state == "empty":
		remedy = "the task produced no commits since its base, so there is nothing to integrate; review the task outcome and archive it if no change was expected"
	case state == "history_equivalent" || state == "history_rewritten":
		// Integration delivers these as one net-change commit; it reaches
		// this message only when the assessment withheld that action.
		remedy = fmt.Sprintf("the task branch's net change cannot be integrated as it stands; send the task's agent a follow-up (swarm_send_message) asking it to rebase its branch %q onto %q (rebase, not merge) and commit the result, then integrate again", a.SourceBranch, a.TargetBranch)
	case state == "ambiguous" || state == "unavailable":
		remedy = "inspect the task worktree and target branch in Git, resolve the reported condition, then re-read the task and integrate again"
	default:
		remedy = "re-read the task and integrate again"
	}
	return prefix + "; " + remedy
}

// taskDeliveryDirtyMessage names the exact uncommitted changes that block
// integration, in the task worktree and in the target checkout.
func taskDeliveryDirtyMessage(a *pebblestore.TaskDeliveryAssessment) string {
	if a == nil || (!a.SourceDirty && !a.TargetDirty) {
		return ""
	}
	describe := func(files []string) string {
		if len(files) == 0 {
			return "uncommitted changes"
		}
		return "uncommitted changes: " + strings.Join(files, ", ")
	}
	var parts []string
	if a.SourceDirty {
		parts = append(parts, fmt.Sprintf("Task branch %q has %s. Commit them on the task branch (or remove them) before integrating.", a.SourceBranch, describe(a.SourceDirtyFiles)))
	}
	if a.TargetDirty {
		parts = append(parts, fmt.Sprintf("Target checkout on %q has %s. Commit, stash or move them before integrating.", a.TargetBranch, describe(a.TargetDirtyFiles)))
	}
	return strings.Join(parts, " ")
}
