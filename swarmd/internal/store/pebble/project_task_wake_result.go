package pebblestore

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A task wake (wait_tasks outcome or milestone row, local or from a linked
// machine) must tell the woken AI what the agent finally reported, the full
// reason for a failure and each workflow step's outcome. These are the
// bounds and shapes both paths share, so the two rows never drift.

const (
	// ProjectTaskWakeResultBytes bounds the agent's final result message in
	// a wake row. A longer message keeps its end (where the handoff is) and
	// is marked; read_full pages the whole message.
	ProjectTaskWakeResultBytes = 4000
	// ProjectTaskWakeReasonRunes bounds a failure or reopen reason in a
	// wake row and in task progress. A longer reason keeps its head and tail
	// with a marker between them.
	ProjectTaskWakeReasonRunes = 2000
	// ProjectTaskStepOutputLines and ProjectTaskStepOutputBytes bound the
	// output tail of one workflow step result.
	ProjectTaskStepOutputLines = 30
	ProjectTaskStepOutputBytes = 4000
	// ProjectTaskStepBlockingMax and ProjectTaskStepBlockingRunes bound a
	// review step's BLOCKING findings list.
	ProjectTaskStepBlockingMax   = 20
	ProjectTaskStepBlockingRunes = 500
	// ProjectTaskStepResultsMax bounds the step results of one delivery.
	ProjectTaskStepResultsMax = 32
)

// Summary sources of a wake row (summary_source).
const (
	// ProjectTaskSummaryHandoff: the agent's own handoff, the summary of its
	// task_progress done call for the followed run (attempt handoff).
	ProjectTaskSummaryHandoff      = "handoff"
	ProjectTaskSummaryFinalMessage = "final_message"
	ProjectTaskSummaryLastError    = "last_error"
	ProjectTaskSummaryActionNeeded = "action_needed"
	ProjectTaskSummaryStatus       = "status"
)

// ProjectTaskStepResult is one finished step of a task's workflow delivery as
// task progress (progress.workflow.deliveries[].steps_result, get_task's
// progress.workflow.steps_result) and wake rows (workflow_steps_result) show
// it. Every text field is untrusted step output, bounded here.
type ProjectTaskStepResult struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	// Verdict is an agent (review) step's verdict: ok, warn or block.
	Verdict    string `json:"verdict,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	// ErrorKind is why a run step whose status is error never started its
	// script (checkout, environment, lease, script_missing,
	// script_unusable, start_timeout): an infrastructure problem, not the
	// script's result (status failed is).
	ErrorKind string `json:"error_kind,omitempty"`
	// Error is the step's failure reason, head and tail kept when long.
	Error          string `json:"error,omitempty"`
	ErrorTruncated bool   `json:"error_truncated,omitempty"`
	// OutputTail is the end of the step's output (a run step's last lines,
	// an agent step's report).
	OutputTail      string `json:"output_tail,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	// Blocking lists a review step's BLOCKING findings.
	Blocking []string `json:"blocking,omitempty"`
	// More is the call that reads the step in full when anything was cut.
	More string `json:"more,omitempty"`
}

// ClipProjectTaskReason bounds a failure reason to limit runes: a longer one
// keeps its head and tail with a marker saying how much was cut between them.
// cut reports whether anything was removed. Invalid UTF-8 is replaced.
func ClipProjectTaskReason(text string, limit int) (string, bool) {
	text = strings.TrimSpace(strings.ToValidUTF8(text, string(utf8.RuneError)))
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text, false
	}
	marker := func(n int) string { return " […" + strconv.Itoa(n) + " chars cut…] " }
	keep := limit - utf8.RuneCountInString(marker(len(runes)))
	if keep < 2 {
		return string(runes[:limit]), true
	}
	head := keep / 2
	tail := keep - head
	return strings.TrimSpace(string(runes[:head])) + marker(len(runes)-head-tail) + strings.TrimSpace(string(runes[len(runes)-tail:])), true
}

// ClipProjectTaskResultTail bounds a result message to limit bytes keeping
// its end: the start is cut on a rune boundary and replaced by a marker
// saying how many bytes were cut. cut reports whether anything was removed.
func ClipProjectTaskResultTail(text string, limit int) (string, bool) {
	text = strings.TrimSpace(strings.ToValidUTF8(text, string(utf8.RuneError)))
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	marker := func(n int) string {
		return "[…first " + strconv.Itoa(n) + " bytes cut; read_full pages the whole message…]\n"
	}
	start := len(text) - (limit - len(marker(len(text))))
	if start < 0 {
		start = 0
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return marker(start) + text[start:], true
}

// ClipProjectTaskOutputTail keeps the last maxLines lines of output within
// maxBytes (the end kept). cut reports whether anything was removed.
func ClipProjectTaskOutputTail(output string, maxLines, maxBytes int) (string, bool) {
	output = strings.TrimRight(strings.ToValidUTF8(output, string(utf8.RuneError)), " \t\r\n")
	cut := false
	if maxLines > 0 {
		lines := strings.Split(output, "\n")
		if len(lines) > maxLines {
			output, cut = strings.Join(lines[len(lines)-maxLines:], "\n"), true
		}
	}
	if maxBytes > 0 && len(output) > maxBytes {
		start := len(output) - maxBytes
		for start < len(output) && !utf8.RuneStart(output[start]) {
			start++
		}
		output, cut = output[start:], true
	}
	return output, cut
}

// Negated forms of "blocking" in a review's findings, which name no
// blocking finding: "non-blocking", "no / not / none … blocking" (at most two
// words between) and "blocking …: none" ending the line. Matched on the
// upper-cased line.
var (
	projectTaskBlockingWord       = regexp.MustCompile(`\bBLOCKING\b`)
	projectTaskBlockingNonPrefix  = regexp.MustCompile(`\bNON[- ]?BLOCKING\b`)
	projectTaskBlockingNegBefore  = regexp.MustCompile(`\b(NO|NOT|NONE|NOTHING|NEVER|NOR|WITHOUT|ZERO|ISN'?T|AREN'?T|WASN'?T|WEREN'?T)\s+([A-Z0-9'-]+\s+){0,2}BLOCKING\b`)
	projectTaskBlockingNegNothing = regexp.MustCompile(`\bBLOCKING\b[^:=]*[:=]\s*[(\[]?\s*(NONE|NOTHING|N/?A|NO|0|-)\s*[)\]]?\s*[.!]?\s*$`)
)

// projectTaskNamesBlocking reports whether a findings line names a
// blocking finding: it says BLOCKING (a word) other than in a negated form.
func projectTaskNamesBlocking(line string) bool {
	upper := strings.ToUpper(line)
	for _, negated := range []*regexp.Regexp{projectTaskBlockingNegNothing, projectTaskBlockingNonPrefix, projectTaskBlockingNegBefore} {
		upper = negated.ReplaceAllString(upper, " ")
	}
	return projectTaskBlockingWord.MatchString(upper)
}

// ProjectTaskBlockingFindings lists the lines of a block review's findings
// that name a BLOCKING finding (bounded); negated forms ("No blocking
// issues", "BLOCKING: none", "non-blocking") are not findings. A review
// with any other verdict (ok, warn) has no blocking findings, whatever its
// text says. With verdict block and no such line, the findings' first line
// stands for the block.
func ProjectTaskBlockingFindings(findings, verdict string) []string {
	if strings.TrimSpace(verdict) != "block" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(findings, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !projectTaskNamesBlocking(line) {
			continue
		}
		if len(out) == ProjectTaskStepBlockingMax {
			break
		}
		clipped, _ := ClipProjectTaskReason(line, ProjectTaskStepBlockingRunes)
		out = append(out, clipped)
	}
	if len(out) == 0 {
		if first, _, _ := strings.Cut(strings.TrimSpace(findings), "\n"); strings.TrimSpace(first) != "" {
			clipped, _ := ClipProjectTaskReason(first, ProjectTaskStepBlockingRunes)
			out = append(out, clipped)
		}
	}
	return out
}

// ClipProjectTaskStepResults bounds step results that may come from a linked
// machine: their count and every field, so a remote row is held to the same
// bounds as a local one.
func ClipProjectTaskStepResults(in []ProjectTaskStepResult) []ProjectTaskStepResult {
	if len(in) == 0 {
		return nil
	}
	if len(in) > ProjectTaskStepResultsMax {
		in = in[len(in)-ProjectTaskStepResultsMax:]
	}
	out := make([]ProjectTaskStepResult, 0, len(in))
	for _, r := range in {
		step := ProjectTaskStepResult{ID: clipProjectTaskWaitField(r.ID), Type: clipProjectTaskWaitField(r.Type), Status: clipProjectTaskWaitField(r.Status), ErrorKind: clipProjectTaskWaitField(r.ErrorKind), Verdict: clipProjectTaskWaitField(r.Verdict), DurationMs: max(r.DurationMs, 0), More: clipProjectTaskWaitField(r.More)}
		if r.ExitCode != nil {
			code := *r.ExitCode
			step.ExitCode = &code
		}
		var cut bool
		step.Error, cut = ClipProjectTaskReason(r.Error, ProjectTaskWakeReasonRunes)
		step.ErrorTruncated = r.ErrorTruncated || cut
		step.OutputTail, cut = ClipProjectTaskOutputTail(r.OutputTail, ProjectTaskStepOutputLines, ProjectTaskStepOutputBytes)
		step.OutputTruncated = r.OutputTruncated || cut
		for i, b := range r.Blocking {
			if i == ProjectTaskStepBlockingMax {
				break
			}
			clipped, _ := ClipProjectTaskReason(b, ProjectTaskStepBlockingRunes)
			step.Blocking = append(step.Blocking, clipped)
		}
		out = append(out, step)
	}
	return out
}

// ProjectTaskWakeResult is what a wake row says about a task's result: the
// summary and where it came from, the full failure reason, and where to read
// the whole result.
type ProjectTaskWakeResult struct {
	Summary string
	Source  string
	// Failure is the run's failure reason (its last error or the action it
	// needs), set for a task that did not finish.
	Failure string
	// ReadFull is how to read the whole result (prose) and ReadFullArgs the
	// exact call; ReadFullBytes is the whole result's size when known.
	ReadFull      string
	ReadFullArgs  map[string]any
	ReadFullBytes int
}

// setProjectTaskWakeResult puts a wake row's result fields in row, the same
// way for local and linked-machine rows: summary (the agent's final result
// message, its end kept when long: result_truncated, result_bytes,
// read_full, read_full_args), summary_source, and failure (head and tail
// kept: failure_truncated).
func setProjectTaskWakeResult(row map[string]any, r ProjectTaskWakeResult) {
	summary, cut := r.Summary, false
	if r.Source == ProjectTaskSummaryFinalMessage || r.Source == ProjectTaskSummaryHandoff {
		summary, cut = ClipProjectTaskResultTail(summary, ProjectTaskWakeResultBytes)
	} else {
		summary, cut = ClipProjectTaskReason(summary, ProjectTaskWakeReasonRunes)
	}
	// A summary the store already cut (it carries the marker) is cut too;
	// the marker says how much of the message came before it.
	earlier := projectTaskResultCutBytes(summary)
	cut = cut || earlier > 0
	row["summary"] = summary
	if r.Source != "" {
		row["summary_source"] = r.Source
	}
	if cut {
		whole := len(r.Summary)
		if earlier > 0 {
			whole = earlier + len(summary[strings.Index(summary, "\n")+1:])
		}
		row["result_truncated"] = true
		row["result_bytes"] = max(r.ReadFullBytes, whole)
		row["read_full"] = r.ReadFull
		if len(r.ReadFullArgs) > 0 {
			row["read_full_args"] = r.ReadFullArgs
		}
	}
	if failure := strings.TrimSpace(r.Failure); failure != "" {
		clipped, cut := ClipProjectTaskReason(failure, ProjectTaskWakeReasonRunes)
		row["failure"] = clipped
		if cut {
			row["failure_truncated"] = true
			row["failure_bytes"] = len(failure)
			if row["read_full"] == nil {
				row["read_full"] = r.ReadFull
				if len(r.ReadFullArgs) > 0 {
					row["read_full_args"] = r.ReadFullArgs
				}
			}
		}
	}
}

// projectTaskResultCutBytes reads the marker ClipProjectTaskResultTail puts
// at the start of a cut result: how many bytes were cut before it (0: not
// cut).
func projectTaskResultCutBytes(summary string) int {
	rest, ok := strings.CutPrefix(summary, "[…first ")
	if !ok {
		return 0
	}
	number, _, ok := strings.Cut(rest, " bytes cut")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// projectTaskWakeSummary picks a wake row's summary from a task's result:
// the agent's own handoff (its task_progress done summary for the run), else
// its final result message, else its last error, else the action it needs,
// else its status.
func projectTaskWakeSummary(status, handoff, final, lastError, actionNeeded string) (string, string) {
	switch {
	case strings.TrimSpace(handoff) != "":
		return handoff, ProjectTaskSummaryHandoff
	case strings.TrimSpace(final) != "":
		return final, ProjectTaskSummaryFinalMessage
	case strings.TrimSpace(lastError) != "":
		return lastError, ProjectTaskSummaryLastError
	case strings.TrimSpace(actionNeeded) != "":
		return actionNeeded, ProjectTaskSummaryActionNeeded
	}
	return status, ProjectTaskSummaryStatus
}

// projectTaskWakeFailure is a task's failure reason for its wake row: set
// only for a task whose run did not finish.
func projectTaskWakeFailure(status, lastError, actionNeeded string) string {
	switch status {
	case "failed", "blocked", "error", "cancelled", "interrupted", "expired", "needs_input":
		return firstNonEmptyTrimmed(lastError, actionNeeded)
	}
	return ""
}

// ProjectTaskWakeRowKeys are the keys a wake row may carry (local and linked
// machine alike; machine and progress only where noted). The parity tests
// hold both builders, the contract and the tool description to this list.
var ProjectTaskWakeRowKeys = []string{
	"task_id", "attempt_id", "session_id", "title", "status", "pinned_attempt_id",
	"summary", "summary_source", "result_truncated", "result_bytes", "read_full", "read_full_args",
	"failure", "failure_truncated", "failure_bytes",
	"workflow_state", "workflow", "workflow_step", "workflow_steps", "workflow_step_type", "workflow_round",
	"delivery_id", "workflow_steps_result",
	"reopen_cause", "reopen_failure", "reopen_failure_truncated", "reopen_failure_more",
	"integration_state", "task_state", "task_state_reason", "pending_request",
	"agent_stall", "milestone",
	// Local rows only: the task's whole progress. Linked rows only: machine.
	"progress", "machine",
}
