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
	WorkspaceID      string                 `json:"workspace_id,omitempty"`
	WorkspaceIDs     []string               `json:"workspace_ids,omitempty"`
	Schedule         AutomationV2Schedule   `json:"schedule"`
	Missed           string                 `json:"missed"`
	Overlap          string                 `json:"overlap"`
	ActivateOnAccept bool                   `json:"activate_on_accept"`
	Expiration       AutomationV2Expiration `json:"expiration"`
	DailyRunCap      int                    `json:"daily_run_cap,omitempty"`
	Webhooks         []AutomationV2Webhook  `json:"webhooks,omitempty"`
}

type AutomationV2Webhook struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Secret  string   `json:"secret,omitempty"`
	Format  string   `json:"format,omitempty"` // "generic" | "slack" | "discord" | "telegram"
	Events  []string `json:"events,omitempty"` // ["*"] or ["started", "succeeded", "failed", "retry_exhausted"]
	Enabled bool     `json:"enabled"`
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
	WorkspaceIDs   []string            `json:"workspace_ids,omitempty"`
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
	// Independent workers do not turn their authoring conversation into a worker.
	// Absent on pre-existing accepted records, which retain legacy binding rules.
	Independent bool `json:"independent,omitempty"`
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
	if a.Expiration.Kind == "" {
		a.Expiration.Kind = "indefinite"
	}
	if a.Schedule.Kind == "" {
		a.Schedule.Kind = "trigger"
	}
	if a.SchemaVersion == 0 {
		a.SchemaVersion = 2
	}
	if !a.ActivateOnAccept {
		a.ActivateOnAccept = true
	}
	if a.Missed == "" {
		a.Missed = "skip"
	}
	if a.Overlap == "" {
		a.Overlap = "serialize"
	}
	if a.SchemaVersion != 2 || !a.ActivateOnAccept || (a.Missed != "skip" && a.Missed != "coalesce") || (a.Overlap != "serialize" && a.Overlap != "independent") {
		return errors.New("invalid automation v2 policy")
	}
	if len(a.WorkspaceID) > 256 {
		return errors.New("workspace_id exceeds 256 characters")
	}
	for _, id := range a.WorkspaceIDs {
		if len(id) > 256 {
			return errors.New("workspace_ids element exceeds 256 characters")
		}
	}
	if a.DailyRunCap < 0 {
		return errors.New("daily_run_cap must not be negative")
	}
	for _, wh := range a.Webhooks {
		cleanURL := strings.TrimSpace(wh.URL)
		if cleanURL == "" {
			return errors.New("webhook url is required")
		}
		if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
			return errors.New("webhook url must begin with http:// or https://")
		}
		if wh.Format != "" && wh.Format != "generic" && wh.Format != "slack" && wh.Format != "discord" && wh.Format != "telegram" {
			return errors.New("unsupported webhook format")
		}
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
		if fields[2] != "*" && fields[4] != "*" {
			return errors.New("both cron day fields cannot be restricted")
		}
	case "trigger", "":
		if s.IntervalSeconds != 0 || s.Cron != "" {
			return errors.New("trigger schedule must not declare interval_seconds or cron")
		}
		if a.Schedule.Kind == "" {
			a.Schedule.Kind = "trigger"
		}
	default:
		return errors.New("explicit schedule kind required")
	}
	return nil
}

func automationV2Key(kind, account, session string) string {
	return fmt.Sprintf("automation/v2/%s/%x/%x", kind, account, session)
}

func automationV2ProposalKey(account, proposalID string) string {
	return fmt.Sprintf("automation/v2/proposal_id/%x/%x", account, proposalID)
}

func automationV2SessionProposalKey(account, sessionID, proposalID string) string {
	return fmt.Sprintf("automation/v2/session_proposal/%x/%x/%x", account, sessionID, proposalID)
}

func automationV2SessionWorkerKey(account, sessionID, automationID string) string {
	return fmt.Sprintf("automation/v2/session_workers/%x/%x/%x", account, sessionID, automationID)
}

func (s *SessionStore) findAcceptedWorker(account, user, workspace, targetID, sessionID string) (AutomationV2Record, bool, error) {
	if targetID == "" && sessionID == "" {
		return AutomationV2Record{}, false, nil
	}
	var r AutomationV2Record
	if targetID != "" {
		if ok, err := s.store.GetJSON(automationV2Key("accepted", account, targetID), &r); err == nil && ok {
			if r.AutomationID != "" && (user == "" || r.AcceptedBy == user) && (workspace == "" || r.WorkspaceID == workspace) {
				return r, true, nil
			}
		}
	}
	if sessionID != "" {
		if ok, err := s.store.GetJSON(automationV2Key("accepted", account, sessionID), &r); err == nil && ok {
			if r.AutomationID != "" && (user == "" || r.AcceptedBy == user) && (workspace == "" || r.WorkspaceID == workspace) {
				if targetID == "" || r.AutomationID == targetID || r.ProposalID == targetID || r.SessionID == targetID {
					return r, true, nil
				}
			}
		}
	}
	prefix := fmt.Sprintf("automation/v2/accepted/%x/", account)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return AutomationV2Record{}, false, err
	}
	defer iter.Close()
	for valid := iter.First(); valid; valid = iter.Next() {
		var rec AutomationV2Record
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		matchesWorkspace := workspace == "" || rec.WorkspaceID == workspace
		if !matchesWorkspace {
			for _, wid := range rec.WorkspaceIDs {
				if wid == workspace {
					matchesWorkspace = true
					break
				}
			}
		}
		if (user != "" && rec.AcceptedBy != user) || !matchesWorkspace {
			continue
		}
		if rec.AutomationID == targetID || rec.ProposalID == targetID || rec.SessionID == targetID || (sessionID != "" && rec.SessionID == sessionID) {
			return rec, true, nil
		}
	}
	return AutomationV2Record{}, false, nil
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
	if _, err := s.automationV2WorkspaceOwner(account, user, workspace); err != nil {
		return current, false, 0, err
	}
	return current, isArchived, archivedAt, nil
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
	var p AutomationV2Proposal
	found := false
	if ok, err := s.store.GetJSON(automationV2Key("proposal", account, id), &p); err == nil && ok {
		found = true
	} else if ok, err := s.store.GetJSON(automationV2ProposalKey(account, id), &p); err == nil && ok {
		found = true
	}
	if !found {
		prefix := fmt.Sprintf("automation/v2/session_proposal/%x/%x/", account, id)
		if iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")}); err == nil {
			for valid := iter.Last(); valid; valid = false {
				if err := json.Unmarshal(iter.Value(), &p); err == nil {
					found = true
				}
			}
			iter.Close()
		}
	}
	if !found {
		return AutomationV2Proposal{}, false, nil
	}
	if _, err := s.automationV2Owner(account, user, p.WorkspaceID, p.SessionID); err != nil {
		return AutomationV2Proposal{}, false, err
	}
	if err := validateAutomationV2Integrity(p, account, user, workspace, id); err != nil {
		return AutomationV2Proposal{}, false, err
	}
	ps := NewPermissionStore(s.store)
	if perm, permFound, err := ps.GetPermission(p.SessionID, AutomationV2PermissionID(p.ProposalID)); err == nil && permFound {
		if perm.Status == PermissionStatusDenied || perm.Status == PermissionStatusCancelled {
			return AutomationV2Proposal{}, false, nil
		}
	}
	return p, true, nil
}
func (s *SessionStore) GetAutomationV2Record(account, user, workspace, id string) (AutomationV2Record, bool, error) {
	var r AutomationV2Record
	targetWorkspace := workspace
	if targetWorkspace == "all" {
		targetWorkspace = ""
	}
	if ok, err := s.store.GetJSON(automationV2Key("accepted", account, id), &r); err == nil && ok {
		if r.AutomationID != "" && (user == "" || r.AcceptedBy == user) {
			wsMatch := targetWorkspace == "" || r.WorkspaceID == targetWorkspace
			if !wsMatch && len(r.WorkspaceIDs) > 0 {
				for _, wid := range r.WorkspaceIDs {
					if wid == targetWorkspace {
						wsMatch = true
						break
					}
				}
			}
			if wsMatch {
				_, isArchived, archivedAt, err := s.automationV2OwnerStatus(account, user, r.WorkspaceID, r.SessionID)
				if err == nil {
					r.Archived = isArchived
					r.ArchivedAt = archivedAt
					return r, true, nil
				}
			}
		}
	}
	records, _, listErr := s.ListAutomationV2Records(account, user, targetWorkspace, "", 100, "include")
	if listErr == nil {
		for _, rec := range records {
			if rec.AutomationID == id || rec.SessionID == id || rec.ProposalID == id {
				return rec, true, nil
			}
		}
	}
	if targetWorkspace != "" {
		allRecords, _, allErr := s.ListAutomationV2Records(account, user, "", "", 100, "include")
		if allErr == nil {
			for _, rec := range allRecords {
				if rec.AutomationID == id || rec.SessionID == id || rec.ProposalID == id {
					return rec, true, nil
				}
			}
		}
	}
	return AutomationV2Record{}, false, nil
}

func validateAutomationV2Integrity(p AutomationV2Proposal, account, user, workspace, id string) error {
	if p.Document.AutomationV2 == nil && p.Document.WorkerV2 != nil {
		p.Document.AutomationV2 = p.Document.WorkerV2
	}
	targetWorkspace := workspace
	if targetWorkspace == "all" {
		targetWorkspace = ""
	}
	if p.AccountID != account || p.UserID != user || (targetWorkspace != "" && p.WorkspaceID != targetWorkspace) || p.ProposalID == "" || p.Revision == 0 || p.Document.AutomationV2 == nil || p.Document.Automation != nil {
		return ErrAutomationV2Conflict
	}
	if id != "" && p.SessionID != id && p.ProposalID != id {
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
	if expected != (AutomationV2Review{}) {
		if prev, found, err := s.findAcceptedWorker(account, user, workspace, expected.ProposalID, id); err != nil {
			return AutomationV2Proposal{}, err
		} else if found {
			baseGeneration = prev.Generation
		}
	}
	var workspaceIDs []string
	if doc.AutomationV2 != nil {
		if doc.AutomationV2.WorkspaceID != "" {
			workspaceIDs = append(workspaceIDs, doc.AutomationV2.WorkspaceID)
		}
		workspaceIDs = append(workspaceIDs, doc.AutomationV2.WorkspaceIDs...)
	}
	if doc.WorkerV2 != nil {
		if doc.WorkerV2.WorkspaceID != "" {
			workspaceIDs = append(workspaceIDs, doc.WorkerV2.WorkspaceID)
		}
		workspaceIDs = append(workspaceIDs, doc.WorkerV2.WorkspaceIDs...)
	}
	deduped := make([]string, 0, len(workspaceIDs))
	seenWS := make(map[string]struct{}, len(workspaceIDs))
	for _, wid := range workspaceIDs {
		wid = strings.TrimSpace(wid)
		if wid == "" {
			continue
		}
		if _, ok := seenWS[wid]; !ok {
			seenWS[wid] = struct{}{}
			deduped = append(deduped, wid)
		}
	}
	p := AutomationV2Proposal{BaseGeneration: baseGeneration, AutomationV2Review: AutomationV2Review{proposalID, expected.Revision + 1, digest}, AccountID: account, UserID: user, WorkspaceID: workspace, WorkspaceIDs: deduped, SessionID: id, Document: doc, CreatedAt: time.Now().UnixMilli()}
	m := &automationV2Mutation{proposal: p, expected: expected, validate: validate}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2.proposed", ClientRequestID: fmt.Sprintf("av2:proposal:%s:%d", proposalID, p.Revision), PayloadHash: digest, automationV2: m})
	if err != nil {
		return AutomationV2Proposal{}, err
	}
	// Never regenerate a replay's timestamp or return an obsolete revision as head.
	persisted, ok, err := s.GetAutomationV2Proposal(account, user, workspace, p.ProposalID)
	if !ok {
		persisted, ok, err = s.GetAutomationV2Proposal(account, user, workspace, id)
	}
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
	if !ok && review.ProposalID != "" {
		p, ok, err = s.GetAutomationV2Proposal(account, user, workspace, review.ProposalID)
		if err != nil {
			return AutomationV2Record{}, err
		}
	}
	if !ok || p.AutomationV2Review != review {
		return AutomationV2Record{}, ErrAutomationV2Conflict
	}
	m := &automationV2Mutation{proposal: p, expected: review, accept: true, validate: validate}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, AccountScopeID: account, UserID: user, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation_v2.accepted", ClientRequestID: fmt.Sprintf("av2:accept:%s:%d", review.ProposalID, review.Revision), PayloadHash: review.Digest, automationV2: m})
	if err != nil {
		return AutomationV2Record{}, err
	}
	r, ok, err := s.GetAutomationV2Record(account, user, workspace, m.record.AutomationID)
	if err == nil && !ok {
		r, ok, err = s.GetAutomationV2Record(account, user, workspace, id)
	}
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
	if !ok && review.ProposalID != "" {
		p, ok, err = s.GetAutomationV2Proposal(account, user, workspace, review.ProposalID)
		if err != nil {
			return err
		}
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
		// Declining an exact pending worker review does not stop the authoring
		// conversation or its active run.
		var prior AutomationV2Proposal
		found := false
		if m.expected.ProposalID != "" {
			if ok, err := s.store.GetJSON(automationV2ProposalKey(p.AccountID, m.expected.ProposalID), &prior); err == nil && ok {
				found = true
			}
		}
		if !found {
			if ok, err := s.store.GetJSON(automationV2Key("proposal", p.AccountID, p.SessionID), &prior); err == nil && ok && (m.expected.ProposalID == "" || prior.ProposalID == m.expected.ProposalID) {
				found = true
			}
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
	if p.BaseGeneration != 0 {
		active, found, err := s.findAcceptedWorker(p.AccountID, in.UserID, p.WorkspaceID, m.expected.ProposalID, p.SessionID)
		if err != nil {
			return err
		}
		if !found || active.Cancelled || active.Generation != p.BaseGeneration {
			return ErrAutomationV2Conflict
		}
	}
	var prior AutomationV2Proposal
	found := false
	if m.expected != (AutomationV2Review{}) {
		if m.expected.ProposalID != "" {
			if ok, err := s.store.GetJSON(automationV2ProposalKey(p.AccountID, m.expected.ProposalID), &prior); err == nil && ok {
				found = true
			} else if ok, err := s.store.GetJSON(automationV2Key("proposal", p.AccountID, p.SessionID), &prior); err == nil && ok && prior.ProposalID == m.expected.ProposalID {
				found = true
			} else if rec, recFound, _ := s.findAcceptedWorker(p.AccountID, in.UserID, p.WorkspaceID, m.expected.ProposalID, p.SessionID); recFound {
				prior = rec.AutomationV2Proposal
				found = true
			}
		}
		if !found || prior.AutomationV2Review != m.expected {
			return ErrAutomationV2Conflict
		}
		if err := validateAutomationV2Integrity(prior, in.AccountScopeID, in.UserID, prior.WorkspaceID, prior.SessionID); err != nil {
			return err
		}
	} else {
		// New proposal without expected review: no prior review to match
		if ok, err := s.store.GetJSON(automationV2ProposalKey(p.AccountID, p.ProposalID), &prior); err == nil && ok {
			found = true
		}
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
		if p.BaseGeneration != 0 {
			prev, prevFound, prevErr := s.findAcceptedWorker(p.AccountID, in.UserID, p.WorkspaceID, m.expected.ProposalID, p.SessionID)
			if prevErr != nil {
				return prevErr
			}
			if !prevFound {
				return ErrAutomationV2Conflict
			}
			previous = prev
			if err := validateAutomationV2Integrity(previous.AutomationV2Proposal, p.AccountID, p.UserID, p.WorkspaceID, previous.SessionID); err != nil {
				return err
			}
			if previous.Cancelled || previous.Revision >= prior.Revision {
				return ErrAutomationV2Conflict
			}
			id, generation = previous.AutomationID, previous.Generation+1
		}
		m.record = AutomationV2Record{AutomationV2Proposal: prior, AutomationID: id, AcceptedBy: in.UserID, AcceptedAt: time.Now().UnixMilli(), Authorization: prior.Document.AutomationV2.Expiration, Enabled: true, Generation: generation, CancelThrough: previous.CancelThrough, Independent: true}
		m.record.NextDueAt, err = AutomationV2NextDue(*prior.Document.AutomationV2, m.record.AcceptedAt, m.record.AcceptedAt)
		if err != nil {
			return err
		}
		// Legacy accepted records bound this conversation and made it read-only.
		// A reviewed revision detaches that binding atomically with the new grant.
		// New workers never bind the authoring session at all.
		if current.AutomationV2 != nil {
			if !found || previous.Independent || current.AutomationV2.AutomationID != id || current.AutomationV2.WorkspaceID != p.WorkspaceID {
				return ErrAutomationV2Conflict
			}
			current.AutomationV2 = nil
			in.Session = &current
		}
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
		_ = batch.Delete([]byte(automationV2Key("proposal", p.AccountID, p.SessionID)), nil)
		_ = batch.Delete([]byte(automationV2ProposalKey(p.AccountID, p.ProposalID)), nil)
		_ = batch.Delete([]byte(automationV2SessionProposalKey(p.AccountID, p.SessionID, p.ProposalID)), nil)
		plan := SessionPlanSnapshot{ID: p.ProposalID, SessionID: p.SessionID, AccountScopeID: p.AccountID, UserID: p.UserID, Title: p.Document.Title, Status: "declined", ApprovalState: "declined", Version: int(p.Revision), Document: &p.Document, CreatedAt: p.CreatedAt, UpdatedAt: time.Now().UnixMilli()}
		if err := setPlanAcceptancePlanInBatch(batch, plan, nil); err != nil {
			return err
		}
		return s.setAutomationV2PermissionInBatch(batch, m)
	}
	var value any = p
	if m.accept {
		value = m.record
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !m.accept {
		if err := batch.Set([]byte(automationV2ProposalKey(p.AccountID, p.ProposalID)), b, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(automationV2SessionProposalKey(p.AccountID, p.SessionID, p.ProposalID)), b, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(automationV2Key("proposal", p.AccountID, p.SessionID)), b, nil); err != nil {
			return err
		}
	} else {
		var priorRec AutomationV2Record
		if ok, err := s.store.GetJSON(automationV2Key("accepted", p.AccountID, p.SessionID), &priorRec); err == nil && ok {
			if priorRec.AutomationID != "" && priorRec.AutomationID != m.record.AutomationID && priorRec.WorkspaceID == m.record.WorkspaceID {
				_ = batch.Delete([]byte(automationV2Key("accepted", p.AccountID, priorRec.AutomationID)), nil)
				_ = batch.Delete([]byte(automationV2SessionWorkerKey(p.AccountID, p.SessionID, priorRec.AutomationID)), nil)
			}
		}
		if err := batch.Set([]byte(automationV2Key("accepted", p.AccountID, m.record.AutomationID)), b, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(automationV2SessionWorkerKey(p.AccountID, p.SessionID, m.record.AutomationID)), b, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(automationV2Key("accepted", p.AccountID, p.SessionID)), b, nil); err != nil {
			return err
		}
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
	if account == "" || user == "" || limit < 1 || limit > 100 {
		return nil, "", ErrAutomationV2Conflict
	}
	targetWorkspace := workspace
	if targetWorkspace == "all" {
		targetWorkspace = ""
	}
	if targetWorkspace != "" {
		if _, err := s.automationV2WorkspaceOwner(account, user, targetWorkspace); err != nil {
			return nil, "", err
		}
	} else {
		member, exists, err := NewIdentityStore(s.store).GetAccountUser(account, user)
		if err != nil {
			return nil, "", err
		}
		if !exists || member.Status != "active" || member.AccountScopeID != account || member.UserID != user {
			return nil, "", ErrAutomationV2Conflict
		}
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
	raw := []AutomationV2Record{}
	next := ""
	bytes := 0
	seenID := make(map[string]bool)
	for visited, valid := 0, iter.First(); valid; valid = iter.Next() {
		if visited >= 100 || len(raw) >= limit*3 || bytes >= 1024*1024 {
			break
		}
		visited++
		next = string(iter.Key())
		var r AutomationV2Record
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			return nil, "", err
		}
		if seenID[r.AutomationID] {
			continue
		}
		matchesWorkspace := targetWorkspace == "" || r.WorkspaceID == targetWorkspace
		if !matchesWorkspace {
			for _, wid := range r.WorkspaceIDs {
				if wid == targetWorkspace {
					matchesWorkspace = true
					break
				}
			}
		}
		if r.UserID != user || !matchesWorkspace {
			continue
		}
		if err := validateAutomationV2Integrity(r.AutomationV2Proposal, account, user, r.WorkspaceID, r.SessionID); err != nil {
			return nil, "", err
		}
		_, isArchived, archivedAt, err := s.automationV2OwnerStatus(account, user, r.WorkspaceID, r.SessionID)
		if err != nil {
			continue
		}
		seenID[r.AutomationID] = true
		r.Archived = isArchived
		r.ArchivedAt = archivedAt
		if mode == "exclude" && isArchived {
			continue
		}
		if mode == "only" && !isArchived {
			continue
		}
		bytes += len(iter.Value())
		raw = append(raw, r)
	}

	dedupMap := make(map[string]int)
	out := []AutomationV2Record{}
	for _, r := range raw {
		title := strings.ToLower(strings.TrimSpace(r.Document.Title))
		stableKey := r.SessionID
		if stableKey == "" {
			stableKey = r.AutomationID
		}
		key := fmt.Sprintf("%s:%s", r.WorkspaceID, stableKey)
		titleKey := ""
		if title != "" {
			titleKey = fmt.Sprintf("%s:title:%s", r.WorkspaceID, title)
		}

		targetIdx := -1
		if idx, ok := dedupMap[key]; ok {
			targetIdx = idx
		} else if titleKey != "" {
			if idx, ok := dedupMap[titleKey]; ok {
				targetIdx = idx
			}
		}

		if targetIdx >= 0 {
			existing := out[targetIdx]
			if isNewerWorkerRecord(r, existing) {
				out[targetIdx] = r
			}
		} else {
			dedupMap[key] = len(out)
			if titleKey != "" {
				dedupMap[titleKey] = len(out)
			}
			out = append(out, r)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, next, iter.Error()
}

func isNewerWorkerRecord(a, b AutomationV2Record) bool {
	if a.Generation != b.Generation {
		return a.Generation > b.Generation
	}
	if a.Revision != b.Revision {
		return a.Revision > b.Revision
	}
	return a.AcceptedAt >= b.AcceptedAt
}
