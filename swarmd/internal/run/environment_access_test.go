package run

import (
	"strings"
	"testing"
)

// Purpose: generic workspace context must describe status without distributing
// lease receipts. The formatter is the narrowest prompt boundary owning this leak.
func TestEnvironmentPromptOmitsLeaseReceipt(t *testing.T) {
	block := FormatWorkspaceEnvironmentPromptBlock(&WorkspaceEnvironmentContext{ActiveDeployments: []ActiveDeploymentSummary{{DeploymentID: "deployment", Status: "ready", LeaseID: "private-receipt", ConsumerID: "consumer", ConsumerType: "session"}}})
	if strings.Contains(block, "private-receipt") || !strings.Contains(block, "ready") {
		t.Fatalf("receipt exposed or status missing: %s", block)
	}
}
