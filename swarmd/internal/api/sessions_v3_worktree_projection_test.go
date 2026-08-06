package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionsV3WorktreeProjectionUsesDurableSnapshotFacts(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "worktree-projection.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	server.v3SessionExecutor = nil

	principal := testPrincipal()
	now := time.Now().UnixMilli()
	created := pebblestore.SessionSnapshot{
		ID:                 "worktree-projection",
		UserID:             principal.UserID,
		AccountScopeID:     principal.AccountScopeID,
		Title:              "Worktree projection",
		Mode:               sessionruntime.ModeAuto,
		WorkspacePath:      "/managed/final-worktree",
		WorkspaceName:      "workspace",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/managed/final-worktree",
		WorktreeBaseBranch: "dev",
		WorktreeBranch:     "agent/final-worktree-1",
		Metadata: map[string]any{
			"routed_worktree_name":            "final-worktree-1",
			"swarm_v3_source_workspace_path":  "/source/workspace",
			"swarm_v3_runtime_workspace_path": "/managed/final-worktree",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID,
		ClientRequestID: "create-worktree-projection", IdempotencyKey: "create-worktree-projection",
		PayloadHash: "create-worktree-projection", RequestHash: "create-worktree-projection",
		Kind: sessionruntime.SessionMutationCreateSession, Session: &created, NowUnixMs: now,
	}); err != nil {
		t.Fatalf("persist worktree session facts: %v", err)
	}
	stored, ok, err := sessionSvc.Store().GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}

	body := `{"surface":"desktop","session_ids":["` + created.ID + `"],"history":{"mode":"none"},"resources":{"session_view":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID     map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionViewsByID map[string]sessionsV3SessionView       `json:"session_views_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	session := payload.SessionsByID[created.ID]
	if !session.WorktreeEnabled || session.WorktreeRootPath != stored.WorktreeRootPath || session.WorktreeBaseBranch != "dev" || session.WorktreeBranch != "agent/final-worktree-1" || session.Metadata["routed_worktree_name"] != "final-worktree-1" {
		t.Fatalf("hydrate durable session worktree facts = %+v", session)
	}
	identity := payload.SessionViewsByID[created.ID].Identity
	if identity == nil || !identity.WorktreeEnabled || identity.RequestedWorktreeName != "final-worktree-1" || identity.WorktreeRootPath != stored.WorktreeRootPath || identity.WorktreeBaseBranch != "dev" || identity.WorktreeBranch != "agent/final-worktree-1" {
		t.Fatalf("hydrate durable worktree identity = %+v", identity)
	}

	// Realtime replay fallback must reconstruct the same captured membership facts,
	// not infer them from a current workspace or topology record.
	fallback := v3RealtimeSessionSnapshotFromMembership(pebblestore.V3RealtimeOutboxMembership{
		SessionID:             stored.ID,
		UserID:                stored.UserID,
		AccountScopeID:        stored.AccountScopeID,
		WorkspacePath:         stored.WorkspacePath,
		WorkspaceName:         stored.WorkspaceName,
		WorktreeEnabled:       true,
		RequestedWorktreeName: "final-worktree-1",
		WorktreeRootPath:      stored.WorktreeRootPath,
		WorktreeBaseBranch:    stored.WorktreeBaseBranch,
		WorktreeBranch:        stored.WorktreeBranch,
	})
	fallbackIdentity, err := sessionsV3SessionIdentityFromSnapshot(fallback)
	if err != nil {
		t.Fatalf("project replay identity: %v", err)
	}
	if fallbackIdentity.RequestedWorktreeName != identity.RequestedWorktreeName || fallbackIdentity.WorktreeRootPath != identity.WorktreeRootPath || fallbackIdentity.WorktreeBaseBranch != identity.WorktreeBaseBranch || fallbackIdentity.WorktreeBranch != identity.WorktreeBranch {
		t.Fatalf("replay identity = %+v hydrate identity = %+v", fallbackIdentity, identity)
	}
}
