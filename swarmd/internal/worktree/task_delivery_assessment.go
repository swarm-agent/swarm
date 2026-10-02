package worktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
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
	// Instance-local seam for deterministic concurrent-mutation tests, never a
	// substitute Git implementation and never used by production callers.
	beforeFinish func()
}

func (d *deliveryInspector) run(path string, args ...string) (string, error) {
	out := &deliveryBuffer{budget: &d.budget}
	cmd := exec.CommandContext(d.ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return strings.TrimRight(out.String(), "\n"), err
}

func (d *deliveryInspector) lane(path, branch string) (head, common string, err error) {
	root, err := d.run(path, "rev-parse", "--show-toplevel")
	if err != nil || gitstatus.NormalizePath(root) != gitstatus.NormalizePath(path) {
		return "", "", errors.New("captured root changed")
	}
	common, err = d.run(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	common = gitstatus.NormalizePath(common)
	actual, err := d.run(path, "symbolic-ref", "--short", "HEAD")
	if err != nil || actual != branch {
		return "", "", errors.New("captured branch changed")
	}
	head, err = d.run(path, "rev-parse", "--verify", "HEAD^{commit}")
	return head, common, err
}

func deliveryUnknown(a pebblestore.TaskDeliveryAssessment, code, reason, freshness string) pebblestore.TaskDeliveryAssessment {
	a.State, a.ReasonCode, a.Reason, a.Freshness = "unavailable", code, reason, freshness
	a.AllowedActions = []string{}
	a.ObservedAt = 0
	return a
}

// AssessTaskDelivery caps the entire inspection at three seconds and one MiB,
// admits at most two analyses per repository and shares only exact OID tuples.
// There is no durable or cross-request cache and no Git writes.
func AssessTaskDelivery(parent context.Context, in TaskDeliveryInput) pebblestore.TaskDeliveryAssessment {
	return assessDeliveryWithHook(parent, in, nil)
}

func assessDeliveryWithHook(parent context.Context, in TaskDeliveryInput, beforeFinish func()) pebblestore.TaskDeliveryAssessment {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
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
	source, repo, err := d.lane(in.SourcePath, a.SourceBranch)
	if err != nil {
		return a
	}
	target, targetRepo, err := d.lane(in.TargetPath, a.TargetBranch)
	if err != nil || repo != targetRepo {
		return a
	}
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
			return deliveryUnknown(a, "inspection_cancelled", "Assessment cancelled", "unknown")
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
	for _, path := range []string{in.SourcePath, in.TargetPath} {
		status, err := d.run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil || status != "" {
			return deliveryUnknown(a, "worktree_changed", "Worktree changed during assessment; refresh", "stale")
		}
	}
	source, sourceRepo, e1 := d.lane(in.SourcePath, a.SourceBranch)
	target, targetRepo, e2 := d.lane(in.TargetPath, a.TargetBranch)
	if e1 != nil || e2 != nil || source != a.SourceOID || target != a.TargetOID || sourceRepo != repo || targetRepo != repo || d.ctx.Err() != nil {
		return deliveryUnknown(a, "heads_changed", "Heads moved during assessment; refresh", "stale")
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
		return deliveryUnknown(a, "inspection_failed", "Git inspection failed, cancelled or exceeded output budget", "unknown")
	}
	base, err := d.run(in.SourcePath, "rev-parse", "--verify", a.BaseOID+"^{commit}")
	if err != nil || base != a.BaseOID {
		return fail()
	}
	for _, path := range []string{in.SourcePath, in.TargetPath} {
		status, err := d.run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil {
			return fail()
		}
		if status != "" {
			a.State, a.ReasonCode, a.Reason = "dirty", "uncommitted_changes", "Source or target has uncommitted changes"
			return a
		}
	}
	ancestor := func(left, right string) (bool, error) {
		_, err := d.run(in.SourcePath, "merge-base", "--is-ancestor", left, right)
		if err == nil {
			return true, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
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
	integrated, err := ancestor(a.SourceOID, a.TargetOID)
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
	for _, line := range lines {
		if len(strings.Fields(line)) != 2 {
			return set("ambiguous", "merge_topology", "Merge topology requires reviewed recovery")
		}
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
	targetTree, e2 := d.run(in.SourcePath, "rev-parse", a.TargetOID+"^{tree}")
	if e1 != nil || e2 != nil {
		return fail()
	}
	if sourceTree == targetTree {
		return set("history_equivalent", "current_tree_equal", "Current trees match; original source history is not integrated")
	}
	baseOnTarget, err := ancestor(a.BaseOID, a.TargetOID)
	if err != nil {
		return fail()
	}
	if !baseOnTarget {
		return set("history_rewritten", "base_not_on_target", "Target no longer contains recorded base; reviewed recovery required")
	}
	if a.TargetOID != a.BaseOID {
		return set("ambiguous", "target_advanced", "Target advanced; review overlap before integration")
	}
	a.AllowedActions = []string{"integrate"}
	return set("candidate_work", "candidate_delta", "Bounded changes since captured base")
}
