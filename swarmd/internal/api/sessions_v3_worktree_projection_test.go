package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3WorktreeProjectionUsesDurableSnapshotFacts(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "worktree-projection", "Worktree projection", "/source/workspace")

	stored, ok, err := sessionSvc.Store().GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	stored.WorkspacePath = "/managed/final-worktree"
	stored.WorktreeEnabled = true
	stored.WorktreeRootPath = "/managed/final-worktree"
	stored.WorktreeBaseBranch = "dev"
	stored.WorktreeBranch = "agent/final-worktree-1"
	if stored.Metadata == nil {
		stored.Metadata = map[string]any{}
	}
	stored.Metadata["routed_worktree_name"] = "final-worktree-1"
	stored.Metadata["swarm_v3_source_workspace_path"] = "/source/workspace"
	stored.Metadata["swarm_v3_runtime_workspace_path"] = stored.WorktreeRootPath
	stored.UpdatedAt++
	if err := sessionSvc.Store().UpdateSession(stored); err != nil {
		t.Fatalf("persist worktree session facts: %v", err)
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
