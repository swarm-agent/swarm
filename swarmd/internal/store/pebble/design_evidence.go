package pebblestore

import (
	"bytes"
	"fmt"
	"image/png"
	"strings"
	"unicode/utf8"
)

const MaxDesignPreviewBytes = 4 << 20

// Output refs are NOT ready revision refs and cannot be selected or used as edit bases.
type DesignOutputRef struct {
	RequestID      string `json:"request_id"`
	Candidate      int    `json:"candidate"`
	Attempt        int    `json:"attempt"`
	ChildSessionID string `json:"child_session_id"`
	RunID          string `json:"run_id"`
	SHA256         string `json:"sha256"`
}

// Nil usage means unavailable, not zero. Populate only from provider receipts.
type DesignResponseUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	ThinkingTokens    int64 `json:"thinking_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
}

type DesignResponse struct {
	Ref      DesignOutputRef      `json:"ref"`
	Provider string               `json:"provider"`
	Model    string               `json:"model"`
	Thinking string               `json:"thinking,omitempty"`
	Usage    *DesignResponseUsage `json:"usage,omitempty"`
	Content  []byte               `json:"content"`
}

type DesignResponseMutation struct {
	IdempotencyKey   string         `json:"idempotency_key"`
	ExpectedRevision uint64         `json:"expected_revision"`
	Response         DesignResponse `json:"response"`
}

type DesignPreviewRef struct {
	Output DesignOutputRef `json:"output"`
	SHA256 string          `json:"sha256"`
}

type DesignValidation struct {
	Output  DesignOutputRef   `json:"output"`
	Passed  bool              `json:"passed"`
	Code    string            `json:"code"`
	Preview *DesignPreviewRef `json:"preview,omitempty"`
}

type DesignValidationMutation struct {
	IdempotencyKey   string          `json:"idempotency_key"`
	ExpectedRevision uint64          `json:"expected_revision"`
	Output           DesignOutputRef `json:"output"`
	Passed           bool            `json:"passed"`
	Code             string          `json:"code"`
	PNG              []byte          `json:"png,omitempty"`
}

func designOutputValid(ref DesignOutputRef) bool {
	return designID(ref.RequestID) && ref.Candidate >= 0 && ref.Candidate < MaxDesignCandidates && ref.Attempt > 0 && ref.Attempt <= MaxDesignAttempts && designID(ref.ChildSessionID) && designID(ref.RunID) && len(ref.SHA256) == 64
}

func designEvidenceKey(p DesignPrincipal, kind string, ref DesignOutputRef) string {
	return designKey(p, kind, fmt.Sprintf("%s/%d/%d", ref.RequestID, ref.Candidate, ref.Attempt))
}

func designEvidenceAttempt(r *DesignRequest, ref DesignOutputRef, revision uint64) (*DesignAttempt, error) {
	if r.Revision != revision || ref.Candidate >= len(r.Candidates) {
		return nil, ErrDesignConflict
	}
	c := &r.Candidates[ref.Candidate]
	if (c.State != DesignRunning && c.State != DesignCancelRequested) || len(c.Attempts) != ref.Attempt {
		return nil, ErrDesignConflict
	}
	a := &c.Attempts[ref.Attempt-1]
	if a.Number != ref.Attempt || a.ChildSessionID != ref.ChildSessionID || a.RunID != ref.RunID {
		return nil, ErrDesignConflict
	}
	return a, nil
}

// RecordDesignResponse must complete before any validation. Even empty, binary or
// invalid HTML output is retained exactly; only the byte budget is enforced here.
func (s *Store) RecordDesignResponse(p DesignPrincipal, in DesignResponseMutation) (DesignRequest, error) {
	var zero DesignRequest
	x := in.Response
	if designOwner(p) != nil || !designID(in.IdempotencyKey) || !designOutputValid(x.Ref) || len(x.Content) > MaxDesignContentBytes || designDigest(x.Content) != x.Ref.SHA256 || !designModelLabel(x.Provider, false) || !designModelLabel(x.Model, false) || !designModelLabel(x.Thinking, true) {
		return zero, ErrDesignInvalid
	}
	if x.Usage != nil && (x.Usage.InputTokens < 0 || x.Usage.OutputTokens < 0 || x.Usage.CachedInputTokens < 0 || x.Usage.ThinkingTokens < 0 || x.Usage.TotalTokens < 0 || x.Usage.CacheWriteTokens < 0) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	receipt := designKey(p, "response-receipt", x.Ref.RequestID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	r, err := s.GetDesignRequest(p, x.Ref.RequestID)
	if err != nil {
		return zero, err
	}
	a, err := designEvidenceAttempt(&r, x.Ref, in.ExpectedRevision)
	if err != nil {
		return zero, err
	}
	if a.Output != nil || (a.EvidenceRequired && (a.Provider != x.Provider || a.Model != x.Model || a.Thinking != x.Thinking)) {
		return zero, ErrDesignConflict
	}
	key := designEvidenceKey(p, "response", x.Ref)
	if _, ok, err := s.GetBytes(key); err != nil {
		return zero, err
	} else if ok {
		return zero, ErrDesignConflict
	}
	a.EvidenceRequired, a.Output, a.Usage = true, &x.Ref, x.Usage
	a.Provider, a.Model, a.Thinking = x.Provider, x.Model, x.Thinking
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, key, x); err != nil {
		return zero, err
	}
	return s.designCommitRequest(p, r, receipt, in, b)
}

func designModelLabel(v string, empty bool) bool {
	if len(v) > 256 || !utf8.ValidString(v) || (!empty && strings.TrimSpace(v) == "") {
		return false
	}
	for _, c := range v {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func (s *Store) ReadDesignResponse(p DesignPrincipal, ref DesignOutputRef) (DesignResponse, error) {
	var out DesignResponse
	if designOwner(p) != nil || !designOutputValid(ref) {
		return out, ErrDesignInvalid
	}
	if _, err := s.GetDesignRequest(p, ref.RequestID); err != nil {
		return out, err
	}
	if err := s.designGet(designEvidenceKey(p, "response", ref), &out); err != nil {
		return out, err
	}
	if out.Ref != ref || len(out.Content) > MaxDesignContentBytes || designDigest(out.Content) != ref.SHA256 {
		return DesignResponse{}, ErrDesignConflict
	}
	return out, nil
}

// Codes are an allowlist, never browser console text, URLs or provider errors.
func designValidationCode(passed bool, code string) bool {
	if passed {
		return code == "renderable" || code == "plan_valid"
	}
	switch code {
	case "invalid_html", "invalid_text", "empty_output", "browser_runtime_error", "render_timeout", "renderer_unavailable", "preview_invalid", "network_blocked", "validation_interrupted":
		return true
	}
	return false
}

func designValidPNG(data []byte) bool {
	if len(data) == 0 || len(data) > MaxDesignPreviewBytes {
		return false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 8*1024*1024 {
		return false
	}
	_, err = png.Decode(bytes.NewReader(data))
	return err == nil
}

// Validation is a trusted renderer observation, not aesthetic approval. One
// immutable observation per output; repairs require a new canonical child attempt.
func (s *Store) RecordDesignValidation(p DesignPrincipal, in DesignValidationMutation) (DesignRequest, error) {
	var zero DesignRequest
	if designOwner(p) != nil || !designID(in.IdempotencyKey) || !designOutputValid(in.Output) || !designValidationCode(in.Passed, in.Code) || len(in.PNG) > MaxDesignPreviewBytes {
		return zero, ErrDesignInvalid
	}
	if (!in.Passed && len(in.PNG) != 0) || (in.Passed && in.Code == "renderable" && !designValidPNG(in.PNG)) || (in.Code == "plan_valid" && len(in.PNG) != 0) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	receipt := designKey(p, "validation-receipt", in.Output.RequestID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	r, err := s.GetDesignRequest(p, in.Output.RequestID)
	if err != nil {
		return zero, err
	}
	a, err := designEvidenceAttempt(&r, in.Output, in.ExpectedRevision)
	if err != nil {
		return zero, err
	}
	if a.Output == nil || *a.Output != in.Output || a.Validation != nil {
		return zero, ErrDesignConflict
	}
	x, err := s.ReadDesignResponse(p, in.Output)
	if err != nil {
		return zero, err
	}
	if in.Passed && (len(x.Content) == 0 || !utf8.Valid(x.Content) || (r.Candidates[in.Output.Candidate].Spec.Kind == DesignPlan) != (in.Code == "plan_valid")) {
		return zero, ErrDesignInvalid
	}
	key := designEvidenceKey(p, "validation", in.Output)
	if _, ok, err := s.GetBytes(key); err != nil {
		return zero, err
	} else if ok {
		return zero, ErrDesignConflict
	}
	v := DesignValidation{Output: in.Output, Passed: in.Passed, Code: in.Code}
	if len(in.PNG) > 0 {
		v.Preview = &DesignPreviewRef{Output: in.Output, SHA256: designDigest(in.PNG)}
	}
	a.Validation = &v
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, key, v); err != nil {
		return zero, err
	}
	if v.Preview != nil {
		if err := designSet(b, designEvidenceKey(p, "preview", in.Output), in.PNG); err != nil {
			return zero, err
		}
	}
	return s.designCommitRequest(p, r, receipt, in, b)
}

func (s *Store) ReadDesignValidation(p DesignPrincipal, ref DesignOutputRef) (DesignValidation, error) {
	var v DesignValidation
	if _, err := s.ReadDesignResponse(p, ref); err != nil {
		return v, err
	}
	if err := s.designGet(designEvidenceKey(p, "validation", ref), &v); err != nil {
		return v, err
	}
	if v.Output != ref {
		return DesignValidation{}, ErrDesignConflict
	}
	return v, nil
}

func (s *Store) ReadDesignPreview(p DesignPrincipal, ref DesignPreviewRef) ([]byte, error) {
	v, err := s.ReadDesignValidation(p, ref.Output)
	if err != nil {
		return nil, err
	}
	if !v.Passed || v.Preview == nil || *v.Preview != ref {
		return nil, ErrDesignConflict
	}
	var data []byte
	if err := s.designGet(designEvidenceKey(p, "preview", ref.Output), &data); err != nil {
		return nil, err
	}
	if designDigest(data) != ref.SHA256 || !designValidPNG(data) {
		return nil, ErrDesignConflict
	}
	return data, nil
}

func (s *Store) designPublicationEvidence(p DesignPrincipal, a DesignAttempt, content []byte) error {
	if !a.EvidenceRequired && a.Output == nil {
		return nil
	} // old records/low-level callers only
	if a.Output == nil {
		return ErrDesignConflict
	}
	x, err := s.ReadDesignResponse(p, *a.Output)
	if err != nil {
		return err
	}
	if !bytes.Equal(x.Content, content) {
		return ErrDesignConflict
	}
	v, err := s.ReadDesignValidation(p, *a.Output)
	if err != nil {
		return err
	}
	if !v.Passed {
		return ErrDesignConflict
	}
	if v.Code == "renderable" {
		if v.Preview == nil {
			return ErrDesignConflict
		}
		_, err = s.ReadDesignPreview(p, *v.Preview)
		return err
	}
	if v.Code != "plan_valid" {
		return ErrDesignConflict
	}
	return nil
}
