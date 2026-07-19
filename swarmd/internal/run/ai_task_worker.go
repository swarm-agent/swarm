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
	aiTaskMessagePageLimit  = 500
	aiTaskQueueCapacity     = 128
	aiTaskWorkerCount       = 1
	aiTaskOriginContextOpen = "----- BEGIN DURABLE ORIGIN CONVERSATION -----"
	aiTaskOriginContextEnd  = "----- END DURABLE ORIGIN CONVERSATION -----"
	aiTaskRequestOpen       = "----- BEGIN TASK INSTRUCTION -----"
	aiTaskRequestEnd        = "----- END TASK INSTRUCTION -----"
)

type AITaskQueueTransition struct {
	Item                                                                                                                                 pebblestore.WorkspaceTodoItem
	ExpectedState, State, Mode                                                                                                           string
	Worktree                                                                                                                             bool
	ManagedSessionID, DisplayTitle, FinalRunID, Result, PreparationSessionID, PreparationRunID, PreparationAttemptID, Error, Disposition string
}

// AITaskLifecycleWriter is the durable write/read-model side of task execution.
// Implementations persist lifecycle and audit records, but never schedule work.
type AITaskLifecycleWriter interface {
	TransitionAITask(AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error)
	AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record pebblestore.AITaskAuditRecord) error
}

// AITaskJob is the immutable, complete trusted payload accepted by the request
// path and dispatched only through the in-memory queue.
type AITaskJob struct {
	Task pebblestore.WorkspaceTodoItem
}

func NewAITaskJob(item pebblestore.WorkspaceTodoItem) (AITaskJob, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.AccountScopeID = strings.TrimSpace(item.AccountScopeID)
	item.UserID = strings.TrimSpace(item.UserID)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.WorkspacePath = strings.TrimSpace(item.WorkspacePath)
	item.AIRequest = strings.TrimSpace(item.AIRequest)
	item.Tags = append([]string(nil), item.Tags...)
	if item.ID == "" || item.AccountScopeID == "" || item.UserID == "" || item.WorkspaceID == "" || item.WorkspacePath == "" || item.AIRequest == "" {
		return AITaskJob{}, errors.New("AI task job requires complete trusted task payload")
	}
	if item.AIState != pebblestore.WorkspaceTodoAIStateQueued {
		return AITaskJob{}, fmt.Errorf("AI task job state %q is not queued", item.AIState)
	}
	return AITaskJob{Task: item}, nil
}

// AITaskDispatcher is the always-on in-memory scheduling authority. Accepted
// immutable jobs enter its bounded channel directly; Pebble never wakes workers.
type AITaskDispatcher struct {
	service   *Service
	lifecycle AITaskLifecycleWriter
	apply     func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
	jobs      chan AITaskJob
	done      chan struct{}
	wg        sync.WaitGroup
	mu        sync.Mutex
	inflight  map[string]struct{}
	closed    bool
}

func (s *Service) StartAITaskDispatcher(ctx context.Context, lifecycle AITaskLifecycleWriter, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) *AITaskDispatcher {
	d := &AITaskDispatcher{
		service: s, lifecycle: lifecycle, apply: apply,
		jobs: make(chan AITaskJob, aiTaskQueueCapacity), done: make(chan struct{}),
		inflight: make(map[string]struct{}),
	}
	for i := 0; i < aiTaskWorkerCount; i++ {
		d.wg.Add(1)
		go d.worker(ctx)
	}
	return d
}

// Enqueue accepts a complete task payload without reading durable state. It is
// deliberately non-blocking so queue saturation is reported to the caller.
func (d *AITaskDispatcher) Enqueue(item pebblestore.WorkspaceTodoItem) bool {
	job, err := NewAITaskJob(item)
	if err != nil {
		return false
	}
	return d.EnqueueJob(job)
}

func (d *AITaskDispatcher) EnqueueJob(job AITaskJob) bool {
	if d == nil {
		return false
	}
	key := aiTaskJobKey(job.Task)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return false
	}
	if _, exists := d.inflight[key]; exists {
		d.mu.Unlock()
		return true
	}
	d.inflight[key] = struct{}{}
	select {
	case d.jobs <- job:
		d.mu.Unlock()
		return true
	default:
		delete(d.inflight, key)
		d.mu.Unlock()
		return false
	}
}

// Close stops admission and workers, then waits for deterministic worker exit.
// It is idempotent and does not silently accept jobs after shutdown.
func (d *AITaskDispatcher) Close() {
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

func (d *AITaskDispatcher) Wait() {
	if d != nil {
		d.wg.Wait()
	}
}

func (d *AITaskDispatcher) worker(ctx context.Context) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case job := <-d.jobs:
			if ctx.Err() != nil {
				d.release(job)
				return
			}
			d.process(ctx, job)
			d.release(job)
		}
	}
}

func (d *AITaskDispatcher) release(job AITaskJob) {
	d.mu.Lock()
	delete(d.inflight, aiTaskJobKey(job.Task))
	d.mu.Unlock()
}

func (d *AITaskDispatcher) process(ctx context.Context, job AITaskJob) {
	item := job.Task
	defer func() {
		if recovered := recover(); recovered != nil {
			d.recoverPanickedJob(item, recovered)
		}
	}()
	attemptID := fmt.Sprintf("ai-task-attempt:%s:%d", item.ID, item.AIStateVersion)
	prepSessionID := deterministicAITaskID(item.AccountScopeID, item.WorkspaceID, item.ID, "preparation-session")
	prepRunID := "ai-task-preparation-run:" + deterministicAITaskID(item.AccountScopeID, item.WorkspaceID, item.ID, fmt.Sprintf("attempt-%d", item.AIStateVersion))
	claimed, err := d.lifecycle.TransitionAITask(AITaskQueueTransition{Item: item, ExpectedState: pebblestore.WorkspaceTodoAIStateQueued, State: pebblestore.WorkspaceTodoAIStatePreparing, PreparationSessionID: prepSessionID, PreparationRunID: prepRunID, PreparationAttemptID: attemptID, Disposition: "claimed"})
	if err != nil {
		log.Printf("AI task %s claim failed: %v", item.ID, err)
		return
	}
	item = claimed
	if err := d.service.runClaimedAITask(ctx, d.lifecycle, item, d.apply); err != nil && ctx.Err() == nil {
		log.Printf("AI task %s execution failed: %v", item.ID, err)
	}
}

// recoverPanickedJob contains task-local failures at the dispatcher goroutine
// boundary. Without this guard, a panic anywhere in preparation or deployment
// terminates the daemon and interrupts every unrelated active session.
func (d *AITaskDispatcher) recoverPanickedJob(item pebblestore.WorkspaceTodoItem, recovered any) {
	message := fmt.Sprintf("AI task worker panic: %v", recovered)
	log.Printf("AI task %s panic contained: %s stack=%s", item.ID, message, strings.TrimSpace(string(debug.Stack())))

	// Lifecycle persistence is best effort after a panic. Guard it separately so
	// a panicking storage adapter cannot escape the dispatcher recovery boundary.
	func() {
		defer func() {
			if nested := recover(); nested != nil {
				log.Printf("AI task %s panic recovery failed: %v", item.ID, nested)
			}
		}()
		if d == nil || d.lifecycle == nil {
			return
		}
		if err := terminalizeAITask(d.lifecycle, item, errors.New(message)); err != nil {
			log.Printf("AI task %s panic terminalization failed: %v", item.ID, err)
		}
	}()
}

func aiTaskJobKey(item pebblestore.WorkspaceTodoItem) string {
	return strings.TrimSpace(item.AccountScopeID) + "\x00" + strings.TrimSpace(item.ID)
}

func (s *Service) runClaimedAITask(ctx context.Context, queue AITaskLifecycleWriter, task pebblestore.WorkspaceTodoItem, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if task.AIState != pebblestore.WorkspaceTodoAIStatePreparing {
		return fmt.Errorf("AI task must already be preparing")
	}
	if apply == nil {
		return terminalizeAITask(queue, task, errors.New("AI task requires canonical V3 mutation publisher"))
	}
	profile, err := s.ResolveAITaskPreparer(task.AccountScopeID)
	if err != nil {
		return terminalizeAITask(queue, task, err)
	}
	prompt, err := s.buildAITaskPreparationPrompt(task)
	if err != nil {
		return terminalizeAITask(queue, task, err)
	}

	preference := pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.AutoServiceTier}
	if _, err = s.model.ResolvePreference(preference); err != nil {
		return terminalizeAITask(queue, task, fmt.Errorf("resolve configured Swarm auto preference: %w", err))
	}
	_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_model_resolved", task.AIStateVersion), Stage: "model_resolved", Disposition: "resolved", Provider: preference.Provider, Model: preference.Model, Thinking: preference.Thinking, ServiceTier: preference.ServiceTier, CreatedAt: time.Now().UnixMilli()})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: task.UserID, AccountScopeID: task.AccountScopeID, SessionID: task.PreparationSessionID, AccountScopeSource: identity.AccountScopeSourceSession}
	metadata := map[string]any{"source": "ai_task_preparation", "navigation_hidden": true, "system_session": true, "lineage_kind": "ai_task_preparation", "ai_task_id": task.ID, "ai_task_preparation_session_id": task.PreparationSessionID, "ai_task_preparation_run_id": task.PreparationRunID, "ai_task_preparation_attempt_id": task.PreparationAttemptID}
	if originSessionID := strings.TrimSpace(task.OriginSessionID); originSessionID != "" {
		metadata["ai_task_origin_session_id"] = originSessionID
	}
	canonical, err := s.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: principal, WorkspacePath: task.WorkspacePath, AgentProfile: profile, RuntimeMode: pebblestore.AgentRuntimeModeRead, Metadata: metadata})
	if err != nil {
		return terminalizeAITask(queue, task, fmt.Errorf("canonicalize preparation session: %w", err))
	}
	for k, v := range metadata {
		canonical.Metadata[k] = v
	}
	now := time.Now().UnixMilli()
	prep := pebblestore.SessionSnapshot{ID: task.PreparationSessionID, UserID: task.UserID, AccountScopeID: task.AccountScopeID, WorkspacePath: canonical.RuntimeWorkspacePath, WorkspaceName: canonical.SourceWorkspaceName, Title: "AI task preparation", Mode: sessionruntime.ModeAuto, Preference: preference, Metadata: canonical.Metadata, CreatedAt: now, UpdatedAt: now}
	createKey := "ai-task:preparation:create:" + task.ID
	created, err := apply(sessionruntime.SessionMutationInput{SessionID: prep.ID, UserID: task.UserID, AccountScopeID: task.AccountScopeID, ClientRequestID: createKey, IdempotencyKey: createKey, PayloadHash: createKey, RequestHash: createKey, Kind: sessionruntime.SessionMutationCreateSession, Session: &prep, NowUnixMs: now})
	if err != nil {
		return terminalizeAITask(queue, task, err)
	}
	_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_preparation_parent", task.AIStateVersion), Stage: "preparation_parent", Disposition: map[bool]string{true: "reused", false: "created"}[created.Replayed], CreatedAt: now})

	messages, err := s.sessions.ListSessionMessages(task.PreparationSessionID, 0, 1000)
	if err != nil {
		return terminalizeAITask(queue, task, err)
	}
	var raw string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			raw = messages[i].Content
			break
		}
	}
	if raw == "" {
		intent := pebblestore.V3SessionRunIntent{SessionID: prep.ID, UserID: task.UserID, AccountScopeID: task.AccountScopeID, RunID: task.PreparationRunID, Status: sessionruntime.RunIntentPendingExecutor, RunSessionID: prep.ID, ParentSessionID: prep.ID}
		message := pebblestore.MessageSnapshot{ID: deterministicAITaskID(task.ID, task.PreparationRunID, "prompt", "message"), SessionID: prep.ID, UserID: task.UserID, AccountScopeID: task.AccountScopeID, Role: "user", Content: prompt, Metadata: map[string]any{"source": "ai_task_preparation"}, CreatedAt: now}
		appendKey := "ai-task:preparation:prompt:" + task.PreparationRunID
		if _, err = apply(sessionruntime.SessionMutationInput{SessionID: prep.ID, UserID: task.UserID, AccountScopeID: task.AccountScopeID, ClientRequestID: appendKey, IdempotencyKey: appendKey, PayloadHash: appendKey, RequestHash: appendKey, Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, RunIntent: &intent, NowUnixMs: now}); err != nil {
			return terminalizeAITask(queue, task, err)
		}
		_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_preparation_run_intent", task.AIStateVersion), Stage: "preparation_run_intent", Disposition: "created_or_reused", CreatedAt: now})
		result, runErr := s.RunTurnWithOptions(ctx, prep.ID, RunOptions{Prompt: prompt, AgentName: profile.Name, Instructions: profile.Prompt, TrustedAgentProfile: &profile, AllowSubagent: true, RunID: task.PreparationRunID, TargetKind: RunTargetKindSubagent, TargetName: profile.Name, Background: true, ExecutionContext: &RunExecutionContext{WorkspacePath: task.WorkspacePath, CWD: task.WorkspacePath, WorktreeMode: RunWorktreeModeOff}, Principal: principal, ApplySessionMutation: apply, SkipInitialUserMessage: true})
		if runErr != nil {
			return terminalizeAITask(queue, task, fmt.Errorf("preparer run: %w", runErr))
		}
		raw = result.AssistantMessage.Content
		_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_provider_completed", task.AIStateVersion), Stage: "provider_completed", Disposition: "completed", CreatedAt: time.Now().UnixMilli()})
	} else {
		_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_provider_completed", task.AIStateVersion), Stage: "provider_completed", Disposition: "reused", CreatedAt: time.Now().UnixMilli()})
	}
	preparation, err := ParseAITaskPreparation(raw)
	if err != nil {
		return terminalizeAITask(queue, task, fmt.Errorf("parse preparation: %w", err))
	}
	_ = queue.AppendAITaskAudit(task.AccountScopeID, task.WorkspacePath, task.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_parse_success", task.AIStateVersion), Stage: "parse_success", Disposition: "parsed", CreatedAt: time.Now().UnixMilli()})
	_, err = s.ExecutePreparedAITask(ctx, task.PreparationSessionID, task.AccountScopeID, task.WorkspacePath, task.ID, preparation, apply)
	return err
}

func (s *Service) buildAITaskPreparationPrompt(task pebblestore.WorkspaceTodoItem) (string, error) {
	request := strings.TrimSpace(task.AIRequest)
	originSessionID := strings.TrimSpace(task.OriginSessionID)
	if originSessionID == "" {
		return request, nil
	}

	origin, ok, err := s.sessions.GetSession(originSessionID)
	if err != nil {
		return "", fmt.Errorf("load AI task origin session: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("AI task origin session %q not found", originSessionID)
	}
	if strings.TrimSpace(origin.AccountScopeID) != strings.TrimSpace(task.AccountScopeID) || strings.TrimSpace(origin.UserID) != strings.TrimSpace(task.UserID) || strings.TrimSpace(origin.WorkspacePath) != strings.TrimSpace(task.WorkspacePath) {
		return "", fmt.Errorf("AI task origin session is not authorized for this task")
	}

	messages, err := s.listAllAITaskOriginMessages(originSessionID)
	if err != nil {
		return "", fmt.Errorf("load AI task origin conversation: %w", err)
	}
	return formatAITaskPreparationPrompt(messages, request), nil
}

func (s *Service) listAllAITaskOriginMessages(sessionID string) ([]pebblestore.MessageSnapshot, error) {
	var all []pebblestore.MessageSnapshot
	var afterSeq uint64
	for {
		page, err := s.sessions.ListSessionMessages(sessionID, afterSeq, aiTaskMessagePageLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < aiTaskMessagePageLimit {
			return all, nil
		}
		nextSeq := page[len(page)-1].GlobalSeq
		if nextSeq <= afterSeq {
			return nil, fmt.Errorf("origin conversation pagination did not advance")
		}
		afterSeq = nextSeq
	}
}

func formatAITaskPreparationPrompt(messages []pebblestore.MessageSnapshot, request string) string {
	var b strings.Builder
	b.WriteString(aiTaskOriginContextOpen)
	for _, message := range messages {
		b.WriteString("\n\n[")
		b.WriteString(strings.TrimSpace(message.Role))
		b.WriteString("]\n")
		b.WriteString(message.Content)
	}
	b.WriteString("\n\n")
	b.WriteString(aiTaskOriginContextEnd)
	b.WriteString("\n\n")
	b.WriteString(aiTaskRequestOpen)
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(request))
	b.WriteString("\n")
	b.WriteString(aiTaskRequestEnd)
	return b.String()
}

func terminalizeAITask(queue AITaskLifecycleWriter, task pebblestore.WorkspaceTodoItem, err error) error {
	message := sanitizeAITaskError(err.Error())
	_, transitionErr := queue.TransitionAITask(AITaskQueueTransition{Item: task, ExpectedState: task.AIState, State: pebblestore.WorkspaceTodoAIStateFailed, Mode: task.AIMode, Worktree: task.AIWorktree, ManagedSessionID: task.ManagedSessionID, Error: message, Disposition: "failed"})
	if transitionErr != nil {
		return fmt.Errorf("%v; terminalize: %w", err, transitionErr)
	}
	return err
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
