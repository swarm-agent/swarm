package pebblestore

import (
	"strings"
)

// Outcomes of a delivery's rebase onto its target branch, as task progress
// and wake rows name them (jobautomation's RebaseRecord.Outcome).
const (
	ProjectTaskRebaseRebased   = "rebased"
	ProjectTaskRebaseResolving = "resolving"
	ProjectTaskRebaseConflict  = "conflict"
	ProjectTaskRebaseError     = "error"
)

// ProjectTaskWorkflowConflict is the workflow state of a task whose
// delivery ended conflict: its rebase onto the target conflicted and the
// resolver agents did not resolve it.
const ProjectTaskWorkflowConflict = "conflict"

const (
	maxProjectTaskRebaseFiles = 10
	maxProjectTaskRebaseJobs  = 5
)

// ProjectTaskRebase is a delivery's newest rebase as task progress (each
// delivery's rebase), get_task and wake rows (local and linked) show it:
// its outcome, the target branch and tip it rebased onto, the head before
// and after, and for a conflict the files, the resolver jobs and whether
// they resolved it. Rebases counts the rebases that moved the head. Remote
// values are untrusted and bounded (ClipProjectTaskRebase).
type ProjectTaskRebase struct {
	Outcome      string   `json:"outcome"`
	Target       string   `json:"target,omitempty"`
	Onto         string   `json:"onto,omitempty"`
	From         string   `json:"from,omitempty"`
	To           string   `json:"to,omitempty"`
	Files        []string `json:"files,omitempty"`
	ResolverJobs []string `json:"resolver_jobs,omitempty"`
	Resolved     bool     `json:"resolved,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Rebases      int      `json:"rebases,omitempty"`
}

// ClipProjectTaskRebase bounds a rebase view (it may come from a linked
// machine); nil for none or an unknown outcome.
func ClipProjectTaskRebase(r *ProjectTaskRebase) *ProjectTaskRebase {
	if r == nil {
		return nil
	}
	switch r.Outcome {
	case ProjectTaskRebaseRebased, ProjectTaskRebaseResolving, ProjectTaskRebaseConflict, ProjectTaskRebaseError:
	default:
		return nil
	}
	out := &ProjectTaskRebase{Outcome: r.Outcome, Target: clipProjectTaskWaitField(r.Target), Onto: clipProjectTaskWaitField(r.Onto),
		From: clipProjectTaskWaitField(r.From), To: clipProjectTaskWaitField(r.To), Resolved: r.Resolved, Rebases: max(r.Rebases, 0)}
	out.Reason, _ = ClipProjectTaskReason(r.Reason, ProjectTaskWakeReasonRunes)
	for i, f := range r.Files {
		if i == maxProjectTaskRebaseFiles {
			break
		}
		out.Files = append(out.Files, clipProjectTaskWaitField(f))
	}
	for i, j := range r.ResolverJobs {
		if i == maxProjectTaskRebaseJobs {
			break
		}
		out.ResolverJobs = append(out.ResolverJobs, clipProjectTaskWaitField(j))
	}
	return out
}

// Milestone is the rebase's milestone line: "rebased onto dev (abc123 →
// def456)", "rebase conflict on dev: a.go, b.go (resolver working)" or
// "conflict on dev: a.go (not resolved)".
func (r ProjectTaskRebase) Milestone() string {
	short := func(c string) string {
		if len(c) > 12 {
			return c[:12]
		}
		return c
	}
	target := firstNonEmptyTrimmed(r.Target, "the target")
	files := strings.Join(r.Files, ", ")
	switch r.Outcome {
	case ProjectTaskRebaseRebased:
		out := "rebased onto " + target
		if r.From != "" && r.To != "" {
			out += " (" + short(r.From) + " → " + short(r.To) + ")"
		}
		if r.Resolved {
			out += ", conflict in " + files + " resolved"
		}
		return out
	case ProjectTaskRebaseResolving:
		return "rebase conflict on " + target + ": " + files + " (resolver working)"
	case ProjectTaskRebaseConflict:
		return "conflict on " + target + ": " + files + " (not resolved)"
	case ProjectTaskRebaseError:
		return "rebase onto " + target + " could not run"
	}
	return ""
}

// projectTaskRowRebase reads a wake row's rebase (as built, or decoded).
func projectTaskRowRebase(v any) *ProjectTaskRebase {
	switch r := v.(type) {
	case *ProjectTaskRebase:
		return r
	case ProjectTaskRebase:
		return &r
	case map[string]any:
		out := &ProjectTaskRebase{}
		out.Outcome, _ = r["outcome"].(string)
		out.Target, _ = r["target"].(string)
		out.From, _ = r["from"].(string)
		out.To, _ = r["to"].(string)
		out.Resolved, _ = r["resolved"].(bool)
		if files, ok := r["files"].([]any); ok {
			for _, f := range files {
				if s, ok := f.(string); ok {
					out.Files = append(out.Files, s)
				}
			}
		}
		return out
	}
	return nil
}
