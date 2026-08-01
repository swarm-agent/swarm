package api

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildSessionsV3RoutedStartResponseProjectsDurableAuthority(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		ID:             "routed-session",
		UserID:         "user",
		AccountScopeID: "account",
		Title:          "Authoritative routed title",
		Mode:           "plan",
		WorkspacePath:  "/runtime/worktree",
		WorkspaceName:  "source-name",
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-routed", Thinking: "high", ServiceTier: "fast"},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			UseAccountDefault:  true,
			ActionFavoriteID:   "favorite-action",
			ActionFavoriteName: "Action favorite",
			Action:             pebblestore.ModelProfileSelection{Provider: "codex", Model: "gpt-action", Thinking: "medium"},
			PlanFavoriteID:     "favorite-plan",
			PlanFavoriteName:   "Plan favorite",
			Plan:               &pebblestore.ModelProfileSelection{Provider: "codex", Model: "gpt-routed", Thinking: "high", ServiceTier: "fast"},
			AppliedAt:          100,
		},
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/runtime/worktree",
		WorktreeBaseBranch: "dev",
		WorktreeBranch:     "agent/routed",
		Metadata: map[string]any{
			"workspace_id":                        "worktree-id",
			"swarm_v3_workspace_binding_id":       "binding-id",
			"swarm_v3_source_workspace_id":        "source-id",
			"swarm_v3_source_workspace_name":      "source-name",
			"swarm_v3_source_workspace_path":      "/source/workspace",
			"swarm_v3_runtime_workspace_path":     "/runtime/worktree",
			"swarm_v3_runtime_swarm_id":           "runtime-swarm",
			"swarm_v3_authority_host_swarm_id":    "host-swarm",
			"routed_worktree_name":                 "routed-final-1",
		},
	}
	message := pebblestore.MessageSnapshot{ID: "message-1", SessionID: session.ID, Role: "user", Content: "first prompt", CreatedAt: 101}
	projection := pebblestore.V3SessionProjection{SessionID: session.ID, LastEventSeq: 2, ProjectionHighWatermarkSeq: 2, UpdatedAt: 101}
	mutation := sessionruntime.SessionMutationResult{SessionID: session.ID, Session: &session, Message: &message, Projection: projection}
	view := sessionsV3SessionView{AgenticSettings: sessionsV3AgenticSettings{Mode: session.Mode, EffectivePreference: session.Preference}}

	response, err := (&Server{}).buildSessionsV3RoutedStartResponse(view, session, message, projection, mutation, false)
	if err != nil {
		t.Fatalf("build routed response: %v", err)
	}
	if !response.OK || response.SessionID != session.ID || response.Title != session.Title || response.StartingMode != "plan" {
		t.Fatalf("response authority = %+v", response)
	}
	identity := response.SessionView.Identity
	if identity == nil || identity.WorkspaceBindingID != "binding-id" || identity.SourceWorkspaceID != "source-id" || identity.SourceWorkspacePath != "/source/workspace" || identity.RuntimeWorkspacePath != "/runtime/worktree" {
		t.Fatalf("workspace identity = %+v", identity)
	}
	if !identity.WorktreeEnabled || identity.RequestedWorktreeName != "routed-final-1" || identity.WorkspaceID != "worktree-id" || identity.WorktreeRootPath != "/runtime/worktree" || identity.WorktreeBaseBranch != "dev" || identity.WorktreeBranch != "agent/routed" {
		t.Fatalf("worktree identity = %+v", identity)
	}
	if response.Session.ModelProfile == nil || response.Session.ModelProfile.PlanFavoriteID != "favorite-plan" || response.Session.ModelProfile.Plan.Model != "gpt-routed" {
		t.Fatalf("effective flat favorite snapshot = %+v", response.Session.ModelProfile)
	}
	if response.FirstMessage.Content != "first prompt" || response.Projection.LastEventSeq != 2 {
		t.Fatalf("message/projection = %+v %+v", response.FirstMessage, response.Projection)
	}
	if response.Mutation.Session != nil || response.Mutation.Message == nil {
		t.Fatalf("atomic mutation response = %+v", response.Mutation)
	}
	if response.Replayed {
		t.Fatal("new routed response was marked replayed")
	}

	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal routed response: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode routed response: %v", err)
	}
	for _, key := range []string{"session_id", "title", "starting_mode", "replayed", "session", "session_view", "first_message", "projection", "mutation"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("wire response missing %q: %s", key, string(raw))
		}
	}
}

func TestSessionsV3SessionIdentityReportsOnlyPersistedWorktreeFacts(t *testing.T) {
	identity, err := sessionsV3SessionIdentityFromSnapshot(pebblestore.SessionSnapshot{
		ID:            "plain-session",
		Title:         "Plain",
		WorkspacePath: "/source/workspace",
		WorkspaceName: "source",
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path":  "/source/workspace",
			"swarm_v3_runtime_workspace_path": "/runtime/workspace",
		},
	})
	if err != nil {
		t.Fatalf("project plain identity: %v", err)
	}
	if identity.WorktreeEnabled || identity.RequestedWorktreeName != "" || identity.WorktreeRootPath != "" || identity.WorktreeBaseBranch != "" || identity.WorktreeBranch != "" || identity.WorkspaceID != "" {
		t.Fatalf("plain identity invented worktree facts: %+v", identity)
	}
}

func TestBuildSessionsV3RoutedStartResponseRejectsNonDurableMessage(t *testing.T) {
	session := pebblestore.SessionSnapshot{ID: "session", Title: "Title", Mode: "auto", WorkspacePath: "/workspace", WorkspaceName: "workspace"}
	projection := pebblestore.V3SessionProjection{SessionID: session.ID, LastEventSeq: 2}
	message := pebblestore.MessageSnapshot{ID: "message", SessionID: "other", Role: "user"}
	mutation := sessionruntime.SessionMutationResult{SessionID: session.ID, Message: &message, Projection: projection}
	_, err := (&Server{}).buildSessionsV3RoutedStartResponse(sessionsV3SessionView{}, session, message, projection, mutation, false)
	if err == nil {
		t.Fatal("mismatched first message was accepted")
	}
}

func TestBuildSessionsV3RoutedStartResponseRejectsSplitMutationProjection(t *testing.T) {
	session := pebblestore.SessionSnapshot{ID: "session", Title: "Title", Mode: "auto", WorkspacePath: "/workspace", WorkspaceName: "workspace"}
	message := pebblestore.MessageSnapshot{ID: "message", SessionID: session.ID, Role: "user"}
	projection := pebblestore.V3SessionProjection{SessionID: session.ID, LastEventSeq: 2}
	mutation := sessionruntime.SessionMutationResult{SessionID: session.ID, Message: &message, Projection: pebblestore.V3SessionProjection{SessionID: session.ID, LastEventSeq: 1}}
	_, err := (&Server{}).buildSessionsV3RoutedStartResponse(sessionsV3SessionView{}, session, message, projection, mutation, false)
	if err == nil {
		t.Fatal("split create/message mutation projection was accepted")
	}
}
