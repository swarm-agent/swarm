package worktree

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TaskDeliveryInput is an already authenticated, captured session lane. This
// read-only service never resolves a different repository or repairs Git state.
type TaskDeliveryInput struct {
	Identity pebblestore.TaskDeliveryAssessment
	SourcePath string
	TargetPath string
}

type deliveryFlight struct {
	done chan struct{}
	result pebblestore.TaskDeliveryAssessment
}

var deliveryFlights = struct {
	sync.Mutex
	active map[TaskDeliveryInputKey]*deliveryFlight
	counts map[string]int
}{active: make(map[TaskDeliveryInputKey]*deliveryFlight), counts: make(map[string]int)}

type TaskDeliveryInputKey struct {
	Account, Task, Session, Attempt, Workspace, Base, Source, Target, SourceBranch, TargetBranch string
	Revision int
	Generation int64
}

type deliveryBudget struct { remaining int }
type deliveryBuffer struct { bytes.Buffer; budget *deliveryBudget }
func (b *deliveryBuffer) Write(p []byte) (int, error) {
	if len(p) > b.budget.remaining { return 0, errors.New("delivery output limit exceeded") }
	b.budget.remaining -= len(p)
	return b.Buffer.Write(p)
}

// AssessTaskDelivery bounds aggregate output, elapsed time, candidate history,
// paths and concurrent analyses. Flights are shared only while running, never
// cached across reads of mutable heads.
func AssessTaskDelivery(parent context.Context, in TaskDeliveryInput) pebblestore.TaskDeliveryAssessment {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	a := in.Identity
	a.State, a.Reason, a.Freshness = "unavailable", "Captured Git lane is unavailable", "unknown"
	a.AllowedActions = []string{}
	watch, err := gitstatus.ResolveWatchPaths(ctx, in.TargetPath)
	if err != nil { return a }
	repo := gitstatus.NormalizePath(watch.CommonDir)
	key := TaskDeliveryInputKey{a.AccountID, a.TaskID, a.SessionID, a.AttemptID, a.WorkspaceID, a.BaseOID, in.SourcePath, in.TargetPath, a.SourceBranch, a.TargetBranch, a.TaskRevision, a.WorkspaceGeneration}
	deliveryFlights.Lock()
	if flight := deliveryFlights.active[key]; flight != nil {
		deliveryFlights.Unlock()
		select { case <-flight.done: return flight.result; case <-ctx.Done(): return a }
	}
	if deliveryFlights.counts[repo] >= 2 { deliveryFlights.Unlock(); a.Reason = "Repository assessment capacity reached"; return a }
	flight := &deliveryFlight{done: make(chan struct{})}
	deliveryFlights.active[key] = flight
	deliveryFlights.counts[repo]++
	deliveryFlights.Unlock()
	defer func() {
		deliveryFlights.Lock()
		delete(deliveryFlights.active, key)
		deliveryFlights.counts[repo]--
		if deliveryFlights.counts[repo] == 0 { delete(deliveryFlights.counts, repo) }
		close(flight.done)
		deliveryFlights.Unlock()
	}()
	flight.result = assessTaskDelivery(ctx, in, a, repo)
	return flight.result
}

func assessTaskDelivery(ctx context.Context, in TaskDeliveryInput, a pebblestore.TaskDeliveryAssessment, repo string) pebblestore.TaskDeliveryAssessment {
	if len(a.BaseOID) != 40 && len(a.BaseOID) != 64 { return a }
	for _, c := range a.BaseOID { if !strings.ContainsRune("0123456789abcdef", c) { return a } }
	budget := &deliveryBudget{remaining: 1 << 20}
	run := func(path string, args ...string) (string, error) {
		out := &deliveryBuffer{budget: budget}
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", path}, args...)...)
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		return strings.TrimRight(out.String(), "\n"), err
	}
	childWatch, err := gitstatus.ResolveWatchPaths(ctx, in.SourcePath)
	if err != nil || gitstatus.NormalizePath(childWatch.CommonDir) != repo || gitstatus.NormalizePath(childWatch.RepoRoot) == gitstatus.NormalizePath(in.TargetPath) { return a }
	head := func(path, branch string) (string, error) {
		actual, err := run(path, "symbolic-ref", "--short", "HEAD")
		if err != nil || actual != branch { return "", errors.New("captured branch changed") }
		return run(path, "rev-parse", "--verify", "HEAD^{commit}")
	}
	a.SourceOID, err = head(in.SourcePath, a.SourceBranch)
	if err != nil { return a }
	a.TargetOID, err = head(in.TargetPath, a.TargetBranch)
	if err != nil { return a }
	base, err := run(in.SourcePath, "rev-parse", "--verify", a.BaseOID+"^{commit}")
	if err != nil || base != a.BaseOID { return a }
	for _, path := range []string{in.SourcePath, in.TargetPath} {
		status, err := run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil { a.Reason = "Git status unavailable or output limit exceeded"; return a }
		if status != "" { a.State, a.Reason = "dirty", "Source or target has uncommitted changes"; return a }
	}
	ancestor := func(left, right string) (bool, error) {
		_, err := run(in.SourcePath, "merge-base", "--is-ancestor", left, right)
		if err == nil { return true, nil }
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 { return false, nil }
		return false, err
	}
	finish := func() pebblestore.TaskDeliveryAssessment {
		source, e1 := head(in.SourcePath, a.SourceBranch)
		target, e2 := head(in.TargetPath, a.TargetBranch)
		if e1 != nil || e2 != nil || source != a.SourceOID || target != a.TargetOID || ctx.Err() != nil {
			a.State, a.Reason, a.Freshness = "unavailable", "Heads moved during assessment; refresh", "stale"
			a.AllowedActions = []string{}
		} else {
			for _, path := range []string{in.SourcePath, in.TargetPath} {
				status, err := run(path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
				if err != nil || status != "" {
					a.State, a.Reason, a.Freshness = "unavailable", "Worktree changed during assessment; refresh", "stale"
					a.AllowedActions = []string{}
					return a
				}
			}
			a.Freshness = "observed"; a.ObservedAt = time.Now().UnixMilli()
		}
		return a
	}
	if a.SourceOID == a.BaseOID { a.State, a.Reason = "empty", "No commits since captured base"; return finish() }
	baseAncestor, err := ancestor(a.BaseOID, a.SourceOID)
	if err != nil { return a }
	if !baseAncestor { a.State, a.Reason = "ambiguous", "Recorded base is not an ancestor of source"; return finish() }
	integrated, err := ancestor(a.SourceOID, a.TargetOID)
	if err != nil { return a }
	if integrated { a.State, a.Reason = "integrated", "Original source ancestry verified on target"; return finish() }
	commits, err := run(in.SourcePath, "rev-list", "--max-count=257", "--parents", a.BaseOID+".."+a.SourceOID)
	if err != nil { return a }
	lines := strings.Split(commits, "\n")
	if len(lines) > 256 { a.State, a.Reason = "ambiguous", "Candidate commit limit exceeded"; return finish() }
	for _, line := range lines { if len(strings.Fields(line)) != 2 { a.State, a.Reason = "ambiguous", "Merge topology requires reviewed recovery"; return finish() } }
	a.CandidateCommits = len(lines)
	paths, err := run(in.SourcePath, "diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", a.BaseOID, a.SourceOID, "--")
	if err != nil { return a }
	if paths != "" { a.Files = strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00") }
	if len(a.Files) > 512 { a.Files = nil; a.State, a.Reason = "ambiguous", "Candidate path limit exceeded"; return finish() }
	// Full current-tree identity is deliberately stricter than patch history:
	// cherry-picks, squash and reverted patches never prove original integration.
	sourceTree, e1 := run(in.SourcePath, "rev-parse", a.SourceOID+"^{tree}")
	targetTree, e2 := run(in.SourcePath, "rev-parse", a.TargetOID+"^{tree}")
	if e1 != nil || e2 != nil { return a }
	if sourceTree == targetTree { a.State, a.Reason = "history_equivalent", "Current trees match; original source history is not integrated"; return finish() }
	baseOnTarget, err := ancestor(a.BaseOID, a.TargetOID)
	if err != nil { return a }
	if !baseOnTarget { a.State, a.Reason = "history_rewritten", "Target no longer contains recorded base; reviewed recovery required"; return finish() }
	// Only a target still at the captured base can be offered direct integration.
	// Divergence may include partial overlap, squash or reverts; do not guess.
	if a.TargetOID != a.BaseOID { a.State, a.Reason = "ambiguous", "Target advanced; review overlap before integration"; return finish() }
	if len(a.Files) == 0 { a.State, a.Reason = "empty", "Candidate commits have no net file changes"; return finish() }
	a.State, a.Reason = "candidate_work", "Bounded changes since captured base"
	a.AllowedActions = []string{"integrate"}
	return finish()
}
