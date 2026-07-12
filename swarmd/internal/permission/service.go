package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarm/packages/swarmd/internal/notification"
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
	store                            *pebblestore.PermissionStore
	events                           *pebblestore.EventLog
	publish                          func(pebblestore.EventEnvelope)
	summaryRealtimePublish           func(sessionID string, summary pebblestore.PermissionSummary) error
	permissionRealtimePublish        func(sessionID string, record pebblestore.PermissionRecord) error
	sessions                         sessionLookup
	followupCheckpointPolicyResolver func(accountScopeID string) (string, error)
	hosted                           HostedPermissionSync
	notifications                    *notification.Service
	localSwarmIDResolver             func() string
	principalID                      string
	bypassPermissions                bool
	retainToolOutputHistory          bool

	mu                   sync.Mutex
	waiters              map[string][]chan pebblestore.PermissionRecord
	permissionStateCache map[string]permissionStateCacheEntry
	counter              atomic.Uint64
	reconciled           bool
}

type permissionStateCacheEntry struct {
	AccountScopeID    string
	Policy            Policy
	BypassPermissions bool
	LoadedAt          int64
	PolicyUpdatedAt   int64
	BypassUpdatedAt   int64
}

type CreateInput struct {
	SessionID         string
	RunID             string
	Step              int
	CallID            string
	ToolName          string
	ToolArguments     string
	ToolCallArguments string
	Requirement       string
	Mode              string
}

type AuthorizationDecision string

const (
	AuthorizationApprove AuthorizationDecision = "approved"
	AuthorizationDeny    AuthorizationDecision = "denied"
	AuthorizationPending AuthorizationDecision = "pending"
)

type AuthorizationInput struct {
	SessionID           string
	AccountScopeID      string
	RunID               string
	Step                int
	CallID              string
	ToolName            string
	ToolArguments       string
	ToolCallArguments   string
	Mode                string
	Overlay             *Policy
	SubagentReservation *SubagentReservationResult
}

type AuthorizationResult struct {
	Decision    AuthorizationDecision
	Requirement string
	Reason      string
	Source      string
	RulePreview string
	Record      *pebblestore.PermissionRecord
	Reservation *pebblestore.SubagentWaveReservation
}

type sessionLookup interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
}

type sessionPlanLookup interface {
	GetPlan(sessionID, planID string) (pebblestore.SessionPlanSnapshot, bool, error)
	GetActivePlan(sessionID string) (pebblestore.SessionPlanSnapshot, bool, error)
}

type ResolveInput struct {
	SessionID         string
	PermissionID      string
	Action            string
	Reason            string
	ApprovedArguments string
}

type ResolveResult struct {
	Record    pebblestore.PermissionRecord
	SavedRule *PolicyRule
}

type PendingPlanProposalEditInput struct {
	SessionID        string
	PermissionID     string
	ExpectedRevision int64
	Document         *pebblestore.SessionPlanDocument
}

type PendingPlanProposalEditResult struct {
	Record           pebblestore.PermissionRecord
	ProposalRevision int64
}

type HostedPermissionSync interface {
	CreatePending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input CreateInput) (pebblestore.PermissionRecord, error)
	WaitForResolution(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, permissionID string) (pebblestore.PermissionRecord, error)
	Resolve(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input ResolveInput) (ResolveResult, error)
	CancelRunPending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error)
	MarkToolStarted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error)
	MarkToolCompleted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error)
}

func NewService(store *pebblestore.PermissionStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{
		store:                store,
		events:               events,
		publish:              publish,
		principalID:          defaultPrincipalID,
		waiters:              make(map[string][]chan pebblestore.PermissionRecord),
		permissionStateCache: make(map[string]permissionStateCacheEntry),
	}
}

func (s *Service) SetSessionResolver(resolver sessionLookup) {
	if s == nil {
		return
	}
	s.sessions = resolver
}

func (s *Service) SetFollowupCheckpointPolicyResolver(resolver func(accountScopeID string) (string, error)) {
	if s == nil {
		return
	}
	s.followupCheckpointPolicyResolver = resolver
}

func (s *Service) SetPermissionRealtimePublisher(publish func(sessionID string, record pebblestore.PermissionRecord) error) {
	if s == nil {
		return
	}
	s.permissionRealtimePublish = publish
}

func (s *Service) SetSummaryRealtimePublisher(publish func(sessionID string, summary pebblestore.PermissionSummary) error) {
	if s == nil {
		return
	}
	s.summaryRealtimePublish = publish
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

func (s *Service) SetNotificationService(notifications *notification.Service) {
	if s == nil {
		return
	}
	s.notifications = notifications
}

func (s *Service) SetBypassPermissions(enabled bool) {
	if s == nil {
		return
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	s.bypassPermissions = enabled
	s.invalidatePermissionStateCacheLocked("")
	for accountScopeID, entry := range s.permissionStateCache {
		entry.BypassPermissions = enabled
		entry.BypassUpdatedAt = now
		entry.LoadedAt = now
		s.permissionStateCache[accountScopeID] = entry
	}
	s.mu.Unlock()
}

func (s *Service) BypassPermissions() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadPermissionStateLocked("")
	if err != nil {
		return s.bypassPermissions
	}
	return state.BypassPermissions
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

func (s *Service) ListPendingSummaries(accountScopeID, principalID string, limit int) ([]pebblestore.PermissionSummary, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	principalID = strings.TrimSpace(principalID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if principalID == "" {
		return nil, errors.New("principal id is required")
	}
	return s.store.ListPendingSummaries(accountScopeID, principalID, limit)
}

func (s *Service) RepairSummaryPendingIndex(accountScopeID, principalID string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	principalID = strings.TrimSpace(principalID)
	if s == nil || s.store == nil {
		return errors.New("permission service is not configured")
	}
	stats, err := s.store.ListPendingPermissionSessionStats(1000000)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.ClearSummaryPendingIndex(accountScopeID, principalID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, stat := range stats {
		summary, err := s.summaryForSessionStatsLocked(stat.SessionID, stat.PendingCount, stat.OldestPendingAt, stat.NewestPendingAt, now)
		if err != nil {
			return err
		}
		if accountScopeID != "" && strings.TrimSpace(summary.AccountScopeID) != accountScopeID {
			continue
		}
		if principalID != "" && strings.TrimSpace(summary.PrincipalID) != principalID {
			continue
		}
		if summary.PendingCount <= 0 || strings.TrimSpace(summary.SessionID) == "" {
			continue
		}
		if err := s.store.PutSummary(summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) PendingCount(sessionID string) (int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, errors.New("session id is required")
	}
	count, _, _, err := s.store.CountPendingPermissions(sessionID)
	if err != nil {
		return 0, err
	}
	return count, nil
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

	state, err := s.CurrentPermissionStateForAccount(input.AccountScopeID)
	if err != nil {
		return AuthorizationResult{}, err
	}
	// The reserved Plan sidechat's only mutating capability edits the already-
	// pending parent proposal. It must execute inline so the parent approval card,
	// rather than a second generic permission prompt, remains the decision surface.
	if normalizePolicyToolName(input.ToolName) == "edit_pending_plan" && s.sessions != nil {
		session, found, lookupErr := s.sessions.GetSession(sessionID)
		if lookupErr != nil {
			return AuthorizationResult{}, lookupErr
		}
		if found && strings.EqualFold(mapStringAny(session.Metadata["system_sidechat_kind"]), "plan") && strings.EqualFold(mapStringAny(session.Metadata["lineage_kind"]), "system_sidechat") {
			return AuthorizationResult{Decision: AuthorizationApprove, Requirement: requirement, Reason: "reserved Plan sidechat edits remain pending for parent decision", Source: "system_sidechat"}, nil
		}
	}
	effectiveMode := strings.TrimSpace(input.Mode)
	// Explicit task denial remains restrictive. Evaluate it without the generic
	// bypass flag, then apply orchestration limits before any bypass/allow path.
	if input.SubagentReservation != nil {
		explicit := ExplainPolicy(effectiveMode, input.ToolName, input.ToolArguments, state.Policy)
		if explicit.Decision == PolicyDecisionDeny {
			return AuthorizationResult{Decision: AuthorizationDeny, Requirement: requirement, Reason: explicit.Reason, Source: explicit.Source, RulePreview: explicit.RulePreview}, nil
		}
	}
	// Session deployment always asks before generic bypass or persisted allow rules.
	if requirement == "session_deploy" {
		input.Mode = effectiveMode
		return s.createPendingAuthorization(input, sessionID, requirement, "session deployment always requires fresh user approval", "builtin", "ask exact session deployment manifest")
	}
	// Delegation safeguards are resolved before generic permission bypass.
	if input.SubagentReservation != nil {
		reservation := input.SubagentReservation
		switch reservation.Decision {
		case SubagentReservationDeny:
			return AuthorizationResult{Decision: AuthorizationDeny, Requirement: requirement, Reason: reservation.Reason, Source: "subagent_orchestration", Reservation: &reservation.Reservation}, nil
		case SubagentReservationAsk:
			return s.createPendingAuthorization(input, sessionID, requirement, reservation.Reason, "subagent_orchestration", "ask exact subagent wave")
		case SubagentReservationApprove:
			return AuthorizationResult{Decision: AuthorizationApprove, Requirement: requirement, Reason: reservation.Reason, Source: "subagent_orchestration", Reservation: &reservation.Reservation}, nil
		}
	}
	if state.BypassPermissions {
		result := AuthorizationResult{
			Decision:    AuthorizationApprove,
			Requirement: requirement,
			Reason:      "permissions are bypassed",
			Source:      "bypass_permissions",
		}
		return result, nil
	}
	input.Mode = effectiveMode

	explain, err := s.ExplainToolForAccount(input.AccountScopeID, effectiveMode, input.ToolName, input.ToolArguments, input.Overlay)
	if err != nil {
		return AuthorizationResult{}, err
	}

	result := AuthorizationResult{
		Requirement: requirement,
		Reason:      strings.TrimSpace(explain.Reason),
		Source:      strings.TrimSpace(explain.Source),
		RulePreview: strings.TrimSpace(explain.RulePreview),
	}

	if explain.Decision != PolicyDecisionDeny {
		if dynamic, handled, err := s.authorizeDynamicToolAction(input, sessionID, requirement); handled || err != nil {
			return dynamic, err
		}
	}

	switch explain.Decision {
	case PolicyDecisionAllow:
		result.Decision = AuthorizationApprove
		return result, nil
	case PolicyDecisionDeny:
		result.Decision = AuthorizationDeny
		return result, nil
	default:
		return s.createPendingAuthorization(input, sessionID, requirement, strings.TrimSpace(explain.Reason), strings.TrimSpace(explain.Source), strings.TrimSpace(explain.RulePreview))
	}
}

func (s *Service) createPendingAuthorization(input AuthorizationInput, sessionID, requirement, reason, source, rulePreview string) (AuthorizationResult, error) {
	record, err := s.CreatePending(CreateInput{
		SessionID:         sessionID,
		RunID:             input.RunID,
		Step:              input.Step,
		CallID:            input.CallID,
		ToolName:          input.ToolName,
		ToolArguments:     input.ToolArguments,
		ToolCallArguments: input.ToolCallArguments,
		Requirement:       requirement,
		Mode:              input.Mode,
	})
	if err != nil {
		return AuthorizationResult{}, err
	}
	result := AuthorizationResult{Decision: AuthorizationPending, Requirement: requirement, Reason: reason, Source: source, RulePreview: rulePreview, Record: &record}
	return result, nil
}

func (s *Service) authorizeDynamicToolAction(input AuthorizationInput, sessionID, requirement string) (AuthorizationResult, bool, error) {
	if normalizePolicyToolName(input.ToolName) != "plan_manage" {
		return AuthorizationResult{}, false, nil
	}
	args := parsePermissionJSONMap(input.ToolArguments)
	action := normalizePlanManageAction(mapStringAny(args["action"]), mapStringAny(args["op"]), args)
	switch action {
	case "request_followup_checkpoint":
		approvalRequired, policyEffective, err := s.planFollowupCheckpointApprovalRequired(sessionID, input.AccountScopeID, args)
		if err != nil {
			return AuthorizationResult{}, true, err
		}
		if !approvalRequired {
			return AuthorizationResult{Decision: AuthorizationApprove, Requirement: requirement, Reason: "resolved session checkpoint policy allows auto-add and start", Source: "dynamic_action_policy"}, true, nil
		}
		reason := "resolved session checkpoint policy requires approval"
		if policyEffective != "" {
			reason = fmt.Sprintf("resolved session checkpoint policy %q requires approval", policyEffective)
		}
		result, err := s.createPendingAuthorization(input, sessionID, requirement, reason, "dynamic_action_policy", "ask plan session checkpoint request")
		return result, true, err
	case "request_plan_revision", "amend_plan", "request_new_plan":
		result, err := s.createPendingAuthorization(input, sessionID, requirement, "typed plan lifecycle request requires approval", "dynamic_action_policy", "ask plan lifecycle request")
		return result, true, err
	default:
		return AuthorizationResult{}, false, nil
	}
}

func (s *Service) planFollowupCheckpointApprovalRequired(sessionID, accountScopeID string, args map[string]any) (bool, string, error) {
	if s == nil || s.sessions == nil {
		return true, sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, nil
	}
	plans, ok := s.sessions.(sessionPlanLookup)
	if !ok {
		return true, sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, nil
	}
	planID := strings.TrimSpace(firstNonEmpty(mapStringAny(args["plan_id"]), mapStringAny(args["planID"]), mapStringAny(args["id"])))
	var plan pebblestore.SessionPlanSnapshot
	var found bool
	var err error
	if planID != "" {
		plan, found, err = plans.GetPlan(sessionID, planID)
	} else {
		plan, found, err = plans.GetActivePlan(sessionID)
	}
	if err != nil {
		return false, "", err
	}
	if !found {
		return true, sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, nil
	}
	globalDefault := ""
	if s.followupCheckpointPolicyResolver != nil {
		account := strings.TrimSpace(plan.AccountScopeID)
		if account == "" {
			account = strings.TrimSpace(accountScopeID)
		}
		if account == "" {
			if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
				return false, "", err
			} else if ok {
				account = strings.TrimSpace(session.AccountScopeID)
			}
		}
		resolved, err := s.followupCheckpointPolicyResolver(account)
		if err != nil {
			return false, "", err
		}
		globalDefault = strings.TrimSpace(resolved)
	}
	policy := sessionruntime.ResolvePlanFollowupCheckpointPolicy(plan.Document, globalDefault)
	return policy == sessionruntime.PlanFollowupCheckpointPolicyRequireApproval, policy, nil
}

func parsePermissionJSONMap(raw string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err != nil || payload == nil {
		return map[string]any{}
	}
	return payload
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
		s.syncNotification(record, descriptor.HostSwarmID, strings.TrimSpace(descriptor.ChildSwarmID), "permission.requested")
		return record, nil
	}
	runID := strings.TrimSpace(input.RunID)
	now := time.Now().UnixMilli()

	record := pebblestore.PermissionRecord{
		ID:                  s.newPermissionID(now, sessionID, runID, strings.TrimSpace(input.CallID)),
		SessionID:           sessionID,
		RunID:               runID,
		Step:                input.Step,
		CallID:              strings.TrimSpace(input.CallID),
		ToolName:            strings.TrimSpace(input.ToolName),
		ToolArguments:       permissionStoredArguments(input.ToolArguments),
		ToolCallArguments:   permissionStoredArguments(input.ToolCallArguments),
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
	if isPendingPlanProposalRecord(record) {
		record.ProposalRevision = 1
		payload := parsePermissionJSONMap(record.ToolArguments)
		planID := strings.TrimSpace(firstNonEmptyPermissionString(stringValue(payload["plan_id"]), stringValue(payload["id"])))
		if planID == "" {
			planID = "plan_" + record.ID
		}
		payload["plan_id"] = planID
		payload["proposal_revision"] = record.ProposalRevision
		if document, ok := payload["document"].(map[string]any); ok {
			document["id"] = planID
			payload["document"] = document
		}
		if approved, ok := payload["approved_arguments"].(map[string]any); ok {
			approved["plan_id"] = planID
			if document, ok := approved["document"].(map[string]any); ok {
				document["id"] = planID
				approved["document"] = document
			}
			payload["approved_arguments"] = approved
			if rawApproved, err := json.Marshal(approved); err != nil {
				return pebblestore.PermissionRecord{}, fmt.Errorf("encode pending plan approved arguments: %w", err)
			} else {
				record.ApprovedArguments = permissionStoredArguments(string(rawApproved))
			}
		}
		if rawPayload, err := json.Marshal(payload); err != nil {
			return pebblestore.PermissionRecord{}, fmt.Errorf("encode pending plan proposal metadata: %w", err)
		} else {
			record.ToolArguments = permissionStoredArguments(string(rawPayload))
		}
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

	summary, err := s.summaryForMutationLocked(sessionID, previous, record, now)
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if err := s.store.PutPermissionWithSummary(record, previous, summary); err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	if err := s.attachRunWaitLocked(record, now); err != nil {
		return pebblestore.PermissionRecord{}, err
	}

	s.syncNotification(record, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.requested")
	_, _ = s.emitLocked("session:"+sessionID, "permission.requested", sessionID, map[string]any{
		"permission": record,
	})
	s.publishPermissionSummaryUpdatedLocked(sessionID, summary)
	return record, nil
}

func (s *Service) EditPendingPlanProposal(input PendingPlanProposalEditInput) (PendingPlanProposalEditResult, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	permissionID := strings.TrimSpace(input.PermissionID)
	if sessionID == "" || permissionID == "" {
		return PendingPlanProposalEditResult{}, errors.New("session id and permission id are required")
	}
	if input.ExpectedRevision <= 0 {
		return PendingPlanProposalEditResult{}, errors.New("expected proposal revision must be positive")
	}
	if input.Document == nil {
		return PendingPlanProposalEditResult{}, errors.New("structured plan document is required")
	}

	s.mu.Lock()
	record, ok, err := s.store.GetPermission(sessionID, permissionID)
	if err != nil {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, err
	}
	if !ok {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, fmt.Errorf("permission %q not found", permissionID)
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) || !isPendingPlanProposalRecord(record) {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, errors.New("permission is not a recognized pending plan proposal")
	}
	currentRevision := record.ProposalRevision
	if currentRevision <= 0 {
		currentRevision = 1
	}
	if input.ExpectedRevision != currentRevision {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, fmt.Errorf("pending plan proposal revision conflict: expected %d, current revision is %d", input.ExpectedRevision, currentRevision)
	}

	payload := parsePermissionJSONMap(record.ToolArguments)
	approved, ok := payload["approved_arguments"].(map[string]any)
	if !ok {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, errors.New("pending plan proposal has no canonical approved arguments")
	}
	planID := strings.TrimSpace(firstNonEmptyPermissionString(stringValue(approved["plan_id"]), stringValue(payload["plan_id"]), input.Document.ID))
	title := strings.TrimSpace(firstNonEmptyPermissionString(input.Document.Title, stringValue(approved["title"]), stringValue(payload["title"])))
	canonical, err := sessionruntime.NormalizePlanDocumentForSave(planID, title, input.Document, nil)
	if err != nil {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, fmt.Errorf("canonicalize pending plan proposal: %w", err)
	}
	approved["document"] = canonical
	approved["plan_id"] = canonical.ID
	approved["title"] = canonical.Title
	if display := strings.TrimSpace(firstNonEmptyPermissionString(canonical.DisplayText, canonical.RenderedText)); display != "" {
		approved["plan"] = display
	} else {
		delete(approved, "plan")
	}
	payload["approved_arguments"] = approved
	payload["document"] = canonical
	payload["plan_id"] = canonical.ID
	payload["title"] = canonical.Title
	if plan, ok := approved["plan"]; ok {
		payload["plan"] = plan
	} else {
		delete(payload, "plan")
	}
	payload["proposal_revision"] = currentRevision + 1
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, err
	}
	rawApproved, err := json.Marshal(approved)
	if err != nil {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, err
	}
	updated := record
	updated.ToolArguments = permissionStoredArguments(string(rawPayload))
	updated.ApprovedArguments = permissionStoredArguments(string(rawApproved))
	updated.ProposalRevision = currentRevision + 1
	updated.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutPermission(updated, &record); err != nil {
		s.mu.Unlock()
		return PendingPlanProposalEditResult{}, err
	}
	// Deliberately do not notify resolution waiters or detach the run wait: this
	// mutation changes only the still-pending proposal.
	s.syncNotification(updated, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.updated")
	_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{"permission": updated})
	publisher := s.permissionRealtimePublish
	s.mu.Unlock()
	if publisher != nil {
		if err := publisher(sessionID, updated); err != nil {
			return PendingPlanProposalEditResult{}, err
		}
	}
	return PendingPlanProposalEditResult{Record: updated, ProposalRevision: updated.ProposalRevision}, nil
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

	current, found, loadErr := s.store.GetPermission(sessionID, permissionID)
	if loadErr != nil {
		return pebblestore.PermissionRecord{}, loadErr
	}
	if found && authorizationRequirement(current.Mode, current.ToolName, current.ToolArguments) == "session_deploy" && action == ActionAllowAlways {
		return pebblestore.PermissionRecord{}, errors.New("session deployment does not support persistent allow")
	}
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
		for _, record := range records {
			s.syncNotification(record, descriptor.HostSwarmID, strings.TrimSpace(descriptor.ChildSwarmID), "permission.updated")
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
		summary, err := s.summaryForMutationLocked(sessionID, &record, updated, now)
		if err != nil {
			return nil, err
		}
		if err := s.store.PutPermissionWithSummary(updated, &record, summary); err != nil {
			return nil, err
		}
		s.detachRunWaitLocked(updated, now)
		s.notifyWaitersLocked(updated)
		cancelled = append(cancelled, updated)
		s.syncNotification(updated, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.updated")
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
		s.publishPermissionSummaryUpdatedLocked(sessionID, summary)
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
	updated.ApprovedArguments, err = approvedArgumentsForResolution(record, action, approvedArguments)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
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

	summary, err := s.summaryForMutationLocked(sessionID, &record, updated, now)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if err := s.store.PutPermissionWithSummary(updated, &record, summary); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	s.detachRunWaitLocked(updated, now)

	s.notifyWaitersLocked(updated)
	s.syncNotification(updated, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.updated")
	_, _ = s.emitLocked("session:"+sessionID, "permission.updated", sessionID, map[string]any{
		"permission": updated,
	})
	s.publishPermissionSummaryUpdatedLocked(sessionID, summary)
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
	summary, err := s.summaryForSessionStatsLocked(sessionID, count, oldest, newest, now)
	if err != nil {
		return pebblestore.PermissionSummary{}, err
	}
	if err := s.store.PutSummary(summary); err != nil {
		return pebblestore.PermissionSummary{}, err
	}
	return summary, nil
}

func (s *Service) summaryForMutationLocked(sessionID string, previous *pebblestore.PermissionRecord, next pebblestore.PermissionRecord, now int64) (pebblestore.PermissionSummary, error) {
	pending, err := s.store.ListPendingPermissions(sessionID, 1000000)
	if err != nil {
		return pebblestore.PermissionSummary{}, err
	}
	count := 0
	oldest := int64(0)
	newest := int64(0)
	add := func(record pebblestore.PermissionRecord) {
		if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
			return
		}
		createdAt := firstNonZero(record.CreatedAt, record.PermissionRequested)
		count++
		if createdAt > 0 && (oldest == 0 || createdAt < oldest) {
			oldest = createdAt
		}
		if createdAt > newest {
			newest = createdAt
		}
	}
	previousID := ""
	if previous != nil {
		previousID = strings.TrimSpace(previous.ID)
	}
	for _, record := range pending {
		if previousID != "" && strings.TrimSpace(record.ID) == previousID {
			continue
		}
		add(record)
	}
	add(next)
	return s.summaryForSessionStatsLocked(sessionID, count, oldest, newest, now)
}

func (s *Service) summaryForSessionStatsLocked(sessionID string, count int, oldest, newest, now int64) (pebblestore.PermissionSummary, error) {
	accountScopeID := ""
	principalID := strings.TrimSpace(s.principalID)
	if s.sessions != nil {
		if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
			return pebblestore.PermissionSummary{}, err
		} else if ok {
			accountScopeID = strings.TrimSpace(session.AccountScopeID)
			if sessionUserID := strings.TrimSpace(session.UserID); sessionUserID != "" {
				principalID = sessionUserID
			}
		}
	}
	return pebblestore.PermissionSummary{
		AccountScopeID:  accountScopeID,
		PrincipalID:     principalID,
		SessionID:       strings.TrimSpace(sessionID),
		PendingCount:    count,
		OldestPendingAt: oldest,
		NewestPendingAt: newest,
		UpdatedAt:       now,
	}, nil
}

func (s *Service) publishPermissionSummaryUpdatedLocked(sessionID string, summary pebblestore.PermissionSummary) {
	payload := permissionSummaryUpdatePayload(summary)
	streamPrincipalID := strings.TrimSpace(summary.PrincipalID)
	if streamPrincipalID == "" {
		streamPrincipalID = strings.TrimSpace(s.principalID)
	}
	_, _ = s.emitLocked("user:"+streamPrincipalID, "permission.summary.updated", sessionID, payload)
	if s.summaryRealtimePublish == nil {
		return
	}
	if err := s.summaryRealtimePublish(sessionID, summary); err != nil {
		log.Printf("warning: permission summary realtime publish failed session=%q: %v", sessionID, err)
	}
}

type permissionSummaryUpdatedPayload struct {
	SessionID            string `json:"session_id"`
	PendingApprovalCount int    `json:"pending_approval_count"`
	OldestPendingAt      int64  `json:"oldest_pending_at"`
	NewestPendingAt      int64  `json:"newest_pending_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

func permissionSummaryUpdatePayload(summary pebblestore.PermissionSummary) permissionSummaryUpdatedPayload {
	payload := permissionSummaryUpdatedPayload{
		SessionID:            strings.TrimSpace(summary.SessionID),
		PendingApprovalCount: summary.PendingCount,
		OldestPendingAt:      summary.OldestPendingAt,
		NewestPendingAt:      summary.NewestPendingAt,
		UpdatedAt:            summary.UpdatedAt,
	}
	if payload.PendingApprovalCount <= 0 {
		payload.PendingApprovalCount = 0
		payload.OldestPendingAt = 0
		payload.NewestPendingAt = 0
	}
	return payload
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
		s.syncNotification(record, descriptor.HostSwarmID, strings.TrimSpace(descriptor.ChildSwarmID), "permission.updated")
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
	summary, err := s.summaryForMutationLocked(sessionID, &previous, record, startedAt)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if err := s.store.PutPermissionWithSummary(record, &previous, summary); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	s.syncNotification(record, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.updated")
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
		s.syncNotification(record, descriptor.HostSwarmID, strings.TrimSpace(descriptor.ChildSwarmID), "permission.updated")
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
	summary, err := s.summaryForMutationLocked(sessionID, &previous, record, completedAt)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	if err := s.store.PutPermissionWithSummary(record, &previous, summary); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	s.syncNotification(record, s.localSwarmID(), s.originSwarmIDForSession(sessionID), "permission.updated")
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
		if requirement := PlanManageLifecycleRequirement(toolArguments); requirement != "" {
			return requirement
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
	case "manage_sessions":
		if ShouldApproveManageSessionsDeploy(toolArguments) {
			return "session_deploy"
		}
		if ShouldApproveManageSessionsArchive(toolArguments) {
			return "session_archive"
		}
		return "manage_sessions"
	case "":
		return "tool"
	default:
		return toolName
	}
}

func ShouldApproveManageAgentMutation(toolArguments string) bool {
	action := manageAction(toolArguments)
	switch action {
	case "create", "update", "delete", "remove", "create_custom_tool", "create-custom-tool", "update_custom_tool", "update-custom-tool", "delete_custom_tool", "delete-custom-tool", "remove_custom_tool", "remove-custom-tool", "assign_custom_tool", "assign-custom-tool", "unassign_custom_tool", "unassign-custom-tool":
		return true
	default:
		return false
	}
}

func manageAgentAction(toolArguments string) string {
	return manageAction(toolArguments)
}

func manageAction(toolArguments string) string {
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
	return PlanManageLifecycleRequirement(toolArguments) != ""
}

func PlanManageLifecycleRequirement(toolArguments string) string {
	toolArguments = strings.TrimSpace(toolArguments)
	if toolArguments == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolArguments), &payload); err != nil {
		return ""
	}
	action := normalizePlanManageAction(mapStringAny(payload["action"]), mapStringAny(payload["op"]), payload)
	switch action {
	case "request_followup_checkpoint":
		return "plan_followup_request"
	case "request_plan_revision":
		return "plan_revision_request"
	case "amend_plan":
		return "plan_amendment_request"
	case "request_new_plan":
		return "plan_new_request"
	case "save":
		if planManageTargetsExistingPlan(payload) {
			return "plan_revision_request"
		}
	case "patch", "update_section", "update_info", "update_execution_policy", "update_execution_state", "upsert_checkpoint", "remove_checkpoint", "reorder_checkpoints", "set_active_checkpoint":
		if planManageTargetsExistingPlan(payload) || planManageHasRevisionPayload(payload) {
			return "plan_revision_request"
		}
	}
	return ""
}

func planManageTargetsExistingPlan(payload map[string]any) bool {
	planID := strings.TrimSpace(mapStringAny(payload["plan_id"]))
	if planID == "" {
		planID = strings.TrimSpace(mapStringAny(payload["id"]))
	}
	if planID != "" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(mapStringAny(payload["update_type"]))) == "existing_plan"
}

func planManageHasRevisionPayload(payload map[string]any) bool {
	for _, key := range []string{"plan", "document", "document_patch", "operations", "info", "execution_policy", "execution_state", "checkpoint_id", "checkpoint_order", "active_checkpoint_id", "active_checkpoint", "patch", "old_text", "new_text", "text", "checklist_item", "item"} {
		if value, ok := payload[key]; ok && value != nil {
			if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
				continue
			}
			return true
		}
	}
	if value, ok := payload["checkpoint"]; ok && value != nil {
		if _, isBool := value.(bool); !isBool {
			return true
		}
	}
	return false
}

func normalizePlanManageAction(action string, op string, payload map[string]any) string {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(op))
	}
	switch action {
	case "start-session-checkpoint", "start_session_checkpoint", "session-checkpoint", "session_checkpoint", "auto-checkpoint", "auto_checkpoint":
		return "start_session_checkpoint"
	case "request-followup-checkpoint", "request_followup_checkpoint", "followup-checkpoint", "followup_checkpoint", "request-changes", "request_changes":
		return "request_followup_checkpoint"
	case "request-plan-revision", "request_plan_revision", "plan-revision", "plan_revision":
		return "request_plan_revision"
	case "amend-plan", "amend_plan", "plan-amendment", "plan_amendment", "amend-future-checkpoints", "amend_future_checkpoints":
		return "amend_plan"
	case "request-new-plan", "request_new_plan", "new-plan-proposal", "new_plan_proposal", "create-plan", "create_plan", "propose-plan", "propose_plan":
		return "request_new_plan"
	case "upsert", "set", "write-active", "write_active":
		return "save"
	case "update", "edit":
		if strings.TrimSpace(mapStringAny(payload["plan"])) == "" && payload["document"] == nil {
			return "patch"
		}
		return "save"
	default:
		return action
	}
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

func isPendingPlanProposalRecord(record pebblestore.PermissionRecord) bool {
	toolName := strings.ToLower(strings.TrimSpace(record.ToolName))
	if toolName != "exit_plan_mode" && toolName != "plan_manage" {
		return false
	}
	payload := parsePermissionJSONMap(record.ToolArguments)
	if toolName == "exit_plan_mode" {
		return strings.TrimSpace(stringValue(payload["path_id"])) == "permission.exit-plan-mode.v1"
	}
	action := strings.ToLower(strings.TrimSpace(stringValue(payload["action"])))
	switch action {
	case "request_new_plan", "request_plan_revision":
		return true
	default:
		return false
	}
}

func approvedArgumentsForResolution(record pebblestore.PermissionRecord, action, clientArguments string) (string, error) {
	if !actionIsAllow(action) || !isPendingPlanProposalRecord(record) {
		return sanitizeApprovedArguments(record.ToolName, action, clientArguments, record.ToolArguments), nil
	}
	canonical := approvedArgumentsFromToolArguments(record.ToolName, record.ToolArguments)
	if canonical == "" {
		return "", errors.New("pending plan proposal has no canonical approved arguments")
	}
	var backend map[string]any
	if err := json.Unmarshal([]byte(canonical), &backend); err != nil {
		return "", errors.New("pending plan proposal approved arguments are invalid")
	}
	clientArguments = strings.TrimSpace(clientArguments)
	if clientArguments != "" {
		var client map[string]any
		if err := json.Unmarshal([]byte(clientArguments), &client); err != nil {
			return "", errors.New("approved arguments must be a JSON object")
		}
		granularity, hasGranularity := client["execution_granularity"]
		continuation, hasContinuation := client["continuation_policy"]
		continueAutomatically, hasAutomatic := client["continue_automatically"]
		if hasGranularity {
			if _, ok := granularity.(string); !ok {
				return "", errors.New("execution_granularity overlay must be a string")
			}
			backend["execution_granularity"] = granularity
		}
		if hasContinuation {
			if _, ok := continuation.(string); !ok {
				return "", errors.New("continuation_policy overlay must be a string")
			}
			backend["continuation_policy"] = continuation
		}
		if hasAutomatic {
			if _, ok := continueAutomatically.(bool); !ok {
				return "", errors.New("continue_automatically overlay must be boolean")
			}
			backend["continue_automatically"] = continueAutomatically
		}
		if hasGranularity || hasContinuation || hasAutomatic {
			documentRaw, err := json.Marshal(backend["document"])
			if err != nil {
				return "", errors.New("pending plan proposal document is invalid")
			}
			var document pebblestore.SessionPlanDocument
			if err := json.Unmarshal(documentRaw, &document); err != nil {
				return "", errors.New("pending plan proposal document is invalid")
			}
			granularityText, _ := backend["execution_granularity"].(string)
			continuationText, _ := backend["continuation_policy"].(string)
			if automatic, ok := backend["continue_automatically"].(bool); ok {
				if automatic {
					continuationText = sessionruntime.PlanAcceptanceContinuationAutomatic
				} else {
					continuationText = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
				}
			}
			if _, err := sessionruntime.ApplyPlanAcceptanceExecutionPolicy(&document, sessionruntime.PlanAcceptanceExecutionOptions{ExecutionGranularity: granularityText, ContinuationPolicy: continuationText}); err != nil {
				return "", fmt.Errorf("unsupported execution policy overlay: %w", err)
			}
		}
	}
	raw, err := json.Marshal(backend)
	if err != nil {
		return "", err
	}
	return privacy.SanitizeJSONText(string(raw)), nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func firstNonEmptyPermissionString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	descriptor, hosted := sessionruntime.HostedSessionFromMetadata(session.Metadata)
	if !hosted {
		return sessionruntime.HostedSessionDescriptor{}, false, nil
	}
	localSwarmID := ""
	if s.localSwarmIDResolver != nil {
		localSwarmID = strings.TrimSpace(s.localSwarmIDResolver())
	}
	if localSwarmID != "" && strings.EqualFold(strings.TrimSpace(descriptor.HostSwarmID), localSwarmID) {
		return sessionruntime.HostedSessionDescriptor{}, false, nil
	}
	return descriptor, true, nil
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

func (s *Service) StoreMirroredPermission(record pebblestore.PermissionRecord) error {
	return s.storeMirroredPermission(record)
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
	now := firstNonZero(record.UpdatedAt, record.ResolvedAt, record.CompletedAt, record.PermissionRequested, record.CreatedAt, time.Now().UnixMilli())
	summary, err := s.summaryForMutationLocked(record.SessionID, previousPtr, record, now)
	if err != nil {
		return err
	}
	if err := s.store.PutPermissionWithSummary(record, previousPtr, summary); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		if err := s.attachRunWaitLocked(record, now); err != nil {
			return err
		}
	} else {
		s.detachRunWaitLocked(record, now)
		s.notifyWaitersLocked(record)
	}
	s.syncNotification(record, s.hostSwarmIDForSession(record.SessionID), s.originSwarmIDForSession(record.SessionID), permissionNotificationEventType(record))
	s.publishPermissionSummaryUpdatedLocked(record.SessionID, summary)
	return nil
}

func (s *Service) syncNotification(record pebblestore.PermissionRecord, swarmID, originSwarmID, sourceEventType string) {
	if s == nil || s.notifications == nil {
		return
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		swarmID = s.localSwarmID()
	}
	if swarmID == "" {
		return
	}
	severity := pebblestore.NotificationSeverityWarning
	status := pebblestore.NotificationStatusActive
	readAt := int64(0)
	ackedAt := int64(0)
	if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		status = pebblestore.NotificationStatusResolved
		readAt = firstNonZero(record.ResolvedAt, record.UpdatedAt)
		ackedAt = readAt
		switch strings.ToLower(strings.TrimSpace(record.Status)) {
		case pebblestore.PermissionStatusApproved:
			severity = pebblestore.NotificationSeverityInfo
		case pebblestore.PermissionStatusDenied, pebblestore.PermissionStatusCancelled:
			severity = pebblestore.NotificationSeverityError
		default:
			severity = pebblestore.NotificationSeverityInfo
		}
	}
	sessionTitle, workspaceName, workspacePath, originLabel := s.permissionNotificationSessionContext(record.SessionID, firstNonEmpty(originSwarmID, swarmID))
	_, _, _ = s.notifications.UpsertPermissionNotification(notification.PermissionUpsertInput{
		AccountScopeID:  accountScopeIDForPermissionRecord(s.sessions, record),
		SwarmID:         swarmID,
		OriginSwarmID:   firstNonEmpty(originSwarmID, swarmID),
		SessionID:       record.SessionID,
		RunID:           record.RunID,
		PermissionID:    record.ID,
		ToolName:        record.ToolName,
		Requirement:     record.Requirement,
		Title:           permissionNotificationTitleFromRecord(record),
		Body:            permissionNotificationBodyFromRecord(record),
		SessionTitle:    sessionTitle,
		SessionLabel:    permissionNotificationSessionLabel(sessionTitle, workspaceName, record.SessionID),
		WorkspacePath:   workspacePath,
		WorkspaceName:   workspaceName,
		OriginLabel:     originLabel,
		Severity:        severity,
		Status:          status,
		SourceEventType: sourceEventType,
		CreatedAt:       firstNonZero(record.PermissionRequested, record.CreatedAt),
		UpdatedAt:       firstNonZero(record.UpdatedAt, record.ResolvedAt, record.CreatedAt),
		ReadAt:          readAt,
		AckedAt:         ackedAt,
	})
}

func accountScopeIDForPermissionRecord(sessions sessionLookup, record pebblestore.PermissionRecord) string {
	if sessions == nil {
		return ""
	}
	session, ok, err := sessions.GetSession(record.SessionID)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(session.AccountScopeID)
}

func (s *Service) localSwarmID() string {
	if s == nil || s.localSwarmIDResolver == nil {
		return ""
	}
	return strings.TrimSpace(s.localSwarmIDResolver())
}

func (s *Service) originSwarmIDForSession(sessionID string) string {
	descriptor, hosted, err := s.hostedDescriptorForSession(sessionID)
	if err == nil && hosted && strings.TrimSpace(descriptor.ChildSwarmID) != "" {
		return strings.TrimSpace(descriptor.ChildSwarmID)
	}
	return s.localSwarmID()
}

func (s *Service) hostSwarmIDForSession(sessionID string) string {
	descriptor, hosted, err := s.hostedDescriptorForSession(sessionID)
	if err == nil && hosted && strings.TrimSpace(descriptor.HostSwarmID) != "" {
		return strings.TrimSpace(descriptor.HostSwarmID)
	}
	return s.localSwarmID()
}

func permissionNotificationEventType(record pebblestore.PermissionRecord) string {
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		return "permission.requested"
	}
	return "permission.updated"
}

func (s *Service) permissionNotificationSessionContext(sessionID, originSwarmID string) (sessionTitle, workspaceName, workspacePath, originLabel string) {
	if s == nil || s.sessions == nil || strings.TrimSpace(sessionID) == "" {
		return "", "", "", shortPermissionNotificationID(originSwarmID)
	}
	session, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		return "", "", "", shortPermissionNotificationID(originSwarmID)
	}
	sessionTitle = strings.TrimSpace(session.Title)
	workspacePath = strings.TrimSpace(session.WorkspacePath)
	workspaceName = strings.TrimSpace(session.WorkspaceName)
	if workspaceName == "" && workspacePath != "" {
		workspaceName = filepath.Base(workspacePath)
	}
	originLabel = permissionNotificationOriginLabel(originSwarmID, session.Metadata)
	return sessionTitle, workspaceName, workspacePath, originLabel
}

func permissionNotificationSessionLabel(title, workspaceName, sessionID string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	if workspaceName = strings.TrimSpace(workspaceName); workspaceName != "" {
		return workspaceName + " " + shortPermissionNotificationID(sessionID)
	}
	return shortPermissionNotificationID(sessionID)
}

func permissionNotificationOriginLabel(originSwarmID string, metadata map[string]any) string {
	for _, key := range []string{"swarm_route_label", "swarm_target_name", "target_display_name"} {
		if value := strings.TrimSpace(fmt.Sprint(metadata[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return shortPermissionNotificationID(originSwarmID)
}

func shortPermissionNotificationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func permissionNotificationTitleFromRecord(record pebblestore.PermissionRecord) string {
	summary := permissionNotificationActionSummary(record)
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		if summary.title != "" {
			return summary.title
		}
		return fmt.Sprintf("Permission requested: %s", fallbackToolName(record.ToolName))
	}
	return fmt.Sprintf("Permission %s: %s", strings.TrimSpace(record.Status), firstNonEmpty(summary.label, fallbackToolName(record.ToolName)))
}

func permissionNotificationBodyFromRecord(record pebblestore.PermissionRecord) string {
	summary := permissionNotificationActionSummary(record)
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		if summary.body != "" {
			return summary.body
		}
		if strings.TrimSpace(record.Requirement) == "" {
			return fmt.Sprintf("The %s action is waiting for approval.", fallbackToolName(record.ToolName))
		}
		return fmt.Sprintf("The %s %s action is waiting for approval.", strings.TrimSpace(record.Requirement), fallbackToolName(record.ToolName))
	}
	if strings.TrimSpace(record.Reason) != "" {
		return fmt.Sprintf("%s %s: %s", firstNonEmpty(summary.label, fallbackToolName(record.ToolName)), strings.TrimSpace(record.Status), boundedNotificationText(record.Reason, 120))
	}
	return fmt.Sprintf("%s %s.", firstNonEmpty(summary.label, fallbackToolName(record.ToolName)), strings.TrimSpace(record.Status))
}

type permissionNotificationSummary struct {
	title string
	body  string
	label string
}

func permissionNotificationActionSummary(record pebblestore.PermissionRecord) permissionNotificationSummary {
	toolName := strings.ToLower(strings.TrimSpace(record.ToolName))
	args := parsePermissionJSONMap(firstNonEmpty(record.ToolCallArguments, record.ToolArguments))
	switch toolName {
	case "exit_plan_mode":
		plan := boundedNotificationText(firstNonEmpty(
			mapStringAny(args["title"]),
			nestedPermissionString(args, "document", "title"),
			nestedPermissionString(args, "document", "info", "goal"),
		), 100)
		if plan == "" {
			return permissionNotificationSummary{title: "Plan approval requested", body: "Review and approve the proposed plan.", label: "plan approval"}
		}
		return permissionNotificationSummary{title: "Plan approval requested: " + plan, body: "Review and approve the plan: " + plan + ".", label: "plan approval: " + plan}
	case "plan_manage":
		action := strings.ToLower(strings.TrimSpace(mapStringAny(args["action"])))
		label := planPermissionActionLabel(action)
		detail := boundedNotificationText(firstNonEmpty(mapStringAny(args["checkpoint_title"]), mapStringAny(args["title"])), 100)
		if detail != "" {
			return permissionNotificationSummary{title: label + ": " + detail, body: "Review and approve: " + detail + ".", label: label + ": " + detail}
		}
		return permissionNotificationSummary{title: label, body: "Review and approve this " + strings.ToLower(label) + ".", label: label}
	case "bash":
		program := permissionCommandProgram(firstNonEmpty(mapStringAny(args["command"]), mapStringAny(args["cmd"])))
		if program != "" {
			label := "run " + program
			return permissionNotificationSummary{title: "Command approval requested: " + program, body: "Approve running a " + program + " command.", label: label}
		}
		return permissionNotificationSummary{title: "Command approval requested", body: "Approve running a shell command.", label: "shell command"}
	case "read", "write", "edit":
		operation := map[string]string{"read": "Read", "write": "Write", "edit": "Edit"}[toolName]
		name := boundedNotificationText(filepath.Base(strings.TrimSpace(mapStringAny(args["path"]))), 80)
		if name != "" && name != "." {
			return permissionNotificationSummary{title: operation + " file: " + name, body: "Approve access to " + name + ".", label: strings.ToLower(operation) + " " + name}
		}
		return permissionNotificationSummary{title: operation + " file approval requested", body: "Approve the requested file operation.", label: strings.ToLower(operation) + " file"}
	case "ask_user", "ask-user":
		return permissionNotificationSummary{title: "Input requested", body: "The agent is waiting for your answer to a question.", label: "user input"}
	case "task":
		description := boundedNotificationText(mapStringAny(args["description"]), 100)
		if description != "" {
			return permissionNotificationSummary{title: "Agent launch requested: " + description, body: "Approve delegating: " + description + ".", label: "agent launch: " + description}
		}
		return permissionNotificationSummary{title: "Agent launch requested", body: "Approve launching a delegated agent task.", label: "agent launch"}
	}

	action := boundedNotificationText(mapStringAny(args["action"]), 60)
	toolLabel := humanizePermissionAction(toolName)
	if action != "" {
		label := toolLabel + ": " + humanizePermissionAction(action)
		return permissionNotificationSummary{title: "Permission requested: " + label, body: "Approve the requested " + strings.ToLower(label) + " operation.", label: label}
	}
	if toolName != "" {
		return permissionNotificationSummary{title: "Permission requested: " + toolLabel, body: "Approve the requested " + strings.ToLower(toolLabel) + " operation.", label: toolLabel}
	}
	return permissionNotificationSummary{}
}

func nestedPermissionString(value any, keys ...string) string {
	current := value
	for _, key := range keys {
		if raw, ok := current.(string); ok {
			current = parsePermissionJSONMap(raw)
		}
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	return mapStringAny(current)
}

func planPermissionActionLabel(action string) string {
	switch action {
	case "request_followup_checkpoint", "request_changes":
		return "Plan checkpoint approval requested"
	case "request_plan_revision", "amend_plan":
		return "Plan revision approval requested"
	case "request_new_plan":
		return "New plan approval requested"
	default:
		return "Plan action approval requested"
	}
}

func permissionCommandProgram(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	for len(fields) > 1 {
		candidate := strings.ToLower(filepath.Base(fields[0]))
		if candidate == "sudo" || candidate == "env" || strings.Contains(fields[0], "=") {
			fields = fields[1:]
			continue
		}
		break
	}
	program := filepath.Base(fields[0])
	if strings.ContainsAny(program, "=$'\"`\\") {
		return ""
	}
	return boundedNotificationText(program, 32)
}

func humanizePermissionAction(action string) string {
	action = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(action, "_", " "), "-", " "))
	if action == "" {
		return "requested action"
	}
	return strings.ToUpper(action[:1]) + action[1:]
}

func boundedNotificationText(value string, limit int) string {
	value = privacy.SanitizeText(value)
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func fallbackToolName(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return "tool"
	}
	return toolName
}
