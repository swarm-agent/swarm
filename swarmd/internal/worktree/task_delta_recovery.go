package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TaskDeltaRecovery contains only the recorded base-to-source net change, rebased
// onto the inspected target. Original history is never merged into that target.
// PreparedHead is retained even on conflicts, for the existing isolated repair
// workflow; conflict-marker content is NEVER promoted automatically.
type TaskDeltaRecovery struct {
	PreparedHead string
	RetainedRef  string
	Equivalent   bool
	Conflict     string
}

func (s *Service) PrepareTaskDeltaRecovery(ctx context.Context, in TaskDeliveryInput) (TaskDeltaRecovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	d := &deliveryInspector{ctx: ctx, budget: deliveryBudget{remaining: 2 << 20}}
	a := in.Identity
	result := TaskDeltaRecovery{}
	if !validCommitID(a.BaseOID) || !validCommitID(a.SourceOID) || !validCommitID(a.TargetOID) || a.BaseOID == a.SourceOID {
		return result, errors.New("recovery requires exact nonempty recorded base/source/target commits")
	}
	if err := d.checkRecoveryLane(in); err != nil {
		return result, err
	}
	// Reuse the inspected source's date for deterministic preparation identities
	// and conflict labels across retries, rather than inventing source authorship.
	date, err := d.run(in.SourcePath, "show", "-s", "--format=%cI", a.SourceOID)
	if err != nil {
		return result, err
	}
	d.env = []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	// A first-parent boundary establishes the task's net delta even when it
	// includes legitimate merges. A base reachable only via a merged side lane
	// is ambiguous; never guess which history belongs to the task.
	chain, err := d.run(in.SourcePath, "rev-list", "--first-parent", "--max-count=258", a.SourceOID)
	if err != nil {
		return result, err
	}
	found := false
	for _, oid := range strings.Fields(chain) {
		if oid == a.BaseOID {
			found = true
			break
		}
	}
	if !found {
		return result, errors.New("recorded base is not within the bounded first-parent task history; open the task and repair its recorded boundary before recovery")
	}
	paths, err := d.run(in.SourcePath, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", a.BaseOID, a.SourceOID, "--")
	if err != nil || paths == "" || len(strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")) > 512 {
		return result, errors.New("recorded task delta is empty, unavailable or exceeds the 512-path recovery limit")
	}
	tree, err := d.run(in.SourcePath, "rev-parse", a.SourceOID+"^{tree}")
	if err != nil {
		return result, err
	}
	// Squash *only* base..source. This synthetic commit is not an assertion
	// that any original source commit was merged or patch-equivalent.
	// Use a target-tree commit parented to the recorded base. Its sole merge
	// base with the squashed delta is therefore exactly that recorded base,
	// even on Git versions without merge-tree --merge-base support.
	delta, err := d.run(in.SourcePath, "commit-tree", tree, "-p", a.BaseOID, "-m", "Prepare recorded task net delta")
	if err != nil {
		return result, fmt.Errorf("prepare task delta: %w", err)
	}
	currentTree, err := d.run(in.SourcePath, "rev-parse", a.TargetOID+"^{tree}")
	if err != nil {
		return result, err
	}
	targetSide, err := d.run(in.SourcePath, "commit-tree", currentTree, "-p", a.BaseOID, "-m", "Prepare current target tree for task delta")
	if err != nil {
		return result, err
	}
	merged, mergeErr := d.run(in.SourcePath, "merge-tree", "--write-tree", targetSide, delta)
	lines := strings.SplitN(merged, "\n", 2)
	if len(lines) == 0 || !validCommitID(lines[0]) {
		return result, fmt.Errorf("task delta preflight unavailable: %s", merged)
	}
	if mergeErr != nil {
		// Git exit 1 with a tree is a content conflict; other failures must not
		// create a repair source from an unverified output.
		if !isRecoveryConflict(mergeErr) {
			return result, fmt.Errorf("task delta preflight failed: %w", mergeErr)
		}
		result.Conflict = "Task delta conflicts with the captured target. Launch repair session to resolve the retained delta, then integrate."
		if len(lines) == 2 {
			result.Conflict += "\n" + lines[1]
		}
	}
	targetTree, err := d.run(in.SourcePath, "rev-parse", a.TargetOID+"^{tree}")
	if err != nil {
		return result, err
	}
	if err := d.checkRecoveryLane(in); err != nil {
		return result, err
	}
	if mergeErr == nil && lines[0] == targetTree {
		result.Equivalent = true
		return result, nil
	}
	message := "Recover recorded task delta\n\nOriginal-base: " + a.BaseOID + "\nOriginal-source: " + a.SourceOID
	if result.Conflict != "" {
		message += "\nSwarm-Recovery-Conflict: unresolved"
	}
	result.PreparedHead, err = d.run(in.SourcePath, "commit-tree", lines[0], "-p", a.TargetOID, "-m", message)
	if err != nil {
		return result, err
	}
	// Deterministic identity bounds retained refs across retries; update-ref CAS
	// avoids replacing any preexisting prepared source with different bytes.
	digest := sha256.Sum256([]byte(strings.Join([]string{a.AccountID, a.TaskID, a.AttemptID, a.BaseOID, a.SourceOID, a.TargetOID}, "\x00")))
	result.RetainedRef = "refs/swarm/task-recovery/" + hex.EncodeToString(digest[:])
	previous, lookupErr := d.run(in.SourcePath, "rev-parse", "--verify", result.RetainedRef+"^{commit}")
	if lookupErr == nil {
		previousTree, e1 := d.run(in.SourcePath, "rev-parse", previous+"^{tree}")
		parent, e2 := d.run(in.SourcePath, "rev-parse", previous+"^")
		if e1 != nil || e2 != nil || previousTree != lines[0] || parent != a.TargetOID {
			return TaskDeltaRecovery{}, errors.New("retained recovery source differs; inspect task recovery provenance")
		}
		result.PreparedHead = previous
	} else if _, err := d.run(in.SourcePath, "update-ref", result.RetainedRef, result.PreparedHead, strings.Repeat("0", len(result.PreparedHead))); err != nil {
		return TaskDeltaRecovery{}, fmt.Errorf("retain recovery source: %w", err)
	}
	return result, nil
}

func isRecoveryConflict(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func (d *deliveryInspector) checkRecoveryLane(in TaskDeliveryInput) error {
	a := in.Identity
	if in.SourcePath == in.TargetPath {
		return errors.New("recovery requires an isolated source")
	}
	source, repo, e1 := d.lane(in.SourcePath, a.SourceBranch)
	target, targetRepo, e2 := d.lane(in.TargetPath, a.TargetBranch)
	if e1 != nil || e2 != nil || repo != targetRepo || source != a.SourceOID || target != a.TargetOID {
		return errors.New("recovery source/repository/target changed; refresh task")
	}
	for _, path := range []string{in.SourcePath, in.TargetPath} {
		status, err := d.run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil || status != "" {
			return errors.New("recovery source or captured target is dirty or unavailable")
		}
	}
	return nil
}

// CheckTaskDeltaRecovery is the last source/target guard before promotion.
func (s *Service) CheckTaskDeltaRecovery(ctx context.Context, in TaskDeliveryInput) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return (&deliveryInspector{ctx: ctx, budget: deliveryBudget{remaining: 1 << 20}}).checkRecoveryLane(in)
}

// ObserveTaskDeltaReceipt projects only exact, authenticated recovery evidence.
// It does not write task state and never labels the original source integrated.
func ObserveTaskDeltaReceipt(ctx context.Context, in TaskDeliveryInput, receipt *pebblestore.ProjectTaskIntegration) pebblestore.TaskDeliveryAssessment {
	a := in.Identity
	if receipt == nil || a.Freshness != "observed" || receipt.SessionID != a.SessionID || receipt.AttemptID != a.AttemptID || receipt.SourceHead != a.SourceOID || receipt.RecoveryBase != a.BaseOID || receipt.TargetBranch != a.TargetBranch || receipt.SourceBranch != a.SourceBranch || receipt.TargetWorkspacePath != in.TargetPath {
		return a
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d := &deliveryInspector{ctx: ctx, budget: deliveryBudget{remaining: 1 << 20}}
	verified := false
	switch receipt.State {
	case "recovered", "in_progress":
		if validCommitID(receipt.RecoveredHead) {
			_, err := d.run(in.TargetPath, "merge-base", "--is-ancestor", receipt.RecoveredHead, a.TargetOID)
			verified = err == nil
		}
	case "equivalent":
		verified = receipt.ResultingTargetHead == a.TargetOID && receipt.PreviousTargetHead == a.TargetOID
	}
	if verified && d.checkRecoveryLane(in) == nil {
		a.State, a.ReasonCode = receipt.State, "verified_delta_receipt"
		if a.State == "in_progress" {
			a.State = "recovered"
		}
		a.Reason = "Task delta recovered and delivered; original source history preserved separately"
		if receipt.State == "equivalent" {
			a.Reason = "Task delta already present; no original source merge performed"
		}
		a.AllowedActions, a.CandidateCommits = []string{}, 0
	}
	return a
}
