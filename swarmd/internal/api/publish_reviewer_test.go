package api

import (
	"swarm/packages/swarmd/internal/publish"
	"testing"
)

// Purpose: the auto-publisher must skip terminal report-only jobs without a
// retry, allowing the next deliverable job immediately; publishJob must refuse
// even fabricated commit evidence before any publication side effect. These
// policy boundaries are the narrowest deterministic decision-layer proof.
func TestAutoPublishReviewerDoesNotDelayCoder(t *testing.T) {
	jobs := []map[string]any{
		{"delivery_state": "report_only", "head": "review", "commits": []any{"review"}},
		{"delivery_state": "ready", "head": "coder", "commits": []any{"coder"}},
	}
	if autoPublishNext("needs_review", jobs[0]) != autoPublishSkip {
		t.Fatal("reviewer held cursor")
	}
	if autoPublishNext("needs_review", jobs[1]) != autoPublishDeliver {
		t.Fatal("coder not immediately deliverable")
	}
	c := &controlMCPCall{}
	if _, err := c.publishJob(controlMCPJobWorker, "w", "r", controlMCPJobRefs{run: map[string]any{"role": "reviewer"}}, publish.Rule{Mode: "land"}, jobs[1], false); err == nil {
		t.Fatal("reviewer published")
	}
	if _, err := c.publishJob(controlMCPJobWorker, "w", "r", controlMCPJobRefs{}, publish.Rule{Mode: "land"}, jobs[0], false); err == nil {
		t.Fatal("report-only published")
	}
}
