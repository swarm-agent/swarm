package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/htmlcapture"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type designTestRenderer struct {
	call func(context.Context, htmlcapture.StandaloneRequest) error
}

func (r designTestRenderer) CaptureStandalone(ctx context.Context, req htmlcapture.StandaloneRequest) (htmlcapture.StandaloneResult, error) {
	if r.call != nil {
		if err := r.call(ctx, req); err != nil {
			return htmlcapture.StandaloneResult{}, err
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		return htmlcapture.StandaloneResult{}, err
	}
	digest := sha256.Sum256(req.HTML)
	return htmlcapture.StandaloneResult{PNG: b.Bytes(), SourceSHA256: hex.EncodeToString(digest[:])}, nil
}

// Purpose: executeDesign must persist exact output before invoking the renderer,
// and only content diagnostics can trigger bounded fresh canonical children.
// This service/store/interface test is the narrow hermetic orchestration boundary,
// not browser validation or a live provider benchmark.
func TestDesignRepairFreshChildrenAndBound(t *testing.T) {
	for _, succeeds := range []bool{true, false} {
		t.Run(map[bool]string{true: "repair", false: "exhausted"}[succeeds], func(t *testing.T) {
			s, p, r, runner := designExecutionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			s.SetDesignRenderer(designTestRenderer{call: func(_ context.Context, req htmlcapture.StandaloneRequest) error {
				calls++
				current, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				a := current.Candidates[0].Attempts[calls-1]
				if a.Output == nil {
					t.Fatal("renderer ran before retention")
				}
				x, err := s.sessions.DesignStore().ReadDesignResponse(p, *a.Output)
				if err != nil || !bytes.Equal(x.Content, req.HTML) {
					t.Fatal("wrong exact bytes", err)
				}
				if succeeds && calls == 2 {
					return nil
				}
				return &htmlcapture.StandaloneError{Code: "standalone_runtime_exception", FailureClass: "content"}
			}})
			s.executeDesign(ctx, p, r.ID, 0)
			got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			state := store.DesignFailed
			if succeeds {
				want, state = 2, store.DesignSucceeded
			}
			if len(got.Candidates[0].Attempts) != want || got.Candidates[0].State != state || len(runner.requests) != want {
				t.Fatalf("unbounded/lost attempt: %+v", got)
			}
			for i, req := range runner.requests {
				if !req.ForceFreshProviderContext || !req.StartNewChain || req.ToolChoice != "none" {
					t.Fatal("context authority")
				}
				if i == 0 {
					continue
				}
				prior := runner.requests[i-1]
				if req.SessionID == prior.SessionID || req.ProviderCacheKey == prior.ProviderCacheKey || req.ProviderLineageID == prior.ProviderLineageID {
					t.Fatal("reused child context")
				}
				payload := req.Input[0]["content"].([]map[string]any)[0]["text"].(string)
				var decoded map[string]any
				if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
					t.Fatal(err)
				}
				repair := decoded["repair"].(map[string]any)
				if repair["failed_output"] != "<!doctype html><html><body>card</body></html>" || repair["diagnostic_code"] != "browser_runtime_error" || !strings.Contains(payload, got.Candidates[0].Attempts[i-1].RunID) {
					t.Fatal("missing exact repair evidence")
				}
			}
			s.executeDesign(ctx, p, r.ID, 0)
			if len(runner.requests) != want {
				t.Fatal("terminal failure replayed")
			}
		})
	}
}

// Purpose: renderer/provider infrastructure and cancellation must never be
// interpreted as generated-content errors. Retention must survive all three.
// The adapter/renderer interfaces isolate the failure boundary without live I/O.
func TestDesignRepairInfrastructureNoRetry(t *testing.T) {
	for _, mode := range []string{"renderer", "provider", "cancel", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s, p, r, runner := designExecutionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "missing" {
				s.SetDesignRenderer(nil)
			}
			if mode == "renderer" {
				s.SetDesignRenderer(designTestRenderer{call: func(context.Context, htmlcapture.StandaloneRequest) error {
					return &htmlcapture.StandaloneError{Code: "standalone_timeout", FailureClass: "infrastructure"}
				}})
			}
			if mode == "provider" || mode == "cancel" {
				runner.call = func(context.Context, provideriface.Request) (provideriface.Response, error) {
					if mode == "cancel" {
						cancel()
					}
					return provideriface.Response{Text: "partial returned bytes"}, errors.New("private provider failure")
				}
			}
			s.executeDesign(ctx, p, r.ID, 0)
			got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
			if err != nil || len(runner.requests) != 1 || len(got.Candidates[0].Attempts) != 1 || got.Candidates[0].Attempts[0].Output == nil || got.Candidates[0].Attempts[0].Result != nil {
				t.Fatalf("retry or lost output: %+v %v", got, err)
			}
			want := store.DesignFailed
			if mode == "cancel" {
				want = store.DesignInterrupted
			}
			if got.Candidates[0].State != want {
				t.Fatalf("untruthful terminal state: %+v", got)
			}
			artifact, err := s.sessions.DesignStore().GetDesignArtifact(p, got.Candidates[0].Spec.ArtifactID)
			if err != nil || artifact.RevisionCount != 0 || artifact.Selected != nil {
				t.Fatal("failure published", err)
			}
		})
	}
}

// Purpose: persisted validation success can be published after a crash either
// before or after canonical completion, but response-only uncertainty must stop.
// finishDesign/store is the narrow recovery boundary and must be idempotent.
func TestDesignRepairRecoveryEvidenceWindows(t *testing.T) {
	for _, window := range []string{"response", "validation", "completion"} {
		t.Run(window, func(t *testing.T) {
			s, p, r, runner := designExecutionFixture(t)
			a, lease, err := s.AllocateDesignChild(context.Background(), p, r.ID, r.Revision, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			lease.Release()
			var providerErr error
			if window == "response" {
				providerErr = errors.New("uncertain")
			}
			if _, err := s.retainAndValidateDesign(context.Background(), p, r.ID, 0, provideriface.Response{Text: "<!doctype html><html></html>"}, providerErr, false); err != nil {
				t.Fatal(err)
			}
			if window == "completion" {
				if err := s.designRunState(p, a, store.V3RunIntentPendingExecutor, store.V3RunIntentCompleted); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := s.finishDesign(p, r.ID, 0, nil, store.DesignInterrupted); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
			if err != nil || len(runner.requests) != 0 {
				t.Fatal("replayed provider", err)
			}
			want := store.DesignSucceeded
			if window == "response" {
				want = store.DesignInterrupted
			}
			if got.Candidates[0].State != want || got.Candidates[0].Attempts[0].Output == nil {
				t.Fatalf("bad recovery: %+v", got)
			}
			history, err := s.sessions.DesignStore().DesignHistory(p, got.Candidates[0].Spec.ArtifactID, 0, 10)
			count := 1
			if window == "response" {
				count = 0
			}
			if err != nil || len(history) != count {
				t.Fatal("duplicate ready revision", err)
			}
		})
	}
}

// Purpose: plan-only requests must never invoke browser capture, and retained
// usage must come from the adapter receipt rather than fabricated estimates.
// executeDesign through real evidence storage is the narrow contract layer.
func TestDesignRepairPlanSkipsRendererAndRetainsUsage(t *testing.T) {
	s, p, parent, runner := designExecutionFixture(t)
	r := acceptDesignFixture(t, s, p, parent, "plan-evidence", []store.DesignCandidateSpec{{ArtifactID: "plan-evidence", Kind: store.DesignPlan, Operation: store.DesignGenerate, Brief: "plan only"}}, nil)
	s.SetDesignRenderer(designTestRenderer{call: func(context.Context, htmlcapture.StandaloneRequest) error { t.Fatal("plan rendered"); return nil }})
	runner.call = func(context.Context, provideriface.Request) (provideriface.Response, error) {
		return provideriface.Response{Text: "Keep the layout accessible.", Usage: provideriface.TokenUsage{InputTokens: 17, OutputTokens: 9, CacheReadTokens: 3, ThinkingTokens: 4, TotalTokens: 30, CacheWriteTokens: 2}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.executeDesign(ctx, p, r.ID, 0)
	got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || got.State != store.DesignSucceeded {
		t.Fatalf("plan: %+v %v", got, err)
	}
	a := got.Candidates[0].Attempts[0]
	if a.Usage == nil || a.Usage.InputTokens != 17 || a.Usage.OutputTokens != 9 || a.Usage.CachedInputTokens != 3 || a.Usage.ThinkingTokens != 4 || a.Usage.TotalTokens != 30 || a.Usage.CacheWriteTokens != 2 || a.Validation.Code != "plan_valid" || a.Validation.Preview != nil {
		t.Fatal("lost receipt or fabricated preview")
	}
}

// Requirement: cancellation between terminal content failure and fresh repair
// allocation must fence the next child without changing the failed receipt.
// Threat: a cancel accepted at a retry boundary nevertheless dispatching more
// provider work. RecordDesignAttempt + AllocateDesignChild are the narrow store/
// service boundary; no live provider is needed to prove zero submission.
func TestDesignRepairCancellationBetweenAttempts(t *testing.T) {
	s, p, r, runner := designExecutionFixture(t)
	s.SetDesignRenderer(designTestRenderer{call: func(context.Context, htmlcapture.StandaloneRequest) error {
		return &htmlcapture.StandaloneError{Code: "standalone_runtime_exception", FailureClass: "content"}
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.executeDesignAttempt(ctx, p, r.ID, 0, 1)
	failed, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || failed.Candidates[0].State != store.DesignFailed {
		t.Fatalf("missing failure: %+v %v", failed, err)
	}
	a := failed.Candidates[0].Attempts[0]
	cancelled, err := s.sessions.DesignStore().RecordDesignAttempt(p, r.ID, store.DesignAttemptMutation{IdempotencyKey: "cancel-gap", ExpectedRevision: failed.Revision, Candidate: 0, State: store.DesignCancelRequested, ChildSessionID: a.ChildSessionID, RunID: a.RunID})
	if err != nil || cancelled.Candidates[0].State != store.DesignCancelled {
		t.Fatalf("cancel gap: %+v %v", cancelled, err)
	}
	s.executeDesignAttempt(ctx, p, r.ID, 0, 2)
	got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || len(runner.requests) != 1 || len(got.Candidates[0].Attempts) != 1 || got.Candidates[0].Attempts[0].State != store.DesignFailed {
		t.Fatal("cancel replayed or rewrote failed attempt", err)
	}
	if _, exists, err := s.sessions.GetSession(store.DesignChildID(p, r.ID, 0, 2)); err != nil || exists {
		t.Fatal("cancel allocated repair child", err)
	}
}
