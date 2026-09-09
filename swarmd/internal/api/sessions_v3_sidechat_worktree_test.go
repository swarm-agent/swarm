package api

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Plan creation and rebind must retain genuine parent lane provenance.
// Threat: metadata-only creation succeeds but run-start rejects the missing base,
// or a repair silently converts borrowed access into ownership or accepts stale facts.
// Authority: handleSessionV3SystemSidechat -> ResolveRuntimeWorkspaceScope ->
// ValidateOwnedIdentity. This API/runtime integration uses real temporary Git
// identity without involving a provider; rejection precedes provider execution.
func TestSessionsV3PlanSidechatWorktreeRuntimeIdentity(t *testing.T) {
	server, sessions, permissions, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	parent := createSessionsV3PrimaryTestSession(t, server, "sidechat-worktree", "Plan")
	root := t.TempDir()
	source, lane := filepath.Join(root, "source"), filepath.Join(root, "lane")
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev", source)
	git("-C", source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := git("-C", source, "rev-parse", "HEAD")
	git("-C", source, "worktree", "add", "-b", "agent/parent", lane)
	parent.WorkspacePath, parent.WorktreeRootPath = lane, lane
	parent.WorktreeEnabled, parent.WorktreeBranch, parent.WorktreeBaseBranch = true, "agent/parent", "dev"
	parent.Metadata["swarm_v3_source_workspace_path"] = source
	parent.Metadata["swarm_v3_runtime_workspace_path"] = lane
	parent.Metadata["swarm_v3_worktree_owner_session_id"] = parent.ID
	parent.Metadata["swarm_v3_worktree_base_commit"], parent.Metadata["base_commit"] = base, base
	parent.ModelProfile = &pebblestore.SessionModelProfileSnapshot{Action: pebblestore.ModelProfileSelection{Provider: "test", Model: "action"}, Plan: &pebblestore.ModelProfileSelection{Provider: "test", Model: "plan"}}
	if err := sessions.Store().UpdateSession(parent); err != nil {
		t.Fatal(err)
	}
	pending := createSessionsV3PlanInvariantPermission(t, permissions, parent.ID)
	parent, _, _ = sessions.GetSession(parent.ID)
	resolver := runruntime.NewService(sessions, nil, nil, nil, permissions, nil, nil, nil)
	sideID, _ := sessionsV3SystemSidechatID(parent.ID, "plan")
	open := func() pebblestore.SessionSnapshot {
		t.Helper()
		rec := postSessionsV3PlanStateSidechat(t, server, parent.ID, pending.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
		}
		side, ok, err := sessions.GetSession(sideID)
		if err != nil || !ok {
			t.Fatalf("get sidechat: %t %v", ok, err)
		}
		scope, err := resolver.ResolveRuntimeWorkspaceScope(side, testPrincipal())
		if err != nil {
			t.Fatalf("real runtime resolution: %v", err)
		}
		if scope.PrimaryPath != lane || scope.WorktreeBaseCommit != base || side.Metadata["swarm_v3_worktree_owner_session_id"] != parent.ID {
			t.Fatalf("lost borrowed provenance: %+v", scope)
		}
		return side
	}
	side := open()
	appendSessionsV3PrimaryTestUserMessage(t, server, side.ID, "retained-conversation", "Keep this discussion")
	side, _, _ = sessions.GetSession(side.ID)
	messages, err := sessions.ListSessionMessages(side.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("seed messages: %v %+v", err, messages)
	}
	// Simulate a sidebar persisted by the affected version, including reopening
	// the same proposal more than once (the old bind idempotency key is reused).
	for i := 0; i < 2; i++ {
		delete(side.Metadata, "swarm_v3_worktree_base_commit")
		delete(side.Metadata, "base_commit")
		delete(side.Metadata, "swarm_v3_worktree_owner_session_id")
		if err := sessions.Store().UpdateSession(side); err != nil {
			t.Fatal(err)
		}
		side = open()
		retained, err := sessions.ListSessionMessages(side.ID, 0, 10)
		if err != nil || !reflect.DeepEqual(retained, messages) {
			t.Fatal("rebind lost conversation")
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*pebblestore.SessionSnapshot)
	}{
		{"missing-base", func(p *pebblestore.SessionSnapshot) {
			delete(p.Metadata, "base_commit")
			delete(p.Metadata, "swarm_v3_worktree_base_commit")
		}},
		{"foreign-owner", func(p *pebblestore.SessionSnapshot) { p.Metadata["swarm_v3_worktree_owner_session_id"] = "foreign" }},
		{"foreign-account", func(p *pebblestore.SessionSnapshot) { p.AccountScopeID = "foreign" }},
		{"stale-branch", func(p *pebblestore.SessionSnapshot) { p.WorktreeBranch = "agent/stale" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, _, _ := sessions.GetSession(parent.ID)
			tc.mutate(&bad)
			if err := sessions.Store().UpdateSession(bad); err != nil {
				t.Fatal(err)
			}
			if _, err := resolver.ResolveRuntimeWorkspaceScope(side, testPrincipal()); err == nil {
				t.Fatal("invalid parent accepted")
			}
			after, _, _ := sessions.GetSession(parent.ID)
			if !reflect.DeepEqual(after, bad) {
				t.Fatal("rejection mutated parent")
			}
			if err := sessions.Store().UpdateSession(parent); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A reserved identifier must not let rebind overwrite a foreign sidechat.
	foreignSide, _, _ := sessions.GetSession(sideID)
	foreignSide.AccountScopeID = "foreign"
	if err := sessions.Store().UpdateSession(foreignSide); err != nil {
		t.Fatal(err)
	}
	rejected := postSessionsV3PlanStateSidechat(t, server, parent.ID, pending.ID)
	if rejected.Code != http.StatusConflict {
		t.Fatalf("foreign rebind: %d %s", rejected.Code, rejected.Body.String())
	}
	unchanged, _, _ := sessions.GetSession(sideID)
	if !reflect.DeepEqual(unchanged, foreignSide) {
		t.Fatal("foreign sidechat overwritten")
	}
	if err := sessions.Store().UpdateSession(side); err != nil {
		t.Fatal(err)
	}
	foreign := testPrincipal()
	foreign.UserID = "foreign"
	if _, err := resolver.ResolveRuntimeWorkspaceScope(side, foreign); err == nil {
		t.Fatal("foreign principal accepted")
	}
	side.WorktreeBranch = "agent/stale"
	if _, err := resolver.ResolveRuntimeWorkspaceScope(side, testPrincipal()); err == nil {
		t.Fatal("stale sidebar accepted")
	}
	after, _, _ := sessions.GetSession(parent.ID)
	if !reflect.DeepEqual(after, parent) {
		t.Fatal("sidebar changed parent snapshot")
	}
	remaining, err := permissions.ListPending(parent.ID, 10)
	if err != nil || len(remaining) != 1 || !reflect.DeepEqual(remaining[0], pending) {
		t.Fatalf("pending approval changed: %v %+v", err, remaining)
	}
}
