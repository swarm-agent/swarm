package api

import (
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A workflow listing is bounded: by default each workflow's definition and
// its last workflowListDeliveries deliveries as one compact line each (job,
// state, round, head, blocked reason, each step's state and verdict), with
// no findings or outputs. Delivery and step detail come only on request
// (detail "deliveries", or one delivery_id), and stay bounded.
const (
	workflowListDeliveries   = 3
	workflowDetailDeliveries = "deliveries"
	// maxWorkflowDetailText bounds each text of a detailed delivery's step
	// (findings, report, output, reason).
	maxWorkflowDetailText = 1500
	// maxWorkflowDetailItems bounds each list of a detailed delivery
	// (fix commits, produced files, items).
	maxWorkflowDetailItems = 20
	// maxWorkflowDetailFailures keeps the newest failed attempts of a step.
	maxWorkflowDetailFailures = 3
)

// WorkflowListOptions is what a workflow listing includes beyond the default.
type WorkflowListOptions struct {
	// DeliveryID reads that one delivery in bounded detail instead of the list.
	DeliveryID string
	// Detail "deliveries" lists each workflow's latest deliveries in bounded
	// detail instead of one line each.
	Detail string
}

// workflowListOptions reads the listing options from tool arguments or a
// query (as strings).
func workflowListOptions(get func(string) string) (WorkflowListOptions, error) {
	opts := WorkflowListOptions{DeliveryID: strings.TrimSpace(get("delivery_id")), Detail: strings.TrimSpace(get("detail"))}
	if opts.Detail != "" && opts.Detail != workflowDetailDeliveries {
		return opts, controlMCPToolFailure("detail must be %q (or omitted for one line per delivery)", workflowDetailDeliveries)
	}
	if opts.DeliveryID != "" && !controlMCPSafeID.MatchString(opts.DeliveryID) {
		return opts, controlMCPToolFailure("invalid delivery_id")
	}
	return opts, nil
}

// deliveryJobIDNote says a job_id filter matched no delivery, so an empty
// list is never read as "this task had no workflow" by mistake. "" when no
// job_id was given or something matched.
func deliveryJobIDNote(jobID string, matched int) string {
	if strings.TrimSpace(jobID) == "" || matched > 0 {
		return ""
	}
	return fmt.Sprintf("no deliveries match job_id %q: no workflow delivery was recorded for that task or job id (job_id takes a task id or a job id such as task/<project>/<task> or worker/<worker>/<run>)", jobID)
}

// workflowDeliveryLine is one delivery as one compact line: no findings,
// outputs, reasons or log tails.
func workflowDeliveryLine(d jobautomation.Delivery) map[string]any {
	steps := make([]map[string]any, 0, len(d.Steps))
	for _, step := range d.Steps {
		line := map[string]any{"id": step.ID, "state": step.State}
		if verdict, _ := step.Outcome["verdict"].(string); verdict != "" {
			line["verdict"] = verdict
		}
		steps = append(steps, line)
	}
	out := map[string]any{"delivery_id": d.ID, "job_id": d.JobID, "state": d.State, "round": d.Round, "created_at": d.CreatedAt, "steps": steps}
	if d.Head != "" {
		out["head"] = d.Head
	}
	if d.BlockedReason != "" {
		out["blocked_reason"] = controlMCPTruncate(d.BlockedReason, 300)
	}
	return out
}

// workflowDeliveryDetail is a delivery with its steps' outcomes, every text
// cut to maxWorkflowDetailText characters, every list to
// maxWorkflowDetailItems and each step's failures to the newest
// maxWorkflowDetailFailures. Log tails are never included. With fullFindings
// (one delivery read by delivery_id, where get_task points for the full
// text) each step's findings are returned whole: the review record already
// bounds them to pebblestore.MaxJobReviewFindingsBytes.
func workflowDeliveryDetail(d jobautomation.Delivery, fullFindings bool) map[string]any {
	d.Steps = append([]jobautomation.StepRecord(nil), d.Steps...)
	for i := range d.Steps {
		d.Steps[i].LogTail = ""
		if extra := len(d.Steps[i].Failures) - maxWorkflowDetailFailures; extra > 0 {
			d.Steps[i].Failures = d.Steps[i].Failures[extra:]
		}
	}
	view := jobDeliveryView(d)
	for key, value := range view {
		if key != "steps" {
			view[key] = boundWorkflowValue(value)
		}
	}
	if steps, ok := view["steps"].([]map[string]any); ok {
		for i, step := range steps {
			for key, value := range step {
				step[key] = boundWorkflowValue(value)
			}
			if !fullFindings || i >= len(d.Steps) {
				continue
			}
			findings, _ := d.Steps[i].Outcome["findings"].(string)
			if outcome, ok := step["outcome"].(map[string]any); ok && findings != "" {
				outcome["findings"] = controlMCPTruncate(findings, pebblestore.MaxJobReviewFindingsBytes)
			}
		}
	}
	return view
}

// boundWorkflowValue copies a JSON-like value with each string cut to
// maxWorkflowDetailText characters and each list to maxWorkflowDetailItems.
func boundWorkflowValue(value any) any {
	switch v := value.(type) {
	case string:
		return controlMCPTruncate(v, maxWorkflowDetailText)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = boundWorkflowValue(item)
		}
		return out
	case []any:
		return boundWorkflowList(v)
	case []map[string]any:
		items := make([]any, len(v))
		for i := range v {
			items[i] = v[i]
		}
		return boundWorkflowList(items)
	case []string:
		items := make([]any, len(v))
		for i := range v {
			items[i] = v[i]
		}
		return boundWorkflowList(items)
	case []jobautomation.AttemptFailure:
		out := make([]map[string]any, 0, len(v))
		for _, f := range v {
			entry := map[string]any{"attempt": f.Attempt, "at": f.At, "reason": controlMCPTruncate(f.Reason, maxWorkflowDetailText)}
			if f.Rerun > 0 {
				entry["rerun"] = f.Rerun
			}
			out = append(out, entry)
		}
		return out
	}
	return value
}

func boundWorkflowList(items []any) any {
	out := make([]any, 0, min(len(items), maxWorkflowDetailItems)+1)
	for i, item := range items {
		if i == maxWorkflowDetailItems {
			out = append(out, map[string]any{"truncated": len(items) - maxWorkflowDetailItems})
			break
		}
		out = append(out, boundWorkflowValue(item))
	}
	return out
}
