package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	aiTaskV2RecoveryLimit = 10000
	aiTaskV2RetryLimit    = 5
	aiTaskV2RetryBase     = 500 * time.Millisecond
	aiTaskV2RetryMax      = 8 * time.Second
)

// AITaskV2Transition is the complete durable state change requested by the V2
// executor. The store owns compare-and-swap and synchronous persistence.
type AITaskV2Transition struct {
	Item                                                                                                                                               pebblestore.WorkspaceTodoItem
	ExpectedState, State, Mode                                                                                                                         string
	Worktree                                                                                                                                           bool
	ManagedSessionID, DisplayTitle, WorktreeName, FinalRunID, Result, PreparationSessionID, PreparationRunID, PreparationAttemptID, Error, Disposition string
	RetryCount                                                                                                                                         uint32
	NextAttemptAt                                                                                                                                      int64
}

// AITaskV2Store is the durable half of the queue. Recovery is read exactly once
// during construction; normal dispatch never reads, polls, or scans Pebble.
type AITaskV2Store interface {
	TransitionAITaskV2(AITaskV2Transition) (pebblestore.WorkspaceTodoItem, error)
	AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record pebblestore.AITaskAuditRecord) error
	LoadAITaskV2RecoveryQueue(limit int) ([]pebblestore.AITaskV2QueueRecord, error)
	DeleteAITaskV2QueueRecord(key string) error
}

type AITaskV2Job struct {
	Task        pebblestore.WorkspaceTodoItem
	RecoveryKey string
}

func newAITaskV2Job(item pebblestore.WorkspaceTodoItem, recoveryKey string) (AITaskV2Job, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.AccountScopeID = strings.TrimSpace(item.AccountScopeID)
	item.UserID = strings.TrimSpace(item.UserID)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.WorkspacePath = strings.TrimSpace(item.WorkspacePath)
	item.Tags = append([]string(nil), item.Tags...)
	if item.ID == "" || item.AccountScopeID == "" || item.UserID == "" || item.WorkspaceID == "" || item.WorkspacePath == "" || strings.TrimSpace(item.AIRequest) == "" {
		return AITaskV2Job{}, errors.New("AI task V2 job requires complete trusted task payload")
	}
	if item.AIState != pebblestore.WorkspaceTodoAIStateQueued && item.AIState != pebblestore.WorkspaceTodoAIStatePreparing {
		return AITaskV2Job{}, fmt.Errorf("AI task V2 job state %q is not recoverable", item.AIState)
	}
	return AITaskV2Job{Task: item, RecoveryKey: strings.TrimSpace(recoveryKey)}, nil
}

// AITaskV2Dispatcher is a bounded-lifetime, unbounded FIFO protected by one
// mutex. A capacity-one wake channel coalesces notifications without polling.
// Queue admission copies a task that the request path already committed with a
// durable recovery record in the same synchronous Pebble batch.
type AITaskV2Dispatcher struct {
	service *Service
	store   AITaskV2Store
	apply   func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)

	mu       sync.Mutex
	jobs     []AITaskV2Job
	inflight map[string]struct{}
	closed   bool
	wake     chan struct{}
	done     chan struct{}
	wg       sync.WaitGroup
}

func (s *Service) StartAITaskV2Dispatcher(ctx context.Context, store AITaskV2Store, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (*AITaskV2Dispatcher, error) {
	if s == nil || store == nil || apply == nil {
		return nil, errors.New("AI task V2 dispatcher requires service, durable store, and canonical V3 mutation publisher")
	}
	recovery, err := store.LoadAITaskV2RecoveryQueue(aiTaskV2RecoveryLimit)
	if err != nil {
		return nil, fmt.Errorf("load AI task V2 recovery queue: %w", err)
	}
	d := &AITaskV2Dispatcher{
		service:  s,
		store:    store,
		apply:    apply,
		inflight: make(map[string]struct{}),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	for _, record := range recovery {
		job, jobErr := newAITaskV2Job(record.Task, record.Key)
		if jobErr != nil {
			if deleteErr := store.DeleteAITaskV2QueueRecord(record.Key); deleteErr != nil {
				return nil, fmt.Errorf("discard invalid AI task V2 recovery record: %w", deleteErr)
			}
			continue
		}
		d.enqueueLocked(job)
	}
	d.wg.Add(1)
	go d.run(ctx)
	d.signal()
	return d, nil
}

func (d *AITaskV2Dispatcher) Enqueue(item pebblestore.WorkspaceTodoItem) bool {
	job, err := newAITaskV2Job(item, "")
	if err != nil || d == nil {
		return false
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return false
	}
	d.enqueueLocked(job)
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *AITaskV2Dispatcher) enqueueLocked(job AITaskV2Job) {
	key := aiTaskV2JobKey(job.Task)
	if _, exists := d.inflight[key]; exists {
		return
	}
	d.inflight[key] = struct{}{}
	d.jobs = append(d.jobs, job)
}

func (d *AITaskV2Dispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *AITaskV2Dispatcher) pop() (AITaskV2Job, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.jobs) == 0 {
		return AITaskV2Job{}, false
	}
	job := d.jobs[0]
	d.jobs[0] = AITaskV2Job{}
	d.jobs = d.jobs[1:]
	return job, true
}

func (d *AITaskV2Dispatcher) release(job AITaskV2Job) {
	d.releaseItem(job.Task)
}

func (d *AITaskV2Dispatcher) releaseItem(item pebblestore.WorkspaceTodoItem) {
	d.mu.Lock()
	delete(d.inflight, aiTaskV2JobKey(item))
	d.mu.Unlock()
}

func (d *AITaskV2Dispatcher) run(ctx context.Context) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-d.wake:
			for {
				job, ok := d.pop()
				if !ok {
					break
				}
				if d.executeSafely(ctx, job) {
					d.release(job)
				}
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}

func (d *AITaskV2Dispatcher) executeSafely(ctx context.Context, job AITaskV2Job) (releaseNow bool) {
	releaseNow = true
	item := job.Task
	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Errorf("AI task V2 panic: %v", recovered)
			log.Printf("AI task V2 %s panic contained: %v stack=%s", item.ID, recovered, strings.TrimSpace(string(debug.Stack())))
			_ = terminalizeAITaskV2(d.store, item, message)
		}
	}()

	if wait := time.Until(time.UnixMilli(item.AINextAttemptAt)); item.AINextAttemptAt > 0 && wait > 0 {
		d.scheduleRetry(ctx, item, wait)
		return false
	}
	if item.AIState == pebblestore.WorkspaceTodoAIStateQueued {
		attemptID := fmt.Sprintf("ai-task-v2-attempt:%s:%d", item.ID, item.AIStateVersion)
		claimed, err := d.store.TransitionAITaskV2(AITaskV2Transition{
			Item: item, ExpectedState: pebblestore.WorkspaceTodoAIStateQueued, State: pebblestore.WorkspaceTodoAIStatePreparing,
			PreparationAttemptID: attemptID, Disposition: "v2_claimed",
		})
		if err != nil {
			log.Printf("AI task V2 %s claim failed: %v", item.ID, err)
			return
		}
		item = claimed
	}
	updated, err := d.service.executeAITaskV2(ctx, d.store, item, d.apply)
	if updated.ID != "" {
		item = updated
	}
	if err != nil && ctx.Err() == nil {
		if isNonRetryableAITaskV2Error(err) || item.AIRetryCount >= aiTaskV2RetryLimit {
			_ = terminalizeAITaskV2(d.store, item, err)
			log.Printf("AI task V2 %s terminal failure: %v", item.ID, err)
			return
		}
		delay := aiTaskV2RetryDelay(item.AIRetryCount + 1)
		next := time.Now().Add(delay).UnixMilli()
		requeued, transitionErr := d.store.TransitionAITaskV2(AITaskV2Transition{
			Item: item, ExpectedState: pebblestore.WorkspaceTodoAIStatePreparing, State: pebblestore.WorkspaceTodoAIStatePreparing,
			Mode: item.AIMode, Worktree: item.AIWorktree, WorktreeName: item.AIWorktreeName,
			ManagedSessionID: item.ManagedSessionID, DisplayTitle: item.AIDisplayTitle, FinalRunID: item.FinalRunID,
			RetryCount: item.AIRetryCount + 1, NextAttemptAt: next, Error: sanitizeAITaskError(err.Error()), Disposition: "v2_retryable",
		})
		if transitionErr != nil {
			log.Printf("AI task V2 %s retry persistence failed: %v", item.ID, transitionErr)
			return
		}
		d.scheduleRetry(ctx, requeued, delay)
		releaseNow = false
		log.Printf("AI task V2 %s retry %d scheduled after %s: %v", item.ID, requeued.AIRetryCount, delay, err)
	}
	return releaseNow
}

func (d *AITaskV2Dispatcher) scheduleRetry(ctx context.Context, item pebblestore.WorkspaceTodoItem, delay time.Duration) {
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-timer.C:
			d.releaseItem(item)
			_ = d.Enqueue(item)
		}
	}()
}

func (s *Service) executeAITaskV2(ctx context.Context, store AITaskV2Store, task pebblestore.WorkspaceTodoItem, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (pebblestore.WorkspaceTodoItem, error) {
	if task.AIState != pebblestore.WorkspaceTodoAIStatePreparing {
		return task, permanentAITaskV2Error(fmt.Errorf("AI task V2 must be durably preparing"))
	}
	if apply == nil || s == nil || s.sessions == nil {
		return task, permanentAITaskV2Error(errors.New("AI task V2 canonical deployment services are not configured"))
	}
	parentID := strings.TrimSpace(task.OriginSessionID)
	if parentID == "" {
		return task, permanentAITaskV2Error(errors.New("AI task V2 requires an authorized origin session for canonical deployment"))
	}
	parent, ok, err := s.sessions.GetSession(parentID)
	if err != nil {
		return task, fmt.Errorf("load AI task V2 deployment parent: %w", err)
	}
	if !ok || parent.UserID != task.UserID || parent.AccountScopeID != task.AccountScopeID {
		return task, permanentAITaskV2Error(errors.New("AI task V2 deployment parent ownership is invalid"))
	}

	preparation := AITaskPreparation{Title: task.AIDisplayTitle, WorktreeName: task.AIWorktreeName}
	if preparation.Title == "" || preparation.WorktreeName == "" {
		basePreference := parent.Preference
		if strings.TrimSpace(basePreference.Provider) == "" && s.agents != nil {
			if state, stateErr := s.agents.ListStateForAccount(task.AccountScopeID, 2000); stateErr == nil {
				for _, profile := range state.Profiles {
					if strings.EqualFold(profile.Name, state.ActivePrimary) {
						basePreference = pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.AutoServiceTier}
						break
					}
				}
			}
		}
		principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: task.UserID, AccountScopeID: task.AccountScopeID, SessionID: parentID, AccountScopeSource: identity.AccountScopeSourceSession}
		preparation, err = s.PrepareAITaskMetadata(ctx, task.ID, task.AIRequest, basePreference, principal)
		if err != nil {
			return task, err
		}
		prepared, persistErr := store.TransitionAITaskV2(AITaskV2Transition{
			Item: task, ExpectedState: pebblestore.WorkspaceTodoAIStatePreparing, State: pebblestore.WorkspaceTodoAIStatePreparing,
			Mode: sessionruntime.ModeAuto, Worktree: true, WorktreeName: preparation.WorktreeName, DisplayTitle: preparation.Title,
			RetryCount: task.AIRetryCount, Disposition: "v2_metadata_prepared",
		})
		if persistErr != nil {
			return task, fmt.Errorf("persist AI task Compact metadata: %w", persistErr)
		}
		task = prepared
	}
	if _, err := s.ExecutePreparedAITask(ctx, parentID, task.AccountScopeID, task.WorkspacePath, task.ID, task.AIRequest, preparation, apply); err != nil {
		return task, err
	}
	return task, nil
}

type aiTaskV2PermanentError struct{ error }

func permanentAITaskV2Error(err error) error { return aiTaskV2PermanentError{error: err} }

func isNonRetryableAITaskV2Error(err error) bool {
	var permanent aiTaskV2PermanentError
	return errors.As(err, &permanent)
}

func aiTaskV2RetryDelay(attempt uint32) time.Duration {
	delay := aiTaskV2RetryBase
	for n := uint32(1); n < attempt && delay < aiTaskV2RetryMax; n++ {
		delay *= 2
	}
	if delay > aiTaskV2RetryMax {
		return aiTaskV2RetryMax
	}
	return delay
}

func terminalizeAITaskV2(store AITaskV2Store, task pebblestore.WorkspaceTodoItem, cause error) error {
	message := sanitizeAITaskError(cause.Error())
	_, transitionErr := store.TransitionAITaskV2(AITaskV2Transition{
		Item: task, ExpectedState: task.AIState, State: pebblestore.WorkspaceTodoAIStateFailed,
		Mode: task.AIMode, Worktree: task.AIWorktree, WorktreeName: task.AIWorktreeName, ManagedSessionID: task.ManagedSessionID,
		DisplayTitle: task.AIDisplayTitle, FinalRunID: task.FinalRunID, RetryCount: task.AIRetryCount,
		Error: message, Disposition: "v2_failed",
	})
	if transitionErr != nil {
		return fmt.Errorf("%v; terminalize AI task V2: %w", cause, transitionErr)
	}
	return cause
}

func aiTaskV2JobKey(item pebblestore.WorkspaceTodoItem) string {
	return strings.TrimSpace(item.AccountScopeID) + "\x00" + strings.TrimSpace(item.ID)
}

func deterministicAITaskID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	raw := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:32])
}

func sanitizeAITaskError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

func (d *AITaskV2Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.done)
	}
	d.mu.Unlock()
	d.wg.Wait()
}
