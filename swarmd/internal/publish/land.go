package publish

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// LandResult is a strict receipt, not arbitrary command output.
type LandResult struct {
	Commit string `json:"commit,omitempty"`
	Moved  bool   `json:"moved,omitempty"`
}

// Land invokes the owner's fast-forward-only command. A receipt must name the
// exact requested head; MOVED asks the queue to refresh the base and retry.
func Land(ctx context.Context, rule Rule, req Request) (LandResult, error) {
	if rule.Mode != "land" || !commitPattern.MatchString(req.Head) || !commitPattern.MatchString(req.BaseCommit) || !branchPattern.MatchString(req.BaseBranch) {
		return LandResult{}, errors.New("land requires a valid head, base commit and base branch")
	}
	if err := ownedByRoot(rule.Command, true); err != nil {
		return LandResult{}, errors.New("land command is not a trusted executable")
	}
	scratch, err := os.MkdirTemp("", "swarm-land-*")
	if err != nil {
		return LandResult{}, err
	}
	defer os.RemoveAll(scratch)
	output, err := command(ctx, rule, scratch, 5*time.Minute, "land",
		"SWARM_PUBLISH_JOB_ID="+req.JobID, "SWARM_PUBLISH_BRANCH="+req.Branch,
		"SWARM_PUBLISH_HEAD="+req.Head, "SWARM_PUBLISH_BASE_BRANCH="+req.BaseBranch,
		"SWARM_PUBLISH_BASE_COMMIT="+req.BaseCommit)
	// Do not expose privileged command output: it may contain credentials.
	if err != nil {
		return LandResult{}, errors.New("land command failed")
	}
	receipt := lastLine(output)
	if receipt == "MOVED" {
		return LandResult{Moved: true}, nil
	}
	fields := strings.Fields(receipt)
	if len(fields) != 2 || fields[0] != "LANDED" || !commitPattern.MatchString(fields[1]) || fields[1] != req.Head {
		return LandResult{}, errors.New("land command did not print LANDED with the requested head or MOVED")
	}
	return LandResult{Commit: fields[1]}, nil
}
