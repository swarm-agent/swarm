package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3SyncBootstrapReturnsCanonicalSnapshotScopeAndReplayInstructions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap", "Sync Bootstrap", "/workspace/cp5")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "hello sync")

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5","recent":{"limit":10}},"history":{"mode":"tail","max_messages_per_session":10},"resources":{"messages":true,"run_intents":true,"active_plan":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                     bool                                        `json:"ok"`
		Rev                    uint64                                      `json:"rev"`
		SnapshotEndpointCursor string                                      `json:"snapshot_endpoint_cursor"`
		SyncScope              map[string]string                           `json:"sync_scope"`
		SessionsByID           map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
		ProjectionsBySession   map[string]pebblestore.V3SessionProjection  `json:"projections_by_session"`
		MessagesBySession      map[string][]pebblestore.MessageSnapshot    `json:"messages_by_session"`
		RunIntentsBySession    map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
		SessionOrder           []string                                    `json:"session_order"`
		ReplayInstructions     map[string]any                              `json:"replay_instructions"`
		TombstonesBySession    map[string]any                              `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if !payload.OK || payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("bootstrap sessions_by_id missing created session: %+v", payload.SessionsByID)
	}
	if payload.ProjectionsBySession[created.ID].SessionID != created.ID {
		t.Fatalf("bootstrap projection missing: %+v", payload.ProjectionsBySession)
	}
	if len(payload.MessagesBySession[created.ID]) != 1 {
		t.Fatalf("bootstrap messages_by_session = %+v", payload.MessagesBySession)
	}
	if payload.Rev == 0 || !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
		t.Fatalf("bootstrap cursor/rev invalid: rev=%d cursor=%q", payload.Rev, payload.SnapshotEndpointCursor)
	}
	if payload.SyncScope["surface"] != "desktop" || payload.SyncScope["stream_kind"] != "v3.sync.snapshot" || payload.SyncScope["selector_filter_hash"] == "" {
		t.Fatalf("bootstrap sync_scope invalid: %+v", payload.SyncScope)
	}
	if payload.ReplayInstructions["stream_path"] != V3SyncStreamPath || payload.ReplayInstructions["after_endpoint_cursor"] != payload.SnapshotEndpointCursor {
		t.Fatalf("bootstrap replay instructions invalid: %+v", payload.ReplayInstructions)
	}
	if payload.TombstonesBySession == nil {
		t.Fatalf("bootstrap must include tombstones_by_session map")
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != created.ID {
		t.Fatalf("session_order = %+v", payload.SessionOrder)
	}
}

func TestSessionsV3SelectedSessionHydrateResourcesIncludeEvents(t *testing.T) {
	req := sessionsV3SelectedSessionHydrateRequest(" session-a ")
	if req.SessionIDs[0] != "session-a" {
		t.Fatalf("selected hydrate did not trim session id: %+v", req.SessionIDs)
	}
	if !req.Resources.Messages || !req.Resources.Events || !req.Resources.RunIntents || !req.Resources.CurrentRunState || !req.Resources.SessionView || !req.Resources.ActivePlan {
		t.Fatalf("selected hydrate resources missing durable reconstruction data: %+v", req.Resources)
	}
	if req.History.Mode != pebblestore.V3SyncSnapshotHistoryModeTail || req.History.MaxMessagesPerSession <= 0 || req.History.MaxEventsPerSession <= 0 || req.History.ManifestPolicy != "manifest" {
		t.Fatalf("selected hydrate history missing bounded message/event tail: %+v", req.History)
	}
	resources, err := sessionsV3SelectedSessionHydrateResources("session-a")
	if err != nil {
		t.Fatalf("resolve selected hydrate resources: %v", err)
	}
	resourceSet := map[string]bool{}
	for _, resource := range resources {
		resourceSet[resource] = true
	}
	for _, want := range []string{"messages", "events", "run_intents", "current_run_state", "session_view", "active_plan"} {
		if !resourceSet[want] {
			t.Fatalf("selected hydrate resource set %v missing %s", resources, want)
		}
	}
}

func TestSessionsV3SyncHydrateReturnsDurableUpdatedSessionTitle(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-hydrate-title", "Initial title", "/workspace/hydrate-title")

	updated, ok, err := sessionSvc.Store().GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load session for title update: ok=%t err=%v", ok, err)
	}
	updated.Title = "Canonical generated title"
	updated.UpdatedAt++
	if err := sessionSvc.Store().UpdateSession(updated); err != nil {
		t.Fatalf("persist generated title: %v", err)
	}

	body := `{"surface":"desktop","session_ids":["` + created.ID + `"],"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if got := payload.SessionsByID[created.ID].Title; got != updated.Title {
		t.Fatalf("hydrate durable title = %q, want %q", got, updated.Title)
	}
}

func TestSessionsV3SyncBootstrapMetadataOnlyDoesNotEmitPerSessionMessageKeys(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-metadata-only", "Sync Bootstrap Metadata Only", "/workspace/metadata-only")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "hello metadata only")

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/metadata-only","recent":{"limit":10}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SyncScope                 map[string]string                                        `json:"sync_scope"`
		SessionsByID              map[string]pebblestore.SessionSnapshot                   `json:"sessions_by_id"`
		MessagesBySession         map[string][]pebblestore.MessageSnapshot                 `json:"messages_by_session"`
		EventsBySession           map[string][]pebblestore.V3SessionEvent                  `json:"events_by_session"`
		RunIntentsBySession       map[string][]pebblestore.V3SessionRunIntent              `json:"run_intents_by_session"`
		HistoryManifestsBySession map[string][]pebblestore.V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("metadata-only bootstrap missing session metadata: %+v", payload.SessionsByID)
	}
	if strings.Contains(payload.SyncScope["resource_set"], "messages") || strings.Contains(payload.SyncScope["resource_set"], "events") || strings.Contains(payload.SyncScope["resource_set"], "run_intents") {
		t.Fatalf("metadata-only resource_set should not include message/event/run-intent resources: %+v", payload.SyncScope)
	}
	if payload.MessagesBySession == nil || payload.EventsBySession == nil || payload.RunIntentsBySession == nil || payload.HistoryManifestsBySession == nil {
		t.Fatalf("metadata-only bootstrap must keep top-level maps non-nil: messages=%v events=%v runIntents=%v manifests=%v", payload.MessagesBySession, payload.EventsBySession, payload.RunIntentsBySession, payload.HistoryManifestsBySession)
	}
	if _, ok := payload.MessagesBySession[created.ID]; ok {
		t.Fatalf("metadata-only bootstrap emitted per-session messages key: %+v", payload.MessagesBySession)
	}
	if _, ok := payload.EventsBySession[created.ID]; ok {
		t.Fatalf("metadata-only bootstrap emitted per-session events key: %+v", payload.EventsBySession)
	}
	if _, ok := payload.RunIntentsBySession[created.ID]; ok {
		t.Fatalf("metadata-only bootstrap emitted per-session run intents key: %+v", payload.RunIntentsBySession)
	}
	if _, ok := payload.HistoryManifestsBySession[created.ID]; ok {
		t.Fatalf("metadata-only bootstrap emitted per-session history manifest key: %+v", payload.HistoryManifestsBySession)
	}
}

func TestSessionsV3SyncSessionShellProjectsImmutableCurrentModePreference(t *testing.T) {
	plan := pebblestore.ModelProfileSelection{Provider: "codex", Model: "plan-model", Thinking: "xhigh", ServiceTier: "fast", ContextMode: "compact"}
	session := pebblestore.SessionSnapshot{
		Mode:       sessionruntime.ModePlan,
		Preference: pebblestore.ModelPreference{Provider: "stale", Model: "stale-model"},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			ActionFavoriteID:   "favorite-action",
			ActionFavoriteName: "Action Favorite",
			Action:             pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model", Thinking: "high"},
			PlanFavoriteID:     "favorite-plan",
			PlanFavoriteName:   "Plan Favorite",
			Plan:               &plan,
			AppliedAt:          42,
		},
	}

	shell, err := sessionsV3SyncSessionShell(session)
	if err != nil {
		t.Fatalf("project plan shell: %v", err)
	}
	if shell.Preference.Provider != "codex" || shell.Preference.Model != "plan-model" || shell.Preference.Thinking != "xhigh" || shell.Preference.UpdatedAt != 42 {
		t.Fatalf("plan shell preference = %+v", shell.Preference)
	}
	if shell.ModelProfile == session.ModelProfile || shell.ModelProfile.Plan == session.ModelProfile.Plan {
		t.Fatalf("sync shell must deep-clone immutable model profile")
	}

	session.Mode = sessionruntime.ModeAuto
	shell, err = sessionsV3SyncSessionShell(session)
	if err != nil {
		t.Fatalf("project action shell: %v", err)
	}
	if shell.Preference.Model != "action-model" {
		t.Fatalf("action shell preference = %+v", shell.Preference)
	}
}

func TestSessionsV3SyncSessionShellRejectsDisabledPlan(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		Mode: sessionruntime.ModePlan,
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source: pebblestore.SessionModelProfileSourceTemporary,
			Action: pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model"},
		},
	}
	if _, err := sessionsV3SyncSessionShell(session); err == nil || !strings.Contains(err.Error(), "Plan mode disabled") {
		t.Fatalf("disabled Plan shell error = %v", err)
	}
}

func TestSessionsV3SyncShellMetadataPreservesModelProfileIdentity(t *testing.T) {
	metadata := sessionsV3SyncShellMetadata(map[string]any{
		"agent_name": "swarm",
		"model_profile": pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			ActionFavoriteID:   "mp_exact",
			ActionFavoriteName: "Exact",
			Action:             pebblestore.ModelProfileSelection{Provider: "openai", Model: "same-model"},
		},
	})
	profile, ok := metadata["model_profile"].(pebblestore.SessionModelProfileSnapshot)
	if !ok || profile.ActionFavoriteID != "mp_exact" || profile.ActionFavoriteName != "Exact" || profile.Action.Model != "same-model" {
		t.Fatalf("sync shell model profile metadata = %#v", metadata["model_profile"])
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal sync shell model profile: %v", err)
	}
	for _, removed := range []string{`"model_mode"`, `"single"`, `"auto"`, `"saved_profile_id"`} {
		if strings.Contains(string(raw), removed) {
			t.Fatalf("sync shell model profile retained removed field %s: %s", removed, raw)
		}
	}
}

func TestSessionsV3AgentModelPolicyIgnoresLegacySplitMetadata(t *testing.T) {
	server := &Server{}
	fallback := pebblestore.ModelPreference{Provider: "codex", Model: "durable-model"}
	session := pebblestore.SessionSnapshot{
		Mode:       sessionruntime.ModePlan,
		Preference: fallback,
		Metadata: map[string]any{
			"agent_name": "legacy-agent",
			"agent_profile": pebblestore.AgentProfile{
				Name: "legacy-agent", ModelMode: "split",
				PlanProvider: "codex", PlanModel: "mutable-plan-model",
				AutoProvider: "codex", AutoModel: "mutable-action-model",
			},
		},
	}
	policy := server.sessionsV3AgentModelPolicyWithResolver(session, fallback, 0, 0, nil)
	if policy.Locked || policy.Source != "default" || policy.Preference.Model != "durable-model" {
		t.Fatalf("legacy split metadata changed durable projection policy: %+v", policy)
	}
}

func TestSessionsV3AgentModelPolicyAttributesCurrentSnapshotFavorite(t *testing.T) {
	server := &Server{}
	session := pebblestore.SessionSnapshot{
		Mode: sessionruntime.ModePlan,
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			ActionFavoriteID:   "favorite-action",
			ActionFavoriteName: "Action Favorite",
			Action:             pebblestore.ModelProfileSelection{Provider: "openai", Model: "action-model"},
			PlanFavoriteID:     "favorite-plan",
			PlanFavoriteName:   "Plan Favorite",
			Plan:               &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan-model"},
		},
	}
	policy := server.sessionsV3AgentModelPolicyWithResolver(session, pebblestore.ModelPreference{}, 0, 0, nil)
	if policy.ProfileID != "favorite-plan" || policy.ProfileName != "Plan Favorite" || policy.ProfileMode != sessionruntime.ModePlan {
		t.Fatalf("plan profile attribution = %+v", policy)
	}

	session.Mode = sessionruntime.ModeAuto
	policy = server.sessionsV3AgentModelPolicyWithResolver(session, pebblestore.ModelPreference{}, 0, 0, nil)
	if policy.ProfileID != "favorite-action" || policy.ProfileName != "Action Favorite" || policy.ProfileMode != sessionruntime.ModeAuto {
		t.Fatalf("action profile attribution = %+v", policy)
	}
}

func TestSessionsV3SyncBootstrapSessionShellOmitsSettingsOnlyMetadata(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-metadata-shell", "Sync Bootstrap Metadata Shell", "/workspace/metadata-shell")

	body := `{"metadata":{"parent_session_id":"parent-1","lineage_kind":"delegated_subagent","agent_profile":{"name":"forbidden"},"model_profile":{"saved_profile_id":"forbidden"},"tool_contract":{"preset":"forbidden"},"tool_scope":{"preset":"forbidden"},"prompt":"forbidden","provider":"forbidden","model":"forbidden","purpose":"client-only"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/metadata", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata update status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/metadata-shell","recent":{"limit":10}},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, withTestPrincipal(bootstrapReq))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	metadata := payload.SessionsByID[created.ID].Metadata
	for _, key := range []string{"agent_profile", "model_profile", "tool_contract", "tool_scope", "prompt", "provider", "model", "purpose"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("bootstrap shell leaked settings-only metadata %s: %+v", key, metadata)
		}
	}
	if metadata["agent_name"] != "swarm" || metadata["runtime_mode"] == "" || metadata["parent_session_id"] != "parent-1" || metadata["lineage_kind"] != "delegated_subagent" {
		t.Fatalf("bootstrap shell dropped required identity metadata: %+v", metadata)
	}
}

func TestSessionsV3SyncBootstrapRunIntentsRequestedNoHistoryEmitsAuthoritativeEmptyKey(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-zero-run-intents", "Sync Bootstrap Zero Run Intents", "/workspace/zero-run-intents")

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/zero-run-intents","recent":{"limit":10}},"history":{"mode":"none"},"resources":{"run_intents":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SyncScope           map[string]string                           `json:"sync_scope"`
		RunIntentsBySession map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if !strings.Contains(payload.SyncScope["resource_set"], "run_intents") {
		t.Fatalf("run_intents resource missing from scope: %+v", payload.SyncScope)
	}
	intents, ok := payload.RunIntentsBySession[created.ID]
	if !ok {
		t.Fatalf("requested run_intents omitted authoritative empty key: %+v", payload.RunIntentsBySession)
	}
	if len(intents) != 0 {
		t.Fatalf("requested zero run_intents = %+v, want empty array", intents)
	}
}

func TestSessionsV3SyncHydrateRunIntentsRequestedNoHistoryEmitsAuthoritativeEmptyKey(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-hydrate-zero-run-intents", "Sync Hydrate Zero Run Intents", "/workspace/hydrate-zero-run-intents")

	body := `{"surface":"desktop","session_ids":["` + created.ID + `"],"history":{"mode":"none"},"resources":{"run_intents":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SyncScope           map[string]string                           `json:"sync_scope"`
		RunIntentsBySession map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !strings.Contains(payload.SyncScope["resource_set"], "run_intents") {
		t.Fatalf("run_intents resource missing from scope: %+v", payload.SyncScope)
	}
	intents, ok := payload.RunIntentsBySession[created.ID]
	if !ok {
		t.Fatalf("requested hydrate run_intents omitted authoritative empty key: %+v", payload.RunIntentsBySession)
	}
	if len(intents) != 0 {
		t.Fatalf("requested hydrate zero run_intents = %+v, want empty array", intents)
	}
}

func TestSessionsV3SyncIncludeActiveUsesCurrentRunStateWithoutRunIntentHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body func(string) string
	}{
		{
			name: "bootstrap",
			path: V3SyncBootstrapPath,
			body: func(sessionID string) string {
				return `{"surface":"desktop","selector":{"kind":"session_ids","session_ids":["` + sessionID + `"]},"history":{"mode":"none"},"include_active":true}`
			},
		},
		{
			name: "hydrate",
			path: V3SyncHydratePath,
			body: func(sessionID string) string {
				return `{"surface":"desktop","session_ids":["` + sessionID + `"],"history":{"mode":"none"},"include_active":true}`
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-"+tc.name+"-active-current-state", "Sync "+tc.name+" Active Current State", "/workspace/active-current-state")
			runID := "run-sync-" + tc.name + "-active-current-state"
			now := time.Now().UnixMilli()
			if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
				SessionID:       created.ID,
				UserID:          testPrincipal().UserID,
				AccountScopeID:  testPrincipal().AccountScopeID,
				ClientRequestID: "sync-" + tc.name + "-active-current-state-running",
				IdempotencyKey:  "sync-" + tc.name + "-active-current-state-running",
				PayloadHash:     "hash-sync-" + tc.name + "-active-current-state-running",
				RequestHash:     "hash-sync-" + tc.name + "-active-current-state-running",
				Kind:            sessionruntime.SessionMutationRecordRunIntent,
				EventType:       "session.run.queued",
				RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now},
				NowUnixMs:       now,
			}); err != nil {
				t.Fatalf("mark session active: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body(created.ID)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want %d, body=%s", tc.name, rec.Code, http.StatusOK, rec.Body.String())
			}
			var payload struct {
				SyncScope                map[string]string                           `json:"sync_scope"`
				RunIntentsBySession      map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
				CurrentRunStateBySession map[string]pebblestore.V3SessionRunState    `json:"current_run_state_by_session"`
				ActiveSessionIDs         []string                                    `json:"active_session_ids"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode %s response: %v", tc.name, err)
			}
			resourceSet := payload.SyncScope["resource_set"]
			if !strings.Contains(resourceSet, "current_run_state") {
				t.Fatalf("include_active did not request current_run_state resource: %+v", payload.SyncScope)
			}
			if strings.Contains(resourceSet, "run_intents") {
				t.Fatalf("include_active implicitly requested historical run_intents resource: %+v", payload.SyncScope)
			}
			state, ok := payload.CurrentRunStateBySession[created.ID]
			if !ok || state.RunID != runID || !state.Active {
				t.Fatalf("current_run_state_by_session[%s] = %+v, ok=%v", created.ID, state, ok)
			}
			if state.StartedAt != now || state.CompletedAt != 0 || state.DurationMs != 0 || state.CumulativeDurationMs != 0 {
				t.Fatalf("current_run_state timing = %+v, want started_at=%d and no terminal durations", state, now)
			}
			foundActiveSessionID := false
			for _, sessionID := range payload.ActiveSessionIDs {
				if sessionID == created.ID {
					foundActiveSessionID = true
					break
				}
			}
			if !foundActiveSessionID {
				t.Fatalf("active_session_ids missing %s: %+v", created.ID, payload.ActiveSessionIDs)
			}
			if _, ok := payload.RunIntentsBySession[created.ID]; ok {
				t.Fatalf("include_active emitted historical run intents without explicit resource: %+v", payload.RunIntentsBySession)
			}
		})
	}
}

func TestSessionsV3SyncBootstrapGlobalSelectorWithoutRecentUsesNativeAccountSnapshot(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-a", "Sync Global A", "/workspace/global-a")
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-b", "Sync Global B", "/workspace/global-b")

	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("global bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
		Selector               sessionsV3SyncSelector                 `json:"selector"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode global bootstrap response: %v", err)
	}
	if payload.Selector.Kind != "global" || !payload.Selector.Global {
		t.Fatalf("global bootstrap selector = %+v", payload.Selector)
	}
	if !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
		t.Fatalf("global bootstrap missing signed cursor: %+v", payload)
	}
	if payload.SessionsByID[createdA.ID].ID != createdA.ID || payload.SessionsByID[createdB.ID].ID != createdB.ID {
		t.Fatalf("global bootstrap missing account sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
	}
}

func TestSessionsV3SyncBootstrapGlobalRecentDoesNotSelectAllAccountSessions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-a", "Sync Global Recent A", "/workspace/global-recent-a")
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-b", "Sync Global Recent B", "/workspace/global-recent-b")
	createdC := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-global-recent-c", "Sync Global Recent C", "/workspace/global-recent-c")

	body := `{"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":1}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("global recent bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder []string                               `json:"session_order"`
		Selector     sessionsV3SyncSelector                 `json:"selector"`
		Pagination   pebblestore.V3SyncSnapshotPagination   `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode global recent bootstrap response: %v", err)
	}
	if payload.Selector.Kind != "recent" || !payload.Selector.Global || payload.Selector.Recent.Limit != 1 {
		t.Fatalf("global recent bootstrap selector = %+v", payload.Selector)
	}
	if len(payload.SessionOrder) != 1 || len(payload.SessionsByID) != 1 {
		t.Fatalf("global recent bootstrap selected all sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
	}
	selectedID := payload.SessionOrder[0]
	if selectedID != createdA.ID && selectedID != createdB.ID && selectedID != createdC.ID {
		t.Fatalf("global recent bootstrap selected unexpected session %q from %+v", selectedID, payload.SessionOrder)
	}
	if !payload.Pagination.HasMore {
		t.Fatalf("global recent bootstrap did not report remaining recent page: %+v", payload.Pagination)
	}
}

func TestSessionsV3SyncBootstrapUsesCanonicalSelectorForSnapshotOptions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	selectorSession := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-selector", "Sync Bootstrap Selector", "/workspace/bootstrap-selector")
	rawWorkspaceSession := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-raw", "Sync Bootstrap Raw", "/workspace/bootstrap-raw")
	expectedSelector := sessionsV3SyncSelector{Kind: "workspace", WorkspacePath: "/workspace/bootstrap-selector", Recent: sessionsV3WorksetRecent{Limit: 10}}
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(expectedSelector))

	body, err := json.Marshal(map[string]any{
		"surface": "desktop",
		"selector": map[string]any{
			"kind":           "workspace",
			"workspace_path": "/workspace/bootstrap-selector",
			"recent":         map[string]any{"limit": 10},
		},
		"workspace": map[string]any{"workspace_path": "/workspace/bootstrap-selector"},
		"recent":    map[string]any{"limit": 10},
		"history":   map[string]any{"mode": "none"},
	})
	if err != nil {
		t.Fatalf("marshal bootstrap body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		Selector               sessionsV3SyncSelector                 `json:"selector"`
		SyncScope              map[string]string                      `json:"sync_scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if payload.Selector.Kind != expectedSelector.Kind || payload.Selector.WorkspacePath != expectedSelector.WorkspacePath || payload.Selector.Recent.Limit != expectedSelector.Recent.Limit {
		t.Fatalf("bootstrap selector = %+v, want %+v", payload.Selector, expectedSelector)
	}
	if payload.SessionsByID[selectorSession.ID].ID != selectorSession.ID {
		t.Fatalf("bootstrap did not read from canonical selector workspace: %+v", payload.SessionsByID)
	}
	if payload.SessionsByID[rawWorkspaceSession.ID].ID != "" {
		t.Fatalf("bootstrap read from conflicting raw workspace field: %+v", payload.SessionsByID[rawWorkspaceSession.ID])
	}
	if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
		t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
	}
	cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
	if err != nil {
		t.Fatalf("verify bootstrap cursor: %v", err)
	}
	if cursorPayload.SelectorFilterHash != expectedSelectorHash {
		t.Fatalf("cursor selector hash = %q, want %q", cursorPayload.SelectorFilterHash, expectedSelectorHash)
	}
}

func TestSessionsV3SyncBootstrapRejectsGlobalSelectorWithConflictingRawWorkspace(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"workspace":{"workspace_path":"/workspace/bootstrap-raw"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "global selector cannot be combined") {
		t.Fatalf("bootstrap error did not report selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsConflictingRawWorkspaceSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/selector","recent":{"limit":10}},"workspace":{"workspace_path":"/workspace/raw"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync selector conflicts with top-level workspace") {
		t.Fatalf("bootstrap error did not report workspace selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsUnboundedWorkspaceSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/unbounded"},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace selector requires recent.limit") {
		t.Fatalf("bootstrap error did not report bounded workspace requirement: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsSessionViewResource(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-bootstrap-session-view", "Sync Bootstrap Session View")
	body := `{"surface":"desktop","selector":{"kind":"session_ids","session_ids":["` + created.ID + `"]},"resources":{"session_view":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not support resources.session_view") {
		t.Fatalf("bootstrap error did not report hydrate-only session_view: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapDesktopIncludesUnresolvedPlanSessionsOutsideRecentLimit(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	unresolved := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-unresolved-old", "Unresolved old", "/workspace/bootstrap-unresolved")
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-unresolved-recent-new", "Recent new", "/workspace/bootstrap-unresolved")
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-unresolved-recent-mid", "Recent mid", "/workspace/bootstrap-unresolved")
	otherWorkspaceUnresolved := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-other-workspace-unresolved", "Other workspace unresolved", "/workspace/other-bootstrap-unresolved")

	for _, session := range []pebblestore.SessionSnapshot{unresolved, otherWorkspaceUnresolved} {
		plan := pebblestore.SessionPlanSnapshot{
			ID:             "plan-" + session.ID,
			SessionID:      session.ID,
			UserID:         session.UserID,
			AccountScopeID: session.AccountScopeID,
			Title:          "Unresolved plan",
			Status:         "approved",
			ApprovalState:  "approved",
			Active:         true,
			CreatedAt:      10,
			UpdatedAt:      10,
			Version:        1,
			Document: &pebblestore.SessionPlanDocument{
				ID:     "plan-" + session.ID,
				Title:  "Unresolved plan",
				Status: "approved",
				ExecutionState: &pebblestore.SessionPlanExecutionState{
					Status:           "waiting_review",
					LastCheckpointID: "cp-1",
					LastOutcome:      "needs_review",
				},
				ActiveCheckpointID: "cp-1",
				Checkpoints: []pebblestore.SessionPlanCheckpoint{{
					ID:     "cp-1",
					Title:  "Review",
					Status: "needs_review",
					Review: &pebblestore.SessionPlanCheckpointReview{Status: "pending"},
					Order:  1,
				}},
			},
		}
		if err := sessionSvc.Store().PutPlan(plan); err != nil {
			t.Fatalf("put unresolved plan for %s: %v", session.ID, err)
		}
		if err := sessionSvc.Store().SetActivePlan(session.ID, plan.ID, plan.UpdatedAt); err != nil {
			t.Fatalf("set active unresolved plan for %s: %v", session.ID, err)
		}
	}

	older, ok, err := sessionSvc.Store().GetSession(unresolved.ID)
	if err != nil || !ok {
		t.Fatalf("load unresolved session: ok=%t err=%v", ok, err)
	}
	older.UpdatedAt = 1000
	if err := sessionSvc.Store().UpdateSession(older); err != nil {
		t.Fatalf("age unresolved session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/bootstrap-unresolved","recent":{"limit":2}},"history":{"mode":"none"},"resources":{"active_plan":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionsByID     map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionViewsByID map[string]sessionsV3SessionView       `json:"session_views_by_id"`
		SessionOrder     []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if len(payload.SessionOrder) != 3 || payload.SessionOrder[2] != unresolved.ID {
		t.Fatalf("bootstrap session_order = %+v, want unresolved session outside recent limit included last", payload.SessionOrder)
	}
	if payload.SessionViewsByID[unresolved.ID].ActivePlan == nil {
		t.Fatalf("bootstrap active plan missing for unresolved session: %+v", payload.SessionViewsByID[unresolved.ID])
	}
	if _, ok := payload.SessionsByID[otherWorkspaceUnresolved.ID]; ok {
		t.Fatalf("bootstrap leaked unresolved session from other workspace: %+v", payload.SessionsByID)
	}
}

func TestSessionsV3SyncBootstrapDesktopIncludesPinnedSessionsOutsideRecentLimit(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	pinned := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-pinned-old", "Pinned old", "/workspace/bootstrap-pinned")
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-recent-new", "Recent new", "/workspace/bootstrap-pinned")
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-recent-mid", "Recent mid", "/workspace/bootstrap-pinned")
	otherWorkspacePinned := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-bootstrap-other-workspace-pinned", "Other workspace pinned", "/workspace/other-bootstrap-pinned")

	for _, session := range []pebblestore.SessionSnapshot{pinned, otherWorkspacePinned} {
		body := `{"metadata":{"swarm_v3_desktop_sidebar_pinned":true}}`
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/metadata", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("pin %s status = %d, want %d, body=%s", session.ID, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	older, ok, err := sessionSvc.Store().GetSession(pinned.ID)
	if err != nil || !ok {
		t.Fatalf("load pinned session: ok=%t err=%v", ok, err)
	}
	older.UpdatedAt = 1000
	if err := sessionSvc.Store().UpdateSession(older); err != nil {
		t.Fatalf("age pinned session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/bootstrap-pinned","recent":{"limit":2}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if len(payload.SessionOrder) != 3 || payload.SessionOrder[2] != pinned.ID {
		t.Fatalf("bootstrap session_order = %+v, want pinned session outside recent limit included last", payload.SessionOrder)
	}
	if payload.SessionsByID[pinned.ID].Metadata["swarm_v3_desktop_sidebar_pinned"] != true {
		t.Fatalf("bootstrap pinned metadata missing: %+v", payload.SessionsByID[pinned.ID].Metadata)
	}
	if _, ok := payload.SessionsByID[otherWorkspacePinned.ID]; ok {
		t.Fatalf("bootstrap leaked pinned session from other workspace: %+v", payload.SessionsByID)
	}
}

func TestSessionsV3SyncBootstrapUsesNativeSnapshotBuilderNotLegacyWorkset(t *testing.T) {
	source, err := os.ReadFile("sessions_v3_sync_bootstrap.go")
	if err != nil {
		t.Fatalf("read sync bootstrap source: %v", err)
	}
	body := string(source)
	for _, forbidden := range []string{"BuildSessionWorkset", "V3SessionWorksetOptions", "V3SessionWorksetResult", "sessionsV3SyncPlans", "sessionsV3SyncTombstonesBySession", "ListSessionTombstonesForAccount("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sync bootstrap must not use legacy/out-of-snapshot path %q", forbidden)
		}
	}
}

func TestSessionsV3SyncHydrateTargetsSessionIDs(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate", "Sync Hydrate")
	body := `{"surface":"desktop","session_ids":["` + created.ID + `"]}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                     bool                                   `json:"ok"`
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
		ReplayInstructions     map[string]any                         `json:"replay_instructions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !payload.OK || payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("hydrate missing target session: %+v", payload.SessionsByID)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != created.ID {
		t.Fatalf("hydrate session_order = %+v", payload.SessionOrder)
	}
	if payload.SnapshotEndpointCursor == "" {
		t.Fatalf("hydrate did not return scoped cursor")
	}
	if payload.ReplayInstructions["after_endpoint_cursor"] != payload.SnapshotEndpointCursor {
		t.Fatalf("hydrate replay instructions invalid: %+v", payload.ReplayInstructions)
	}
}

func TestSessionsV3SyncHydrateReturnsSessionView(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-view", "Sync Hydrate View")
	if err := server.sessions.Store().PutUsageSummary(pebblestore.SessionUsageSummary{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Provider: "test-provider", Model: "test-model", InputTokens: 7, OutputTokens: 11}); err != nil {
		t.Fatalf("put usage summary: %v", err)
	}
	if _, err := server.perm.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: "run-sync-hydrate-view", CallID: "call-sync-hydrate-view", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"}); err != nil {
		t.Fatalf("create pending permission: %v", err)
	}
	if _, _, err := server.sessions.SavePlan(created.ID, "sync-view-plan", "Sync View Plan", "# Sync View Plan", "approved", "approved", true); err != nil {
		t.Fatalf("save active plan: %v", err)
	}
	now := time.Now().UnixMilli()
	for _, intentStatus := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
		if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
			SessionID:       created.ID,
			UserID:          testPrincipal().UserID,
			AccountScopeID:  testPrincipal().AccountScopeID,
			ClientRequestID: "sync-hydrate-view-" + intentStatus,
			IdempotencyKey:  "sync-hydrate-view-" + intentStatus,
			PayloadHash:     "sync-hydrate-view-" + intentStatus,
			RequestHash:     "sync-hydrate-view-" + intentStatus,
			Kind:            sessionruntime.SessionMutationRecordRunIntent,
			RunIntent: &pebblestore.V3SessionRunIntent{
				SessionID:      created.ID,
				RunID:          "run-sync-hydrate-view",
				UserID:         testPrincipal().UserID,
				AccountScopeID: testPrincipal().AccountScopeID,
				Status:         intentStatus,
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			NowUnixMs: now,
		}); err != nil {
			t.Fatalf("record run intent %s: %v", intentStatus, err)
		}
	}

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"history":     map[string]any{"mode": "tail", "max_messages_per_session": 200, "manifest_policy": "manifest"},
		"resources": map[string]any{
			"messages":          true,
			"run_intents":       true,
			"current_run_state": true,
			"session_view":      true,
			"active_plan":       true,
		},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SessionViewsByID map[string]sessionsV3SessionView `json:"session_views_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	view, ok := payload.SessionViewsByID[created.ID]
	if !ok {
		t.Fatalf("hydrate missing session view: %+v", payload.SessionViewsByID)
	}
	if view.CurrentExecutionEpoch == nil || view.CurrentExecutionEpoch.SessionID != created.ID || view.CurrentExecutionEpoch.Status != pebblestore.ExecutionEpochStatusActive || view.CurrentExecutionEpoch.Ordinal != 1 {
		t.Fatalf("hydrate current execution epoch = %+v", view.CurrentExecutionEpoch)
	}
	if view.AgenticSettings.Mode == "" || view.AgenticSettings.ProjectionSeq == 0 {
		t.Fatalf("session view missing agentic settings: %+v", view.AgenticSettings)
	}
	if view.AgenticSettings.AgentModelPolicy.AgentName == "" || view.AgenticSettings.AgentModelPolicy.ResolvedAgent == "" {
		t.Fatalf("session view missing agent model policy: %+v", view.AgenticSettings.AgentModelPolicy)
	}
	if len(view.PendingPermissions) != 1 || view.PendingPermissions[0].SessionID != created.ID {
		t.Fatalf("session view pending permissions = %+v", view.PendingPermissions)
	}
	if view.UsageSummary == nil || view.UsageSummary.InputTokens != 7 || view.UsageSummary.OutputTokens != 11 {
		t.Fatalf("session view usage summary = %+v", view.UsageSummary)
	}
	if view.CurrentRunState == nil || !view.CurrentRunState.Active || view.CurrentRunState.RunID != "run-sync-hydrate-view" {
		t.Fatalf("session view current run state = %+v", view.CurrentRunState)
	}
	if view.CurrentRunState.StartedAt != now || view.CurrentRunState.CompletedAt != 0 || view.CurrentRunState.DurationMs != 0 || view.CurrentRunState.CumulativeDurationMs != 0 {
		t.Fatalf("session view current run state timing = %+v", view.CurrentRunState)
	}
	if view.HasActivePlan == nil || !*view.HasActivePlan || view.ActivePlan == nil || view.ActivePlan.ID != "sync-view-plan" {
		t.Fatalf("session view active plan = has:%v plan:%+v", view.HasActivePlan, view.ActivePlan)
	}
}

func TestSessionsV3SyncHydrateReturnsLatestSealedExecutionEpoch(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-sealed-epoch", "Sync Hydrate Sealed Epoch")
	active, ok, err := sessionSvc.GetActiveExecutionEpoch(created.ID)
	if err != nil || !ok {
		t.Fatalf("get active epoch: ok=%v err=%v", ok, err)
	}
	if _, err := sessionSvc.Store().SealExecutionEpoch(pebblestore.SealExecutionEpochInput{SessionID: created.ID, EpochID: active.EpochID}); err != nil {
		t.Fatalf("seal epoch: %v", err)
	}
	body := bytes.NewBufferString(`{"surface":"desktop","session_ids":["` + created.ID + `"],"resources":{"session_view":true}}`)
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionViewsByID map[string]sessionsV3SessionView `json:"session_views_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	epoch := payload.SessionViewsByID[created.ID].CurrentExecutionEpoch
	if epoch == nil || epoch.EpochID != active.EpochID || epoch.Status != pebblestore.ExecutionEpochStatusSealed || epoch.LastRootSeq == 0 {
		t.Fatalf("hydrate sealed epoch = %+v", epoch)
	}
}

func TestSessionsV3SyncHydrateUsesSessionPreferenceInsteadOfAgentModelFields(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "sync-session-policy-create", "sync session policy", pebblestore.ModelPreference{Provider: "test-provider", Model: "stored-model", Thinking: "medium"})

	hydrate := func(wantMode, wantSource, wantProvider, wantModel, wantThinking string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"surface":     "desktop",
			"session_ids": []string{created.ID},
			"resources": map[string]any{
				"session_view": true,
			},
		})
		if err != nil {
			t.Fatalf("marshal hydrate body: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("hydrate status = %d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			SessionViewsByID map[string]sessionsV3SessionView `json:"session_views_by_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode hydrate response: %v", err)
		}
		view := payload.SessionViewsByID[created.ID]
		policy := view.AgenticSettings.AgentModelPolicy
		if view.AgenticSettings.Mode != wantMode || policy.Locked || policy.Source != wantSource || policy.Preference.Provider != wantProvider || policy.Preference.Model != wantModel || policy.Preference.Thinking != wantThinking {
			t.Fatalf("agentic settings = %+v policy=%+v, want mode=%s source=%s preference=%s/%s/%s", view.AgenticSettings, policy, wantMode, wantSource, wantProvider, wantModel, wantThinking)
		}
		if view.AgenticSettings.EffectivePreference.Provider != wantProvider || view.AgenticSettings.EffectivePreference.Model != wantModel || view.AgenticSettings.EffectivePreference.Thinking != wantThinking {
			t.Fatalf("effective preference = %+v, want %s/%s/%s", view.AgenticSettings.EffectivePreference, wantProvider, wantModel, wantThinking)
		}
	}

	hydrate(sessionruntime.ModeAuto, "default", "test-provider", "stored-model", "medium")
}

func TestSessionsV3SyncHydrateCanonicalizesSessionIDSelectorForCursorScope(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-canonical-a", "Sync Hydrate Canonical A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-canonical-b", "Sync Hydrate Canonical B")
	expectedIDs := canonicalV3SyncSessionIDs([]string{createdA.ID, createdB.ID})
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: expectedIDs}))

	var firstSelectorHash string
	for _, tc := range []struct {
		name       string
		sessionIDs []string
	}{
		{name: "ordered", sessionIDs: []string{createdA.ID, createdB.ID}},
		{name: "reversed", sessionIDs: []string{createdB.ID, createdA.ID}},
		{name: "duplicate and empty", sessionIDs: []string{createdA.ID, "", createdB.ID, createdA.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"surface": "desktop", "session_ids": tc.sessionIDs})
			if err != nil {
				t.Fatalf("marshal hydrate body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var payload struct {
				SnapshotEndpointCursor string                 `json:"snapshot_endpoint_cursor"`
				Selector               sessionsV3SyncSelector `json:"selector"`
				SyncScope              map[string]string      `json:"sync_scope"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode hydrate response: %v", err)
			}
			if payload.Selector.Kind != "session_ids" || strings.Join(payload.Selector.SessionIDs, ",") != strings.Join(expectedIDs, ",") {
				t.Fatalf("hydrate selector = %+v, want canonical ids %+v", payload.Selector, expectedIDs)
			}
			if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
				t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
			}
			cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
			if err != nil {
				t.Fatalf("verify hydrate cursor: %v", err)
			}
			if cursorPayload.SelectorFilterHash != payload.SyncScope["selector_filter_hash"] {
				t.Fatalf("cursor selector hash = %q, response hash = %q", cursorPayload.SelectorFilterHash, payload.SyncScope["selector_filter_hash"])
			}
			if firstSelectorHash == "" {
				firstSelectorHash = payload.SyncScope["selector_filter_hash"]
			} else if payload.SyncScope["selector_filter_hash"] != firstSelectorHash {
				t.Fatalf("selector hash = %q, want stable hash %q", payload.SyncScope["selector_filter_hash"], firstSelectorHash)
			}
		})
	}
}

func TestSessionsV3SyncHydrateIncludeActiveDoesNotWidenSessionIDs(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-active-a", "Sync Hydrate Active A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-active-b", "Sync Hydrate Active B")
	now := time.Now().UnixMilli()
	runID := "run-sync-hydrate-active-b"
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       createdB.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "sync-hydrate-active-b-running",
		IdempotencyKey:  "sync-hydrate-active-b-running",
		PayloadHash:     "hash-sync-hydrate-active-b-running",
		RequestHash:     "hash-sync-hydrate-active-b-running",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run.queued",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now},
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("mark session B active: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"surface":        "desktop",
		"session_ids":    []string{createdA.ID},
		"include_active": true,
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		SnapshotEndpointCursor string                                      `json:"snapshot_endpoint_cursor"`
		Selector               sessionsV3SyncSelector                      `json:"selector"`
		SyncScope              map[string]string                           `json:"sync_scope"`
		SessionsByID           map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
		SessionOrder           []string                                    `json:"session_order"`
		RunIntentsBySession    map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if payload.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("hydrate missing requested session A: %+v", payload.SessionsByID)
	}
	if _, leaked := payload.SessionsByID[createdB.ID]; leaked {
		t.Fatalf("hydrate include_active widened to active session B: %+v", payload.SessionsByID)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != createdA.ID {
		t.Fatalf("hydrate session_order = %+v, want only %s", payload.SessionOrder, createdA.ID)
	}
	if _, leaked := payload.RunIntentsBySession[createdB.ID]; leaked {
		t.Fatalf("hydrate include_active leaked active run intents for session B: %+v", payload.RunIntentsBySession)
	}
	expectedSelectorHash := v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: []string{createdA.ID}}))
	if payload.Selector.Kind != "session_ids" || len(payload.Selector.SessionIDs) != 1 || payload.Selector.SessionIDs[0] != createdA.ID {
		t.Fatalf("hydrate selector = %+v, want only %s", payload.Selector, createdA.ID)
	}
	if payload.SyncScope["selector_filter_hash"] != expectedSelectorHash {
		t.Fatalf("selector hash = %q, want %q", payload.SyncScope["selector_filter_hash"], expectedSelectorHash)
	}
	cursorPayload, err := server.verifyV3SyncCursor(payload.SnapshotEndpointCursor)
	if err != nil {
		t.Fatalf("verify hydrate cursor: %v", err)
	}
	if cursorPayload.SelectorFilterHash != expectedSelectorHash {
		t.Fatalf("cursor selector hash = %q, want %q", cursorPayload.SelectorFilterHash, expectedSelectorHash)
	}

	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdA.ID, "hydrate stream handoff A")
	mutatedAt := time.Now().UnixMilli()
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       createdB.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "sync-hydrate-active-b-running-after-cursor",
		IdempotencyKey:  "sync-hydrate-active-b-running-after-cursor",
		PayloadHash:     "hash-sync-hydrate-active-b-running-after-cursor",
		RequestHash:     "hash-sync-hydrate-active-b-running-after-cursor",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, CreatedAt: now, UpdatedAt: mutatedAt},
		NowUnixMs:       mutatedAt,
	}); err != nil {
		t.Fatalf("mutate session B after hydrate: %v", err)
	}

	streamBody, err := json.Marshal(map[string]any{
		"surface":         "desktop",
		"session_ids":     []string{createdA.ID},
		"include_active":  true,
		"endpoint_cursor": payload.SnapshotEndpointCursor,
	})
	if err != nil {
		t.Fatalf("marshal stream body: %v", err)
	}
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewReader(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamRec.Code, http.StatusOK, streamRec.Body.String())
	}
	var streamPayload struct {
		EndpointCursor string `json:"endpoint_cursor"`
		Events         []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &streamPayload); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if !strings.HasPrefix(streamPayload.EndpointCursor, v3SyncCursorPrefix) || strings.HasPrefix(streamPayload.EndpointCursor, "cursor-") {
		t.Fatalf("stream endpoint_cursor is not signed/opaque: %q", streamPayload.EndpointCursor)
	}
	foundA := false
	for _, event := range streamPayload.Events {
		switch event.SessionID {
		case createdA.ID:
			foundA = true
		case createdB.ID:
			t.Fatalf("stream using hydrate cursor leaked active session B mutation: %+v", streamPayload.Events)
		}
	}
	if !foundA {
		t.Fatalf("stream using hydrate cursor missed requested session A mutation: %+v", streamPayload.Events)
	}
}

func TestSessionsV3SyncHydrateRejectsConflictingSelectorFields(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-conflict", "Sync Hydrate Conflict")

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"selector": map[string]any{
			"kind":           "workspace",
			"workspace_path": "/x",
			"recent":         map[string]any{"limit": 50},
		},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync hydrate selector conflicts") {
		t.Fatalf("hydrate error did not report selector conflict: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapSnapshotCursorCoversConcurrentMutationReplay(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-race-a", "Sync Race A", "/workspace/cp5-race")
	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-race","recent":{"limit":10}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if bootstrap.SessionsByID[created.ID].ID != created.ID || bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap invalid: %+v", bootstrap)
	}
	createdAfter := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-race-b", "Sync Race B", "/workspace/cp5-race")
	if bootstrap.SessionsByID[createdAfter.ID].ID != "" {
		t.Fatalf("test setup expected mutation after snapshot cursor, but bootstrap included %s", createdAfter.ID)
	}
	scope := v3SyncCursorScopeForSnapshot(testPrincipal(), "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "workspace", WorkspacePath: "/workspace/cp5-race", Recent: sessionsV3WorksetRecent{Limit: 10}}, sessionsV3SyncResourceSet(sessionsV3WorksetResources{}, sessionsV3WorksetHistory{Mode: "none"}, false))
	afterSeq, _, err := server.parseV3SyncEndpointCursor(bootstrap.SnapshotEndpointCursor, scope)
	if err != nil {
		t.Fatalf("parse bootstrap cursor: %v", err)
	}
	if createdAfter.ID == "" || createdAfter.UpdatedAt == 0 {
		t.Fatalf("post-bootstrap mutation was not committed: %+v", createdAfter)
	}
	currentHead, err := server.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatalf("current outbox head: %v", err)
	}
	if currentHead <= afterSeq {
		t.Fatalf("post-bootstrap mutation not replayable after cursor: head=%d after=%d", currentHead, afterSeq)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-race","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamRec.Code, http.StatusOK, streamRec.Body.String())
	}
	var stream struct {
		EndpointCursor string `json:"endpoint_cursor"`
		Events         []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	advancedSeq, _, err := server.parseV3SyncEndpointCursor(stream.EndpointCursor, scope)
	if err != nil {
		t.Fatalf("parse stream cursor: %v", err)
	}
	if stream.EndpointCursor == "" || advancedSeq <= afterSeq {
		t.Fatalf("stream did not advance past bootstrap cursor: %+v after=%d advanced=%d", stream, afterSeq, advancedSeq)
	}
	found := false
	for _, event := range stream.Events {
		if event.SessionID == createdAfter.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("stream replay after bootstrap cursor missed post-bootstrap session %s: %+v", createdAfter.ID, stream.Events)
	}
}

func TestSessionsV3SyncHydrateRejectsKnownSessionWrongScopeCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-hydrate-reject", "Sync Hydrate Reject")
	otherPrincipal := testPrincipal()
	otherPrincipal.AccountScopeID = "acct-other"
	wrongScopeCursor, err := server.signV3SyncEndpointCursor(v3SyncCursorScopeForSnapshot(otherPrincipal, "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: []string{created.ID}}, []string{"sessions", "projections", "membership", "tombstones"}), 1)
	if err != nil {
		t.Fatalf("sign wrong scope cursor: %v", err)
	}

	body := `{"surface":"desktop","session_ids":["` + created.ID + `"],"known_sessions":{"` + created.ID + `":{"endpoint_cursor":"` + wrongScopeCursor + `"}}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("hydrate status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "endpoint_cursor_scope_mismatch") {
		t.Fatalf("hydrate wrong-scope cursor error missing code: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncBootstrapRejectsKnownSessionLegacyEndpointCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-bootstrap-legacy-known", "Sync Bootstrap Legacy Known")
	body := `{"surface":"desktop","selector":{"kind":"session_ids","session_ids":["` + created.ID + `"]},"known_sessions":{"` + created.ID + `":{"endpoint_cursor":"cursor-1"}}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "endpoint_cursor_legacy_unsupported") {
		t.Fatalf("bootstrap legacy known cursor error missing code: %s", rec.Body.String())
	}
}

func TestSessionsV3SyncRejectsKnownSessionSequenceState(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-known-seq", "Sync Known Seq")

	for _, tc := range []struct {
		name       string
		path       string
		body       map[string]any
		statusCode int
	}{
		{
			name: "bootstrap rejects applied_seq",
			path: V3SyncBootstrapPath,
			body: map[string]any{
				"surface":        "desktop",
				"selector":       map[string]any{"kind": "session_ids", "session_ids": []string{created.ID}},
				"known_sessions": map[string]any{created.ID: map[string]any{"applied_seq": 1}},
			},
			statusCode: http.StatusBadRequest,
		},
		{
			name: "bootstrap rejects high_watermark",
			path: V3SyncBootstrapPath,
			body: map[string]any{
				"surface":        "desktop",
				"selector":       map[string]any{"kind": "session_ids", "session_ids": []string{created.ID}},
				"known_sessions": map[string]any{created.ID: map[string]any{"high_watermark": 1}},
			},
			statusCode: http.StatusBadRequest,
		},
		{
			name: "hydrate rejects applied_seq",
			path: V3SyncHydratePath,
			body: map[string]any{
				"surface":        "desktop",
				"session_ids":    []string{created.ID},
				"known_sessions": map[string]any{created.ID: map[string]any{"applied_seq": 1}},
			},
			statusCode: http.StatusBadRequest,
		},
		{
			name: "hydrate rejects high_watermark",
			path: V3SyncHydratePath,
			body: map[string]any{
				"surface":        "desktop",
				"session_ids":    []string{created.ID},
				"known_sessions": map[string]any{created.ID: map[string]any{"high_watermark": 1}},
			},
			statusCode: http.StatusBadRequest,
		},
		{
			name: "bootstrap accepts zero sequence state",
			path: V3SyncBootstrapPath,
			body: map[string]any{
				"surface":        "desktop",
				"selector":       map[string]any{"kind": "session_ids", "session_ids": []string{created.ID}},
				"known_sessions": map[string]any{created.ID: map[string]any{"applied_seq": 0, "high_watermark": 0}},
			},
			statusCode: http.StatusOK,
		},
		{
			name: "hydrate accepts zero sequence state",
			path: V3SyncHydratePath,
			body: map[string]any{
				"surface":        "desktop",
				"session_ids":    []string{created.ID},
				"known_sessions": map[string]any{created.ID: map[string]any{"applied_seq": 0, "high_watermark": 0}},
			},
			statusCode: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != tc.statusCode {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.statusCode, rec.Body.String())
			}
			if tc.statusCode == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "known_sessions_sequence_state_unsupported") {
				t.Fatalf("sequence state error missing code: %s", rec.Body.String())
			}
		})
	}
}

func TestSessionsV3SyncBootstrapOmitsRemovedAllSessionResourceMaps(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-extra-resources", "Sync Extra Resources", "/workspace/cp5-extra")
	if err := server.sessions.Store().PutUsageSummary(pebblestore.SessionUsageSummary{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Provider: "test-provider", Model: "test-model", InputTokens: 3, OutputTokens: 4}); err != nil {
		t.Fatalf("put usage summary: %v", err)
	}
	if _, _, err := server.sessions.SavePlan(created.ID, "sync-plan", "Sync Plan", "# Sync Plan", "draft", "draft", true); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if _, err := server.perm.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: "run-sync-extra", CallID: "call-sync-extra", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"}); err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-extra","recent":{"limit":10}},"history":{"mode":"none"},"resources":{"active_plan":true,"plan_revisions":true}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	for _, forbidden := range []string{"permissions_by_session", "usage_by_session", "plans_by_session", "plan_revisions_by_session", "preferences_by_session", "agent_model_policy_by_session"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("bootstrap emitted removed all-session resource %s: %+v", forbidden, payload[forbidden])
		}
	}
	if payload["tombstones_by_session"] == nil || payload["snapshot_endpoint_cursor"] == "" {
		t.Fatalf("bootstrap response missing durable sync fields: %+v", payload)
	}
	replay, _ := payload["replay_instructions"].(map[string]any)
	if replay["after_endpoint_cursor"] != payload["snapshot_endpoint_cursor"] {
		t.Fatalf("bootstrap replay instructions invalid: %+v", replay)
	}
}

func TestSessionsV3SyncHydrateReturnsArchivedRequestedSessionBody(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-hydrate-archived-open", "Sync Hydrate Archived Open", "/workspace/cp4-hydrate-archived")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "archived hydrate message")
	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+created.ID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"history":     map[string]any{"mode": "tail", "max_messages_per_session": 200, "max_events_per_session": 200},
		"resources":   map[string]any{"messages": true, "events": true, "session_view": true, "run_intents": true, "current_run_state": true, "active_plan": true},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate archived status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		MessagesBySession   map[string][]pebblestore.MessageSnapshot  `json:"messages_by_session"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
		SessionViewsByID    map[string]sessionsV3SessionView          `json:"session_views_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate archived: %v", err)
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("hydrate archived missing session body: %+v", payload.SessionsByID)
	}
	if len(payload.MessagesBySession[created.ID]) != 1 || payload.MessagesBySession[created.ID][0].Content != "archived hydrate message" {
		t.Fatalf("hydrate archived messages invalid: %+v", payload.MessagesBySession[created.ID])
	}
	view, ok := payload.SessionViewsByID[created.ID]
	if !ok {
		t.Fatalf("hydrate archived missing session view: %+v", payload.SessionViewsByID)
	}
	if view.HasActivePlan == nil || *view.HasActivePlan || view.ActivePlan != nil {
		t.Fatalf("hydrate archived active plan marker invalid: %+v", view)
	}
	tombstone := payload.TombstonesBySession[created.ID]
	if !tombstone.Archived || tombstone.Deleted || tombstone.Kind != "archived" || tombstone.Session.ID != created.ID {
		t.Fatalf("hydrate archived tombstone invalid: %+v", tombstone)
	}
}

func TestSessionsV3SyncHydrateReturnsDeletedRequestedSessionTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-hydrate-tombstone", "Sync Hydrate Tombstone", "/workspace/cp4-hydrate-tombstone")
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"surface":     "desktop",
		"session_ids": []string{created.ID},
		"history":     map[string]any{"mode": "none"},
	})
	if err != nil {
		t.Fatalf("marshal hydrate body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("deleted session still present in sessions_by_id: %+v", payload.SessionsByID[created.ID])
	}
	tombstone := payload.TombstonesBySession[created.ID]
	if !tombstone.Deleted || tombstone.Kind != "deleted" || tombstone.Session.ID != created.ID || tombstone.WorkspacePath != "/workspace/cp4-hydrate-tombstone" {
		t.Fatalf("hydrate deleted tombstone invalid: %+v", tombstone)
	}
}

func TestSessionsV3SyncBootstrapReturnsDeletedSessionTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-tombstone", "Sync Tombstone", "/workspace/cp5-tombstone")
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-tombstone","recent":{"limit":10}},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("deleted session still present in sessions_by_id: %+v", payload.SessionsByID[created.ID])
	}
	tombstone := payload.TombstonesBySession[created.ID]
	if !tombstone.Deleted || tombstone.Kind != "deleted" || tombstone.Session.ID != created.ID || tombstone.WorkspacePath != "/workspace/cp5-tombstone" {
		t.Fatalf("deleted tombstone invalid: %+v", tombstone)
	}
}

func TestSessionsV3SyncWorkspaceStreamDoesNotDropDeletedMembershipEvent(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "sync-delete-stream", "Sync Delete Stream", "/workspace/cp5-delete-stream")

	bootstrapBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-delete-stream","recent":{"limit":10}},"history":{"mode":"none"}}`
	bootstrapReq := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(bootstrapBody))
	bootstrapReq.Header.Set("Content-Type", "application/json")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, withTestPrincipal(bootstrapReq))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string `json:"snapshot_endpoint_cursor"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap cursor missing")
	}
	if err := server.sessions.DeleteSession(created.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	streamBody := `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-delete-stream","recent":{"limit":10}},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var stream struct {
		Events []struct {
			SessionID string `json:"session_id"`
			Event     struct {
				EventType string `json:"event_type"`
			} `json:"event"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == created.ID && event.Event.EventType == "session.deleted" {
			return
		}
	}
	t.Fatalf("workspace stream missed durable delete membership event for %s: %+v", created.ID, stream.Events)
}

func TestSessionsV3SyncStreamWebsocketRejectsUnauthenticatedBeforeUpgrade(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + V3SyncStreamPath
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatalf("unauthenticated sync stream websocket unexpectedly upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("unauthenticated sync stream websocket status=%d err=%v, want 401", status, err)
	}
}

func TestSessionsV3SyncStreamWebsocketIsExplicitlyUnsupportedForAuthenticatedSnapshotCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	t.Cleanup(httpServer.Close)

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + V3SyncStreamPath
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial authenticated sync stream websocket: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial authenticated sync stream websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set sync stream read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read sync stream unsupported frame: %v", err)
	}
	var frame struct {
		Kind string `json:"kind"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode sync stream unsupported frame %s: %v", string(raw), err)
	}
	if frame.Kind != V3RealtimeKindCursorError || frame.Code != "sync_websocket_unsupported" {
		t.Fatalf("sync websocket frame = %+v raw=%s", frame, string(raw))
	}
}

func TestSessionsV3SyncAuthSeparationForHydrateAndStream(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "sync-auth-separation", "Sync Auth Separation")

	hydrateReq := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(`{"surface":"desktop","session_ids":["`+created.ID+`"]}`))
	hydrateReq.Header.Set("Content-Type", "application/json")
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, requestWithTestPrincipalForAccount(hydrateReq, "other-user", "other-account"))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("cross-account hydrate status=%d body=%s", hydrateRec.Code, hydrateRec.Body.String())
	}
	var hydrate struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrate); err != nil {
		t.Fatalf("decode cross-account hydrate: %v", err)
	}
	if len(hydrate.SessionsByID) != 0 || len(hydrate.SessionOrder) != 0 {
		t.Fatalf("cross-account hydrate leaked session: %+v", hydrate)
	}

	scope := v3SyncCursorScopeForSnapshot(testPrincipal(), "desktop", "v3.sync.snapshot", sessionsV3SyncSelector{Kind: "global", Global: true}, []string{"sessions", "projections", "membership", "tombstones"})
	cursor, err := server.signV3SyncEndpointCursor(scope, 1)
	if err != nil {
		t.Fatalf("sign cursor: %v", err)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + cursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, requestWithTestPrincipalForAccount(streamReq, "other-user", "other-account"))
	if streamRec.Code != http.StatusBadRequest {
		t.Fatalf("cross-account stream status=%d want=%d body=%s", streamRec.Code, http.StatusBadRequest, streamRec.Body.String())
	}
	if !strings.Contains(streamRec.Body.String(), "endpoint_cursor_scope_mismatch") {
		t.Fatalf("cross-account stream did not fail closed on cursor scope: %s", streamRec.Body.String())
	}
}

func TestSessionsV3SyncCanonicalScopeIsAccountAndUser(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	userA := testPrincipal()
	userB := testPrincipal()
	userB.UserID = "test-user-b"

	createForPrincipal := func(principal identity.Principal, sessionID, workspace string) pebblestore.SessionSnapshot {
		t.Helper()
		result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
			SessionID:       sessionID,
			UserID:          principal.UserID,
			AccountScopeID:  principal.AccountScopeID,
			ClientRequestID: "create-" + sessionID,
			IdempotencyKey:  "create-" + sessionID,
			PayloadHash:     "hash-create-" + sessionID,
			Kind:            sessionruntime.SessionMutationCreateSession,
			Session: &pebblestore.SessionSnapshot{
				ID:             sessionID,
				UserID:         principal.UserID,
				AccountScopeID: principal.AccountScopeID,
				WorkspacePath:  workspace,
				WorkspaceName:  strings.Trim(workspace, "/"),
				Title:          sessionID,
			},
			NowUnixMs: time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatalf("create session %s: %v", sessionID, err)
		}
		if result.Session == nil {
			t.Fatalf("create session %s returned no session", sessionID)
		}
		return *result.Session
	}

	createdA := createForPrincipal(userA, "sync-scope-user-a", "/workspace/sync-scope")
	createdB := createForPrincipal(userB, "sync-scope-user-b", "/workspace/sync-scope")
	legacy := createdA
	legacy.ID = "sync-scope-legacy-empty-user"
	legacy.UserID = ""
	legacy.Title = "legacy empty user"
	legacy.UpdatedAt = time.Now().UnixMilli()
	if err := sessionSvc.Store().CreateSession(legacy); err != nil {
		t.Fatalf("create legacy empty-user session: %v", err)
	}

	body := `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, requestWithTestPrincipalForAccount(req, userA.UserID, userA.AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var bootstrap struct {
		SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		SessionOrder           []string                               `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("bootstrap missing user A session: %+v", bootstrap.SessionsByID)
	}
	if bootstrap.SessionsByID[createdB.ID].ID != "" || bootstrap.SessionsByID[legacy.ID].ID != "" {
		t.Fatalf("bootstrap leaked other user or empty user session: %+v", bootstrap.SessionsByID)
	}

	hydrateBody := `{"surface":"desktop","session_ids":["` + createdA.ID + `","` + createdB.ID + `","` + legacy.ID + `"]}`
	hydrateReq := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(hydrateBody))
	hydrateReq.Header.Set("Content-Type", "application/json")
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, requestWithTestPrincipalForAccount(hydrateReq, userA.UserID, userA.AccountScopeID))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status=%d body=%s", hydrateRec.Code, hydrateRec.Body.String())
	}
	var hydrate struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrate); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	if hydrate.SessionsByID[createdA.ID].ID != createdA.ID {
		t.Fatalf("hydrate missing user A session: %+v", hydrate.SessionsByID)
	}
	if hydrate.SessionsByID[createdB.ID].ID != "" || hydrate.SessionsByID[legacy.ID].ID != "" {
		t.Fatalf("hydrate leaked other user or empty user session: %+v", hydrate.SessionsByID)
	}

	if bootstrap.SnapshotEndpointCursor == "" {
		t.Fatalf("bootstrap missing cursor")
	}
	message := pebblestore.MessageSnapshot{ID: "sync-scope-user-b-message", SessionID: createdB.ID, UserID: userB.UserID, AccountScopeID: userB.AccountScopeID, Role: "user", Content: "should not leak", CreatedAt: time.Now().UnixMilli()}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: createdB.ID, UserID: userB.UserID, AccountScopeID: userB.AccountScopeID, ClientRequestID: "sync-scope-user-b-message", IdempotencyKey: "sync-scope-user-b-message", PayloadHash: "hash-sync-scope-user-b-message", Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("append user B message: %v", err)
	}
	streamBody := `{"surface":"desktop","selector":{"kind":"global","global":true},"endpoint_cursor":"` + bootstrap.SnapshotEndpointCursor + `"}`
	streamReq := httptest.NewRequest(http.MethodPost, V3SyncStreamPath, bytes.NewBufferString(streamBody))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(streamRec, requestWithTestPrincipalForAccount(streamReq, userA.UserID, userA.AccountScopeID))
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamRec.Code, streamRec.Body.String())
	}
	var stream struct {
		Events []struct {
			SessionID string `json:"session_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(streamRec.Body.Bytes(), &stream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	for _, event := range stream.Events {
		if event.SessionID == createdB.ID || event.SessionID == legacy.ID {
			t.Fatalf("stream leaked other user or empty user event: %+v", stream.Events)
		}
	}
}

func TestSessionsV3SyncTUIAndPhoneEquivalentBootstrapScopes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	phoneCreated := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "phone-equivalent-create", "Phone Equivalent", "/workspace/cp5-phone")
	inactiveCreated := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "inactive-create", "Inactive Session", "/workspace/cp5-phone")

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "desktop workspace", body: `{"surface":"desktop","selector":{"kind":"workspace","workspace_path":"/workspace/cp5-phone","recent":{"limit":10}},"history":{"mode":"none"}}`},
		{name: "tui cwd", body: `{"surface":"tui","selector":{"kind":"tui","cwd_path":"/workspace/cp5-phone","recent":{"limit":10}},"history":{"mode":"none"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
			}
			var payload struct {
				SnapshotEndpointCursor string                                 `json:"snapshot_endpoint_cursor"`
				SessionsByID           map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
				SessionOrder           []string                               `json:"session_order"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode bootstrap: %v", err)
			}
			if !strings.HasPrefix(payload.SnapshotEndpointCursor, "v3c1.") {
				t.Fatalf("bootstrap missing signed cursor: %+v", payload)
			}
			if payload.SessionsByID[phoneCreated.ID].ID != phoneCreated.ID || payload.SessionsByID[inactiveCreated.ID].ID != inactiveCreated.ID {
				t.Fatalf("bootstrap missing phone/inactive sessions: order=%+v sessions=%+v", payload.SessionOrder, payload.SessionsByID)
			}
		})
	}
}

func TestSessionsV3SyncHydrateEmptyUserLegacyTombstoneReturnsOmission(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	legacy := pebblestore.SessionSnapshot{
		ID:             "sync-hydrate-empty-user-tombstone",
		UserID:         "",
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/workspace/legacy-tombstone",
		WorkspaceName:  "legacy-tombstone",
		Title:          "Legacy Tombstone",
		CreatedAt:      time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
	}
	if err := sessionSvc.Store().CreateSession(legacy); err != nil {
		t.Fatalf("create legacy empty-user session: %v", err)
	}
	if err := server.sessions.DeleteSession(legacy.ID); err != nil {
		t.Fatalf("delete legacy empty-user session: %v", err)
	}

	body := `{"surface":"desktop","session_ids":["` + legacy.ID + `"],"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK                  bool                                      `json:"ok"`
		SessionsByID        map[string]pebblestore.SessionSnapshot    `json:"sessions_by_id"`
		TombstonesBySession map[string]pebblestore.V3SessionTombstone `json:"tombstones_by_session"`
		Omissions           []pebblestore.V3SyncSnapshotOmission      `json:"omissions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hydrate: %v", err)
	}
	if !payload.OK {
		t.Fatalf("hydrate ok=false, want omission response: %s", rec.Body.String())
	}
	if len(payload.SessionsByID) != 0 || len(payload.TombstonesBySession) != 0 {
		t.Fatalf("legacy empty-user tombstone leaked session/tombstone: sessions=%+v tombstones=%+v", payload.SessionsByID, payload.TombstonesBySession)
	}
	if len(payload.Omissions) != 1 || payload.Omissions[0].SessionID != legacy.ID || payload.Omissions[0].Resource != "tombstones" || payload.Omissions[0].Reason != "bootstrap_required" {
		t.Fatalf("legacy empty-user tombstone returned silent absence; omissions=%+v", payload.Omissions)
	}
}
