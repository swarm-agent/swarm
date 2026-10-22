package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type projectContextRetryRunner struct {
	calls atomic.Int32
}

func (r *projectContextRetryRunner) ID() string { return "codex" }
func (r *projectContextRetryRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	r.calls.Add(1)
	return provideriface.Response{Text: "# Recovered project"}, nil
}
func (r *projectContextRetryRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}

// Requirement: usable credential events recover failed project context only in
// their account, without blocking auth or regenerating ready/running projects.
// Threat: onboarding leaves projects permanently failed or duplicate events
// spend multiple attempts. Owners: publishAuthCredentialV3Realtime,
// startProjectContext and ClaimProjectContext. This temporary-store API test
// exercises the real claim/finish boundary with only the provider stubbed.
func TestProjectContextCredentialRetry(t *testing.T) {
	for _, eventType := range []string{"auth.credential.upserted", "auth.credential.activated", "auth.codex.updated"} {
		t.Run(eventType, func(t *testing.T) {
			s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			p := testPrincipal()
			db := sessions.Store()
			failed, err := s.CreateProject(context.Background(), p, pebblestore.ProjectRecord{Name: "Before provider"}, "failed")
			if err != nil {
				t.Fatal(err)
			}
			// WaitForInFlightRuns is a shutdown operation, not a test barrier:
			// it closes admission and also waits for unrelated server workers.
			waitStatus := func(id, status string) {
				t.Helper()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					rec, _, err := db.GetProject(p.AccountScopeID, id)
					if err != nil {
						t.Fatal(err)
					}
					if rec.ContextGeneration.Status == status {
						return
					}
					select {
					case <-deadline.C:
						t.Fatalf("project status = %s, want %s", rec.ContextGeneration.Status, status)
					case <-tick.C:
					}
				}
			}
			waitStatus(failed.ID, "failed")
			failed, _, err = db.GetProject(p.AccountScopeID, failed.ID)
			if err != nil || failed.ContextGeneration.Status != "failed" {
				t.Fatalf("initial failure: %+v %v", failed, err)
			}
			seed := func(account, name, status string) *pebblestore.ProjectRecord {
				t.Helper()
				rec, _, err := db.CreateProjectWithContext(account, name, name, pebblestore.ProjectRecord{Name: name})
				if err != nil {
					t.Fatal(err)
				}
				rec, err = db.ClaimProjectContext(account, rec.ID, rec.ContextGeneration.Attempt, false)
				if err != nil {
					t.Fatal(err)
				}
				if status != "running" {
					failure := ""
					if status == "failed" {
						failure = "no provider"
					}
					rec, err = db.FinishProjectContext(account, rec.ID, rec.ContextGeneration.Attempt, "# Original", failure, "")
					if err != nil {
						t.Fatal(err)
					}
				}
				return rec
			}
			ready := seed(p.AccountScopeID, "ready", "ready")
			running := seed(p.AccountScopeID, "running", "running")
			other := seed("other-account", "other", "failed")
			catalog := pebblestore.NewModelCatalogStore(db.Underlying())
			if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: "codex", Model: "router", ThinkingOptions: []string{"high"}}); err != nil {
				t.Fatal(err)
			}
			runner := &projectContextRetryRunner{}
			s.providers = registry.New()
			s.providers.RegisterRunner(runner)
			s.publishAuthCredentialV3Realtime(p.AccountScopeID, pebblestore.EventEnvelope{EventType: "auth.credential.deleted", GlobalSeq: 1, TsUnixMs: 1})
			if runner.calls.Load() != 0 {
				t.Fatal("deletion triggered retry")
			}
			for seq := uint64(2); seq <= 3; seq++ {
				s.publishAuthCredentialV3Realtime(p.AccountScopeID, pebblestore.EventEnvelope{EventType: eventType, GlobalSeq: seq, TsUnixMs: int64(seq)})
			}
			waitStatus(failed.ID, "ready")
			got, _, err := db.GetProject(p.AccountScopeID, failed.ID)
			if err != nil || got.ContextGeneration.Status != "ready" || got.ContextGeneration.Attempt != failed.ContextGeneration.Attempt+1 || got.ProjectContext != "# Recovered project" {
				t.Fatalf("retry result: %+v %v", got, err)
			}
			if runner.calls.Load() != 1 {
				t.Fatalf("provider calls = %d", runner.calls.Load())
			}
			for _, original := range []*pebblestore.ProjectRecord{ready, running, other} {
				got, _, err := db.GetProject(original.AccountID, original.ID)
				if err != nil || got.ContextGeneration.Attempt != original.ContextGeneration.Attempt || got.ContextGeneration.Status != original.ContextGeneration.Status || got.ProjectContext != original.ProjectContext {
					t.Fatalf("unrelated project changed: %+v %v", got, err)
				}
			}
		})
	}
}
