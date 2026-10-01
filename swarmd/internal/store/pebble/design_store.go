package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

const (
	DesignHTML            = "html"
	DesignPlan            = "plan"
	DesignGenerate        = "generate"
	DesignEdit            = "edit"
	DesignQueued          = "queued"
	DesignRunning         = "running"
	DesignCancelRequested = "cancel_requested"
	DesignCancelled       = "cancelled"
	DesignFailed          = "failed"
	DesignInterrupted     = "interrupted"
	DesignSucceeded       = "succeeded"
	DesignPartial         = "partial_success"
	MaxDesignCandidates   = 8
	MaxDesignAttempts     = 16
	MaxDesignContentBytes = 2 << 20
	MaxDesignContextBytes = 256 << 10
	MaxDesignHistoryPage  = 50
)

var (
	ErrDesignInvalid  = errors.New("invalid design operation")
	ErrDesignNotFound = errors.New("design record not found")
	ErrDesignConflict = errors.New("design revision or idempotency conflict")
)

// DesignPrincipal must be derived from authenticated account identity, never tool
// arguments. Records are account-owned and additionally restricted to their creator.
// Runtime must authorize canonical session/workspace access before calling this store.
type DesignPrincipal struct {
	AccountID   string `json:"account_id"`
	PrincipalID string `json:"principal_id"`
}

// DesignRef identifies immutable bytes, not a mutable selection. Historical exact
// refs remain valid edit bases; a mismatching revision/hash is never resolved to latest.
type DesignRef struct {
	ArtifactID string `json:"artifact_id"`
	Revision   uint64 `json:"revision"`
	SHA256     string `json:"sha256"`
}

type DesignContextSnapshot struct {
	LineStart    int    `json:"line_start,omitempty"`
	LineEnd      int    `json:"line_end,omitempty"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	Path         string `json:"path"`
	Content      []byte `json:"content"`
	SHA256       string `json:"sha256"`
}

type DesignCandidateSpec struct {
	ArtifactID string     `json:"artifact_id"`
	Kind       string     `json:"kind"`
	Operation  string     `json:"operation"`
	Brief      string     `json:"brief"`
	Base       *DesignRef `json:"base,omitempty"`
	PlanSource *DesignRef `json:"plan_source,omitempty"`
}

// DesignSubmit deliberately has no output/HTML field. Selected source examples
// are immutable input snapshots; only PublishDesignRevision accepts output bytes.
type DesignSubmit struct {
	RequestID       string                  `json:"request_id"`
	IdempotencyKey  string                  `json:"idempotency_key"`
	ParentSessionID string                  `json:"parent_session_id"`
	ParentRunID     string                  `json:"parent_run_id"`
	Candidates      []DesignCandidateSpec   `json:"candidates"`
	Context         []DesignContextSnapshot `json:"context,omitempty"`
}

// DesignAttempt is execution provenance, NOT a session lifecycle. Runtime observes
// canonical child state and records outcomes here. Reopen never fabricates success
// or silently retries; interrupted attempts require an explicit fresh child attempt.
type DesignAttempt struct {
	EvidenceRequired bool                 `json:"evidence_required,omitempty"`
	Provider         string               `json:"provider,omitempty"`
	Model            string               `json:"model,omitempty"`
	Thinking         string               `json:"thinking,omitempty"`
	Output           *DesignOutputRef     `json:"output,omitempty"`
	Usage            *DesignResponseUsage `json:"usage,omitempty"`
	Validation       *DesignValidation    `json:"validation,omitempty"`
	RouterAlert      string               `json:"router_alert,omitempty"`
	Number           int                  `json:"number"`
	ChildSessionID   string               `json:"child_session_id"`
	RunID            string               `json:"run_id"`
	State            string               `json:"state"`
	ReasonCode       string               `json:"reason_code,omitempty"`
	Result           *DesignRef           `json:"result,omitempty"`
}

type DesignCandidate struct {
	FailureReason string              `json:"failure_reason,omitempty"`
	RouterAlert   string              `json:"router_alert,omitempty"`
	Spec          DesignCandidateSpec `json:"spec"`
	State         string              `json:"state"`
	Attempts      []DesignAttempt     `json:"attempts,omitempty"`
}

type DesignRequest struct {
	Owner           DesignPrincipal   `json:"owner"`
	ID              string            `json:"id"`
	ParentSessionID string            `json:"parent_session_id"`
	ParentRunID     string            `json:"parent_run_id"`
	Revision        uint64            `json:"revision"`
	State           string            `json:"state"`
	Candidates      []DesignCandidate `json:"candidates"`
}

type DesignArtifact struct {
	Owner            DesignPrincipal `json:"owner"`
	ID               string          `json:"id"`
	RequestGroupID   string          `json:"request_group_id"`
	Kind             string          `json:"kind"`
	RevisionCount    uint64          `json:"revision_count"`
	SelectionVersion uint64          `json:"selection_version"`
	Selected         *DesignRef      `json:"selected,omitempty"`
}

type DesignRevision struct {
	Ref       DesignRef     `json:"ref"`
	Kind      string        `json:"kind"`
	RequestID string        `json:"request_id"`
	Candidate int           `json:"candidate"`
	Attempt   DesignAttempt `json:"attempt"`
	Base      *DesignRef    `json:"base,omitempty"`
	Content   []byte        `json:"content"`
}

// Every mutation has a stable idempotency key and a request revision precondition.
// Retries return the original receipt, even after later successful mutations.
type DesignAttemptMutation struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Candidate        int    `json:"candidate"`
	State            string `json:"state"`
	ChildSessionID   string `json:"child_session_id"`
	RunID            string `json:"run_id"`
	ReasonCode       string `json:"reason_code,omitempty"`
}

type DesignPublication struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Candidate        int    `json:"candidate"`
	ChildSessionID   string `json:"child_session_id"`
	RunID            string `json:"run_id"`
	Kind             string `json:"kind"`
	Content          []byte `json:"content"`
}

type DesignSelection struct {
	IdempotencyKey  string     `json:"idempotency_key"`
	ExpectedVersion uint64     `json:"expected_version"`
	ExpectedCurrent *DesignRef `json:"expected_current,omitempty"`
	Ref             DesignRef  `json:"ref"`
}

type designReceipt struct {
	Hash   string          `json:"hash"`
	Result json.RawMessage `json:"result"`
}

func designID(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.') {
			return false
		}
	}
	return true
}

func designOwner(p DesignPrincipal) error {
	if !designID(p.AccountID) || !designID(p.PrincipalID) {
		return ErrDesignInvalid
	}
	return nil
}

func designKey(p DesignPrincipal, kind, id string) string {
	return "design:v1/" + p.AccountID + "/" + p.PrincipalID + "/" + kind + "/" + id
}

func designDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Store) designGet(key string, out any) error {
	b, ok, err := s.GetBytes(key)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDesignNotFound
	}
	return json.Unmarshal(b, out)
}

func designSet(b *pebble.Batch, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return b.Set([]byte(key), data, nil)
}

func (s *Store) designReplay(key string, input, out any) (bool, error) {
	var receipt designReceipt
	if err := s.designGet(key, &receipt); err != nil {
		if errors.Is(err, ErrDesignNotFound) {
			return false, nil
		}
		return false, err
	}
	b, err := json.Marshal(input)
	if err != nil {
		return false, err
	}
	if receipt.Hash != designDigest(b) {
		return false, ErrDesignConflict
	}
	return true, json.Unmarshal(receipt.Result, out)
}

func designSaveReceipt(b *pebble.Batch, key string, input, result any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return designSet(b, key, designReceipt{Hash: designDigest(data), Result: out})
}

func designRefEqual(a, b *DesignRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *Store) GetDesignRequest(p DesignPrincipal, id string) (DesignRequest, error) {
	var r DesignRequest
	if err := designOwner(p); err != nil {
		return r, err
	}
	if !designID(id) {
		return r, ErrDesignInvalid
	}
	err := s.designGet(designKey(p, "request", id), &r)
	return r, err
}

func (s *Store) GetDesignArtifact(p DesignPrincipal, id string) (DesignArtifact, error) {
	var a DesignArtifact
	if err := designOwner(p); err != nil {
		return a, err
	}
	if !designID(id) {
		return a, ErrDesignInvalid
	}
	err := s.designGet(designKey(p, "artifact", id), &a)
	return a, err
}

func (s *Store) ReadDesignContext(p DesignPrincipal, requestID string) ([]DesignContextSnapshot, error) {
	if _, err := s.GetDesignRequest(p, requestID); err != nil {
		return nil, err
	}
	var snapshots []DesignContextSnapshot
	err := s.designGet(designKey(p, "context", requestID), &snapshots)
	return snapshots, err
}

func designRevisionKey(p DesignPrincipal, id string, revision uint64) string {
	return designKey(p, "revision", fmt.Sprintf("%s/%020d", id, revision))
}

func (s *Store) ReadDesignRevision(p DesignPrincipal, ref DesignRef) (DesignRevision, error) {
	var r DesignRevision
	if _, err := s.GetDesignArtifact(p, ref.ArtifactID); err != nil {
		return r, err
	}
	if ref.Revision == 0 || len(ref.SHA256) != 64 {
		return r, ErrDesignInvalid
	}
	if err := s.designGet(designRevisionKey(p, ref.ArtifactID, ref.Revision), &r); err != nil {
		return r, err
	}
	if r.Ref != ref || designDigest(r.Content) != ref.SHA256 {
		return DesignRevision{}, ErrDesignConflict
	}
	return r, nil
}

// DesignHistory returns metadata only, strictly after the given revision. No
// global history scans or unbounded content aggregation are performed.
func (s *Store) DesignHistory(p DesignPrincipal, artifactID string, after uint64, limit int) ([]DesignRevision, error) {
	if limit < 1 || limit > MaxDesignHistoryPage {
		return nil, ErrDesignInvalid
	}
	a, err := s.GetDesignArtifact(p, artifactID)
	if err != nil {
		return nil, err
	}
	rows := make([]DesignRevision, 0, limit)
	for n := after; n < a.RevisionCount && len(rows) < limit; {
		n++
		var r DesignRevision
		if err := s.designGet(designRevisionKey(p, artifactID, n), &r); err != nil {
			return nil, err
		}
		r.Content = nil
		rows = append(rows, r)
	}
	return rows, nil
}

func (s *Store) SubmitDesignRequest(p DesignPrincipal, in DesignSubmit) (DesignRequest, error) {
	s.designMu.Lock()
	defer s.designMu.Unlock()
	b := s.db.NewBatch()
	defer b.Close()
	r, err := s.submitDesignRequestInBatch(p, in, b)
	if err != nil {
		return DesignRequest{}, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return DesignRequest{}, err
	}
	return r, nil
}

// Caller holds designMu, including across the enclosing commit.
func (s *Store) submitDesignRequestInBatch(p DesignPrincipal, in DesignSubmit, b *pebble.Batch) (DesignRequest, error) {
	var zero DesignRequest
	if err := designOwner(p); err != nil {
		return zero, err
	}
	if !designID(in.RequestID) || !designID(in.IdempotencyKey) || !designID(in.ParentSessionID) || !designID(in.ParentRunID) || len(in.Candidates) < 1 || len(in.Candidates) > MaxDesignCandidates || len(in.Context) > 32 {
		return zero, ErrDesignInvalid
	}
	total := 0
	for _, c := range in.Context {
		total += len(c.Content)
		if c.SourceSHA256 != "" {
			decoded, err := hex.DecodeString(c.SourceSHA256)
			if err != nil || len(decoded) != sha256.Size || (c.LineStart == 0 && c.SourceSHA256 != c.SHA256) {
				return zero, ErrDesignInvalid
			}
		}
		if c.Path == "" || len(c.Path) > 4096 || !utf8.ValidString(c.Path) || !utf8.Valid(c.Content) || strings.ContainsRune(string(c.Content), 0) || c.SHA256 != designDigest(c.Content) || c.LineStart < 0 || c.LineEnd < c.LineStart || (c.LineStart == 0 && c.LineEnd != 0) || (c.LineStart > 0 && len(c.SourceSHA256) != 64) {
			return zero, ErrDesignInvalid
		}
	}
	if total > MaxDesignContextBytes {
		return zero, ErrDesignInvalid
	}
	receipt := designKey(p, "submit", in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	if _, err := s.GetDesignRequest(p, in.RequestID); !errors.Is(err, ErrDesignNotFound) {
		if err == nil {
			err = ErrDesignConflict
		}
		return zero, err
	}
	if err := s.admitDesignRequest(p); err != nil {
		return zero, err
	}
	r := DesignRequest{Owner: p, ID: in.RequestID, ParentSessionID: in.ParentSessionID, ParentRunID: in.ParentRunID, Revision: 1, State: DesignQueued}
	artifacts := make([]DesignArtifact, 0, len(in.Candidates))
	seen := map[string]bool{}
	for _, c := range in.Candidates {
		if !designID(c.ArtifactID) || seen[c.ArtifactID] || (c.Kind != DesignHTML && c.Kind != DesignPlan) || len(c.Brief) > 65536 || strings.TrimSpace(c.Brief) == "" || !utf8.ValidString(c.Brief) {
			return zero, ErrDesignInvalid
		}
		seen[c.ArtifactID] = true
		if c.PlanSource != nil {
			if c.Kind != DesignHTML || c.Operation != DesignGenerate {
				return zero, ErrDesignInvalid
			}
			plan, err := s.ReadDesignRevision(p, *c.PlanSource)
			if err != nil {
				return zero, err
			}
			if plan.Kind != DesignPlan {
				return zero, ErrDesignInvalid
			}
		}
		switch c.Operation {
		case DesignGenerate:
			if c.Base != nil {
				return zero, ErrDesignInvalid
			}
			if _, err := s.GetDesignArtifact(p, c.ArtifactID); !errors.Is(err, ErrDesignNotFound) {
				if err == nil {
					err = ErrDesignConflict
				}
				return zero, err
			}
			artifacts = append(artifacts, DesignArtifact{Owner: p, ID: c.ArtifactID, RequestGroupID: in.RequestID, Kind: c.Kind})
		case DesignEdit:
			if c.Base == nil || c.Base.ArtifactID != c.ArtifactID {
				return zero, ErrDesignInvalid
			}
			base, err := s.ReadDesignRevision(p, *c.Base)
			if err != nil {
				return zero, err
			}
			if base.Kind != c.Kind {
				return zero, ErrDesignInvalid
			}
		default:
			return zero, ErrDesignInvalid
		}
		r.Candidates = append(r.Candidates, DesignCandidate{Spec: c, State: DesignQueued})
	}
	for _, a := range artifacts {
		if err := designSet(b, designKey(p, "artifact", a.ID), a); err != nil {
			return zero, err
		}
	}
	if err := designSet(b, designKey(p, "request", r.ID), r); err != nil {
		return zero, err
	}
	if err := designSet(b, designKey(p, "context", r.ID), in.Context); err != nil {
		return zero, err
	}
	if err := designSaveReceipt(b, receipt, in, r); err != nil {
		return zero, err
	}
	if err := designSet(b, designKey(p, "pending", r.ID), r.ID); err != nil {
		return zero, err
	}
	return r, nil
}

func designRequestState(r DesignRequest) string {
	counts := map[string]int{}
	for _, c := range r.Candidates {
		counts[c.State]++
	}
	if counts[DesignCancelRequested] > 0 {
		return DesignCancelRequested
	}
	if counts[DesignRunning] > 0 {
		return DesignRunning
	}
	if counts[DesignQueued] > 0 {
		return DesignQueued
	}
	if counts[DesignSucceeded] == len(r.Candidates) {
		return DesignSucceeded
	}
	if counts[DesignSucceeded] > 0 {
		return DesignPartial
	}
	if counts[DesignInterrupted] > 0 {
		return DesignInterrupted
	}
	if counts[DesignFailed] > 0 {
		return DesignFailed
	}
	return DesignCancelled
}

func (s *Store) designCommitRequest(p DesignPrincipal, r DesignRequest, receipt string, input any, b *pebble.Batch) (DesignRequest, error) {
	if r.Revision == ^uint64(0) {
		return DesignRequest{}, ErrDesignConflict
	}
	r.Revision++
	r.State = designRequestState(r)
	if err := designSet(b, designKey(p, "request", r.ID), r); err != nil {
		return DesignRequest{}, err
	}
	if err := designSaveReceipt(b, receipt, input, r); err != nil {
		return DesignRequest{}, err
	}
	if r.State != DesignQueued && r.State != DesignRunning && r.State != DesignCancelRequested {
		if err := b.Delete([]byte(designKey(p, "pending", r.ID)), nil); err != nil {
			return DesignRequest{}, err
		}
	} else if err := designSet(b, designKey(p, "pending", r.ID), r.ID); err != nil {
		return DesignRequest{}, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return DesignRequest{}, err
	}
	return r, nil
}

// RecordDesignAttempt records canonical execution observations. Starting a retry
// requires a fresh child session; cancellation first blocks publication, then the
// runtime confirms cancelled after canonical child cancellation. Interrupted is
// an explicit reconciliation outcome, not something inferred merely from reopen.
func (s *Store) RecordDesignAttempt(p DesignPrincipal, requestID string, in DesignAttemptMutation) (DesignRequest, error) {
	var zero DesignRequest
	if err := designOwner(p); err != nil {
		return zero, err
	}
	if !designID(requestID) || !designID(in.IdempotencyKey) || (in.ReasonCode != "" && !designID(in.ReasonCode)) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	receipt := designKey(p, "attempt", requestID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	r, err := s.GetDesignRequest(p, requestID)
	if err != nil {
		return zero, err
	}
	if r.Revision != in.ExpectedRevision {
		return zero, ErrDesignConflict
	}
	if in.Candidate < 0 || in.Candidate >= len(r.Candidates) {
		return zero, ErrDesignInvalid
	}
	c := &r.Candidates[in.Candidate]
	switch in.State {
	case DesignRunning:
		if c.State != DesignQueued && c.State != DesignFailed && c.State != DesignInterrupted {
			return zero, ErrDesignConflict
		}
		if len(c.Attempts) >= MaxDesignAttempts || !designID(in.ChildSessionID) || !designID(in.RunID) || in.ReasonCode != "" {
			return zero, ErrDesignInvalid
		}
		if in.ChildSessionID == r.ParentSessionID {
			return zero, ErrDesignInvalid
		}
		for _, candidate := range r.Candidates {
			for _, a := range candidate.Attempts {
				if a.ChildSessionID == in.ChildSessionID {
					return zero, ErrDesignConflict
				}
			}
		}
		c.Attempts = append(c.Attempts, DesignAttempt{Number: len(c.Attempts) + 1, ChildSessionID: in.ChildSessionID, RunID: in.RunID, State: DesignRunning})
	case DesignCancelRequested:
		// A repair may be admitted after a failed attempt. Cancellation in that
		// gap must fence the next allocation without rewriting failed provenance.
		if c.State == DesignFailed {
			if len(c.Attempts) == 0 {
				if in.ChildSessionID != "" || in.RunID != "" {
					return zero, ErrDesignInvalid
				}
			} else {
				a := c.Attempts[len(c.Attempts)-1]
				if a.ChildSessionID != in.ChildSessionID || a.RunID != in.RunID {
					return zero, ErrDesignConflict
				}
			}
			c.State = DesignCancelled
			break
		}
		if c.State != DesignQueued && c.State != DesignRunning {
			return zero, ErrDesignConflict
		}
		if c.State == DesignQueued {
			if in.ChildSessionID != "" || in.RunID != "" {
				return zero, ErrDesignInvalid
			}
			c.State = DesignCancelled
		} else {
			a := &c.Attempts[len(c.Attempts)-1]
			if a.ChildSessionID != in.ChildSessionID || a.RunID != in.RunID {
				return zero, ErrDesignConflict
			}
			a.State = DesignCancelRequested
		}
	case DesignFailed, DesignInterrupted, DesignCancelled:
		if c.State != DesignRunning && c.State != DesignCancelRequested {
			return zero, ErrDesignConflict
		}
		if in.State == DesignCancelled && c.State != DesignCancelRequested {
			return zero, ErrDesignConflict
		}
		if in.ReasonCode == "" {
			return zero, ErrDesignInvalid
		}
		a := &c.Attempts[len(c.Attempts)-1]
		if a.ChildSessionID != in.ChildSessionID || a.RunID != in.RunID {
			return zero, ErrDesignConflict
		}
		a.State, a.ReasonCode = in.State, in.ReasonCode
	default:
		return zero, ErrDesignInvalid
	}
	if c.State != DesignCancelled {
		c.State = in.State
	}
	b := s.db.NewBatch()
	defer b.Close()
	result, err := s.designCommitRequest(p, r, receipt, in, b)
	if err == nil {
		s.WakeDesign()
	}
	return result, err
}

// PublishDesignRevision preserves exact UTF-8 bytes and commits the immutable
// revision, artifact counter, successful attempt and receipt together. It NEVER
// changes selection. HTML safety/standalone validation and rendering sandboxing
// belong to the runtime/preview boundary, not to a lossy store sanitizer.
func (s *Store) PublishDesignRevision(p DesignPrincipal, requestID string, in DesignPublication) (DesignRequest, error) {
	var zero DesignRequest
	if err := designOwner(p); err != nil {
		return zero, err
	}
	if !designID(requestID) || !designID(in.IdempotencyKey) || len(in.Content) == 0 || len(in.Content) > MaxDesignContentBytes || !utf8.Valid(in.Content) || (in.Kind != DesignHTML && in.Kind != DesignPlan) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	receipt := designKey(p, "publish", requestID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	r, err := s.GetDesignRequest(p, requestID)
	if err != nil {
		return zero, err
	}
	if r.Revision != in.ExpectedRevision {
		return zero, ErrDesignConflict
	}
	if in.Candidate < 0 || in.Candidate >= len(r.Candidates) {
		return zero, ErrDesignInvalid
	}
	c := &r.Candidates[in.Candidate]
	if (c.State != DesignRunning && c.State != DesignCancelRequested) || c.Spec.Kind != in.Kind {
		return zero, ErrDesignConflict
	}
	attempt := &c.Attempts[len(c.Attempts)-1]
	if c.State == DesignCancelRequested {
		// Only a real canonical completion that won the cancellation race may
		// publish. Cancellation alone never grants this exception.
		intent, ok, err := NewSessionStore(s).GetV3SessionRunIntent(attempt.ChildSessionID, attempt.RunID)
		if err != nil {
			return zero, err
		}
		if !ok || intent.AccountScopeID != p.AccountID || intent.UserID != p.PrincipalID || intent.Status != V3RunIntentCompleted {
			return zero, ErrDesignConflict
		}
	}
	if attempt.ChildSessionID != in.ChildSessionID || attempt.RunID != in.RunID {
		return zero, ErrDesignConflict
	}
	if err := s.designPublicationEvidence(p, *attempt, in.Content); err != nil {
		return zero, err
	}
	a, err := s.GetDesignArtifact(p, c.Spec.ArtifactID)
	if err != nil {
		return zero, err
	}
	if a.RevisionCount == ^uint64(0) {
		return zero, ErrDesignConflict
	}
	a.RevisionCount++
	ref := DesignRef{ArtifactID: a.ID, Revision: a.RevisionCount, SHA256: designDigest(in.Content)}
	// Refuse corruption/overwrite even if the mutable counter was damaged.
	if _, ok, err := s.GetBytes(designRevisionKey(p, a.ID, ref.Revision)); err != nil || ok {
		if err == nil {
			err = ErrDesignConflict
		}
		return zero, err
	}
	attempt.State, attempt.Result = DesignSucceeded, &ref
	c.State = DesignSucceeded
	rev := DesignRevision{Ref: ref, Kind: in.Kind, RequestID: requestID, Candidate: in.Candidate, Attempt: *attempt, Base: c.Spec.Base, Content: in.Content}
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, designRevisionKey(p, a.ID, ref.Revision), rev); err != nil {
		return zero, err
	}
	if err := designSet(b, designKey(p, "artifact", a.ID), a); err != nil {
		return zero, err
	}
	return s.designCommitRequest(p, r, receipt, in, b)
}

// SelectDesignRevision uses both exact current ref and monotonic version to
// reject ABA. Each successful selection retains an immutable audit receipt.
func (s *Store) SelectDesignRevision(p DesignPrincipal, in DesignSelection) (DesignArtifact, error) {
	var zero DesignArtifact
	if err := designOwner(p); err != nil {
		return zero, err
	}
	if !designID(in.IdempotencyKey) || !designID(in.Ref.ArtifactID) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	receipt := designKey(p, "select", in.Ref.ArtifactID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	if _, err := s.ReadDesignRevision(p, in.Ref); err != nil {
		return zero, err
	}
	a, err := s.GetDesignArtifact(p, in.Ref.ArtifactID)
	if err != nil {
		return zero, err
	}
	if a.SelectionVersion != in.ExpectedVersion || !designRefEqual(a.Selected, in.ExpectedCurrent) || a.SelectionVersion == ^uint64(0) {
		return zero, ErrDesignConflict
	}
	a.Selected = &in.Ref
	a.SelectionVersion++
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, designKey(p, "artifact", a.ID), a); err != nil {
		return zero, err
	}
	if err := designSet(b, designKey(p, "selection", fmt.Sprintf("%s/%020d", a.ID, a.SelectionVersion)), a); err != nil {
		return zero, err
	}
	if err := designSaveReceipt(b, receipt, in, a); err != nil {
		return zero, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return zero, err
	}
	return a, nil
}

func (s *Store) DesignSelectionHistory(p DesignPrincipal, artifactID string, after uint64, limit int) ([]DesignArtifact, error) {
	if limit < 1 || limit > MaxDesignHistoryPage {
		return nil, ErrDesignInvalid
	}
	a, err := s.GetDesignArtifact(p, artifactID)
	if err != nil {
		return nil, err
	}
	rows := make([]DesignArtifact, 0, limit)
	for n := after; n < a.SelectionVersion && len(rows) < limit; {
		n++
		var row DesignArtifact
		if err := s.designGet(designKey(p, "selection", fmt.Sprintf("%s/%020d", artifactID, n)), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
