package publish

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LandResult is a strict receipt, not arbitrary command output.
type LandResult struct {
	Commit string `json:"commit,omitempty"`
	Moved  bool   `json:"moved,omitempty"`
}

// ParseHeadOutput extracts command evidence for landing.
func ParseHeadOutput(output string) (string, error) {
	head := lastLine(output)
	if !commitPattern.MatchString(head) {
		return "", errors.New("land refused: head is not a commit id")
	}
	return head, nil
}

// Land invokes the owner's fast-forward-only command. A receipt must name the
// exact requested head; MOVED asks the queue to refresh the base and retry.
func Land(ctx context.Context, rule Rule, req Request) (LandResult, error) {
	if rule.Mode != "land" {
		return LandResult{}, errors.New("land refused: mode is not land")
	}
	if !commitPattern.MatchString(req.Head) {
		return LandResult{}, errors.New("land refused: head is not a commit id")
	}
	if !commitPattern.MatchString(req.BaseCommit) {
		return LandResult{}, errors.New("land refused: base commit is not a commit id")
	}
	if !branchPattern.MatchString(req.BaseBranch) {
		return LandResult{}, errors.New("land refused: base branch is not a valid branch")
	}
	if err := ownedByRoot(rule.Command, true); err != nil {
		return LandResult{}, errors.New("land command is not a trusted executable")
	}
	scratch, err := os.MkdirTemp("", "swarm-land-*")
	if err != nil {
		return LandResult{}, err
	}
	defer os.RemoveAll(scratch)
	output, err := command(ctx, rule, scratch, landTimeout(), "land",
		"SWARM_PUBLISH_JOB_ID="+req.JobID, "SWARM_PUBLISH_BRANCH="+req.Branch,
		"SWARM_PUBLISH_HEAD="+req.Head, "SWARM_PUBLISH_BASE_BRANCH="+req.BaseBranch,
		"SWARM_PUBLISH_BASE_COMMIT="+req.BaseCommit)
	// command redacts privileged output before it crosses into delivery_reason.
	if err != nil {
		return LandResult{}, landFailure(output)
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

// A valid step receipt takes precedence over diagnostic output.
var appFailureReceipt = regexp.MustCompile(`(?m)^APP_RUNS_FAILED:([a-z][a-z0-9-]{0,63})$`)

func landFailure(output string) error {
	if match := appFailureReceipt.FindStringSubmatch(output); match != nil {
		return errors.New("app-runs landing check failed: " + match[1])
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// Surface the script's summary first, even when followed by noisy Git output.
	var refusal string
	for _, line := range lines {
		if strings.HasPrefix(line, "LAND_REFUSED: ") {
			refusal = line
		}
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	if refusal != "" {
		kept := []string{refusal}
		for _, line := range lines {
			if line != refusal {
				kept = append(kept, line)
			}
		}
		if len(kept) > 12 {
			kept = append(kept[:1], kept[len(kept)-11:]...)
		}
		lines = kept
	}
	details := strings.TrimSpace(strings.Join(lines, "\n"))
	if details == "" {
		return errors.New("land command failed")
	}
	return errors.New("land command failed: " + details)
}

var landCredentials = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*:\s*)[^\r\n]+`),
	regexp.MustCompile(`(?i)(x-access-token:\s*)[^\s@'"<>]+`),
	regexp.MustCompile(`(?:ghp_|github_pat_|gho_)[A-Za-z0-9_]+`),
	regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/]+@`),
}

// Redact the exact command environment, not unrelated daemon environment values.
// Longest values go first so overlapping secrets cannot leave a suffix exposed.
func redactLandOutput(output string, env []string) string {
	var secrets []string
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if ok && value != "" && (strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "KEY") || strings.Contains(name, "PASSWORD")) {
			secrets = append(secrets, value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		output = strings.ReplaceAll(output, secret, "[REDACTED]")
	}
	for i, pattern := range landCredentials {
		replacement := "${1}[REDACTED]"
		if i == 2 {
			replacement = "[REDACTED]"
		}
		if i == 3 {
			replacement += "@"
		}
		output = pattern.ReplaceAllString(output, replacement)
	}
	return output
}

func landTimeout() time.Duration {
	if value, err := time.ParseDuration(os.Getenv("SWARM_LAND_TIMEOUT")); err == nil && value > 0 && value <= time.Hour {
		return value
	}
	return 10 * time.Minute
}
