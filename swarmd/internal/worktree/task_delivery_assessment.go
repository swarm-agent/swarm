package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TaskDeliveryInput is an already authenticated, captured session lane. This
// service never resolves another repository or repairs Git state.
type TaskDeliveryInput struct {
	Identity   pebblestore.TaskDeliveryAssessment
	SourcePath string
	TargetPath string
}

type deliveryFlight struct {
	done   chan struct{}
	result pebblestore.TaskDeliveryAssessment
}

type deliveryKey struct {
	Account, Task, Session, Attempt, Workspace, Base, Source, Target, SourceBranch, TargetBranch, SourcePath, TargetPath string
	Revision                                                                                                             int
	Generation                                                                                                           int64
}

var deliveryFlights = struct {
	sync.Mutex
	active map[deliveryKey]*deliveryFlight
	counts map[string]int
}{active: make(map[deliveryKey]*deliveryFlight), counts: make(map[string]int)}

type deliveryBudget struct{ remaining int }
type deliveryBuffer struct {
	bytes.Buffer
	budget *deliveryBudget
}

func (b *deliveryBuffer) Write(p []byte) (int, error) {
	if len(p) > b.budget.remaining {
		return 0, errors.New("delivery output limit exceeded")
	}
	b.budget.remaining -= len(p)
	return b.Buffer.Write(p)
}

type deliveryInspector struct {
	ctx    context.Context
	budget deliveryBudget
	// Recovery-only process overrides; normal read observations leave this nil.
	env []string
	// Instance-local seam for deterministic concurrent-mutation tests, never a
	// substitute Git implementation and never used by production callers.
	beforeFinish func()
	targetRepo   string
}

func (d *deliveryInspector) run(path string, args ...string) (string, error) {
	out := &deliveryBuffer{budget: &d.budget}
	cmd := exec.CommandContext(d.ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	cmd.Env = append(cmd.Env, d.env...)
	cmd.Stdout, cmd.Stderr = out, out
	sandbox.Prepare(d.ctx, cmd)
	defer sandbox.ReleaseGit(cmd)
	err := cmd.Run()
	return strings.TrimRight(out.String(), "\n"), err
}

// errLaneMoved is a lane Git read that succeeded and observed a different
// root or branch (including a detached HEAD): real movement, not a failure.
var errLaneMoved = errors.New("captured lane moved")

// A branch mismatch on the initial read is distinct from an unreadable lane.
// It still unwraps to movement when encountered during the final re-read.
type laneBranchMismatch struct{ actual, target string }

func (e *laneBranchMismatch) Error() string {
	return fmt.Sprintf("The folder is checked out on %s, not %s", e.actual, e.target)
}

func (e *laneBranchMismatch) Unwrap() error { return errLaneMoved }

// lane reads the root, common directory, HEAD commit and HEAD's symbolic name
// in one Git process: every Git call may be a sandbox exec, so one read per
// lane keeps the assessment inside its time budget.
func (d *deliveryInspector) lane(path, branch string) (head, common string, err error) {
	out, err := d.run(path, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir", "HEAD", "--symbolic-full-name", "HEAD")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 4 || len(lines[2]) < 40 {
		return "", "", errors.New("unexpected lane read output")
	}
	if gitstatus.NormalizePath(lines[0]) != gitstatus.NormalizePath(path) {
		return "", "", errLaneMoved
	}
	if lines[3] != "refs/heads/"+branch {
		return "", "", &laneBranchMismatch{actual: strings.TrimPrefix(lines[3], "refs/heads/"), target: branch}
	}
	return lines[2], gitstatus.NormalizePath(lines[1]), nil
}

// inspectionFailure reports a failed Git read as what it was: the time budget
// running out, the caller cancelling, or Git failing. None of these is evidence
// that the task's heads moved.
func (d *deliveryInspector) inspectionFailure(a pebblestore.TaskDeliveryAssessment, code, reason string) pebblestore.TaskDeliveryAssessment {
	switch err := d.ctx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		return deliveryUnknown(a, "inspection_timeout", "Git inspection exceeded its time budget", "unknown")
	case err != nil:
		return deliveryUnknown(a, "inspection_cancelled", "Assessment cancelled", "unknown")
	}
	return deliveryUnknown(a, code, reason, "unknown")
}

func (d *deliveryInspector) initialLaneFailure(a pebblestore.TaskDeliveryAssessment, err error) pebblestore.TaskDeliveryAssessment {
	var mismatch *laneBranchMismatch
	if errors.As(err, &mismatch) {
		return d.inspectionFailure(a, "lane_branch_mismatch", mismatch.Error())
	}
	return d.inspectionFailure(a, a.ReasonCode, a.Reason)
}

func deliveryUnknown(a pebblestore.TaskDeliveryAssessment, code, reason, freshness string) pebblestore.TaskDeliveryAssessment {
	a.State, a.ReasonCode, a.Reason, a.Freshness = "unavailable", code, reason, freshness
	a.AllowedActions = []string{}
	a.ObservedAt = 0
	return a
}

// Assessment budgets. Every Git read is one process; when the project's
// sandbox is active each is a container exec (a gVisor exec costs a few
// hundred milliseconds where host Git costs a few), so the sandboxed budget is
// sized for roughly fifteen such reads with headroom. Starting a cold sandbox
// is not part of either budget: it is warmed first, under its own bound.
var (
	deliveryHostBudget      = 3 * time.Second
	deliverySandboxedBudget = 15 * time.Second
	deliveryWarmTimeout     = 2 * time.Minute
)

func deliveryBudgetFor() time.Duration {
	if sandbox.Default().Active() {
		return deliverySandboxedBudget
	}
	return deliveryHostBudget
}

// AssessTaskDelivery caps the entire inspection at its time budget (three
// seconds for host Git, fifteen when Git runs in the project's sandbox) and one
// MiB, admits at most two analyses per repository and shares only exact OID
// tuples. There is no durable or cross-request cache and no Git writes.
func AssessTaskDelivery(parent context.Context, in TaskDeliveryInput) pebblestore.TaskDeliveryAssessment {
	return assessDeliveryWithHook(parent, in, nil)
}

func assessDeliveryWithHook(parent context.Context, in TaskDeliveryInput, beforeFinish func()) pebblestore.TaskDeliveryAssessment {
	// A sandbox that cannot start is reported by the reads below.
	warmCtx, cancelWarm := context.WithTimeout(parent, deliveryWarmTimeout)
	for _, path := range []string{in.SourcePath, in.TargetPath} {
		_ = sandbox.Warm(warmCtx, path)
	}
	cancelWarm()
	ctx, cancel := context.WithTimeout(parent, deliveryBudgetFor())
	defer cancel()
	d := &deliveryInspector{ctx: ctx, budget: deliveryBudget{remaining: 1 << 20}, beforeFinish: beforeFinish}
	a := deliveryUnknown(in.Identity, "lane_unavailable", "Captured Git lane is unavailable", "unknown")
	a.SourceOID, a.TargetOID, a.CandidateCommits, a.Files = "", "", 0, nil
	if len(a.BaseOID) != 40 && len(a.BaseOID) != 64 {
		return a
	}
	for _, c := range a.BaseOID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return a
		}
	}
	if gitstatus.NormalizePath(in.SourcePath) == gitstatus.NormalizePath(in.TargetPath) {
		return a
	}
	if _, err := os.Stat(in.SourcePath); errors.Is(err, os.ErrNotExist) {
		return d.inspectionFailure(a, "checkout_missing", "Captured job checkout is missing")
	}
	source, repo, err := d.lane(in.SourcePath, a.SourceBranch)
	if err != nil {
		return d.initialLaneFailure(a, err)
	}
	target, targetRepo, err := d.lane(in.TargetPath, a.TargetBranch)
	if err != nil {
		return d.initialLaneFailure(a, err)
	}
	if repo != targetRepo {
		return d.inspectionFailure(a, a.ReasonCode, a.Reason)
	}
	d.targetRepo = targetRepo
	a.SourceOID, a.TargetOID = source, target
	key := deliveryKey{a.AccountID, a.TaskID, a.SessionID, a.AttemptID, a.WorkspaceID, a.BaseOID, source, target, a.SourceBranch, a.TargetBranch, gitstatus.NormalizePath(in.SourcePath), gitstatus.NormalizePath(in.TargetPath), a.TaskRevision, a.WorkspaceGeneration}
	deliveryFlights.Lock()
	if flight := deliveryFlights.active[key]; flight != nil {
		deliveryFlights.Unlock()
		select {
		case <-flight.done:
			result := flight.result
			result.Files = append([]string(nil), result.Files...)
			result.AllowedActions = append([]string{}, result.AllowedActions...)
			return d.finish(in, result, repo)
		case <-ctx.Done():
			return d.inspectionFailure(a, "inspection_cancelled", "Assessment cancelled")
		}
	}
	if deliveryFlights.counts[repo] >= 2 {
		deliveryFlights.Unlock()
		return deliveryUnknown(a, "capacity_exceeded", "Repository assessment capacity reached", "unknown")
	}
	flight := &deliveryFlight{done: make(chan struct{})}
	deliveryFlights.active[key] = flight
	deliveryFlights.counts[repo]++
	deliveryFlights.Unlock()
	defer func() {
		deliveryFlights.Lock()
		delete(deliveryFlights.active, key)
		deliveryFlights.counts[repo]--
		if deliveryFlights.counts[repo] == 0 {
			delete(deliveryFlights.counts, repo)
		}
		close(flight.done)
		deliveryFlights.Unlock()
	}()
	flight.result = d.assess(in, a, repo)
	return flight.result
}

func (d *deliveryInspector) finish(in TaskDeliveryInput, a pebblestore.TaskDeliveryAssessment, repo string) pebblestore.TaskDeliveryAssessment {
	if d.beforeFinish != nil {
		d.beforeFinish()
		d.beforeFinish = nil
	}
	// Check cleanliness before the final OID reads so a commit made while
	// reading status cannot escape the end-of-observation movement check.
	// Any change left in the task worktree is uncommitted task work. In the
	// target, untracked files the task does not write (an app's own state
	// files, for example) do not block plain integration; tracked changes and
	// colliding untracked files do. Delta recovery keeps a fully clean target.
	integrateOnly := len(a.AllowedActions) == 0 || (len(a.AllowedActions) == 1 && a.AllowedActions[0] == "integrate")
	a.SourceDirtyFiles, a.TargetDirtyFiles = nil, nil
	for i, path := range []string{in.SourcePath, in.TargetPath} {
		status, err := d.run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil {
			return d.inspectionFailure(a, "inspection_failed", "Could not inspect working tree")
		}
		entries := parsePorcelainStatusZ(status)
		if i == 0 {
			a.SourceDirty = status != ""
			a.SourceDirtyFiles = describeStatusEntries(entries)
			continue
		}
		blocking := entries[:0:0]
		for _, entry := range entries {
			if entry.untracked() && untrackedPathCollides(entry.Path, a.Files) {
				entry.Code = untrackedCollisionCode
			} else if entry.untracked() && integrateOnly {
				continue
			}
			blocking = append(blocking, entry)
		}
		a.TargetDirty = len(blocking) > 0 || (status != "" && len(entries) == 0) // unparsed output fails closed
		a.TargetDirtyFiles = describeStatusEntries(blocking)
	}
	// Dirtiness blocks writes, not the committed ancestry observation.
	if a.SourceDirty || a.TargetDirty {
		a.AllowedActions = []string{}
	}
	source, sourceRepo, e1 := d.lane(in.SourcePath, a.SourceBranch)
	target, targetRepo, e2 := d.lane(in.TargetPath, a.TargetBranch)
	// Stale only on observed movement: both reads succeeded and an OID or
	// repository differs, or a read saw another root or branch.
	moved := (e1 == nil && e2 == nil && (source != a.SourceOID || target != a.TargetOID || sourceRepo != repo || targetRepo != d.targetRepo)) || errors.Is(e1, errLaneMoved) || errors.Is(e2, errLaneMoved)
	if moved {
		return deliveryUnknown(a, "heads_changed", "Heads moved during assessment; refresh", "stale")
	}
	if e1 != nil || e2 != nil || d.ctx.Err() != nil {
		return d.inspectionFailure(a, "inspection_failed", "Could not re-read the task's Git heads")
	}
	a.Freshness, a.ObservedAt = "observed", time.Now().UnixMilli()
	return a
}

func (d *deliveryInspector) assess(in TaskDeliveryInput, a pebblestore.TaskDeliveryAssessment, repo string) pebblestore.TaskDeliveryAssessment {
	set := func(state, code, reason string) pebblestore.TaskDeliveryAssessment {
		a.State, a.ReasonCode, a.Reason = state, code, reason
		return d.finish(in, a, repo)
	}
	fail := func() pebblestore.TaskDeliveryAssessment {
		return d.inspectionFailure(a, "inspection_failed", "Git inspection failed or exceeded output budget")
	}
	base, err := d.run(in.SourcePath, "rev-parse", "--verify", a.BaseOID+"^{commit}")
	if err != nil || base != a.BaseOID {
		return fail()
	}
	ancestorAt := func(path, left, right string) (bool, error) {
		_, err := d.run(path, "merge-base", "--is-ancestor", left, right)
		if err == nil {
			return true, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	ancestor := func(left, right string) (bool, error) { return ancestorAt(in.SourcePath, left, right) }
	if a.SourceOID == a.BaseOID {
		return set("empty", "no_commits", "No commits since captured base")
	}
	baseAncestor, err := ancestor(a.BaseOID, a.SourceOID)
	if err != nil {
		return fail()
	}
	if !baseAncestor {
		return set("ambiguous", "base_not_ancestor", "Recorded base is not an ancestor of source")
	}
	// Independent clones need not contain a target advanced since allocation.
	// Test integration in the target only when it actually has the source object;
	// a missing object cannot be an ancestor in this full repository.
	integrated := false
	_, sourceOnTarget := d.run(in.TargetPath, "rev-parse", "--verify", "--quiet", a.SourceOID+"^{commit}")
	if sourceOnTarget == nil {
		integrated, err = ancestorAt(in.TargetPath, a.SourceOID, a.TargetOID)
	} else {
		var exit *exec.ExitError
		if !errors.As(sourceOnTarget, &exit) || exit.ExitCode() != 1 {
			return fail()
		}
	}
	if err != nil {
		return fail()
	}
	if integrated {
		return set("integrated", "source_ancestor", "Original source ancestry verified on target")
	}
	commits, err := d.run(in.SourcePath, "rev-list", "--max-count=257", "--parents", a.BaseOID+".."+a.SourceOID)
	if err != nil {
		return fail()
	}
	lines := strings.Split(commits, "\n")
	if len(lines) > 256 {
		return set("ambiguous", "commit_limit", "Candidate commit limit exceeded")
	}
	hasMerges := false
	for _, line := range lines {
		hasMerges = hasMerges || len(strings.Fields(line)) > 2
	}
	a.CandidateCommits = len(lines)
	paths, err := d.run(in.SourcePath, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", a.BaseOID, a.SourceOID, "--")
	if err != nil {
		return fail()
	}
	if paths != "" {
		a.Files = strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")
	}
	if len(a.Files) > 512 {
		a.Files = nil
		return set("ambiguous", "path_limit", "Candidate path limit exceeded")
	}
	if len(a.Files) == 0 {
		return set("empty", "no_net_changes", "Candidate commits have no net file changes")
	}
	// Exact full-tree equality is intentionally stricter than patch matching.
	// Historical matches (including reverted matches) never authorize merging.
	sourceTree, e1 := d.run(in.SourcePath, "rev-parse", a.SourceOID+"^{tree}")
	targetTree, e2 := d.run(in.TargetPath, "rev-parse", a.TargetOID+"^{tree}")
	if e1 != nil || e2 != nil {
		return fail()
	}
	if sourceTree == targetTree {
		a.AllowedActions = []string{"recover_integrate"}
		return set("history_equivalent", "current_tree_equal", "Current trees match; original source history is not integrated")
	}
	baseOnTarget, err := ancestorAt(in.TargetPath, a.BaseOID, a.TargetOID)
	if err != nil {
		return fail()
	}
	if !baseOnTarget || hasMerges {
		// Recovery preflight additionally verifies a bounded first-parent base
		// before deriving the net delta. This observation grants no Git writes.
		a.AllowedActions = []string{"recover_integrate"}
		if hasMerges && baseOnTarget {
			return set("history_rewritten", "task_merge_delta", "Task contains merges; recover the recorded net delta")
		}
		return set("history_rewritten", "base_not_on_target", "Target no longer contains recorded base; recover the recorded task delta")
	}
	// An advanced target is normal for parallel tasks. This read only admits a
	// candidate; PrepareTaskIntegration owns conflict preflight and Apply retains
	// the expected-head/clean-target guards before changing the checkout.
	a.AllowedActions = []string{"integrate"}
	return set("candidate_work", "candidate_delta", "Bounded changes since captured base")
}
