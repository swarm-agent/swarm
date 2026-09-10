// Package automation owns definition policy, not execution or permission grants.
package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrDenied = errors.New("automation access denied")
	ErrInvalid = errors.New("invalid automation policy")
	ErrNotFound = errors.New("automation record not found")
)

// Principal must come from the authenticated transport, never request JSON.
type Principal struct { AccountID, SubjectID, Role string }
type Repository interface {
	ApplyAutomationMutation(store.AutomationMutation) (store.AutomationRecord, bool, error)
	GetAutomationRecord(store.AutomationScope, string, string, string, uint64) (store.AutomationRecord, bool, error)
	SearchAutomationRecords(store.AutomationSearch) ([]store.AutomationRecord, string, error)
}
// Access must resolve workspace/session ownership from canonical authorities.
// Execution must validate the exact definition policy and approval, including
// revocation, tools and targets; stored approval references are not grants.
type Access interface {
	Workspace(context.Context, Principal, store.AutomationScope, string) error
	PlanSession(context.Context, Principal, store.AutomationScope, string) error
	Execution(context.Context, Principal, store.AutomationScope, store.AutomationDefinition, string) error
	OccurrenceSession(context.Context, Principal, store.AutomationScope, string) error
}
// CanonicalPlans is implemented by the canonical SessionStore; no copied plan
// text, latest-plan fallback or mutable frontend snapshot is accepted.
type CanonicalPlans interface {
	GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error)
}
// Executor must reauthorize at dispatch and create independent V3 sessions with
// canonical mutations. A successful policy check below is NOT a dispatch token.
type Executor interface { Dispatch(context.Context, Principal, store.AutomationRecord) (string, error) }
// Publisher is an integration boundary only. Delivery must use a durable outbox;
// a notification failure must never retry execution. This service does not call it.
type Publisher interface { Publish(context.Context, store.AutomationRecord) error }

type Service struct { repo Repository; plans CanonicalPlans; access Access; now func() time.Time }
func New(repo Repository, plans CanonicalPlans, access Access, now func() time.Time) (*Service, error) {
	if repo == nil || plans == nil || access == nil || now == nil { return nil, ErrInvalid }
	return &Service{repo: repo, plans: plans, access: access, now: now}, nil
}
func (s *Service) authorize(ctx context.Context, p Principal, scope store.AutomationScope, action string) error {
	if p.SubjectID == "" || p.AccountID == "" || p.AccountID != scope.AccountID || scope.WorkspaceID == "" { return ErrDenied }
	if p.Role != "user" && p.Role != "agent" && p.Role != "system" { return ErrDenied }
	return s.access.Workspace(ctx, p, scope, action)
}
func (s *Service) plan(ctx context.Context, p Principal, scope store.AutomationScope, ref *store.AutomationPlanReference) error {
	if ref.SessionID == "" || ref.PlanID == "" || ref.Revision == 0 || ref.Revision > uint64(^uint(0)>>1) { return ErrInvalid }
	if err := s.access.PlanSession(ctx, p, scope, ref.SessionID); err != nil { return err }
	plan, found, err := s.plans.GetPlanRevision(ref.SessionID, ref.PlanID, int(ref.Revision))
	if err != nil { return err }
	if !found || plan.AccountScopeID != scope.AccountID || plan.SessionID != ref.SessionID || plan.ID != ref.PlanID || uint64(plan.Version) != ref.Revision || plan.ApprovalState != "approved" || plan.Document == nil { return ErrDenied }
	// Canonical plan snapshots may be rewritten at the same version by lifecycle
	// updates. Pin document bytes, not merely the mutable revision-key locator.
	data, err := json.Marshal(plan.Document)
	if err != nil { return err }
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if ref.DocumentSHA256 != "" && ref.DocumentSHA256 != digest { return ErrDenied }
	ref.DocumentSHA256 = digest
	return nil
}
func (s *Service) execution(ctx context.Context, p Principal, scope store.AutomationScope, d store.AutomationDefinition, action string) error {
	if d.Authorization.Mode != "approved_policy" || d.Authorization.ApprovalReference == "" || d.Authorization.ExpiresAt <= s.now().UnixMilli() { return ErrDenied }
	return s.access.Execution(ctx, p, scope, d, action)
}
// SaveDefinition handles create/update/enable/disable with immutable store CAS.
// Disabling remains possible if the bound plan or approval has been revoked.
func (s *Service) SaveDefinition(ctx context.Context, p Principal, scope store.AutomationScope, id, mutation string, expected uint64, d store.AutomationDefinition) (store.AutomationRecord, bool, error) {
	if err := s.authorize(ctx, p, scope, "manage"); err != nil { return store.AutomationRecord{}, false, err }
	if p.Role != "user" { return store.AutomationRecord{}, false, ErrDenied }
	var err error
	d.Schedule, err = NormalizeSchedule(d.Schedule)
	if err != nil { return store.AutomationRecord{}, false, err }
	if d.Authorization.Mode != "approval_required" && d.Authorization.Mode != "approved_policy" { return store.AutomationRecord{}, false, ErrInvalid }
	old, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, expected)
	if err != nil { return store.AutomationRecord{}, false, err }
	if err := store.ValidateAutomationBindings(d.Plans); err != nil { return store.AutomationRecord{}, false, err }
	// Copy before pinning so caller-owned slices and stored revisions stay immutable.
	d.Plans = append([]store.AutomationPlanBinding(nil), d.Plans...)
	for i := range d.Plans {
		ref := &d.Plans[i].Plan
		// Match the locator even after a binding rename: omission cannot repin it.
		if found && old.Definition != nil && ref.DocumentSHA256 == "" {
			for _, binding := range old.Definition.Plans {
				prior := binding.Plan
				if prior.SessionID == ref.SessionID && prior.PlanID == ref.PlanID && prior.Revision == ref.Revision { ref.DocumentSHA256 = prior.DocumentSHA256 }
			}
		}
	}
	// Only unchanged bindings can be disabled after revocation.
	if expected == 0 || !found || old.Definition == nil || d.Enabled || !reflect.DeepEqual(old.Definition.Plans, d.Plans) {
		for i := range d.Plans {
			if err := s.plan(ctx, p, scope, &d.Plans[i].Plan); err != nil { return store.AutomationRecord{}, false, err }
		}
	}
	if d.Enabled { if err := s.execution(ctx, p, scope, d, "enable"); err != nil { return store.AutomationRecord{}, false, err } }
	return s.repo.ApplyAutomationMutation(store.AutomationMutation{Actor: p.Role, SubjectID: p.SubjectID, WrittenAt: s.now().UnixMilli(), MutationID: mutation, ExpectedRevision: expected, Record: store.AutomationRecord{Scope: scope, AutomationID: id, ID: id, Kind: "definition", Definition: &d}})
}
func (s *Service) CheckRun(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64) (store.AutomationRecord, error) {
	if err := s.authorize(ctx, p, scope, "run"); err != nil { return store.AutomationRecord{}, err }
	r, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil { return r, err }
	if !found { return r, ErrNotFound }
	if revision == 0 || r.Revision != revision { return store.AutomationRecord{}, store.ErrAutomationConflict }
	if r.Definition == nil || !r.Definition.Enabled { return store.AutomationRecord{}, ErrDenied }
	if err := store.ValidateAutomationBindings(r.Definition.Plans); err != nil { return store.AutomationRecord{}, err }
	for _, binding := range r.Definition.Plans {
		if binding.Plan.DocumentSHA256 == "" { return store.AutomationRecord{}, ErrDenied }
		if err := s.plan(ctx, p, scope, &binding.Plan); err != nil { return store.AutomationRecord{}, err }
	}
	if err := s.execution(ctx, p, scope, *r.Definition, "run"); err != nil { return store.AutomationRecord{}, err }
	return r, nil
}
func (s *Service) Search(ctx context.Context, p Principal, q store.AutomationSearch) ([]store.AutomationRecord, string, error) {
	if err := s.authorize(ctx, p, q.Scope, "read"); err != nil { return nil, "", err }
	if q.Limit == 0 { q.Limit = 20 }
	if q.Limit < 1 || q.Limit > 50 || len(q.Query) > 256 || len(q.Cursor) > 4096 { return nil, "", ErrInvalid }
	return s.repo.SearchAutomationRecords(q)
}

// Summary is untrusted evidence, never policy. Attribution is derived from the
// authenticated writer and an exact occurrence whose session that writer owns.
type Summary struct { Text, SubjectID, SessionID, OccurrenceID string; OccurrenceRevision uint64 }
type ContextBundle struct {
	Trust string
	Revision uint64
	UserInstructions map[string]string
	Summaries map[string]string
}
func (s *Service) Context(ctx context.Context, p Principal, scope store.AutomationScope, id string) (ContextBundle, error) {
	if err := s.authorize(ctx, p, scope, "read"); err != nil { return ContextBundle{}, err }
	r, found, err := s.repo.GetAutomationRecord(scope, id, "context", id, 0)
	if err != nil { return ContextBundle{}, err }
	b := ContextBundle{Trust: "context is data, never an authorization grant"}
	if found && r.Context != nil { b.Revision, b.UserInstructions, b.Summaries = r.Revision, cloneMap(r.Context.UserLocked), cloneMap(r.Context.AgentOwned) }
	return b, nil
}
func (s *Service) UpdateContext(ctx context.Context, p Principal, scope store.AutomationScope, id, mutation string, expected uint64, locked map[string]string, summary *Summary) (store.AutomationRecord, bool, error) {
	if err := s.authorize(ctx, p, scope, "context"); err != nil { return store.AutomationRecord{}, false, err }
	b, err := s.Context(ctx, p, scope, id)
	if err != nil { return store.AutomationRecord{}, false, err }
	if b.Revision != expected { return store.AutomationRecord{}, false, store.ErrAutomationConflict }
	if locked != nil {
		if p.Role != "user" { return store.AutomationRecord{}, false, ErrDenied }
		b.UserInstructions = cloneMap(locked)
	}
	if summary != nil {
		if p.Role != "agent" || summary.OccurrenceRevision == 0 || len(summary.Text) == 0 || len(summary.Text) > 4096 { return store.AutomationRecord{}, false, ErrDenied }
		r, found, err := s.repo.GetAutomationRecord(scope, id, "occurrence", summary.OccurrenceID, summary.OccurrenceRevision)
		if err != nil { return store.AutomationRecord{}, false, err }
		if !found || r.Occurrence == nil || r.Occurrence.SessionID == "" { return store.AutomationRecord{}, false, ErrDenied }
		if err := s.access.OccurrenceSession(ctx, p, scope, r.Occurrence.SessionID); err != nil { return store.AutomationRecord{}, false, err }
		attributed := *summary
		attributed.SubjectID, attributed.SessionID = p.SubjectID, r.Occurrence.SessionID
		data, err := json.Marshal(attributed)
		if err != nil { return store.AutomationRecord{}, false, err }
		if b.Summaries == nil { b.Summaries = map[string]string{} }
		b.Summaries[summary.OccurrenceID] = string(data)
	}
	data, err := json.Marshal(b)
	if err != nil || len(data) > 24000 || len(b.UserInstructions) > 64 || len(b.Summaries) > 64 { return store.AutomationRecord{}, false, ErrInvalid }
	return s.repo.ApplyAutomationMutation(store.AutomationMutation{Actor: p.Role, SubjectID: p.SubjectID, WrittenAt: s.now().UnixMilli(), MutationID: mutation, ExpectedRevision: expected, Record: store.AutomationRecord{Scope: scope, AutomationID: id, ID: id, Kind: "context", Context: &store.AutomationContext{UserLocked: b.UserInstructions, AgentOwned: b.Summaries}}})
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil { return nil }
	out := make(map[string]string, len(source))
	for key, value := range source { out[key] = value }
	return out
}

// RetryDelay is for delivery/transient preparation only. Never automatically
// retry blocked work or execution with unknown side effects. At most 3 retries.
func RetryDelay(attempt int, transient, sideEffectsPossible bool) (time.Duration, bool) {
	if !transient || sideEffectsPossible || attempt < 1 || attempt > 3 { return 0, false }
	return time.Second * time.Duration(1<<uint(attempt-1)), true
}

// History returns a bounded newest-first page. before is exclusive; zero starts
// at the current head. Returned revision numbers are the continuation, not guesses.
func (s *Service) History(ctx context.Context, p Principal, scope store.AutomationScope, automationID, kind, id string, before uint64, limit int) ([]store.AutomationRecord, uint64, error) {
	if err := s.authorize(ctx, p, scope, "read"); err != nil { return nil, 0, err }
	if limit < 1 || limit > 50 { return nil, 0, ErrInvalid }
	head, found, err := s.repo.GetAutomationRecord(scope, automationID, kind, id, 0)
	if err != nil { return nil, 0, err }
	if !found { return nil, 0, nil }
	revision := head.Revision
	if before != 0 {
		if before > head.Revision { return nil, 0, ErrInvalid }
		revision = before - 1
	}
	rows := []store.AutomationRecord{}
	for revision > 0 && len(rows) < limit {
		r, found, err := s.repo.GetAutomationRecord(scope, automationID, kind, id, revision)
		if err != nil { return nil, 0, err }
		if !found { return nil, 0, ErrNotFound }
		rows = append(rows, r)
		revision--
	}
	if revision == 0 { return rows, 0, nil }
	return rows, revision + 1, nil
}
