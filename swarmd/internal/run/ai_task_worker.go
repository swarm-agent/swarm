package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	aiTaskRecoveryLimit = 100
	aiTaskMaxAttempts   = 3
)

type AITaskQueueTransition struct {
	Item                                                                                                           pebblestore.WorkspaceTodoItem
	ExpectedState, State, Mode                                                                                     string
	Worktree                                                                                                       bool
	ManagedSessionID, FinalRunID, PreparationSessionID, PreparationRunID, PreparationAttemptID, Error, Disposition string
}

type AITaskQueue interface {
	ListAITaskAccounts(limit int) ([]string, error)
	ListActiveAITasks(accountScopeID string, limit int) ([]pebblestore.WorkspaceTodoItem, error)
	GetAITask(accountScopeID, workspacePath, taskID string) (pebblestore.WorkspaceTodoItem, bool, error)
	TransitionAITask(AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error)
	AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record pebblestore.AITaskAuditRecord) error
}

type aiTaskWorker struct {
	service *Service
	queue   AITaskQueue
	apply   func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
	wake    chan pebblestore.WorkspaceTodoItem
	wg      sync.WaitGroup
	mu      sync.Mutex
	active  map[string]struct{}
	execute func(context.Context, AITaskQueue, pebblestore.WorkspaceTodoItem, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error
}

type AITaskWorker struct{ worker *aiTaskWorker }

func (w *AITaskWorker) Wake(item pebblestore.WorkspaceTodoItem) {
	if w == nil || w.worker == nil {
		return
	}
	select {
	case w.worker.wake <- item:
	default:
	}
}

func (w *AITaskWorker) Wait() {
	if w != nil && w.worker != nil {
		w.worker.wg.Wait()
	}
}

func (s *Service) StartAITaskWorker(ctx context.Context, queue AITaskQueue, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) *AITaskWorker {
	w := &aiTaskWorker{service: s, queue: queue, apply: apply, wake: make(chan pebblestore.WorkspaceTodoItem, 128), active: map[string]struct{}{}}
	w.wg.Add(1)
	go w.loop(ctx)
	return &AITaskWorker{worker: w}
}

func (w *aiTaskWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	w.recover(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-w.wake:
			w.dispatch(ctx, item)
		case <-ticker.C:
			w.recover(ctx)
		}
	}
}

func (w *aiTaskWorker) recover(ctx context.Context) {
	accounts, err := w.queue.ListAITaskAccounts(1000)
	if err != nil {
		return
	}
	for _, account := range accounts {
		items, listErr := w.queue.ListActiveAITasks(account, aiTaskRecoveryLimit)
		if listErr != nil {
			continue
		}
		for _, item := range items {
			w.dispatch(ctx, item)
		}
	}
}

func (w *aiTaskWorker) dispatch(ctx context.Context, item pebblestore.WorkspaceTodoItem) {
	key := item.AccountScopeID + "\x00" + item.ID
	w.mu.Lock()
	if _, exists := w.active[key]; exists {
		w.mu.Unlock()
		return
	}
	w.active[key] = struct{}{}
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { w.mu.Lock(); delete(w.active, key); w.mu.Unlock() }()
		_ = w.process(ctx, item)
	}()
}

func (w *aiTaskWorker) process(ctx context.Context, item pebblestore.WorkspaceTodoItem) error {
	if item.AIState == pebblestore.WorkspaceTodoAIStateQueued {
		attempt := int(item.AIStateVersion)
		if attempt > aiTaskMaxAttempts {
			return w.fail(item, "AI task exceeded bounded preparation attempts")
		}
		attemptID := fmt.Sprintf("ai-task-attempt:%s:%d", item.ID, attempt)
		prepSessionID := deterministicAITaskID(item.AccountScopeID, item.WorkspaceID, item.ID, "preparation-session")
		prepRunID := "ai-task-preparation-run:" + deterministicAITaskID(item.AccountScopeID, item.WorkspaceID, item.ID, fmt.Sprintf("attempt-%d", attempt))
		claimed, err := w.queue.TransitionAITask(AITaskQueueTransition{Item: item, ExpectedState: pebblestore.WorkspaceTodoAIStateQueued, State: pebblestore.WorkspaceTodoAIStatePreparing, PreparationSessionID: prepSessionID, PreparationRunID: prepRunID, PreparationAttemptID: attemptID, Disposition: "claimed"})
		if err != nil {
			return err
		}
		item = claimed
	} else if item.AIState == pebblestore.WorkspaceTodoAIStatePreparing {
		_ = w.audit(item, "recovery", "recovered", "")
	}
	if w.execute != nil {
		return w.execute(ctx, w.queue, item, w.apply)
	}
	return w.service.runClaimedAITask(ctx, w.queue, item, w.apply)
}

func (w *aiTaskWorker) audit(item pebblestore.WorkspaceTodoItem, stage, disposition, errorText string) error {
	return w.queue.AppendAITaskAudit(item.AccountScopeID, item.WorkspacePath, item.ID, pebblestore.AITaskAuditRecord{StageKey: fmt.Sprintf("%06d_%s", item.AIStateVersion, stage), Stage: stage, Disposition: disposition, Error: sanitizeAITaskError(errorText), CreatedAt: time.Now().UnixMilli()})
}

func (w *aiTaskWorker) fail(item pebblestore.WorkspaceTodoItem, message string) error {
	_, err := w.queue.TransitionAITask(AITaskQueueTransition{Item: item, ExpectedState: item.AIState, State: pebblestore.WorkspaceTodoAIStateFailed, Mode: item.AIMode, Worktree: item.AIWorktree, ManagedSessionID: item.ManagedSessionID, Error: sanitizeAITaskError(message), Disposition: "failed"})
	return err
}

func (s *Service) runClaimedAITask(ctx context.Context, queue AITaskQueue, task pebblestore.WorkspaceTodoItem, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if task.AIState != pebblestore.WorkspaceTodoAIStatePreparing {
		return fmt.Errorf("AI task must already be preparing")
	}
	if apply == nil {
		return errors.New("AI task requires canonical V3 mutation publisher")
	}
	profile, err := s.ResolveAITaskPreparer(task.AccountScopeID)
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
		prompt := strings.TrimSpace(task.AIRequest)
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

func terminalizeAITask(queue AITaskQueue, task pebblestore.WorkspaceTodoItem, err error) error {
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
