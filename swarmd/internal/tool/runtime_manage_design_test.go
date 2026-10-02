package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the model boundary must reject output authoring, foreign identity and
// unknown nested fields before persistence. parseDesignToolArgs is the narrowest
// layer proving the strict action grammar independently of execution adapters.
func TestManageDesignStrictGrammar(t *testing.T) {
	for _, raw := range []string{
		`{"action":"submit","idempotency_key":"k","candidates":[{"kind":"html","operation":"generate","brief":"card","content":"html"}]}`,
		`{"action":"submit","idempotency_key":"k","candidates":[],"account_id":"foreign"}`,
		`{"action":"status","request_id":"r","content":"html"}`,
		`{"action":"publish","content":"html"}`,
		`{"action":"submit","idempotency_key":"k","candidates":[],"files":[{"path":"a","content":"injected"}]}`,
		`{"action":"cancel","request_id":"r","idempotency_key":"k","expected_revision":1}`,
		`{"action":"select","idempotency_key":"k","ref":{},"expected_version":0}`,
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
		if _, err := parseDesignToolArgs(args); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	found := false
	for _, def := range NewRuntime(1).Definitions() {
		if def.Name == "manage_design" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool not registered")
	}
}

// Purpose: real session mutations and Pebble must retain queued batch identities
// through parent termination and process-store reopen, reject changed replay and
// foreign reads, and cancel only explicitly targeted work. This integration layer
// exercises executeManageDesign -> ApplySessionMutation without a provider.
// Shared source snapshots must survive disk changes; changed retries must not
// overwrite the original, and failed hydration must leave no accepted request.
func TestManageDesignDurableQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	ss := pebblestore.NewSessionStore(db)
	svc := session.NewService(ss, nil)
	r := NewRuntime(1)
	r.sessions = svc
	ctx, scope := artifactToolContext()
	p := pebblestore.DesignPrincipal{AccountID: scope.Principal.AccountScopeID, PrincipalID: scope.Principal.UserID}
	for _, input := range []pebblestore.V3SessionMutationInput{
		{SessionID: scope.SessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: pebblestore.V3SessionMutationCreateSession, IdempotencyKey: "create", PayloadHash: "create", Session: &pebblestore.SessionSnapshot{ID: scope.SessionID}},
		{SessionID: scope.SessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: pebblestore.V3SessionMutationRecordRunIntent, IdempotencyKey: "run", PayloadHash: "run", RunIntent: &pebblestore.V3SessionRunIntent{SessionID: scope.SessionID, RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
	} {
		if _, err := svc.ApplySessionMutation(input); err != nil {
			t.Fatal(err)
		}
	}
	scope.PrimaryPath = t.TempDir()
	scope.Roots = []string{scope.PrimaryPath}
	sourcePath := filepath.Join(scope.PrimaryPath, "card.css")
	if err := os.WriteFile(sourcePath, []byte(".card {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx = WithDesignSourceReadAuthorizer(ctx, func(context.Context, WorkspaceScope, string) error { return nil })
	args := map[string]any{"files": []map[string]any{{"path": "card.css"}}, "action": "submit", "idempotency_key": "batch", "candidates": []map[string]any{{"kind": "html", "operation": "generate", "brief": "card"}, {"kind": "plan", "operation": "generate", "brief": "plan"}}}
	out, err := r.executeManageDesign(ctx, scope, args)
	if err != nil {
		t.Fatal(err)
	}
	var request pebblestore.DesignRequest
	if err := json.Unmarshal([]byte(out), &request); err != nil {
		t.Fatal(err)
	}
	if request.State != pebblestore.DesignQueued || len(request.Candidates) != 2 || request.Candidates[0].Spec.ArtifactID == request.Candidates[1].Spec.ArtifactID {
		t.Fatalf("bad batch %+v", request)
	}
	history, err := r.executeManageDesign(ctx, scope, map[string]any{"action": "history", "artifact_id": request.Candidates[0].Spec.ArtifactID})
	if err != nil {
		t.Fatal(err)
	}
	var historyResult struct {
		Artifact  pebblestore.DesignArtifact   `json:"artifact"`
		Revisions []pebblestore.DesignRevision `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(history), &historyResult); err != nil {
		t.Fatal(err)
	}
	if historyResult.Artifact.ID != request.Candidates[0].Spec.ArtifactID || historyResult.Artifact.SelectionVersion != 0 || historyResult.Artifact.Selected != nil || len(historyResult.Revisions) != 0 {
		t.Fatal("history omits selection precondition", history)
	}
	replay, err := r.executeManageDesign(ctx, scope, args)
	if err != nil || replay != out {
		t.Fatalf("replay %s %v", replay, err)
	}
	if err := os.WriteFile(sourcePath, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.executeManageDesign(ctx, scope, args); err == nil {
		t.Fatal("changed snapshot replay accepted")
	}
	snapshots, err := db.ReadDesignContext(p, request.ID)
	if err != nil || len(snapshots) != 1 || string(snapshots[0].Content) != ".card {}\n" {
		t.Fatal("original shared context changed", err)
	}
	args["idempotency_key"] = "missing-source"
	args["files"] = []map[string]any{{"path": "missing"}}
	if _, err := r.executeManageDesign(ctx, scope, args); err == nil {
		t.Fatal("missing source accepted")
	}
	missingID := designStableID(p.AccountID, p.PrincipalID, scope.SessionID, "missing-source")
	if _, err := db.GetDesignRequest(p, missingID); !errors.Is(err, pebblestore.ErrDesignNotFound) {
		t.Fatal("partial acceptance", err)
	}
	args["idempotency_key"] = "batch"
	args["files"] = []map[string]any{{"path": "card.css"}}
	if err := os.WriteFile(sourcePath, []byte(".card {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args["candidates"] = []map[string]any{{"kind": "html", "operation": "generate", "brief": "changed"}}
	if _, err := r.executeManageDesign(ctx, scope, args); err == nil {
		t.Fatal("changed replay accepted")
	}
	foreign := scope
	foreign.Principal.AccountScopeID = "foreign"
	if _, err := r.executeManageDesign(ctx, foreign, map[string]any{"action": "status", "request_id": request.ID}); !errors.Is(err, pebblestore.ErrDesignNotFound) {
		t.Fatal("foreign read", err)
	}
	if _, err := svc.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: scope.SessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: pebblestore.V3SessionMutationRecordRunIntent, IdempotencyKey: "done", PayloadHash: "done", RunIntent: &pebblestore.V3SessionRunIntent{SessionID: scope.SessionID, RunID: "run-1", Status: pebblestore.V3RunIntentCompleted}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	db, err = pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r.sessions = session.NewService(pebblestore.NewSessionStore(db), nil)
	pending, err := db.ListPendingDesignRequests(p, "", 50)
	if err != nil || len(pending) != 1 || pending[0].State != pebblestore.DesignQueued {
		t.Fatalf("lost work %+v %v", pending, err)
	}
	cancel := map[string]any{"action": "cancel", "request_id": request.ID, "idempotency_key": "cancel", "expected_revision": 1, "candidate": 0}
	cancelled, err := r.executeManageDesign(ctx, scope, cancel)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(cancelled), &request); err != nil {
		t.Fatal(err)
	}
	if request.Candidates[0].State != pebblestore.DesignCancelled || request.Candidates[1].State != pebblestore.DesignQueued {
		t.Fatal("cancellation crossed candidate", request)
	}
	if _, err := r.executeManageDesign(ctx, scope, cancel); err != nil {
		t.Fatal("cancel replay", err)
	}
}
