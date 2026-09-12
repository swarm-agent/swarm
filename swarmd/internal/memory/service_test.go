package memory

import (
	"context"
	"errors"
	"path/filepath"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: prove Service's restricted provider boundary rather than prompt text.
// A fake provider observes the exact input/model/limits and injects revocation or
// failure. Real temporary storage proves no unauthorized publication follows.
func serviceFixture(t *testing.T) (*Service, context.Context) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewService(store.NewMemoryStore(db), nil)
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"})
	d, err := s.Store.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	settings := d.Settings
	settings.AutomationEnabled = true
	settings.IncludedWorkspaces = []string{"w"}
	if _, err = s.Configure(ctx, d.Revision, settings); err != nil {
		t.Fatal(err)
	}
	m := store.AgentModelAssignment{Provider: "codex", Model: "configured-default", Thinking: "medium"}
	_, err = store.NewAgentModelSettingsStore(db).PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: "a", Swarm: store.SwarmAgentModelAssignments{Action: m, Plan: m}, SystemAgents: store.SystemAgentModelAssignments{Compact: m, Finder: m, Coder: m, Designer: m, Router: m}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorkspaceEntryByIDForAccount("a", "w"), store.WorkspaceEntry{AccountScopeID: "a", WorkspaceID: "w", State: "active", Path: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	_, err = ss.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: "source", UserID: "u", AccountScopeID: "a", IdempotencyKey: "create", RequestHash: "create", Kind: store.V3SessionMutationCreateSession, Session: &store.SessionSnapshot{ID: "source", WorkspacePath: "/workspace", WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: "w", Path: "/workspace"}}}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ss.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: "source", UserID: "u", AccountScopeID: "a", IdempotencyKey: "message", RequestHash: "message", Kind: store.V3SessionMutationAppendMessage, Message: &store.MessageSnapshot{Role: "user", Content: "Project uses Go"}, RunIntent: &store.V3SessionRunIntent{Status: store.V3RunIntentDispatchBlocked, BlockedReason: "fixture"}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

type fakeProvider struct {
	calls    int
	generate func(context.Context, Request) (Result, error)
}

func (p *fakeProvider) Quote(context.Context, store.AgentModelAssignment, int, int) (int64, error) {
	return 10, nil
}
func (p *fakeProvider) Generate(ctx context.Context, r Request) (Result, error) {
	p.calls++
	return p.generate(ctx, r)
}
func TestMemoryServiceManualAndMissingPrincipal(t *testing.T) {
	s, ctx := serviceFixture(t)
	p := &fakeProvider{generate: func(context.Context, Request) (Result, error) {
		t.Fatal("manual scheduled provider access")
		return Result{}, nil
	}}
	s.Provider = p
	if _, err := s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatal("scheduled manual read")
	}
	if _, err := s.RunNow(context.Background(), "job"); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatal(err)
	}
	s.Provider = nil
	j, err := s.RunNow(ctx, "job")
	if err == nil || j.Status != "failed" {
		t.Fatal("unexpected provider call")
	}
}
func TestMemoryServiceRevocationAndProviderFailure(t *testing.T) {
	for _, mode := range []string{"revoke", "cancel", "failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx := serviceFixture(t)
			p := &fakeProvider{generate: func(callCtx context.Context, r Request) (Result, error) {
				if r.Model.Model != "configured-default" || r.OutputTokens != 2000 || len(r.Input) == 0 {
					t.Fatal("wrong capability snapshot")
				}
				if mode == "failure" {
					return Result{}, errors.New("provider failure")
				}
				if mode == "revoke" {
					d, _ := s.Store.GetForAccount("a")
					settings := d.Settings
					settings.AutomationEnabled = false
					if _, err := s.Configure(ctx, d.Revision, settings); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "cancel" {
					if err := s.Cancel(ctx, "job"); err != nil {
						t.Fatal(err)
					}
				}
				return Result{OutputTokens: 1}, nil
			}}
			s.Provider = p
			j, err := s.RunNow(ctx, "job")
			if mode == "success" {
				if err != nil || j.Status != "completed" {
					t.Fatal(j, err)
				}
			} else if err == nil {
				t.Fatal("revoked/failed call succeeded")
			}
			d, _ := s.Store.GetForAccount("a")
			if len(d.Entries) != 0 {
				t.Fatal("partial apply")
			}
			if p.calls != 1 {
				t.Fatal("provider replay")
			}
			if _, err = s.RunNow(ctx, "job"); err != nil && mode == "success" {
				t.Fatal(err)
			}
			if p.calls != 1 {
				t.Fatal("idempotent retry billed twice")
			}
		})
	}
}

// Purpose: recurring Tick must obey durable due time and pause, not repeatedly
// bill or read in manual mode. Explicit remember remains provider-independent.
func TestMemoryServiceScheduleAndRemember(t *testing.T) {
	s, ctx := serviceFixture(t)
	p := &fakeProvider{generate: func(context.Context, Request) (Result, error) {
		return Result{OutputTokens: 1}, nil
	}}
	s.Provider = p
	d, _ := s.Store.GetForAccount("a")
	settings := d.Settings
	settings.Mode = "recurring"
	if _, err := s.Configure(ctx, d.Revision, settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	j, err := s.Tick(ctx, now)
	if err != nil || j.Status != "completed" {
		t.Fatal(j, err)
	}
	if _, err = s.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatal("schedule billed twice")
	}
	d, _ = s.Store.GetForAccount("a")
	settings = d.Settings
	settings.AutomationEnabled = false
	d, err = s.Configure(ctx, d.Revision, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Tick(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatal("paused scheduler")
	}
	d, err = s.Remember(ctx, d.Revision, store.MemoryEntry{ID: "rule", Kind: "rule", Content: "Use concise answers", Pinned: true}, "explicit user request")
	if err != nil || len(d.Entries) != 1 || !d.Entries[0].Pinned {
		t.Fatal(d, err)
	}
}
