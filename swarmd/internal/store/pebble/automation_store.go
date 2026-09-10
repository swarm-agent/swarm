package pebblestore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

var (
	ErrAutomationConflict = errors.New("automation revision or idempotency conflict")
	ErrAutomationInvalid  = errors.New("invalid automation record")
)

// AutomationScope must be supplied by the authenticated domain service. Storage
// enforces namespace isolation, not principal authentication or plan approval.
type AutomationScope struct {
	AccountID   string `json:"account_id"`
	WorkspaceID string `json:"workspace_id"`
}

type AutomationPlanReference struct {
	SessionID      string `json:"session_id"`
	PlanID         string `json:"plan_id"`
	Revision       uint64 `json:"revision"`
	DocumentSHA256 string `json:"document_sha256,omitempty"`
}

type AutomationSchedulePolicy struct {
	Kind            string `json:"kind"` // manual, interval, cron, event
	Expression      string `json:"expression,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	IntervalSeconds int64  `json:"interval_seconds,omitempty"`
	TriggerSource   string `json:"trigger_source,omitempty"`
	MissedPolicy    string `json:"missed_policy"`  // skip, coalesce
	OverlapPolicy   string `json:"overlap_policy"` // independent, serialize
}

// Authorization is policy data only; a stored reference is never an execution grant.
type AutomationAuthorizationPolicy struct {
	Mode              string   `json:"mode"` // approval_required, approved_policy
	ApprovalReference string   `json:"approval_reference,omitempty"`
	AllowedTools      []string `json:"allowed_tools,omitempty"`
	TargetIDs         []string `json:"target_ids,omitempty"`
	ExpiresAt         int64    `json:"expires_at,omitempty"`
}

// Bindings are ordered references to canonical plans, not an execution engine.
type AutomationPlanBinding struct {
	ID        string                  `json:"id"`
	Plan      AutomationPlanReference `json:"plan"`
	DependsOn []string                `json:"depends_on,omitempty"`
}

// ValidateAutomationBindings rejects forward edges, cycles and ambiguous identities.
func ValidateAutomationBindings(bindings []AutomationPlanBinding) error {
	if len(bindings) < 1 || len(bindings) > 16 {
		return ErrAutomationInvalid
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !automationValidID(b.ID) || seen[b.ID] || !automationValidID(b.Plan.SessionID) || !automationValidID(b.Plan.PlanID) || b.Plan.Revision == 0 || len(b.DependsOn) > 15 {
			return ErrAutomationInvalid
		}
		deps := map[string]bool{}
		for _, dep := range b.DependsOn {
			if !seen[dep] || deps[dep] {
				return ErrAutomationInvalid
			}
			deps[dep] = true
		}
		seen[b.ID] = true
	}
	return nil
}

type AutomationDefinition struct {
	Name          string                        `json:"name"`
	Enabled       bool                          `json:"enabled"`
	Plans         []AutomationPlanBinding       `json:"plans"`
	Schedule      AutomationSchedulePolicy      `json:"schedule"`
	Authorization AutomationAuthorizationPolicy `json:"authorization"`
}

type AutomationOccurrence struct {
	DefinitionRevision uint64 `json:"definition_revision"`
	TriggerIdentity    string `json:"trigger_identity"`
	ScheduledAt        int64  `json:"scheduled_at"`
	State              string `json:"state"`
	SessionID          string `json:"session_id,omitempty"` // reference only; V3 owns execution
}

type AutomationContext struct {
	UserLocked map[string]string `json:"user_locked,omitempty"`
	AgentOwned map[string]string `json:"agent_owned,omitempty"`
}

type AutomationOutcome struct {
	OccurrenceID string            `json:"occurrence_id,omitempty"`
	Kind         string            `json:"kind"`
	Summary      string            `json:"summary"`
	Facts        map[string]string `json:"facts,omitempty"`
}

// Exactly one payload matches Kind. Every revision is immutable and addressable
// by scope, automation ID, kind, record ID and revision. Audit is append-only.
type AutomationRecord struct {
	Scope        AutomationScope       `json:"scope"`
	AutomationID string                `json:"automation_id"`
	Kind         string                `json:"kind"` // definition, occurrence, context, audit
	ID           string                `json:"id"`
	Revision     uint64                `json:"revision"`
	SubjectID    string                `json:"subject_id"`
	Actor        string                `json:"actor"`
	WrittenAt    int64                 `json:"written_at"`
	Definition   *AutomationDefinition `json:"definition,omitempty"`
	Occurrence   *AutomationOccurrence `json:"occurrence,omitempty"`
	Context      *AutomationContext    `json:"context,omitempty"`
	Outcome      *AutomationOutcome    `json:"outcome,omitempty"`
}

type AutomationMutation struct {
	Record           AutomationRecord `json:"record"`
	ExpectedRevision uint64           `json:"expected_revision"`
	MutationID       string           `json:"mutation_id"`
	Actor            string           `json:"actor"`      // user, agent, system; authenticated by caller
	SubjectID        string           `json:"subject_id"` // authenticated subject, never request data
	WrittenAt        int64            `json:"written_at"` // domain clock, milliseconds
}

type automationReceipt struct {
	Hash   string           `json:"hash"`
	Record AutomationRecord `json:"record"`
}

func automationPart(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func automationValidID(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s
}
func automationPrefix(scope AutomationScope) (string, error) {
	if !automationValidID(scope.AccountID) || !automationValidID(scope.WorkspaceID) {
		return "", ErrAutomationInvalid
	}
	return "automation:v1:" + automationPart(scope.AccountID) + ":" + automationPart(scope.WorkspaceID) + ":", nil
}
func automationKey(r AutomationRecord) (string, error) {
	prefix, err := automationPrefix(r.Scope)
	if err != nil || !automationValidID(r.AutomationID) || !automationValidID(r.ID) {
		return "", ErrAutomationInvalid
	}
	switch r.Kind {
	case "definition", "occurrence", "context", "audit":
	default:
		return "", ErrAutomationInvalid
	}
	return prefix + automationPart(r.AutomationID) + ":" + r.Kind + ":" + automationPart(r.ID), nil
}
func automationRevisionKey(key string, revision uint64) string {
	return fmt.Sprintf("%s:revision:%020d", key, revision)
}

func validateAutomationRecord(r AutomationRecord) error {
	count := 0
	for _, present := range []bool{r.Definition != nil, r.Occurrence != nil, r.Context != nil, r.Outcome != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return ErrAutomationInvalid
	}
	switch r.Kind {
	case "definition":
		d := r.Definition
		if d == nil || r.ID != r.AutomationID || !automationValidID(d.Name) || ValidateAutomationBindings(d.Plans) != nil {
			return ErrAutomationInvalid
		}
		s := d.Schedule
		if s.MissedPolicy != "skip" && s.MissedPolicy != "coalesce" {
			return ErrAutomationInvalid
		}
		if s.OverlapPolicy != "independent" && s.OverlapPolicy != "serialize" {
			return ErrAutomationInvalid
		}
		switch s.Kind {
		case "manual":
		case "interval":
			if s.IntervalSeconds < 1 {
				return ErrAutomationInvalid
			}
		case "cron":
			if s.Expression == "" || s.Timezone == "" {
				return ErrAutomationInvalid
			}
		case "event":
			if s.TriggerSource == "" {
				return ErrAutomationInvalid
			}
		default:
			return ErrAutomationInvalid
		}
		a := d.Authorization
		if a.Mode != "approval_required" && a.Mode != "approved_policy" {
			return ErrAutomationInvalid
		}
		if a.Mode == "approved_policy" && (!automationValidID(a.ApprovalReference) || a.ExpiresAt <= 0) {
			return ErrAutomationInvalid
		}
	case "occurrence":
		if r.Occurrence == nil || r.Occurrence.DefinitionRevision == 0 || !automationValidID(r.Occurrence.TriggerIdentity) {
			return ErrAutomationInvalid
		}
		switch r.Occurrence.State {
		case "pending", "running", "blocked", "completed", "failed", "cancelled", "skipped":
		default:
			return ErrAutomationInvalid
		}
	case "context":
		if r.Context == nil || r.ID != r.AutomationID || len(r.Context.UserLocked) > 64 || len(r.Context.AgentOwned) > 64 {
			return ErrAutomationInvalid
		}
	case "audit":
		if r.Outcome == nil || !automationValidID(r.Outcome.Kind) || r.Outcome.Summary == "" {
			return ErrAutomationInvalid
		}
	default:
		return ErrAutomationInvalid
	}
	payload, err := json.Marshal(r)
	if err != nil || len(payload) > 32768 {
		return ErrAutomationInvalid
	}
	return nil
}

// ApplyAutomationMutation commits the head, immutable revision and permanent
// idempotency receipt in one synced batch. Replays return the original result,
// even after later updates or restart; changed payloads with the same ID fail.
func (s *Store) ApplyAutomationMutation(m AutomationMutation) (AutomationRecord, bool, error) {
	var zero AutomationRecord
	key, err := automationKey(m.Record)
	if err != nil || !automationValidID(m.MutationID) || m.Record.Revision != 0 || (m.Actor != "user" && m.Actor != "agent" && m.Actor != "system") {
		return zero, false, ErrAutomationInvalid
	}
	if !automationValidID(m.SubjectID) || m.WrittenAt <= 0 || m.Record.SubjectID != "" || m.Record.Actor != "" || m.Record.WrittenAt != 0 {
		return zero, false, ErrAutomationInvalid
	}
	if err := validateAutomationRecord(m.Record); err != nil {
		return zero, false, err
	}
	// A retry uses a fresh domain clock but must return the original attribution.
	hashInput := m
	hashInput.WrittenAt = 0
	data, err := json.Marshal(hashInput)
	if err != nil {
		return zero, false, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	receiptKey := key + ":mutation:" + automationPart(m.MutationID)
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	var receipt automationReceipt
	found, err := s.GetJSON(receiptKey, &receipt)
	if err != nil {
		return zero, false, err
	}
	if found {
		if receipt.Hash != hash {
			return zero, false, ErrAutomationConflict
		}
		return receipt.Record, false, nil
	}
	var old AutomationRecord
	found, err = s.GetJSON(key+":head", &old)
	if err != nil {
		return zero, false, err
	}
	if old.Revision != m.ExpectedRevision || m.ExpectedRevision == ^uint64(0) {
		return zero, false, ErrAutomationConflict
	}
	r := m.Record
	if r.Kind != "definition" {
		definition := AutomationRecord{Scope: r.Scope, AutomationID: r.AutomationID, Kind: "definition", ID: r.AutomationID}
		dk, _ := automationKey(definition)
		lookup := dk + ":head"
		if r.Occurrence != nil {
			lookup = automationRevisionKey(dk, r.Occurrence.DefinitionRevision)
		}
		var def AutomationRecord
		ok, err := s.GetJSON(lookup, &def)
		if err != nil {
			return zero, false, err
		}
		if !ok {
			return zero, false, ErrAutomationInvalid
		}
	}
	if r.Kind == "audit" && found {
		return zero, false, ErrAutomationConflict
	}
	if r.Outcome != nil && r.Outcome.OccurrenceID != "" {
		ref := AutomationRecord{Scope: r.Scope, AutomationID: r.AutomationID, Kind: "occurrence", ID: r.Outcome.OccurrenceID}
		refKey, err := automationKey(ref)
		if err != nil {
			return zero, false, err
		}
		var occurrence AutomationRecord
		ok, err := s.GetJSON(refKey+":head", &occurrence)
		if err != nil {
			return zero, false, err
		}
		if !ok {
			return zero, false, ErrAutomationInvalid
		}
	}
	if r.Context != nil && m.Actor != "user" {
		var locked map[string]string
		if found {
			locked = old.Context.UserLocked
		}
		if len(locked) != len(r.Context.UserLocked) || (len(locked) != 0 && !reflect.DeepEqual(locked, r.Context.UserLocked)) {
			return zero, false, ErrAutomationConflict
		}
	}
	if r.Occurrence != nil {
		o := r.Occurrence
		if !found && o.State != "pending" {
			return zero, false, ErrAutomationInvalid
		}
		if found {
			p := old.Occurrence
			if p.DefinitionRevision != o.DefinitionRevision || p.TriggerIdentity != o.TriggerIdentity || p.ScheduledAt != o.ScheduledAt || (p.SessionID != "" && p.SessionID != o.SessionID) {
				return zero, false, ErrAutomationConflict
			}
			if !automationStateTransition(p.State, o.State) {
				return zero, false, ErrAutomationConflict
			}
		}
		// One occurrence per external/scheduled trigger identity, regardless of ID.
		prefix, _ := automationPrefix(r.Scope)
		tk := prefix + automationPart(r.AutomationID) + ":trigger:" + automationPart(o.TriggerIdentity)
		var owner string
		ok, err := s.GetJSON(tk, &owner)
		if err != nil {
			return zero, false, err
		}
		if ok && owner != r.ID {
			return zero, false, ErrAutomationConflict
		}
	}
	r.Revision = m.ExpectedRevision + 1
	r.SubjectID, r.Actor, r.WrittenAt = m.SubjectID, m.Actor, m.WrittenAt
	batch := s.NewBatch()
	defer batch.Close()
	put := func(k string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return batch.Set([]byte(k), b, nil)
	}
	for _, k := range []string{key + ":head", automationRevisionKey(key, r.Revision)} {
		if err := put(k, r); err != nil {
			return zero, false, err
		}
	}
	if r.Occurrence != nil {
		prefix, _ := automationPrefix(r.Scope)
		if err := put(prefix+automationPart(r.AutomationID)+":trigger:"+automationPart(r.Occurrence.TriggerIdentity), r.ID); err != nil {
			return zero, false, err
		}
	}
	if err := put(receiptKey, automationReceipt{Hash: hash, Record: r}); err != nil {
		return zero, false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return zero, false, err
	}
	return r, true, nil
}

func automationStateTransition(from, to string) bool {
	if from == "completed" || from == "failed" || from == "cancelled" || from == "skipped" {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case "pending":
		return to == "running" || to == "cancelled" || to == "skipped"
	case "running":
		return to == "blocked" || to == "completed" || to == "failed" || to == "cancelled"
	case "blocked":
		return to == "running" || to == "cancelled" || to == "failed"
	}
	return false
}

// Revision zero reads the current head; positive revisions are exact immutable references.
func (s *Store) GetAutomationRecord(scope AutomationScope, automationID, kind, id string, revision uint64) (AutomationRecord, bool, error) {
	var r AutomationRecord
	key, err := automationKey(AutomationRecord{Scope: scope, AutomationID: automationID, Kind: kind, ID: id})
	if err != nil {
		return r, false, err
	}
	if revision == 0 {
		key += ":head"
	} else {
		key = automationRevisionKey(key, revision)
	}
	ok, err := s.GetJSON(key, &r)
	return r, ok, err
}

type AutomationSearch struct {
	Scope        AutomationScope `json:"scope"`
	AutomationID string          `json:"automation_id"`
	Kind         string          `json:"kind"`
	Query        string          `json:"query"`
	Cursor       string          `json:"cursor"`
	Limit        int             `json:"limit"`
}

type automationCursor struct {
	Scope        AutomationScope `json:"scope"`
	AutomationID string          `json:"automation_id"`
	Kind         string          `json:"kind"`
	Query        string          `json:"query"`
	Key          string          `json:"key"`
}

// SearchAutomationRecords scans at most 512 keys per page, including history.
// Empty pages may have a continuation; cursors are opaque and scope/filter bound.
func (s *Store) SearchAutomationRecords(q AutomationSearch) ([]AutomationRecord, string, error) {
	prefix, err := automationPrefix(q.Scope)
	if err != nil || q.Limit < 1 || q.Limit > 100 || len(q.Query) > 256 {
		return nil, "", ErrAutomationInvalid
	}
	if q.AutomationID != "" {
		if !automationValidID(q.AutomationID) {
			return nil, "", ErrAutomationInvalid
		}
		prefix += automationPart(q.AutomationID) + ":"
	}
	if q.Kind != "" {
		switch q.Kind {
		case "definition", "occurrence", "context", "audit":
		default:
			return nil, "", ErrAutomationInvalid
		}
	}
	cursor := automationCursor{Scope: q.Scope, AutomationID: q.AutomationID, Kind: q.Kind, Query: q.Query}
	start := prefix
	if q.Cursor != "" {
		if len(q.Cursor) > 4096 {
			return nil, "", ErrAutomationInvalid
		}
		data, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		var c automationCursor
		if e != nil || json.Unmarshal(data, &c) != nil || c.Scope != q.Scope || c.AutomationID != q.AutomationID || c.Kind != q.Kind || c.Query != q.Query || !strings.HasPrefix(c.Key, prefix) {
			return nil, "", ErrAutomationInvalid
		}
		start = c.Key + "\x00"
	}
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	rows := []AutomationRecord{}
	scanned := 0
	valid := iter.SeekGE([]byte(start))
	for valid && scanned < 512 && len(rows) < q.Limit {
		key := string(iter.Key())
		cursor.Key = key
		scanned++
		if strings.HasSuffix(key, ":head") {
			var r AutomationRecord
			if err := json.Unmarshal(iter.Value(), &r); err != nil {
				return nil, "", err
			}
			if (q.Kind == "" || q.Kind == r.Kind) && (q.Query == "" || strings.Contains(strings.ToLower(string(iter.Value())), strings.ToLower(q.Query))) {
				rows = append(rows, r)
			}
		}
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return nil, "", err
	}
	if !valid {
		return rows, "", nil
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return nil, "", err
	}
	return rows, base64.RawURLEncoding.EncodeToString(data), nil
}
