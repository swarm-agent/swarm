package api

import (
	"swarm/packages/swarmd/internal/publish"
	"testing"
)

// Purpose: publishJob must refuse reviewer and report-only jobs, even with
// fabricated commit evidence, before any publication side effect. The
// decision layer is the narrowest deterministic proof of this policy.
func TestPublishJobRefusesReportOnlyJobs(t *testing.T) {
	ready := map[string]any{"delivery_state": "ready", "head": "coder", "commits": []any{"coder"}}
	reportOnly := map[string]any{"delivery_state": "report_only", "head": "review", "commits": []any{"review"}}
	c := &controlMCPCall{}
	if _, err := c.publishJob(controlMCPJobWorker, "w", "r", controlMCPJobRefs{run: map[string]any{"role": "reviewer"}}, publish.Rule{}, ready); err == nil {
		t.Fatal("reviewer published")
	}
	if _, err := c.publishJob(controlMCPJobWorker, "w", "r", controlMCPJobRefs{}, publish.Rule{}, reportOnly); err == nil {
		t.Fatal("report-only published")
	}
}
