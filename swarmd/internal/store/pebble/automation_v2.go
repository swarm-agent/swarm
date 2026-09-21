package pebblestore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// AutomationV2Settings contains user-authored policy only. Identity and grants
// are deliberately absent from this independently versioned contract.
type AutomationV2Settings struct {
	SchemaVersion    int                    `json:"schema_version"`
	Schedule         AutomationV2Schedule   `json:"schedule"`
	Missed           string                 `json:"missed"`
	Overlap          string                 `json:"overlap"`
	ActivateOnAccept bool                   `json:"activate_on_accept"`
	Expiration       AutomationV2Expiration `json:"expiration"`
	DailyRunCap      int                    `json:"daily_run_cap,omitempty"`
}
type AutomationV2Schedule struct {
	Kind            string `json:"kind"`
	IntervalSeconds int64  `json:"interval_seconds,omitempty"`
	Cron            string `json:"cron,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
}
type AutomationV2Expiration struct {
	Kind      string `json:"kind"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}
type AutomationV2Review struct {
	ProposalID string `json:"proposal_id"`
	Revision   uint64 `json:"revision"`
	Digest     string `json:"digest"`
}
type AutomationV2Proposal struct {
	AutomationV2Review
	AccountID      string              `json:"account_id"`
	UserID         string              `json:"user_id"`
	WorkspaceID    string              `json:"workspace_id"`
	SessionID      string              `json:"session_id"`
	Document       SessionPlanDocument `json:"document"`
	CreatedAt      int64               `json:"created_at"`
	BaseGeneration uint64              `json:"base_generation,omitempty"`
}
type AutomationV2Record struct {
	AutomationV2Proposal
	AutomationID  string                 `json:"automation_id"`
	AcceptedBy    string                 `json:"accepted_by"`
	AcceptedAt    int64                  `json:"accepted_at"`
	Authorization AutomationV2Expiration `json:"authorization"`
	Enabled       bool                   `json:"enabled"`
	Generation    uint64                 `json:"generation"`
	Cancelled     bool                   `json:"cancelled"`
	NextDueAt     int64                  `json:"next_due_at,omitempty"`
	CancelThrough int64                  `json:"cancel_through,omitempty"`
	Archived      bool                   `json:"archived,omitempty"`
	ArchivedAt    int64                  `json:"archived_at,omitempty"`
}
type SessionAutomationV2Binding struct {
	AutomationID string `json:"automation_id"`
	WorkspaceID  string `json:"workspace_id"`
	Digest       string `json:"digest"`
}

var ErrAutomationV2Conflict = errors.New("automation v2 ownership or review conflict")

func ValidateAutomationV2Settings(a *AutomationV2Settings, now int64) error {
	if a == nil {
		return nil
	}
	if a.SchemaVersion != 2 || !a.ActivateOnAccept || (a.Missed != "skip" && a.Missed != "coalesce") || (a.Overlap != "serialize" && a.Overlap != "independent") {
		return errors.New("invalid automation v2 policy")
	}
	if a.DailyRunCap < 0 {
		return errors.New("daily_run_cap must not be negative")
	}
	if a.Expiration.Kind != "indefinite" && a.Expiration.Kind != "at" {
		return errors.New("explicit expiration kind required")
	}
	if (a.Expiration.Kind == "indefinite" && a.Expiration.ExpiresAt != 0) || (a.Expiration.Kind == "at" && a.Expiration.ExpiresAt <= now) {
		return errors.New("invalid automation v2 expiration")
	}
	s := a.Schedule
	switch s.Kind {
	case "interval":
		if s.IntervalSeconds < 60 || s.IntervalSeconds > 31622400 || s.Cron != "" || s.Timezone != "" {
			return errors.New("interval requires elapsed interval_seconds from 60 to 31622400 and no cron/timezone; ask the user if wall time was intended")
		}
	case "cron":
		if s.IntervalSeconds != 0 || len(s.Cron) > 128 || s.Timezone == "" || s.Timezone == "Local" {
			return errors.New("cron requires five fields and an explicit IANA timezone, without interval_seconds; ask the user for missing wall time or timezone")
		}
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return errors.New("explicit IANA timezone required")
		}
		fields := strings.Fields(s.Cron)
		if len(fields) != 5 {
			return errors.New("five cron fields required")
		}
		bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		for i, f := range fields {
			if f == "*" {
				continue
			}
			step := strings.HasPrefix(f, "*/")
			if step {
				f = strings.TrimPrefix(f, "*/")
			}
			if f == "" {
				return errors.New("invalid cron field")
			}
			for _, c := range f {
				if c < '0' || c > '9' {
					return errors.New("cron supports numeric fields, * and */n only; ranges, lists and names are unsupported, never approximate the requested cadence")
				}
			}
			n, err := strconv.Atoi(f)
			lo, hi := bounds[i][0], bounds[i][1]
			if step {
				lo, hi = 1, hi-lo+1
			}
			if err != nil || n < lo || n > hi {
				return errors.New("cron field outside bounds")
			}
		}
	default:
		return errors.New("explicit schedule kind required")
	}
	return nil
}

func automationV2Key(kind, account, session string) string {
	return fmt.Sprintf("automation/v2/%s/%x/%x", kind, account, session)
}
func automationV2ID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("av2_%x", b), nil
}
func AutomationV2DocumentDigest(doc SessionPlanDocument) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	if len(b) > 256*1024 {
		return "", errors.New("automation document exceeds 256 KiB")
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// The participant is private: callers cannot inject arbitrary keys or grants.
type automationV2Mutation struct {
	proposal AutomationV2Proposal
	expected AutomationV2Review
	accept   bool
	decline  bool
	record   AutomationV2Record
	// The canonical session validator is injected to avoid a store/session import cycle.
	// It is required and invoked against the exact bytes inside the session lock.
	validate  func(*SessionPlanDocument) error
	execution *automationV2ExecutionMutation
}

func (s *SessionStore) automationV2OwnerStatus(account, user, workspace, id string) (SessionSnapshot, bool, int64, error) {
	current, ok, err := s.GetSession(id)
	if err != nil {
		return current, false, 0, err
	}
	isArchived := false
	archivedAt := int64(0)
	if !ok {
		tombstone, tombstoneOK, tombstoneErr := s.GetV3SessionTombstone(id)
		if tombstoneErr != nil {
			return current, false, 0, tombstoneErr
		}
		if tombstoneOK && tombstone.Archived && !tombstone.Deleted && tombstone.Session.ID != "" {
			current = tombstone.Session
			ok = true
			isArchived = true
			archivedAt = tombstone.UpdatedAt
		}
	}
	if !ok || account == "" || user == "" || workspace == "" || current.AccountScopeID != account || current.UserID != user {
		return current, false, 0, ErrAutomationV2Conflict
	}
	entry, err := s.automationV2WorkspaceOwner(account, user, workspace)
	if err != nil {
		return current, false, 0, err
	}
	for _, g := range current.WorkspaceGrants {
		if g.WorkspaceID == workspace && g.Kind == WorkspaceGrantPrimary && g.Available != nil && *g.Available && g.Path == entry.Path {
			return current, isArchived, archivedAt, nil
		}
	}
	return current, false, 0, ErrAutomationV2Conflict
}

func (s *SessionStore) automationV2Owner(account, user, workspace, id string) (SessionSnapshot, error) {
	session, isArchived, _, err := s.automationV2OwnerStatus(account, user, workspace, id)
	if err != nil {
		return session, err
	}
	if isArchived {
		return session, ErrAutomationV2Conflict
	}
	return session, nil
}
func (s *SessionStore) automationV2WorkspaceOwner(account, user, workspace string) (WorkspaceEntry, error) {
	member, exists, err := NewIdentityStore(s.store).GetAccountUser(account, user)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !exists || member.Status != "active" || member.AccountScopeID != account || member.UserID != user {
		return WorkspaceEntry{}, ErrAutomationV2Conflict
	}
	entry, exists, err := NewWorkspaceStore(s.store).GetByWorkspaceIDForAccount(account, workspace)
	if err != nil {
		return WorkspaceEntry{}, err
	}
	if !exists || entry.WorkspaceID != workspace {
		return WorkspaceEntry{}, ErrAutomationV2Conflict
	}
	return entry, nil
}
func (s *SessionStore) GetAutomationV2Proposal(account, user, workspace, id string) (AutomationV2Proposal, bool, error) {
	if _, err := s.automationV2Owner(account, user, workspace, id); err != nil {
		return AutomationV2Proposal{}, false, err
	}
	var p AutomationV2Proposal
	ok, err := s.store.GetJSON(automationV2Key("proposal", account, id), &p)
	if err != nil {
		return AutomationV2Proposal{}, false, err
	}
	if ok {
		if err := validateAutomationV2Integrity(p, account, user, workspace, id); err != nil {
			return AutomationV2Proposal{}, false, err
		}
		ps := NewPermissionStore(s.store)
		if perm, found, err := ps.GetPermission(id, AutomationV2PermissionID(p.ProposalID)); err == nil && found {
			if perm.Status == PermissionStatusDenied || perm.Status == PermissionStatusCancelled {
				return AutomationV2Proposal{}, false, nil
			}
		}
	}
	return p, ok, err
}
func (s *SessionStore) GetAutomationV2Record(account, user, workspace, id string) (AutomationV2Record, bool, error) {
	_, isArchived, archivedAt, err := s.automationV2OwnerStatus(account, user, workspace, id)
	if err == nil {
		var r AutomationV2Record
		ok, err := s.store.GetJSON(automationV2Key("accepted", account, id), &r)
		if err != nil {
			return AutomationV2Record{}, false, err
		}
		if ok {
			if err := validateAutomationV2Integrity(r.AutomationV2Proposal, account, user, workspace, id); err != nil {
				return AutomationV2Record{}, false, err
			}
			if r.AutomationID == "" || r.AcceptedBy != user || r.AcceptedAt <= 0 || r.Authorization != r.Document.AutomationV2.Expiration {
				return AutomationV2Record{}, false, ErrAutomationV2Conflict
			}
			r.Archived = isArchived
			r.ArchivedAt = archivedAt
			return r, true, nil
		}
	}
	// If lookup by session ID does not match, search by AutomationID in this workspace
	records, _, listErr := s.ListAutomationV2Records(account, user, workspace, "", 100, "include")
	if listErr == nil {
		for _, rec := range records {
			if rec.AutomationID == id {
				return rec, true, nil
			}
		}
	}
	if err != nil {
		return AutomationV2Record{}, false, err
	}
	return AutomationV2Record{}, false, nil
}

func validateAutomationV2Integrity(p AutomationV2Proposal, account, user, workspace, id string) error {
	if p.Document.AutomationV2 == nil && p.Document.WorkerV2 != nil {
		p.Document.AutomationV2 = p.Document.WorkerV2
	}
	if p.AccountID != account || p.UserID != user || p.WorkspaceID != workspace || p.SessionID != id || p.ProposalID == "" || p.Revision == 0 || p.Document.AutomationV2 == nil || p.Document.Automation != nil {
		return ErrAutomationV2Conflict
	}
	digest, err := AutomationV2DocumentDigest(p.Document)
	if err != nil {
		return err
	}
	if digest != p.Digest {
		if p.Document.WorkerV2 != nil {
			legacyDoc := p.Document
			legacyDoc.WorkerV2 = nil
			if legacyDigest, legErr := AutomationV2DocumentDigest(legacyDoc); legErr == nil && legacyDigest == p.Digest {
				if p.Document.WorkerV2 == nil && p.Document.AutomationV2 != nil {
					p.Document.WorkerV2 = p.Document.AutomationV2
				}
				return nil
			}
		}
		if p.Document.AutomationV2 != nil {
			workerOnlyDoc := p.Document
			workerOnlyDoc.AutomationV2 = nil
			if workerDigest, wErr := AutomationV2DocumentDigest(workerOnlyDoc); wErr == nil && workerDigest == p.Digest {
				if p.Document.AutomationV2 == nil && p.Document.WorkerV2 != nil {
					p.Document.AutomationV2 = p.Document.WorkerV2
				}
				return nil
			}
		}
		return ErrAutomationV2Conflict
	}
	if p.Document.WorkerV2 == nil && p.Document.AutomationV2 != nil {
		p.Document.WorkerV2 = p.Document.AutomationV2
	}
	return nil
}

// ProposeAutomationV2 stores the reviewed canonical document, but no automation,
// authorization, active plan, or run. Revisions compare the complete prior review.
func (s *SessionStore) ProposeAutomationV2(account, user, workspace, id string, doc SessionPlanDocument, expected AutomationV2Review, validate func(*SessionPlanDocument) error) (AutomationV2Proposal, error) {
	if _, err := s.automationV2Owner(account, user, workspace, id); err != nil {
		return AutomationV2Proposal{}, err
	}
	if doc.AutomationV2 == nil && doc.WorkerV2 != nil {
		doc.AutomationV2 = doc.WorkerV2
	}
	if doc.AutomationV2 == nil || doc.Automation != nil {
		return AutomationV2Proposal{}, ErrAutomationV2Conflict
	}
	if err := ValidateAutomationV2Settings(doc.AutomationV2, time.Now().UnixMilli()); err != nil {
		return AutomationV2Proposal{}, err
	}
	if expected != (AutomationV2Review{}) && (expected.ProposalID == "" || expected.Digest == "" || expected.Revision == 0) {
		return AutomationV2Proposal{}, ErrAutomationV2Conflict
	}
	// The pending plan also stores the revision as int. Reject either overflow.
	if expected.Revision >= uint64(^uint(0)>>1) {
		return AutomationV2Proposal{}, ErrAutomationV2Conflict
	}
	proposalID := expected.ProposalID
	if proposalID == "" {
		var err error
		proposalID, err = automationV2ID()
		if err != nil {
			return AutomationV2Proposal{}, err
		}
	}
	digest, err := AutomationV2DocumentDigest(doc)
	if err != nil {
		return AutomationV2Proposal{}, err
	}
	baseGeneration := uint64(0)
	if current, found, err := s.GetAutomationV2Record(account, user, workspace, id); err != nil {
		return AutomationV2Proposal{}, err
	} else if found {
		baseGeneration = current.Generation
	}
	p := AutomationV2Proposal{BaseGeneration: baseGeneration, AutomationV2Review: AutomationV2Review{proposalID, expected.Revision + 1, digest}, AccountID: account, UserID: user, WorkspaceID: workspace, SessionID: id, Document: doc, CreatedAt: time.Now().UnixMilli()}
	m := &automationV2Mutation{proposal: p, expected: expected, validate: validate}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2.proposed", ClientRequestID: fmt.Sprintf("av2:proposal:%s:%d", proposalID, p.Revision), PayloadHash: digest, automationV2: m})
	if err != nil {
		return AutomationV2Proposal{}, err
	}
	// Never regenerate a replay's timestamp or return an obsolete revision as head.
	persisted, ok, err := s.GetAutomationV2Proposal(account, user, workspace, id)
	if err != nil {
		return AutomationV2Proposal{}, err
	}
	if !ok || persisted.AutomationV2Review != p.AutomationV2Review {
		return AutomationV2Proposal{}, ErrAutomationV2Conflict
	}
	return persisted, nil
}
func (s *SessionStore) AcceptAutomationV2(account, user, workspace, id string, review AutomationV2Review, validate func(*SessionPlanDocument) error) (AutomationV2Record, error) {
	p, ok, err := s.GetAutomationV2Proposal(account, user, workspace, id)
	if err != nil {
		return AutomationV2Record{}, err
	}
	if !ok || p.AutomationV2Review != review {
		return AutomationV2Record{}, ErrAutomationV2Conflict
	}
	m := &automationV2Mutation{proposal: p, expected: review, accept: true, validate: validate}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2.accepted", ClientRequestID: fmt.Sprintf("av2:accept:%s:%d", review.ProposalID, review.Revision), PayloadHash: review.Digest, automationV2: m})
	if err != nil {
		return AutomationV2Record{}, err
	}
	r, ok, err := s.GetAutomationV2Record(account, user, workspace, id)
	if err == nil && !ok {
		err = errors.New("automation acceptance receipt missing")
	}
	return r, err
}
func (s *SessionStore) DeclineAutomationV2(account, user, workspace, id string, review AutomationV2Review) error {
	p, ok, err := s.GetAutomationV2Proposal(account, user, workspace, id)
	if err != nil {
		return err
	}
	if !ok || p.AutomationV2Review != review {
		return ErrAutomationV2Conflict
	}
	m := &automationV2Mutation{proposal: p, expected: review, decline: true}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2.declined", ClientRequestID: fmt.Sprintf("av2:decline:%s:%d", review.ProposalID, review.Revision), PayloadHash: review.Digest, automationV2: m})
	return err
}

func (s *SessionStore) prepareAutomationV2(in *V3SessionMutationInput) error {
	m := in.automationV2
	if m == nil {
		return nil
	}
	if m.execution != nil {
		return s.prepareAutomationV2Execution(in)
	}
	p := m.proposal
	if m.decline {
		if run, ok, err := s.GetV3SessionActiveRunIntent(in.SessionID); err != nil {
			return err
		} else if ok && (run.Status == V3RunIntentRunning || run.Status == V3RunIntentPendingExecutor) {
			return ErrAutomationV2Conflict
		}
		var prior AutomationV2Proposal
		found, err := s.store.GetJSON(automationV2Key("proposal", p.AccountID, p.SessionID), &prior)
		if err != nil {
			return err
		}
		if !found || prior.AutomationV2Review != m.expected {
			return ErrAutomationV2Conflict
		}
		payload, err := json.Marshal(map[string]any{"proposal_id": p.ProposalID, "revision": p.Revision, "digest": p.Digest, "declined": true})
		in.EventPayload = payload
		return err
	}
	if m.validate == nil {
		return errors.New("canonical executable validator required")
	}
	if err := m.validate(&p.Document); err != nil {
		return err
	}
	if err := validateAutomationV2Integrity(p, in.AccountScopeID, in.UserID, p.WorkspaceID, in.SessionID); err != nil {
		return err
	}
	current, err := s.automationV2Owner(in.AccountScopeID, in.UserID, p.WorkspaceID, in.SessionID)
	if err != nil {
		return err
	}
	if current.Automation != nil {
		return ErrAutomationV2Conflict
	}
	var active AutomationV2Record
	if found, err := s.store.GetJSON(automationV2Key("accepted", p.AccountID, p.SessionID), &active); err != nil {
		return err
	} else if found {
		if active.Cancelled || active.Generation != p.BaseGeneration {
			return ErrAutomationV2Conflict
		}
	} else if p.BaseGeneration != 0 {
		return ErrAutomationV2Conflict
	}
	if m.accept {
		if run, ok, err := s.GetV3SessionActiveRunIntent(in.SessionID); err != nil {
			return err
		} else if ok && (run.Status == V3RunIntentRunning || run.Status == V3RunIntentPendingExecutor) {
			return ErrAutomationV2Conflict
		}
	}
	var prior AutomationV2Proposal
	found, err := s.store.GetJSON(automationV2Key("proposal", p.AccountID, p.SessionID), &prior)
	if err != nil {
		return err
	}
	if found {
		if err := validateAutomationV2Integrity(prior, in.AccountScopeID, in.UserID, p.WorkspaceID, in.SessionID); err != nil {
			return err
		}
	}
	if (found && prior.AutomationV2Review != m.expected) || (!found && m.expected != (AutomationV2Review{})) {
		return ErrAutomationV2Conflict
	}
	if m.accept {
		if !found || prior.AutomationV2Review != p.AutomationV2Review || ValidateAutomationV2Settings(prior.Document.AutomationV2, time.Now().UnixMilli()) != nil {
			return ErrAutomationV2Conflict
		}
		id, err := automationV2ID()
		if err != nil {
			return err
		}
		generation := uint64(1)
		var previous AutomationV2Record
		if exists, err := s.store.GetJSON(automationV2Key("accepted", p.AccountID, p.SessionID), &previous); err != nil {
			return err
		} else if exists {
			if err := validateAutomationV2Integrity(previous.AutomationV2Proposal, p.AccountID, p.UserID, p.WorkspaceID, p.SessionID); err != nil {
				return err
			}
			if previous.Cancelled || previous.Revision >= prior.Revision {
				return ErrAutomationV2Conflict
			}
			id, generation = previous.AutomationID, previous.Generation+1
		}
		m.record = AutomationV2Record{AutomationV2Proposal: prior, AutomationID: id, AcceptedBy: in.UserID, AcceptedAt: time.Now().UnixMilli(), Authorization: prior.Document.AutomationV2.Expiration, Enabled: true, Generation: generation, CancelThrough: previous.CancelThrough}
		m.record.NextDueAt, err = AutomationV2NextDue(*prior.Document.AutomationV2, m.record.AcceptedAt, m.record.AcceptedAt)
		if err != nil {
			return err
		}
		current.AutomationV2 = &SessionAutomationV2Binding{id, p.WorkspaceID, p.Digest}
		if current.Metadata != nil {
			delete(current.Metadata, "navigation_hidden")
		}
		in.Session = &current
	}
	payload, err := json.Marshal(map[string]any{"proposal_id": p.ProposalID, "revision": p.Revision, "digest": p.Digest, "automation_id": m.record.AutomationID})
	in.EventPayload = payload
	return err
}
func (s *SessionStore) setAutomationV2InBatch(batch *pebble.Batch, in V3SessionMutationInput) error {
	m := in.automationV2
	if m == nil {
		return nil
	}
	if m.execution != nil {
		return s.setAutomationV2ExecutionInBatch(batch, in)
	}
	p := m.proposal
	if m.decline {
		if err := batch.Delete([]byte(automationV2Key("proposal", p.AccountID, p.SessionID)), nil); err != nil {
			return err
		}
		plan := SessionPlanSnapshot{ID: p.ProposalID, SessionID: p.SessionID, AccountScopeID: p.AccountID, UserID: p.UserID, Title: p.Document.Title, Status: "declined", ApprovalState: "declined", Version: int(p.Revision), Document: &p.Document, CreatedAt: p.CreatedAt, UpdatedAt: time.Now().UnixMilli()}
		if err := setPlanAcceptancePlanInBatch(batch, plan, nil); err != nil {
			return err
		}
		return s.setAutomationV2PermissionInBatch(batch, m)
	}
	var value any = p
	kind := "proposal"
	if m.accept {
		kind, value = "accepted", m.record
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(automationV2Key(kind, p.AccountID, p.SessionID)), b, nil); err != nil {
		return err
	}
	if m.accept {
		// Approve the durable executable snapshot without an ordinary active-plan
		// pointer or run intent. The reviewed document itself stays byte-exact.
		plan := SessionPlanSnapshot{ID: p.ProposalID, SessionID: p.SessionID, AccountScopeID: p.AccountID, UserID: p.UserID, Title: p.Document.Title, Status: "approved", ApprovalState: "approved", Version: int(p.Revision), Document: &p.Document, CreatedAt: p.CreatedAt, UpdatedAt: m.record.AcceptedAt}
		if err := setPlanAcceptancePlanInBatch(batch, plan, nil); err != nil {
			return err
		}
	}
	if !m.accept {
		// Immutable proposal history is committed with its current-head pointer.
		historyKey := automationV2Key("history", p.AccountID, p.SessionID) + fmt.Sprintf("/%x/%020d", p.ProposalID, p.Revision)
		if err := batch.Set([]byte(historyKey), b, nil); err != nil {
			return err
		}
		plan := SessionPlanSnapshot{ID: p.ProposalID, SessionID: p.SessionID, AccountScopeID: p.AccountID, UserID: p.UserID, Title: p.Document.Title, Status: "pending", ApprovalState: "pending", Version: int(p.Revision), Document: &p.Document, CreatedAt: p.CreatedAt, UpdatedAt: p.CreatedAt}
		if err := setPlanAcceptancePlanInBatch(batch, plan, nil); err != nil {
			return err
		}
	}
	if err := s.setAutomationV2PermissionInBatch(batch, m); err != nil {
		return err
	}
	if hook := s.store.sessionMutations.beforeAutomationV2Commit; hook != nil {
		return hook(in.SessionID)
	}
	return nil
}
func (s *SessionStore) SetAutomationV2CommitHookForTest(hook func(string) error) func() {
	prior := s.store.sessionMutations.beforeAutomationV2Commit
	s.store.sessionMutations.beforeAutomationV2Commit = hook
	return func() { s.store.sessionMutations.beforeAutomationV2Commit = prior }
}

// ListAutomationV2Records uses an exclusive opaque storage cursor and caps both
// visited records and returned bytes. Foreign session records are never emitted.
func (s *SessionStore) ListAutomationV2Records(account, user, workspace, after string, limit int, archivedMode ...string) ([]AutomationV2Record, string, error) {
	mode := "exclude"
	if len(archivedMode) > 0 && archivedMode[0] != "" {
		mode = archivedMode[0]
	}
	if mode != "exclude" && mode != "include" && mode != "only" {
		return nil, "", ErrAutomationV2Conflict
	}
	if account == "" || user == "" || workspace == "" || limit < 1 || limit > 100 {
		return nil, "", ErrAutomationV2Conflict
	}
	if _, err := s.automationV2WorkspaceOwner(account, user, workspace); err != nil {
		return nil, "", err
	}
	prefix := fmt.Sprintf("automation/v2/accepted/%x/", account)
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, "", ErrAutomationV2Conflict
	}
	lower := prefix
	if after != "" {
		lower = after + "\x00"
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(lower), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	out := []AutomationV2Record{}
	next := ""
	bytes := 0
	for visited, valid := 0, iter.First(); valid; valid = iter.Next() {
		if visited >= 100 || len(out) >= limit || bytes >= 1024*1024 {
			return out, next, nil
		}
		visited++
		next = string(iter.Key())
		var r AutomationV2Record
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			return nil, "", err
		}
		if r.UserID != user || r.WorkspaceID != workspace {
			continue
		}
		if err := validateAutomationV2Integrity(r.AutomationV2Proposal, account, user, workspace, r.SessionID); err != nil {
			return nil, "", err
		}
		_, isArchived, archivedAt, err := s.automationV2OwnerStatus(account, user, workspace, r.SessionID)
		if err != nil {
			continue
		}
		r.Archived = isArchived
		r.ArchivedAt = archivedAt
		if mode == "exclude" && isArchived {
			continue
		}
		if mode == "only" && !isArchived {
			continue
		}
		bytes += len(iter.Value())
		out = append(out, r)
	}
	return out, "", iter.Error()
}
