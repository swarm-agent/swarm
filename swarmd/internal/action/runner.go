package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ActionRunStatusRunning   = "running"
	ActionRunStatusSucceeded = "succeeded"
	ActionRunStatusFailed    = "failed"
	ActionRunStatusTimedOut  = "timed_out"
	ActionRunStatusCancelled = "cancelled"

	defaultActionRunTimeout = 10 * time.Minute
	maxActionRunOutputBytes = 256 << 10
	maxConcurrentActionRuns = 8
	maxRetainedActionRuns   = 128
)

type Runner struct {
	actions *Service
	rootCtx context.Context

	mu       sync.Mutex
	runs     map[string]*actionRun
	order    []string
	active   int
	stopping bool
	wg       sync.WaitGroup
}

type RunInput struct {
	Scope
	ActionID string
	Inputs   map[string]string
}

type RunSnapshot struct {
	ID                string `json:"id"`
	ActionID          string `json:"action_id"`
	ActionName        string `json:"action_name"`
	Status            string `json:"status"`
	Output            string `json:"output"`
	OutputTruncated   bool   `json:"output_truncated"`
	OutputBytes       int64  `json:"output_bytes"`
	StartedAt         int64  `json:"started_at"`
	CompletedAt       int64  `json:"completed_at,omitempty"`
	DurationMS        int64  `json:"duration_ms"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	TerminationSignal string `json:"termination_signal,omitempty"`
	Error             string `json:"error,omitempty"`
}

type actionRun struct {
	scope           Scope
	snapshot        RunSnapshot
	output          *cappedOutput
	cancel          context.CancelFunc
	cancelRequested bool
}

func NewRunner(rootCtx context.Context, actions *Service) *Runner {
	if actions == nil {
		return nil
	}
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Runner{actions: actions, rootCtx: rootCtx, runs: make(map[string]*actionRun)}
}

func (r *Runner) Start(input RunInput) (RunSnapshot, error) {
	if r == nil || r.actions == nil {
		return RunSnapshot{}, errors.New("action runner is not configured")
	}
	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return RunSnapshot{}, err
	}
	action, found, err := r.actions.Get(scope, strings.TrimSpace(input.ActionID))
	if err != nil {
		return RunSnapshot{}, err
	}
	if !found {
		return RunSnapshot{}, fmt.Errorf("action %q not found", strings.TrimSpace(input.ActionID))
	}
	entrypoint, err := resolveActionEntrypoint(scope.RuntimePath, action.Entrypoint)
	if err != nil {
		return RunSnapshot{}, err
	}
	argv, err := assembleActionArguments(action, input.Inputs)
	if err != nil {
		return RunSnapshot{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping || r.rootCtx.Err() != nil {
		return RunSnapshot{}, errors.New("action runner is shutting down")
	}
	if r.active >= maxConcurrentActionRuns {
		return RunSnapshot{}, fmt.Errorf("too many active action runs; limit is %d", maxConcurrentActionRuns)
	}
	r.pruneLocked()

	runCtx, cancel := context.WithTimeout(r.rootCtx, defaultActionRunTimeout)
	now := time.Now().UnixMilli()
	run := &actionRun{
		scope:  scope,
		output: newCappedOutput(maxActionRunOutputBytes),
		cancel: cancel,
		snapshot: RunSnapshot{
			ID:         newRunID(),
			ActionID:   action.ID,
			ActionName: action.Name,
			Status:     ActionRunStatusRunning,
			StartedAt:  now,
		},
	}
	r.runs[run.snapshot.ID] = run
	r.order = append(r.order, run.snapshot.ID)
	r.active++
	r.wg.Add(1)
	go r.execute(runCtx, run, entrypoint, argv)
	return run.snapshot, nil
}

func (r *Runner) Get(scope Scope, runID string) (RunSnapshot, bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, found := r.runs[strings.TrimSpace(runID)]
	if !found || run.scope.AccountScopeID != scope.AccountScopeID || run.scope.WorkspaceID != scope.WorkspaceID || run.scope.WorkspacePath != scope.WorkspacePath {
		return RunSnapshot{}, false, nil
	}
	return snapshotLocked(run), true, nil
}

func (r *Runner) Cancel(scope Scope, runID string) (RunSnapshot, bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	r.mu.Lock()
	run, found := r.runs[strings.TrimSpace(runID)]
	if !found || run.scope.AccountScopeID != scope.AccountScopeID || run.scope.WorkspaceID != scope.WorkspaceID || run.scope.WorkspacePath != scope.WorkspacePath {
		r.mu.Unlock()
		return RunSnapshot{}, false, nil
	}
	if run.snapshot.Status == ActionRunStatusRunning {
		run.cancelRequested = true
		run.cancel()
	}
	snapshot := snapshotLocked(run)
	r.mu.Unlock()
	return snapshot, true, nil
}

func (r *Runner) CancelAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopping = true
	for _, run := range r.runs {
		if run.snapshot.Status == ActionRunStatusRunning {
			run.cancelRequested = true
			run.cancel()
		}
	}
}

func (r *Runner) Wait(timeout time.Duration) bool {
	if r == nil {
		return true
	}
	if timeout <= 0 {
		r.wg.Wait()
		return true
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (r *Runner) execute(ctx context.Context, run *actionRun, entrypoint string, argv []string) {
	defer r.wg.Done()
	cmd := exec.Command(entrypoint, argv...)
	cmd.Dir = run.scope.RuntimePath
	cmd.Stdin = nil
	cmd.Stdout = run.output
	cmd.Stderr = run.output
	prepareActionCommand(cmd)

	startErr := cmd.Start()
	if startErr == nil {
		stopWatching := watchActionCommandCancellation(ctx, cmd)
		startErr = cmd.Wait()
		stopWatching()
	}
	completedAt := time.Now().UnixMilli()

	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		r.active--
		run.cancel()
	}()
	run.snapshot.CompletedAt = completedAt
	run.snapshot.DurationMS = maxInt64(0, completedAt-run.snapshot.StartedAt)

	if cmd.ProcessState != nil {
		exitCode := cmd.ProcessState.ExitCode()
		run.snapshot.ExitCode = &exitCode
		run.snapshot.TerminationSignal = actionProcessTerminationSignal(cmd.ProcessState)
	}

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		run.snapshot.Status = ActionRunStatusTimedOut
		run.snapshot.Error = "action exceeded the execution time limit"
	case run.cancelRequested || errors.Is(ctx.Err(), context.Canceled):
		run.snapshot.Status = ActionRunStatusCancelled
		run.snapshot.Error = "action was cancelled"
	case startErr == nil:
		run.snapshot.Status = ActionRunStatusSucceeded
	case cmd.ProcessState == nil:
		run.snapshot.Status = ActionRunStatusFailed
		run.snapshot.Error = sanitizeActionStartError(startErr)
	default:
		run.snapshot.Status = ActionRunStatusFailed
		run.snapshot.Error = "action exited unsuccessfully"
	}
}

func (r *Runner) pruneLocked() {
	if len(r.runs) < maxRetainedActionRuns {
		return
	}
	remove := len(r.runs) - maxRetainedActionRuns + 1
	kept := r.order[:0]
	for _, id := range r.order {
		run := r.runs[id]
		if remove > 0 && run != nil && run.snapshot.Status != ActionRunStatusRunning {
			delete(r.runs, id)
			remove--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

func snapshotLocked(run *actionRun) RunSnapshot {
	snapshot := run.snapshot
	snapshot.Output, snapshot.OutputTruncated, snapshot.OutputBytes = run.output.Snapshot()
	if snapshot.Status == ActionRunStatusRunning {
		snapshot.DurationMS = maxInt64(0, time.Now().UnixMilli()-snapshot.StartedAt)
	}
	return snapshot
}

func resolveActionEntrypoint(workspacePath, relativeEntrypoint string) (string, error) {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	if workspacePath == "" || !filepath.IsAbs(workspacePath) {
		return "", errors.New("canonical workspace path must be absolute")
	}
	workspaceReal, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return "", errors.New("canonical workspace is unavailable")
	}
	candidate := filepath.Join(workspaceReal, relativeEntrypoint)
	entrypointReal, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("action entrypoint does not exist")
		}
		return "", errors.New("action entrypoint cannot be resolved")
	}
	rel, err := filepath.Rel(workspaceReal, entrypointReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("action entrypoint escapes the canonical workspace")
	}
	info, err := os.Stat(entrypointReal)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("action entrypoint must be a regular file")
	}
	return entrypointReal, nil
}

func assembleActionArguments(action pebblestore.WorkspaceAction, supplied map[string]string) ([]string, error) {
	known := make(map[string]pebblestore.WorkspaceActionInput, len(action.Inputs))
	for _, input := range action.Inputs {
		known[input.ID] = input
	}
	for id := range supplied {
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("unknown action input %q", id)
		}
	}
	argv := append([]string(nil), action.Arguments...)
	for _, input := range action.Inputs {
		value, provided := supplied[input.ID]
		if !provided {
			value = input.Default
		}
		if input.Required && value == "" {
			return nil, fmt.Errorf("action input %q is required", input.ID)
		}
		if value == "" && !provided {
			continue
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("action input %q contains a null byte", input.ID)
		}
		argv = append(argv, input.Arguments...)
		argv = append(argv, value)
	}
	return argv, nil
}

func sanitizeActionStartError(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "action entrypoint is not executable"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "action entrypoint does not exist"
	}
	return "action could not be started"
}

func newRunID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "action_run_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("action_run_%d", time.Now().UnixNano())
}

type cappedOutput struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	total     int64
	truncated bool
}

func newCappedOutput(limit int) *cappedOutput {
	return &cappedOutput{limit: limit, data: make([]byte, 0, limit)}
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.total += int64(n)
	if n >= w.limit {
		w.data = append(w.data[:0], p[n-w.limit:]...)
		w.truncated = true
		return n, nil
	}
	if overflow := len(w.data) + n - w.limit; overflow > 0 {
		copy(w.data, w.data[overflow:])
		w.data = w.data[:len(w.data)-overflow]
		w.truncated = true
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (w *cappedOutput) Snapshot() (string, bool, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...)), w.truncated, w.total
}

var _ io.Writer = (*cappedOutput)(nil)

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
