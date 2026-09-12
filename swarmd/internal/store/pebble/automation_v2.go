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
	SchemaVersion int `json:"schema_version"`
	Schedule AutomationV2Schedule `json:"schedule"`
	Missed string `json:"missed"`
	Overlap string `json:"overlap"`
	ActivateOnAccept bool `json:"activate_on_accept"`
	Expiration AutomationV2Expiration `json:"expiration"`
}
type AutomationV2Schedule struct {
	Kind string `json:"kind"`
	IntervalSeconds int64 `json:"interval_seconds,omitempty"`
	Cron string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}
type AutomationV2Expiration struct {
	Kind string `json:"kind"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}
type AutomationV2Review struct {
	ProposalID string `json:"proposal_id"`
	Revision uint64 `json:"revision"`
	Digest string `json:"digest"`
}
type AutomationV2Proposal struct {
	AutomationV2Review
	AccountID string `json:"account_id"`
	UserID string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	SessionID string `json:"session_id"`
	Document SessionPlanDocument `json:"document"`
	CreatedAt int64 `json:"created_at"`
}
type AutomationV2Record struct {
	AutomationV2Proposal
	AutomationID string `json:"automation_id"`
	AcceptedBy string `json:"accepted_by"`
	AcceptedAt int64 `json:"accepted_at"`
	Authorization AutomationV2Expiration `json:"authorization"`
	Enabled bool `json:"enabled"`
}
type SessionAutomationV2Binding struct {
	AutomationID string `json:"automation_id"`
	WorkspaceID string `json:"workspace_id"`
	Digest string `json:"digest"`
}
var ErrAutomationV2Conflict = errors.New("automation v2 ownership or review conflict")

func ValidateAutomationV2Settings(a *AutomationV2Settings, now int64) error {
	if a == nil { return nil }
	if a.SchemaVersion != 2 || !a.ActivateOnAccept || (a.Missed != "skip" && a.Missed != "coalesce") || (a.Overlap != "serialize" && a.Overlap != "independent") { return errors.New("invalid automation v2 policy") }
	if a.Expiration.Kind != "indefinite" && a.Expiration.Kind != "at" { return errors.New("explicit expiration kind required") }
	if (a.Expiration.Kind == "indefinite" && a.Expiration.ExpiresAt != 0) || (a.Expiration.Kind == "at" && a.Expiration.ExpiresAt <= now) { return errors.New("invalid automation v2 expiration") }
	s := a.Schedule
	switch s.Kind {
	case "interval":
		if s.IntervalSeconds < 60 || s.IntervalSeconds > 31622400 || s.Cron != "" || s.Timezone != "" { return errors.New("invalid elapsed interval") }
	case "cron":
		if s.IntervalSeconds != 0 || len(s.Cron) > 128 || s.Timezone == "" || s.Timezone == "Local" { return errors.New("invalid cron schedule") }
		if _, err := time.LoadLocation(s.Timezone); err != nil { return errors.New("explicit IANA timezone required") }
		fields := strings.Fields(s.Cron)
		if len(fields) != 5 { return errors.New("five cron fields required") }
		bounds := [][2]int{{0,59},{0,23},{1,31},{1,12},{0,6}}
		for i, f := range fields {
			if f == "*" { continue }
			step := strings.HasPrefix(f, "*/")
			if step { f = strings.TrimPrefix(f, "*/") }
			if f == "" { return errors.New("invalid cron field") }
			for _, c := range f { if c < '0' || c > '9' { return errors.New("numeric cron fields only") } }
			n, err := strconv.Atoi(f)
			lo, hi := bounds[i][0], bounds[i][1]
			if step { lo, hi = 1, hi-lo+1 }
			if err != nil || n < lo || n > hi { return errors.New("cron field outside bounds") }
		}
		if fields[2] != "*" && fields[4] != "*" { return errors.New("both cron day fields cannot be restricted") }
	default: return errors.New("explicit schedule kind required")
	}
	return nil
}

func automationV2Key(kind, account, session string) string {
	return fmt.Sprintf("automation/v2/%s/%x/%x", kind, account, session)
}
func automationV2ID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { return "", err }
	return fmt.Sprintf("av2_%x", b), nil
}
func AutomationV2DocumentDigest(doc SessionPlanDocument) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil { return "", err }
	if len(b) > 256*1024 { return "", errors.New("automation document exceeds 256 KiB") }
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// The participant is private: callers cannot inject arbitrary keys or grants.
type automationV2Mutation struct {
	proposal AutomationV2Proposal
	expected AutomationV2Review
	accept bool
	record AutomationV2Record
}
func (s *SessionStore) automationV2Owner(account, user, workspace, id string) (SessionSnapshot, error) {
	current, ok, err := s.GetSession(id)
	if err != nil { return current, err }
	if !ok || account == "" || user == "" || workspace == "" || current.AccountScopeID != account || current.UserID != user { return current, ErrAutomationV2Conflict }
	member, exists, err := NewIdentityStore(s.store).GetAccountUser(account,user)
	if err != nil { return current,err }
	if exists && member.Status != "active" { return current,ErrAutomationV2Conflict }
	for _, g := range current.WorkspaceGrants {
		if g.WorkspaceID == workspace && g.Kind == WorkspaceGrantPrimary && g.Available != nil && *g.Available { return current, nil }
	}
	return current, ErrAutomationV2Conflict
}
func (s *SessionStore) GetAutomationV2Proposal(account, user, workspace, id string) (AutomationV2Proposal, bool, error) {
	if _, err := s.automationV2Owner(account,user,workspace,id); err != nil { return AutomationV2Proposal{},false,err }
	var p AutomationV2Proposal
	ok, err := s.store.GetJSON(automationV2Key("proposal",account,id), &p)
	if ok && (p.UserID != user || p.WorkspaceID != workspace) { return AutomationV2Proposal{},false,ErrAutomationV2Conflict }
	return p,ok,err
}
func (s *SessionStore) GetAutomationV2Record(account, user, workspace, id string) (AutomationV2Record, bool, error) {
	if _, err := s.automationV2Owner(account,user,workspace,id); err != nil { return AutomationV2Record{},false,err }
	var r AutomationV2Record
	ok, err := s.store.GetJSON(automationV2Key("accepted",account,id), &r)
	if ok && (r.UserID != user || r.WorkspaceID != workspace) { return AutomationV2Record{},false,ErrAutomationV2Conflict }
	return r,ok,err
}

// ProposeAutomationV2 stores the reviewed canonical document, but no automation,
// authorization, active plan, or run. Revisions compare the complete prior review.
func (s *SessionStore) ProposeAutomationV2(account, user, workspace, id string, doc SessionPlanDocument, expected AutomationV2Review) (AutomationV2Proposal, error) {
	if _, err := s.automationV2Owner(account,user,workspace,id); err != nil { return AutomationV2Proposal{},err }
	if doc.AutomationV2 == nil || doc.Automation != nil { return AutomationV2Proposal{},ErrAutomationV2Conflict }
	if err := ValidateAutomationV2Settings(doc.AutomationV2,time.Now().UnixMilli()); err != nil { return AutomationV2Proposal{},err }
	proposalID := expected.ProposalID
	if proposalID == "" { var err error; proposalID,err = automationV2ID(); if err != nil { return AutomationV2Proposal{},err } }
	digest,err := AutomationV2DocumentDigest(doc)
	if err != nil { return AutomationV2Proposal{},err }
	p := AutomationV2Proposal{AutomationV2Review:AutomationV2Review{proposalID,expected.Revision+1,digest},AccountID:account,UserID:user,WorkspaceID:workspace,SessionID:id,Document:doc,CreatedAt:time.Now().UnixMilli()}
	m := &automationV2Mutation{proposal:p,expected:expected}
	_,err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID:id,AccountScopeID:account,UserID:user,Kind:V3SessionMutationUpdateMetadata,EventType:"session.automation_v2.proposed",ClientRequestID:fmt.Sprintf("av2:proposal:%s:%d",proposalID,p.Revision),PayloadHash:digest,automationV2:m})
	return p,err
}
func (s *SessionStore) AcceptAutomationV2(account, user, workspace, id string, review AutomationV2Review) (AutomationV2Record, error) {
	p,ok,err := s.GetAutomationV2Proposal(account,user,workspace,id)
	if err != nil { return AutomationV2Record{},err }
	if !ok || p.AutomationV2Review != review { return AutomationV2Record{},ErrAutomationV2Conflict }
	m := &automationV2Mutation{proposal:p,expected:review,accept:true}
	_,err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID:id,AccountScopeID:account,UserID:user,Kind:V3SessionMutationUpdateMetadata,EventType:"session.automation_v2.accepted",ClientRequestID:"av2:accept:"+review.ProposalID,PayloadHash:review.Digest,automationV2:m})
	if err != nil { return AutomationV2Record{},err }
	r,ok,err := s.GetAutomationV2Record(account,user,workspace,id)
	if err == nil && !ok { err = errors.New("automation acceptance receipt missing") }
	return r,err
}
func (s *SessionStore) prepareAutomationV2(in *V3SessionMutationInput) error {
	m := in.automationV2
	if m == nil { return nil }
	p := m.proposal
	current,err := s.automationV2Owner(in.AccountScopeID,in.UserID,p.WorkspaceID,in.SessionID)
	if err != nil { return err }
	if current.Automation != nil || current.AutomationV2 != nil { return ErrAutomationV2Conflict }
	if m.accept {
		if run,ok,err := s.GetV3SessionActiveRunIntent(in.SessionID); err != nil { return err } else if ok && (run.Status == V3RunIntentRunning || run.Status == V3RunIntentPendingExecutor) { return ErrAutomationV2Conflict }
	}
	var prior AutomationV2Proposal
	found,err := s.store.GetJSON(automationV2Key("proposal",p.AccountID,p.SessionID), &prior)
	if err != nil { return err }
	if (found && prior.AutomationV2Review != m.expected) || (!found && m.expected != (AutomationV2Review{})) { return ErrAutomationV2Conflict }
	if m.accept {
		if !found || ValidateAutomationV2Settings(p.Document.AutomationV2,time.Now().UnixMilli()) != nil { return ErrAutomationV2Conflict }
		id,err := automationV2ID(); if err != nil { return err }
		m.record = AutomationV2Record{AutomationV2Proposal:prior,AutomationID:id,AcceptedBy:in.UserID,AcceptedAt:time.Now().UnixMilli(),Authorization:prior.Document.AutomationV2.Expiration,Enabled:true}
		current.AutomationV2 = &SessionAutomationV2Binding{id,p.WorkspaceID,p.Digest}
		in.Session = &current
	}
	payload,err := json.Marshal(map[string]any{"proposal_id":p.ProposalID,"revision":p.Revision,"digest":p.Digest,"automation_id":m.record.AutomationID})
	in.EventPayload = payload
	return err
}
func (s *SessionStore) setAutomationV2InBatch(batch *pebble.Batch, in V3SessionMutationInput) error {
	m := in.automationV2
	if m == nil { return nil }
	p := m.proposal
	var value any = p
	kind := "proposal"
	if m.accept { kind,value = "accepted",m.record }
	b,err := json.Marshal(value); if err != nil { return err }
	if err := batch.Set([]byte(automationV2Key(kind,p.AccountID,p.SessionID)),b,nil); err != nil { return err }
	if !m.accept {
		plan := SessionPlanSnapshot{ID:p.ProposalID,SessionID:p.SessionID,AccountScopeID:p.AccountID,UserID:p.UserID,Title:p.Document.Title,Status:"pending",ApprovalState:"pending",Version:int(p.Revision),Document:&p.Document,CreatedAt:p.CreatedAt,UpdatedAt:p.CreatedAt}
		if err := setPlanAcceptancePlanInBatch(batch,plan,nil); err != nil { return err }
	}
	if hook := s.store.sessionMutations.beforeAutomationV2Commit; hook != nil { return hook(in.SessionID) }
	return nil
}
func (s *SessionStore) SetAutomationV2CommitHookForTest(hook func(string) error) func() {
	prior := s.store.sessionMutations.beforeAutomationV2Commit
	s.store.sessionMutations.beforeAutomationV2Commit = hook
	return func(){ s.store.sessionMutations.beforeAutomationV2Commit = prior }
}

// ListAutomationV2Records uses an exclusive opaque storage cursor and caps both
// visited records and returned bytes. Foreign session records are never emitted.
func (s *SessionStore) ListAutomationV2Records(account,user,workspace,after string,limit int) ([]AutomationV2Record,string,error) {
	if account == "" || user == "" || workspace == "" || limit < 1 || limit > 100 { return nil,"",ErrAutomationV2Conflict }
	prefix := fmt.Sprintf("automation/v2/accepted/%x/",account)
	if after != "" && !strings.HasPrefix(after,prefix) { return nil,"",ErrAutomationV2Conflict }
	lower := prefix
	if after != "" { lower = after + "\x00" }
	iter,err := s.store.db.NewIter(&pebble.IterOptions{LowerBound:[]byte(lower),UpperBound:[]byte(prefix+"\xff")})
	if err != nil { return nil,"",err }; defer iter.Close()
	out := []AutomationV2Record{}
	next := ""
	bytes := 0
	for visited,valid := 0,iter.First(); valid; valid = iter.Next() {
		if visited >= 100 || len(out) >= limit || bytes >= 1024*1024 { return out,next,nil }
		visited++; next = string(iter.Key())
		var r AutomationV2Record
		if err := json.Unmarshal(iter.Value(),&r); err != nil { return nil,"",err }
		if r.UserID != user || r.WorkspaceID != workspace { continue }
		if _,err := s.automationV2Owner(account,user,workspace,r.SessionID); err != nil { continue }
		bytes += len(iter.Value()); out=append(out,r)
	}
	return out,"",iter.Error()
}
