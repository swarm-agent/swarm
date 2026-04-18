package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarm/packages/swarmd/internal/privacy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	defaultPrincipalID = "local"

	ActionAllowOnce   = "allow_once"
	ActionDenyOnce    = "deny_once"
	ActionAllowAlways = "allow_always"
	ActionDenyAlways  = "deny_always"
	ActionCancel      = "cancel"

	DecisionApprove = ActionAllowOnce
	DecisionDeny    = ActionDenyOnce
	DecisionCancel  = ActionCancel
)

type Service struct {
	store                   *pebblestore.PermissionStore
	events                  *pebblestore.EventLog
	publish                 func(pebblestore.EventEnvelope)
	sessions                sessionLookup
	hosted                  HostedPermissionSync
	localSwarmIDResolver    func() string
	principalID             string
	bypassPermissions       bool
	retainToolOutputHistory bool

	mu         sync.Mutex
	waiters    map[string][]chan pebblestore.PermissionRecord
	counter    atomic.Uint64
	reconciled bool
}

type CreateInput struct {
	SessionID     string
	RunID         string
	CallID        string
	ToolName      string
	ToolArguments string
	Requirement   string
	Mode          string
}

type AuthorizationDecision string

const (
	AuthorizationApprove AuthorizationDecision = "approved"
	AuthorizationDeny    AuthorizationDecision = "denied"
	AuthorizationPending AuthorizationDecision = "pending"
)

type AuthorizationInput struct {
	SessionID     string
	RunID         string
	CallID        string
	ToolName      string
	ToolArguments string
	Mode          string
	Overlay       *Policy
}

type AuthorizationResult struct {
	Decision    AuthorizationDecision
	Requirement string
	Reason      string
	Source      string
	RulePreview string
	Record      *pebblestore.PermissionRecord
}

type sessionLookup interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
}

type HostedPermissionSync interface {
	CreatePending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input CreateInput) (pebblestore.PermissionRecord, error)
	WaitForResolution(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, permissionID string) (pebblestore.PermissionRecord, error)
	CancelRunPending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error)
	MarkToolStarted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error)
	MarkToolCompleted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error)
}

func NewService(store *pebblestore.PermissionStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{
		store:       store,
		events:      events,
		publish:     publish,
		principalID: defaultPrincipalID,
		waiters:     make(map[string][]chan pebblestore.PermissionRecord),
	}
}

func (s *Service) SetSessionResolver(resolver sessionLookup) {
	if s == nil {
		return
	}
	s.sessions = resolver
}

func (s *Service) SetHostedSync(sync HostedPermissionSync) {
	if s == nil {
		return
	}
	s.hosted = sync
}

func (s *Service) SetLocalSwarmIDResolver(resolver func() string) {
	if s == nil {
		return
	}
	s.localSwarmIDResolver = resolver
}

func (s *Service) SetBypassPermissions(enabled bool) {
	if s == nil {
		return
	}
	s.bypassPermissions = enabled
}

func (s *Service) BypassPermissions() bool {
	if s == nil {
		return false
	}
	return s.bypassPermissions
}

func (s *Service) SetRetainToolOutputHistory(enabled bool) {
	if s == nil {
		return
	}
	s.retainToolOutputHistory = enabled
}

func (s *Service) RetainToolOutputHistory() bool {
	if s == nil {
		return false
	}
	return s.retainToolOutputHistory
}

func (s *Service) ListPermissions(sessionID string, limit int) ([]pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return s.store.ListPermissions(sessionID, limit)
}

func (s *Service) ListPending(sessionID string, limit int) ([]pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return s.store.ListPendingPermissions(sessionID, limit)
}

func (s *Service) Summary(sessionID string) (pebblestore.PermissionSummary, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.PermissionSummary{}, errors.New("session id is required")
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshSummaryLocked(sessionID, now)
}

func (s *Service) ReconcilePendingRuns(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "daemon restarted"
	}

	s.mu.Lock()
	if s.reconciled {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	runWaits, err := s.store.ListRunWaits("", 100000)
	if err != nil {
		return err
	}
	for _, state := range runWaits {
		sessionID := strings.TrimSpace(state.SessionID)
		runID := strings.TrimSpace(state.RunID)
		if sessionID == "" || runID == "" {
			continue
		}
		if _, err := s.CancelRunPending(sessionID, runID, reason); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.reconciled = true
	s.mu.Unlock()
	return nil
}

func (s *Service) AuthorizeToolCall(input AuthorizationInput) (AuthorizationResult, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return AuthorizationResult{}, errors.New("session id is required")
	}

	requirement := authorizationRequirement(input.Mode, input.ToolName, input.ToolArguments)
	if requirement == "" {
		requirement = "tool"
	}

	effectiveMode := strings.TrimSpace(input.Mode)
	if s.BypassPermissions() {
		if effectiveMode == "" {
			effectiveMode = "plan"
		}
		effectiveMode += "+bypass_permissions"
	}
	input.Mode = effectiveMode

	explain, err := s.ExplainTool(effectiveMode, input.ToolName, input.ToolArguments, input.Overlay)
	if err != nil {
		return AuthorizationResult{}, err
	}

	result := AuthorizationResult{
		Requirement: requirement,
		Reason:      strings.TrimSpace(explain.Reason),
		Source:      strings.TrimSpace(explain.Source),
		RulePreview: strings.TrimSpace(explain.RulePreview),
	}

	switch explain.Decision {
	case PolicyDecisionAllow:
		result.Decision = AuthorizationApprove
		return result, nil
	case PolicyDecisionDeny:
		result.Decision = AuthorizationDeny
		return result, nil
	default:
		record, err := s.CreatePending(CreateInput{
			SessionID:     sessionID,
			RunID:         input.RunID,
			CallID:        input.CallID,
			ToolName:      input.ToolName,
			ToolArguments: input.ToolArguments,
			Requirement:   requirement,
			Mode:          input.Mode,
		})
		if err != nil {
			return AuthorizationResult{}, err
		}
		result.Decision = AuthorizationPending
		result.Record = &record
		return result, nil
	}
}

func (s *Service) CreatePending(input CreateInput) (pebblestore.PermissionRecord, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("session id is required")
	}
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return pebblestore.PermissionRecord{}, err
	} else if hosted {
		record, err := s.hosted.CreatePending(context.Background(), descriptor, input)
		if err != nil {
			return pebblestore.PermissionRecord{}, err
		}
		if err := s.storeMirroredPermission(record); err != nil {
			return pebblestore.PermissionRecord{}, err
		}
		return record, nil
	}
	runID := strings.TrimSpace(input.RunID)
	now := time.Now().UnixMilli()

	record := pebblestore.PermissionRecord{
		ID:                  s.newPermissionID(now, sessionID, runID, strings.TrimSpace(input.CallID)),
		SessionID:           sessionID,
		RunID:               runID,
		CallID:              strings.TrimSpace(input.CallID),
		ToolName:            strings.TrimSpace(input.ToolName),
		ToolArguments:       permissionStoredArguments(input.ToolArguments),
		Requirement:         strings.TrimSpace(strings.ToLower(input.Requirement)),
		Mode:                strings.TrimSpace(strings.ToLower(input.Mode)),
		Status:              pebblestore.PermissionStatusPending,
		Decision:            "",
		Reason:              "",
		PermissionRequested: now,
		ResolvedAt:          0,
		ExecutionStatus:     pebblestore.PermissionExecWaitingApproval,
		Output:              "",
		Error:               "",
		DurationMS:          0,
		StartedAt:           0,
		CompletedAt:         0,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if record.ToolName == "" {
		record.ToolName = "tool"
	}
	if record.Requirement == "" {
		record.Requirement = "tool"
	}
	if record.Mode == "" {
		record.Mode = "plan"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var previous *pebblestore.PermissionRecord
	if existing, ok, err := s.store.GetPermission(sessionID, record.ID); err != nil {
		return pebblestore.PermissionRecord{}, err
	} else if ok {
		previous = &existing
		if existing.CreatedAt > 0 {
			record.CreatedAt = existing.CreatedAt
		}
		if existing.PermissionRequested > 0 {
			record.PermissionRequested = existing.PermissionRequested
		}
	}

	if err := s.store.PutPermission(record, previous); err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if err := s.attachRunWaitLocked(record, now); err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	summary, err := s.refreshSummaryLocked(sessionID, now)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}

	_, _ = s.emitLocked("session:"+sessionID, "permission.requested", sessionID, map[string]any{
		"permission": record,
	})
	_, _ = s.emitLocked("user:"+s.principalID, "permission.summary.updated", sessionID, summary)
	return record, nil
}

func (s *Service) Resolve(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, error) {
	return s.ResolveWithArguments(sessionID, permissionID, action, reason, "")
}

func (s *Service) ResolveWithArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	permissionID = strings.TrimSpace(permissionID)
	if sessionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("session id is required")
	}
	if permissionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("permission id is required")
	}
	action, err := normalizeResolveAction(action)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		if actionIsAllow(action) {
			reason = "approved by user"
		} else if actionIsDeny(action) {
			reason = "denied by user"
		} else {
			reason = "cancelled"
		}
	}
	reasonKind, reasonChars := classifyPermissionReason(reason)
	permissionDebugf("resolve.request session=%s permission=%s action=%s reason_kind=%s reason_chars=%d approved_args_chars=%d approved_args_preview=%q", sessionID, permissionID, action, reasonKind, reasonChars, len(strings.TrimSpace(approvedArguments)), permissionDebugPreview(approvedArguments, 180))

	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	record, changed, err := s.resolveLocked(sessionID, permissionID, action, reason, approvedArguments, now)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if !changed {
		return record, nil
	}
	return record, nil
}

func (s *Service) ResolveAll(sessionID, action, reason string, limit int) ([]pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	action, err := normalizeResolveAction(action)
	if err != nil {
		return nil, err
	}
	if actionIsPersistent(action) {
		return nil, errors.New("resolve all does not support persistent permission actions")
	}
	if limit <= 0 {
		limit = 1000
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	pending, err := s.store.ListPendingPermissions(sessionID, limit)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	resolved := make([]pebblestore.PermissionRecord, 0, len(pending))
	for _, current := range pending {
		record, changed, err := s.resolveLocked(sessionID, current.ID, action, reason, "", now)
		if err != nil {
			return nil, err
		}
		if changed {
			resolved = append(resolved, record)
		}
	}
	return resolved, nil
}

func (s *Service) WaitForResolution(ctx context.Context, sessionID, permissionID string) (pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	permissionID = strings.TrimSpace(permissionID)
	if sessionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("session id is required")
	}
	if permissionID == "" {
		return pebblestore.PermissionRecord{}, errors.New("permission id is required")
	}
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return pebblestore.PermissionRecord{}, err
	} else if hosted {
		record, err := s.hosted.WaitForResolution(ctx, descriptor, sessionID, permissionID)
		if err != nil {
			return pebblestore.PermissionRecord{}, err
		}
		if err := s.storeMirroredPermission(record); err != nil {
			return pebblestore.PermissionRecord{}, err
		}
		return record, nil
	}

	for {
		record, ok, err := s.store.GetPermission(sessionID, permissionID)
		if err != nil {
			return pebblestore.PermissionRecord{}, err
		}
		if !ok {
			return pebblestore.PermissionRecord{}, fmt.Errorf("permission %q not found", permissionID)
		}
		if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
			return record, nil
		}

		ch := make(chan pebblestore.PermissionRecord, 1)
		key := waitKey(sessionID, permissionID)

		s.mu.Lock()
		current, ok, err := s.store.GetPermission(sessionID, permissionID)
		if err != nil {
			s.mu.Unlock()
			return pebblestore.PermissionRecord{}, err
		}
		if !ok {
			s.mu.Unlock()
			return pebblestore.PermissionRecord{}, fmt.Errorf("permission %q not found", permissionID)
		}
		if !strings.EqualFold(strings.TrimSpace(current.Status), pebblestore.PermissionStatusPending) {
			s.mu.Unlock()
			return current, nil
		}
		s.waiters[key] = append(s.waiters[key], ch)
		s.mu.Unlock()

		select {
		case updated := <-ch:
			return updated, nil
		case <-ctx.Done():
			s.removeWaiter(key, ch)
			return pebblestore.PermissionRecord{}, ctx.Err()
		}
	}
}

func (s *Service) CancelRunPending(sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return nil, nil
	}
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return nil, err
	} else if hosted {
		records, err := s.hosted.CancelRunPending(context.Background(), descriptor, sessionID, runID, reason)
		if err != nil {
			return nil, err
		}
		if err := s.storeMirroredPermissions(records); err != nil {
			return nil, err
		}
		return records, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "run terminated before permission resolution"
	}

	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.store.ListRunPermissions(sessionID, runID, 2000)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		_ = s.store.DeleteRunWait(sessionID, runID)
		return nil, nil
	}

	cancelled := make([]pebblestore.PermissionRecord, 0, len(records))
	for _, record := range records {
		if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
			continue
		}
		updated := record
		updated.Status = pebblestore.PermissionStatusCancelled
		updated.Decision = DecisionCancel
		updated.Reason = reason
		updated.UpdatedAt = now
		updated.ResolvedAt = now
		updated.CompletedAt = now
		updated.PermissionRequested = firstNonZero(updated.PermissionRequested, updated.CreatedAt)
		updated.ExecutionStatus = pebblestore.PermissionExecCancelled
		updated.Output = permissionResolutionSummary(updated.ToolName, updated.Status, updated.Reason)
		updated.Error = permissionResolutionError(updated.Status)
		updated.DurationMS = permissionDurationMS(updated)
		if err := s.store.PutPermission(updated, &record); err != nil {
			return nil, err
		}
		s.detachRunWaitLocked(updated, now)
		s.notifyWaitersLocked(updated)
		cancelled = append(cancelled, updated)
		_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{
			"permission": updated,
		})
	}
	_ = s.store.DeleteRunWait(sessionID, runID)

	if len(cancelled) > 0 {
		summary, err := s.refreshSummaryLocked(sessionID, now)
		if err != nil {
			return nil, err
		}
		_, _ = s.emitLocked("user:"+s.principalID, "permission.summary.updated", sessionID, summary)
	}

	return cancelled, nil
}

func (s *Service) resolveLocked(sessionID, permissionID, action, reason, approvedArguments string, now int64) (pebblestore.PermissionRecord, bool, error) {
	record, ok, err := s.store.GetPermission(sessionID, permissionID)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if !ok {
		return pebblestore.PermissionRecord{}, false, fmt.Errorf("permission %q not found", permissionID)
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		return record, false, nil
	}

	updated := record
	switch {
	case actionIsAllow(action):
		updated.Status = pebblestore.PermissionStatusApproved
	case actionIsDeny(action):
		updated.Status = pebblestore.PermissionStatusDenied
	default:
		updated.Status = pebblestore.PermissionStatusCancelled
	}
	updated.Decision = action
	updated.Reason = strings.TrimSpace(reason)
	updated.ApprovedArguments = sanitizeApprovedArguments(record.ToolName, action, approvedArguments, record.ToolArguments)
	updated.UpdatedAt = now
	updated.ResolvedAt = now
	updated.CompletedAt = now
	updated.PermissionRequested = firstNonZero(updated.PermissionRequested, updated.CreatedAt)
	updated.ExecutionStatus = resolutionExecutionStatus(updated.ExecutionStatus, action)
	updated.Output = permissionResolutionSummary(updated.ToolName, updated.Status, updated.Reason)
	updated.Error = permissionResolutionError(updated.Status)
	updated.DurationMS = permissionDurationMS(updated)
	reasonKind, reasonChars := classifyPermissionReason(updated.Reason)
	permissionDebugf("resolve.apply session=%s permission=%s run=%s call=%s tool=%s status=%s decision=%s reason_kind=%s reason_chars=%d approved_args_chars=%d approved_args_preview=%q", updated.SessionID, updated.ID, updated.RunID, updated.CallID, updated.ToolName, updated.Status, updated.Decision, reasonKind, reasonChars, len(strings.TrimSpace(updated.ApprovedArguments)), permissionDebugPreview(updated.ApprovedArguments, 180))

	if err := s.store.PutPermission(updated, &record); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	s.detachRunWaitLocked(updated, now)
	summary, err := s.refreshSummaryLocked(sessionID, now)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}

	s.notifyWaitersLocked(updated)
	_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{
		"permission": updated,
	})
	_, _ = s.emitLocked("user:"+s.principalID, "permission.summary.updated", sessionID, summary)
	return updated, true, nil
}

func (s *Service) attachRunWaitLocked(record pebblestore.PermissionRecord, now int64) error {
	runID := strings.TrimSpace(record.RunID)
	if runID == "" {
		return nil
	}
	state, ok, err := s.store.GetRunWait(record.SessionID, runID)
	if err != nil {
		return err
	}
	if !ok {
		state = pebblestore.RunWaitState{
			SessionID:            record.SessionID,
			RunID:                runID,
			PendingPermissionIDs: []string{record.ID},
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		return s.store.UpsertRunWait(state)
	}

	found := false
	for _, existing := range state.PendingPermissionIDs {
		if existing == record.ID {
			found = true
			break
		}
	}
	if !found {
		state.PendingPermissionIDs = append(state.PendingPermissionIDs, record.ID)
	}
	state.UpdatedAt = now
	return s.store.UpsertRunWait(state)
}

func (s *Service) detachRunWaitLocked(record pebblestore.PermissionRecord, now int64) {
	runID := strings.TrimSpace(record.RunID)
	if runID == "" {
		return
	}
	state, ok, err := s.store.GetRunWait(record.SessionID, runID)
	if err != nil || !ok {
		return
	}
	next := state.PendingPermissionIDs[:0]
	for _, id := range state.PendingPermissionIDs {
		if id == record.ID {
			continue
		}
		next = append(next, id)
	}
	state.PendingPermissionIDs = append([]string(nil), next...)
	state.UpdatedAt = now
	if len(state.PendingPermissionIDs) == 0 {
		_ = s.store.DeleteRunWait(record.SessionID, runID)
		return
	}
	_ = s.store.UpsertRunWait(state)
}

func (s *Service) refreshSummaryLocked(sessionID string, now int64) (pebblestore.PermissionSummary, error) {
	count, oldest, newest, err := s.store.CountPendingPermissions(sessionID)
	if err != nil {
		return pebblestore.PermissionSummary{}, err
	}
	summary := pebblestore.PermissionSummary{
		PrincipalID:     s.principalID,
		SessionID:       sessionID,
		PendingCount:    count,
		OldestPendingAt: oldest,
		NewestPendingAt: newest,
		UpdatedAt:       now,
	}
	if err := s.store.PutSummary(summary); err != nil {
		return pebblestore.PermissionSummary{}, err
	}
	return summary, nil
}

func (s *Service) emitLocked(streamID, eventType, entityID string, payload any) (*pebblestore.EventEnvelope, error) {
	if s.events == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	env, err := s.events.Append(streamID, eventType, entityID, raw, "", "")
	if err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish(env)
	}
	return &env, nil
}

func (s *Service) notifyWaitersLocked(record pebblestore.PermissionRecord) {
	key := waitKey(record.SessionID, record.ID)
	watchers := s.waiters[key]
	if len(watchers) == 0 {
		return
	}
	delete(s.waiters, key)
	for _, ch := range watchers {
		select {
		case ch <- record:
		default:
		}
		close(ch)
	}
}

func (s *Service) removeWaiter(key string, target chan pebblestore.PermissionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.waiters[key]
	if len(current) == 0 {
		return
	}
	filtered := current[:0]
	for _, existing := range current {
		if existing == target {
			continue
		}
		filtered = append(filtered, existing)
	}
	if len(filtered) == 0 {
		delete(s.waiters, key)
		return
	}
	s.waiters[key] = filtered
}

func (s *Service) MarkToolStarted(sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	callID = strings.TrimSpace(callID)
	if sessionID == "" || runID == "" || callID == "" {
		return pebblestore.PermissionRecord{}, false, nil
	}
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	} else if hosted {
		record, ok, err := s.hosted.MarkToolStarted(context.Background(), descriptor, sessionID, runID, callID, step, startedAt)
		if err != nil || !ok {
			return record, ok, err
		}
		if err := s.storeMirroredPermission(record); err != nil {
			return pebblestore.PermissionRecord{}, false, err
		}
		return record, true, nil
	}
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok, err := s.findByRunAndCallLocked(sessionID, runID, callID)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if !ok {
		return pebblestore.PermissionRecord{}, false, nil
	}
	previous := record
	if previous.Step == 0 && step > 0 {
		record.Step = step
	}
	record.PermissionRequested = firstNonZero(record.PermissionRequested, record.CreatedAt)
	record.StartedAt = startedAt
	record.ExecutionStatus = pebblestore.PermissionExecRunning
	record.UpdatedAt = startedAt
	record.Error = ""
	if err := s.store.PutPermission(record, &previous); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{
		"permission": record,
	})
	return record, true, nil
}

func (s *Service) MarkToolCompleted(sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	callID = strings.TrimSpace(callID)
	if sessionID == "" || runID == "" || callID == "" {
		return pebblestore.PermissionRecord{}, false, nil
	}
	if descriptor, hosted, err := s.hostedDescriptorForSession(sessionID); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	} else if hosted {
		record, ok, err := s.hosted.MarkToolCompleted(context.Background(), descriptor, sessionID, runID, callID, step, result, completedAt)
		if err != nil || !ok {
			return record, ok, err
		}
		if err := s.storeMirroredPermission(record); err != nil {
			return pebblestore.PermissionRecord{}, false, err
		}
		return record, true, nil
	}
	if completedAt <= 0 {
		completedAt = time.Now().UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok, err := s.findByRunAndCallLocked(sessionID, runID, callID)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if !ok {
		return pebblestore.PermissionRecord{}, false, nil
	}
	previous := record
	if previous.Step == 0 && step > 0 {
		record.Step = step
	}
	record.PermissionRequested = firstNonZero(record.PermissionRequested, record.CreatedAt)
	record.StartedAt = firstNonZero(record.StartedAt, completedAt)
	record.CompletedAt = completedAt
	record.UpdatedAt = completedAt
	record.DurationMS = result.DurationMS
	record.Output = permissionStoredOutput(result.Output, record.ToolName, s.RetainToolOutputHistory())
	record.Error = permissionStoredError(result.Error)
	if record.Error != "" {
		record.ExecutionStatus = pebblestore.PermissionExecFailed
	} else if record.ExecutionStatus != pebblestore.PermissionExecSkipped && record.ExecutionStatus != pebblestore.PermissionExecCancelled {
		record.ExecutionStatus = pebblestore.PermissionExecCompleted
	}
	if record.Status == "" {
		record.Status = pebblestore.PermissionStatusNotRequired
	}
	if err := s.store.PutPermission(record, &previous); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{
		"permission": record,
	})
	return record, true, nil
}

func (s *Service) findByRunAndCallLocked(sessionID, runID, callID string) (pebblestore.PermissionRecord, bool, error) {
	records, err := s.store.ListRunPermissions(sessionID, runID, 2000)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	for _, record := range records {
		if strings.TrimSpace(record.CallID) == callID {
			return record, true, nil
		}
	}
	all, err := s.store.ListPermissions(sessionID, 4000)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	for _, record := range all {
		if strings.TrimSpace(record.RunID) == runID && strings.TrimSpace(record.CallID) == callID {
			return record, true, nil
		}
	}
	return pebblestore.PermissionRecord{}, false, nil
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func resolutionExecutionStatus(current, action string) string {
	switch {
	case actionIsAllow(action):
		return pebblestore.PermissionExecQueued
	case actionIsDeny(action):
		return pebblestore.PermissionExecSkipped
	default:
		return pebblestore.PermissionExecCancelled
	}
}

func permissionResolutionSummary(toolName, status, reason string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Sprintf("%s %s", toolName, status)
	}
	return fmt.Sprintf("%s %s: %s", toolName, status, reason)
}

func permissionResolutionError(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case pebblestore.PermissionStatusDenied:
		return "permission denied"
	case pebblestore.PermissionStatusCancelled:
		return "permission cancelled"
	default:
		return ""
	}
}

func permissionDurationMS(record pebblestore.PermissionRecord) int64 {
	start := firstNonZero(record.PermissionRequested, record.CreatedAt)
	end := firstNonZero(record.CompletedAt, record.ResolvedAt, record.UpdatedAt)
	if start <= 0 || end <= start {
		return 0
	}
	return end - start
}

func waitKey(sessionID, permissionID string) string {
	return strings.TrimSpace(sessionID) + "/" + strings.TrimSpace(permissionID)
}

func (s *Service) newPermissionID(now int64, sessionID, runID, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID != "" {
		return fmt.Sprintf("perm_%s_%s_%s", sanitizePermissionIDPart(sessionID), sanitizePermissionIDPart(runID), sanitizePermissionIDPart(callID))
	}
	seq := s.counter.Add(1)
	return fmt.Sprintf("perm_%d_%06d", now, seq)
}

func sanitizePermissionIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "none"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "none"
	}
	return out
}

func authorizationRequirement(mode, toolName, toolArguments string) string {
	toolName = normalizePolicyToolName(toolName)
	switch toolName {
	case "task":
		return "task_launch"
	case "manage_skill":
		if changes, ok := manageSkillPermissionChangeCount(toolArguments); ok && changes > 0 {
			return "skill_change"
		}
		return "manage_skill"
	case "plan_manage":
		if ShouldApprovePlanManageUpdate(toolArguments) {
			return "plan_update"
		}
		return "plan_manage"
	case "manage_agent":
		if ShouldApproveManageAgentMutation(toolArguments) {
			return "agent_change"
		}
		return "manage_agent"
	case "manage_theme":
		if changes, ok := manageThemePermissionChangeCount(toolArguments); ok && changes > 0 {
			return "theme_change"
		}
		return "manage_theme"
	case "manage_worktree":
		return "manage_worktree"
	case "":
		return "tool"
	default:
		return toolName
	}
}

func ShouldApproveManageAgentMutation(toolArguments string) bool {
	action := manageAgentAction(toolArguments)
	switch action {
	case "create", "update", "delete", "remove", "create_custom_tool", "create-custom-tool", "update_custom_tool", "update-custom-tool", "delete_custom_tool", "delete-custom-tool", "remove_custom_tool", "remove-custom-tool", "assign_custom_tool", "assign-custom-tool", "unassign_custom_tool", "unassign-custom-tool":
		return true
	default:
		return false
	}
}

func manageAgentAction(toolArguments string) string {
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return "inspect"
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return "inspect"
	}
	action := strings.ToLower(strings.TrimSpace(mapStringAny(payload["action"])))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(mapStringAny(payload["op"])))
	}
	if action == "" {
		return "inspect"
	}
	return action
}

func ShouldApprovePlanManageUpdate(toolArguments string) bool {
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return false
	}
	action := strings.ToLower(strings.TrimSpace(mapStringAny(payload["action"])))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(mapStringAny(payload["op"])))
	}
	if action != "save" {
		return false
	}
	planID := strings.TrimSpace(mapStringAny(payload["plan_id"]))
	if planID == "" {
		planID = strings.TrimSpace(mapStringAny(payload["id"]))
	}
	if planID == "" {
		if updateType := strings.ToLower(strings.TrimSpace(mapStringAny(payload["update_type"]))); updateType != "" {
			return updateType == "existing_plan"
		}
		return false
	}
	return true
}

func mapStringAny(value any) string {
	text, _ := value.(string)
	return text
}

func manageSkillPermissionChangeCount(toolArguments string) (int, bool) {
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return 0, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return 0, false
	}
	change, ok := payload["change"]
	if !ok {
		return 0, false
	}
	switch typed := change.(type) {
	case map[string]any:
		return 1, true
	case []any:
		count := 0
		for _, item := range typed {
			if _, ok := item.(map[string]any); ok {
				count++
			}
		}
		return count, true
	default:
		return 0, false
	}
}

func manageThemePermissionChangeCount(toolArguments string) (int, bool) {
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return 0, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return 0, false
	}
	change, ok := payload["change"]
	if !ok {
		return 0, false
	}
	switch typed := change.(type) {
	case map[string]any:
		return 1, true
	case []any:
		count := 0
		for _, item := range typed {
			if _, ok := item.(map[string]any); ok {
				count++
			}
		}
		return count, true
	default:
		return 0, false
	}
}

func manageAgentPermissionChangeCount(toolArguments string) (int, bool) {
	if ShouldApproveManageAgentMutation(toolArguments) {
		return 1, true
	}
	return 0, true
}

func normalizeResolveAction(action string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", ActionAllowOnce, "approve", "allow", "yes":
		return ActionAllowOnce, nil
	case ActionDenyOnce, "deny", "reject", "no":
		return ActionDenyOnce, nil
	case ActionAllowAlways, "approve_always", "always_allow":
		return ActionAllowAlways, nil
	case ActionDenyAlways, "always_deny":
		return ActionDenyAlways, nil
	case ActionCancel:
		return ActionCancel, nil
	default:
		return "", fmt.Errorf("unsupported decision %q", action)
	}
}

func actionIsAllow(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionAllowOnce, ActionAllowAlways:
		return true
	default:
		return false
	}
}

func actionIsDeny(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionDenyOnce, ActionDenyAlways:
		return true
	default:
		return false
	}
}

func actionIsPersistent(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionAllowAlways, ActionDenyAlways:
		return true
	default:
		return false
	}
}

func classifyPermissionReason(reason string) (kind string, chars int) {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "empty", 0
	}
	chars = len(trimmed)
	switch strings.ToLower(trimmed) {
	case "approved by user", "approved", "allow", "allowed":
		return "default_approved", chars
	case "denied by user", "denied", "deny", "rejected", "reject":
		return "default_denied", chars
	case "cancelled", "canceled":
		return "default_cancelled", chars
	default:
		return "custom", chars
	}
}

func sanitizeApprovedArguments(toolName, action, approvedArguments, fallbackToolArguments string) string {
	approvedArguments = strings.TrimSpace(approvedArguments)
	if approvedArguments == "" {
		approvedArguments = approvedArgumentsFromToolArguments(toolName, fallbackToolArguments)
	}
	if approvedArguments == "" {
		return ""
	}
	if !actionIsAllow(action) {
		return ""
	}
	return privacy.SanitizeJSONText(approvedArguments)
}

func approvedArgumentsFromToolArguments(toolName, toolArguments string) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return ""
	}
	approved, ok := payload["approved_arguments"].(map[string]any)
	if !ok || len(approved) == 0 {
		return ""
	}
	raw, err := json.Marshal(approved)
	if err != nil {
		return ""
	}
	return string(raw)
}

func permissionDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_PERMISSION_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func permissionDebugf(format string, args ...any) {
	if !permissionDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.permission] "+format+"\n", args...)
}

func permissionDebugPreview(text string, max int) string {
	text = strings.TrimSpace(privacy.SanitizeJSONText(text))
	if text == "" {
		return ""
	}
	if max <= 0 {
		max = 160
	}
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func permissionStoredArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	sanitized := privacy.SanitizeJSONText(trimmed)
	if strings.TrimSpace(sanitized) == "" {
		return "{}"
	}
	return sanitized
}

func permissionStoredOutput(raw, toolName string, retainToolOutputHistory bool) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if retainToolOutputHistory {
		sanitized := privacy.SanitizeJSONText(trimmed)
		if strings.TrimSpace(sanitized) != "" {
			return sanitized
		}
		return privacy.SanitizeText(trimmed)
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("%s executed; detailed output omitted for privacy", toolName)
}

func permissionStoredError(raw string) string {
	return privacy.SanitizeText(raw)
}

func (s *Service) hostedDescriptorForSession(sessionID string) (sessionruntime.HostedSessionDescriptor, bool, error) {
	if s == nil || s.hosted == nil || s.sessions == nil {
		return sessionruntime.HostedSessionDescriptor{}, false, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionruntime.HostedSessionDescriptor{}, false, err
	}
	if !ok {
		return sessionruntime.HostedSessionDescriptor{}, false, nil
	}
	localSwarmID := ""
	if s.localSwarmIDResolver != nil {
		localSwarmID = strings.TrimSpace(s.localSwarmIDResolver())
	}
	descriptor, hosted := sessionruntime.HostedSessionFromMetadataForLocal(session.Metadata, localSwarmID)
	return descriptor, hosted, nil
}

func (s *Service) storeMirroredPermissions(records []pebblestore.PermissionRecord) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if err := s.storeMirroredPermissionLocked(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) storeMirroredPermission(record pebblestore.PermissionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeMirroredPermissionLocked(record)
}

func (s *Service) storeMirroredPermissionLocked(record pebblestore.PermissionRecord) error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.SessionID) == "" {
		return errors.New("mirrored permission record is missing required ids")
	}
	previous, ok, err := s.store.GetPermission(record.SessionID, record.ID)
	if err != nil {
		return err
	}
	var previousPtr *pebblestore.PermissionRecord
	if ok {
		previousPtr = &previous
	}
	if err := s.store.PutPermission(record, previousPtr); err != nil {
		return err
	}
	now := firstNonZero(record.UpdatedAt, record.ResolvedAt, record.CompletedAt, record.PermissionRequested, record.CreatedAt, time.Now().UnixMilli())
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		return s.attachRunWaitLocked(record, now)
	}
	s.detachRunWaitLocked(record, now)
	s.notifyWaitersLocked(record)
	return nil
}
