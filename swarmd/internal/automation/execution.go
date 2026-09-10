package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// ExecutionRuntime is the daemon composition boundary, not permission authority.
// Ensure must durably deduplicate by occurrence identity, revalidate permissions,
// allocate a session-owned managed worktree, create through ApplySessionMutation,
// and install the exact pinned plans through canonical checkpoint lifecycle.
// Serialize requires a durable target reservation, never a scan or local mutex.
// A retry after an ambiguous failure MUST return the same session, not run again.
// No adapter means execution is unavailable; there is no legacy fallback.
type ExecutionRuntime interface {
	Ensure(context.Context, Principal, store.AutomationRecord, store.AutomationRecord) (string, error)
}

// TriggerAuthority verifies event source credentials or a daemon scheduling
// capability. It must not infer authentication from request Source strings.
type TriggerAuthority interface {
	Verify(context.Context, Principal, store.AutomationScope, Trigger) error
}

type Trigger struct {
	Kind, Source, Identity string
	ScheduledAt int64
}

type ExecutionService struct {
	domain *Service
	runtime ExecutionRuntime
	triggers TriggerAuthority
}

func NewExecutionService(domain *Service, runtime ExecutionRuntime, triggers TriggerAuthority) (*ExecutionService, error) {
	if domain == nil || runtime == nil || triggers == nil {
		return nil, ErrInvalid
	}
	return &ExecutionService{domain: domain, runtime: runtime, triggers: triggers}, nil
}

func executionKey(parts ...any) string {
	data, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Admit creates one durable pending occurrence. Replays keep their original
// definition revision; they do not silently repin to an edited definition.
func (e *ExecutionService) Admit(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64, trigger Trigger) (store.AutomationRecord, error) {
	s := e.domain
	if err := s.authorize(ctx, p, scope, "run"); err != nil { return store.AutomationRecord{}, err }
	if len(trigger.Identity) == 0 || len(trigger.Identity) > 256 || trigger.ScheduledAt <= 0 || trigger.ScheduledAt > s.now().UnixMilli() {
		return store.AutomationRecord{}, ErrInvalid
	}
	if trigger.Kind == "event" {
		exact, ok := e.triggers.(interface { VerifyAdmission(context.Context, Principal, store.AutomationScope, string, uint64, Trigger) error })
		if !ok { return store.AutomationRecord{}, ErrDenied }
		if err := exact.VerifyAdmission(ctx, p, scope, id, revision, trigger); err != nil { return store.AutomationRecord{}, err }
	} else if err := e.triggers.Verify(ctx, p, scope, trigger); err != nil { return store.AutomationRecord{}, err }
	def, err := s.CheckRun(ctx, p, scope, id, revision)
	if err != nil { return store.AutomationRecord{}, err }
	schedule := def.Definition.Schedule
	switch trigger.Kind {
	case "manual":
		if p.Role != "user" || trigger.Source != "" { return store.AutomationRecord{}, ErrDenied }
	case "event":
		if schedule.Kind != "event" || trigger.Source != schedule.TriggerSource { return store.AutomationRecord{}, ErrDenied }
	case "schedule":
		if p.Role != "system" || trigger.Source != "" { return store.AutomationRecord{}, ErrDenied }
		due, err := Due(schedule, time.UnixMilli(trigger.ScheduledAt), time.UnixMilli(def.WrittenAt))
		if err != nil { return store.AutomationRecord{}, err }
		if !due { return store.AutomationRecord{}, ErrInvalid }
		// Scheduled identities are computed, never supplied by callers.
		trigger.Identity = fmt.Sprintf("%d", trigger.ScheduledAt)
	default:
		return store.AutomationRecord{}, ErrInvalid
	}
	key := executionKey(trigger.Kind, trigger.Source, trigger.Identity)
	occurrenceID := executionKey(scope, id, key)
	r := store.AutomationRecord{Scope: scope, AutomationID: id, Kind: "occurrence", ID: occurrenceID, Occurrence: &store.AutomationOccurrence{DefinitionRevision: revision, TriggerIdentity: key, ScheduledAt: trigger.ScheduledAt, State: "pending"}}
	out, _, err := s.repo.ApplyAutomationMutation(store.AutomationMutation{Record: r, MutationID: "admit-"+occurrenceID, Actor: p.Role, SubjectID: p.SubjectID, WrittenAt: s.now().UnixMilli()})
	return out, err
}

// Dispatch and recovery use identical logic. Pending records survive a crash;
// Ensure owns idempotent V3/worktree recovery across the non-atomic store boundary.
// Blocked records are never automatically retried. Notification is not involved.
func (e *ExecutionService) Dispatch(ctx context.Context, p Principal, scope store.AutomationScope, id, occurrenceID string) (store.AutomationRecord, error) {
	s := e.domain
	if err := s.authorize(ctx, p, scope, "run"); err != nil { return store.AutomationRecord{}, err }
	r, found, err := s.repo.GetAutomationRecord(scope, id, "occurrence", occurrenceID, 0)
	if err != nil { return r, err }
	if !found || r.Occurrence == nil { return r, ErrNotFound }
	if r.Occurrence.State != "pending" { return r, nil }
	def, err := s.CheckRun(ctx, p, scope, id, r.Occurrence.DefinitionRevision)
	if err != nil { return r, err }
	claims, ok := s.repo.(interface { ClaimAutomationDispatch(store.AutomationScope, string, string) error })
	if !ok { return r, ErrInvalid }
	if err := claims.ClaimAutomationDispatch(scope, id, occurrenceID); err != nil { return r, err }
	sessionID, err := e.runtime.Ensure(ctx, p, def, r)
	if err != nil { return r, err }
	if sessionID == "" { return r, ErrInvalid }
	return e.transition(p, r, "running", sessionID)
}

func (e *ExecutionService) transition(p Principal, r store.AutomationRecord, state, sessionID string) (store.AutomationRecord, error) {
	o := *r.Occurrence
	o.State = state
	if sessionID != "" { o.SessionID = sessionID }
	next := store.AutomationRecord{Scope: r.Scope, AutomationID: r.AutomationID, Kind: r.Kind, ID: r.ID, Occurrence: &o}
	out, _, err := e.domain.repo.ApplyAutomationMutation(store.AutomationMutation{Record: next, ExpectedRevision: r.Revision, MutationID: executionKey(r.ID, r.Revision, state), Actor: p.Role, SubjectID: p.SubjectID, WrittenAt: e.domain.now().UnixMilli()})
	return out, err
}

// RecordOutcome accepts evidence only from an authenticated owner of the exact
// execution session. Audit is written first with a deterministic receipt: a
// crash before the state transition is repaired by replaying the same outcome.
// Summaries remain evidence; they cannot alter plans or deployment permissions.
func (e *ExecutionService) RecordOutcome(ctx context.Context, p Principal, scope store.AutomationScope, id, occurrenceID, state, summary string, facts map[string]string) (store.AutomationRecord, error) {
	s := e.domain
	if err := s.authorize(ctx, p, scope, "outcome"); err != nil { return store.AutomationRecord{}, err }
	if p.Role != "agent" || summary == "" || len(summary) > 4096 || len(facts) > 32 { return store.AutomationRecord{}, ErrDenied }
	switch state { case "completed", "failed", "blocked", "cancelled": default: return store.AutomationRecord{}, ErrInvalid }
	r, found, err := s.repo.GetAutomationRecord(scope, id, "occurrence", occurrenceID, 0)
	if err != nil { return r, err }
	if !found || r.Occurrence == nil || r.Occurrence.SessionID == "" { return r, ErrDenied }
	if err := s.access.OccurrenceSession(ctx, p, scope, r.Occurrence.SessionID); err != nil { return r, err }
	if r.Occurrence.State != "running" && r.Occurrence.State != state { return r, store.ErrAutomationConflict }
	evidence := cloneMap(facts)
	if evidence == nil { evidence = map[string]string{} }
	evidence["session_id"] = r.Occurrence.SessionID
	auditID := executionKey(occurrenceID, "outcome")
	_, _, err = s.repo.ApplyAutomationMutation(store.AutomationMutation{Record: store.AutomationRecord{Scope: scope, AutomationID: id, Kind: "audit", ID: auditID, Outcome: &store.AutomationOutcome{OccurrenceID: occurrenceID, Kind: state, Summary: summary, Facts: evidence}}, MutationID: auditID, Actor: p.Role, SubjectID: p.SubjectID, WrittenAt: s.now().UnixMilli()})
	if err != nil { return r, err }
	if r.Occurrence.State == state { return r, nil }
	return e.transition(p, r, state, "")
}

// Cancel fences dispatch durably before publishing a terminal occurrence. A
// failed stop remains nonterminal and retains the serialize reservation.
// Retry with the original expectedRevision and mutationID, including after restart.
// Concurrent identical retries may call Cancel again: the runtime must durably
// deduplicate/fence the same execution key, as required by V3ExecutionHost.
func (e *ExecutionService) Cancel(ctx context.Context, p Principal, scope store.AutomationScope, id, occurrenceID string, expectedRevision uint64, mutationID string) (store.AutomationRecord, error) {
	if err := e.domain.authorize(ctx, p, scope, "cancel"); err != nil { return store.AutomationRecord{}, err }
	if p.Role != "user" { return store.AutomationRecord{}, ErrDenied }
	if expectedRevision == 0 || mutationID == "" { return store.AutomationRecord{}, ErrInvalid }
	canceller, ok := e.runtime.(interface { Cancel(context.Context, Principal, store.AutomationRecord) error })
	if !ok { return store.AutomationRecord{}, ErrInvalid }
	repo, ok := e.domain.repo.(interface {
		AdmitAutomationCancellation(store.AutomationScope, string, string, uint64, string, string, int64) (store.AutomationRecord, error)
		FinishAutomationCancellation(store.AutomationRecord, string, int64) (store.AutomationRecord, error)
	})
	if !ok { return store.AutomationRecord{}, ErrInvalid }
	r, err := repo.AdmitAutomationCancellation(scope, id, occurrenceID, expectedRevision, mutationID, p.SubjectID, e.domain.now().UnixMilli())
	if err != nil { return r, err }
	if r.Occurrence.State == "cancelled" { return r, nil }
	if err := canceller.Cancel(ctx, p, r); err != nil { return r, err }
	return repo.FinishAutomationCancellation(r, p.SubjectID, e.domain.now().UnixMilli())
}
