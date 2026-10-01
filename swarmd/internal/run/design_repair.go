package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"swarm/packages/swarmd/internal/htmlcapture"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Installed before dispatcher startup; nil deliberately fails closed.
type DesignRenderer interface {
	CaptureStandalone(context.Context, htmlcapture.StandaloneRequest) (htmlcapture.StandaloneResult, error)
}

func (s *Service) SetDesignRenderer(renderer DesignRenderer) { s.designRenderer = renderer }

func repairableDesignCode(code string) bool {
	switch code {
	case "invalid_html", "invalid_text", "empty_output", "browser_runtime_error", "network_blocked":
		return true
	}
	return false
}

// The worker owns the stable candidate key across all fresh child allocations.
// Each call releases its capacity lease before a repair is admitted.
func (s *Service) executeDesign(ctx context.Context, p store.DesignPrincipal, id string, candidate int) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	initial, err := s.sessions.DesignStore().GetDesignRequest(p, id)
	if err != nil || candidate < 0 || candidate >= len(initial.Candidates) || initial.Candidates[candidate].State != store.DesignQueued {
		return
	}
	for attempt := 1; attempt <= 3 && ctx.Err() == nil; attempt++ {
		s.executeDesignAttempt(ctx, p, id, candidate, attempt)
		r, err := s.sessions.DesignStore().GetDesignRequest(p, id)
		if err != nil || candidate < 0 || candidate >= len(r.Candidates) {
			return
		}
		c := r.Candidates[candidate]
		if c.State != store.DesignFailed || len(c.Attempts) != attempt {
			return
		}
		a := c.Attempts[attempt-1]
		if a.Validation == nil || a.Validation.Passed || !repairableDesignCode(a.Validation.Code) {
			return
		}
	}
}

// Only storage CAS is retried. Provider calls and browser observations are never
// inside this loop. Request-wide sibling writes cannot discard response evidence.
func (s *Service) designEvidenceCAS(p store.DesignPrincipal, id string, write func(uint64) error) error {
	for n := 0; n < 32; n++ {
		r, err := s.sessions.DesignStore().GetDesignRequest(p, id)
		if err != nil {
			return err
		}
		err = write(r.Revision)
		if !errors.Is(err, store.ErrDesignConflict) {
			return err
		}
	}
	return store.ErrDesignConflict
}

func (s *Service) retainAndValidateDesign(ctx context.Context, p store.DesignPrincipal, id string, candidate int, response provideriface.Response, providerErr error, toolCalls bool) (string, error) {
	db := s.sessions.DesignStore()
	r, err := db.GetDesignRequest(p, id)
	if err != nil {
		return store.DesignInterrupted, err
	}
	a := r.Candidates[candidate].Attempts[len(r.Candidates[candidate].Attempts)-1]
	digest := sha256.Sum256([]byte(response.Text))
	ref := store.DesignOutputRef{RequestID: id, Candidate: candidate, Attempt: a.Number, ChildSessionID: a.ChildSessionID, RunID: a.RunID, SHA256: hex.EncodeToString(digest[:])}
	x := store.DesignResponse{Ref: ref, Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, Content: []byte(response.Text)}
	u := response.Usage
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.ThinkingTokens != 0 || u.TotalTokens != 0 || u.CacheWriteTokens != 0 || len(u.APIUsageRaw) != 0 {
		x.Usage = &store.DesignResponseUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: u.CacheReadTokens, ThinkingTokens: u.ThinkingTokens, TotalTokens: u.TotalTokens, CacheWriteTokens: u.CacheWriteTokens}
	}
	err = s.designEvidenceCAS(p, id, func(rev uint64) error {
		_, err := db.RecordDesignResponse(p, store.DesignResponseMutation{IdempotencyKey: fmt.Sprintf("response-%s-%d", a.RunID, rev), ExpectedRevision: rev, Response: x})
		return err
	})
	if err != nil {
		return store.DesignInterrupted, err
	}
	// Provider/infrastructure failures retain bounded bytes but never receive a
	// content-repair diagnostic. Cancellation likewise cannot launch a repair.
	if ctx.Err() != nil {
		return store.DesignInterrupted, nil
	}
	if providerErr != nil || toolCalls {
		return store.DesignFailed, nil
	}
	code, passed, png := "invalid_html", false, []byte(nil)
	kind := r.Candidates[candidate].Spec.Kind
	if !validDesignOutput(kind, response.Text) {
		if kind == store.DesignPlan {
			code = "invalid_text"
		}
		if response.Text == "" {
			code = "empty_output"
		}
	} else if kind == store.DesignPlan {
		code, passed = "plan_valid", true
	} else if s.designRenderer == nil {
		code = "renderer_unavailable"
	} else {
		result, renderErr := s.designRenderer.CaptureStandalone(ctx, htmlcapture.StandaloneRequest{HTML: x.Content})
		if renderErr == nil {
			if result.SourceSHA256 == ref.SHA256 {
				code, passed, png = "renderable", true, result.PNG
			} else {
				code = "preview_invalid"
			}
		} else {
			code = "renderer_unavailable"
			var diagnostic *htmlcapture.StandaloneError
			if errors.As(renderErr, &diagnostic) {
				switch diagnostic.Code {
				case "standalone_runtime_exception", "standalone_not_ready":
					if diagnostic.FailureClass == "content" {
						code = "browser_runtime_error"
					}
				case "standalone_security_violation":
					if diagnostic.FailureClass == "content" {
						code = "network_blocked"
					}
				case "standalone_timeout":
					code = "render_timeout"
				}
			}
		}
	}
	if ctx.Err() != nil {
		code, passed, png = "validation_interrupted", false, nil
	}
	err = s.designEvidenceCAS(p, id, func(rev uint64) error {
		_, err := db.RecordDesignValidation(p, store.DesignValidationMutation{IdempotencyKey: fmt.Sprintf("validation-%s-%d", a.RunID, rev), ExpectedRevision: rev, Output: ref, Passed: passed, Code: code, PNG: png})
		return err
	})
	if err != nil {
		return store.DesignInterrupted, err
	}
	if ctx.Err() != nil {
		return store.DesignInterrupted, nil
	}
	if passed {
		return store.DesignSucceeded, nil
	}
	return store.DesignFailed, nil
}
