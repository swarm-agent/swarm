package run

import (
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// Purpose: deployed sessions must use Auto before model selection or allocation.
// Threat: a caller requests Plan, or a stale/edited approval starts a costly Plan
// run. parseManageSessionsDeployArguments and parseApprovedManageSessionsDeploy
// are the narrow pre-side-effect boundaries; these tests exercise their outputs
// and rejection contracts without a provider, store, or worktree allocation.
func TestManageSessionsDeployAutoOnly(t *testing.T) {
	for _, mode := range []string{"", "auto", "plan", " PLAN "} {
		t.Run("requested-"+mode, func(t *testing.T) {
			inputs, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","mode":"` + mode + `"}]}`)
			if err != nil || len(inputs) != 1 || inputs[0].Mode != sessionruntime.ModeAuto {
				t.Fatalf("inputs = %#v, err = %v; want Auto", inputs, err)
			}
		})
	}
	for _, mode := range []string{"", "plan", "PLAN", "read", "auto"} {
		t.Run("approved-"+mode, func(t *testing.T) {
			approved, err := parseApprovedManageSessionsDeploy(`{"action":"deploy","manifest_version":1,"selected_proposal_ids":["proposal-1"],"proposals":[{"id":"proposal-1","prompt":"work","mode":"` + mode + `","managed_worktree":true}]}`)
			if mode != sessionruntime.ModeAuto {
				if err == nil || !strings.Contains(err.Error(), "must start in auto") {
					t.Fatalf("err = %v; want pre-execution Auto-only rejection", err)
				}
				return
			}
			if err != nil || len(approved.Proposals) != 1 || approved.Proposals[0].Mode != sessionruntime.ModeAuto {
				t.Fatalf("approved = %#v, err = %v", approved, err)
			}
		})
	}
}
