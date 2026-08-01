package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestV3SessionModelProfileSnapshotPersistsReplaysAndClears(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "session-profile.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	profile := &SessionModelProfileSnapshot{
		Source:             SessionModelProfileSourceSaved,
		UseAccountDefault:  true,
		ActionFavoriteID:   "favorite-action",
		ActionFavoriteName: "Action Favorite",
		Action:             ModelProfileSelection{Provider: "openai", Model: "action-model", Thinking: "high", ServiceTier: "fast", ContextMode: "compact"},
		PlanFavoriteID:     "favorite-plan",
		PlanFavoriteName:   "Plan Favorite",
		Plan:               &ModelProfileSelection{Provider: "codex", Model: "plan-model", Thinking: "xhigh", ContextMode: "full"},
		AppliedAt:          100,
	}
	created, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "create", PayloadHash: "create-hash", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace", ModelProfile: profile}, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RealtimeOutbox == nil {
		t.Fatal("create missing realtime outbox")
	}

	// Mutating caller-owned optional selections after the write must not change
	// the projection or replay payload captured by the mutation.
	profile.Plan.Model = "mutated-plan"

	stored, ok, err := sessions.GetSession("session")
	assertSessionModelProfileSnapshot(t, stored.ModelProfile, ok, err)
	replay, err := sessions.ReplayV3SessionEvents("session", 0, 10)
	if err != nil || replay.Session == nil {
		t.Fatalf("replay session = %+v err=%v", replay.Session, err)
	}
	assertSessionModelProfileSnapshot(t, replay.Session.ModelProfile, true, nil)

	// Returned optional selections are detached from durable state as well.
	stored.ModelProfile.Plan.Model = "returned-copy-mutated"
	again, ok, err := sessions.GetSession("session")
	assertSessionModelProfileSnapshot(t, again.ModelProfile, ok, err)

	stored.ModelProfile = nil
	cleared, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "clear", PayloadHash: "clear-hash", Kind: V3SessionMutationUpdateModelProfile, Session: &stored, NowUnixMs: 200})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Event.EventType != "session.model_profile.updated" || cleared.RealtimeOutbox == nil {
		t.Fatalf("clear event/outbox = %+v/%+v", cleared.Event, cleared.RealtimeOutbox)
	}
	after, ok, err := sessions.GetSession("session")
	if err != nil || !ok || after.ModelProfile != nil {
		t.Fatalf("cleared stored profile = %+v ok=%t err=%v", after.ModelProfile, ok, err)
	}
}

func TestSessionModelProfileSnapshotJSONUsesActionAndOptionalPlanContract(t *testing.T) {
	actionOnly := SessionModelProfileSnapshot{
		Source:             SessionModelProfileSourceTemporary,
		ActionFavoriteName: "Temporary",
		Action:             ModelProfileSelection{Provider: "openai", Model: "action-model"},
		AppliedAt:          42,
	}
	raw, err := json.Marshal(actionOnly)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	encoded := string(raw)
	for _, removed := range []string{"model_mode", "single", "auto", "saved_profile_id"} {
		if strings.Contains(encoded, `"`+removed+`"`) {
			t.Fatalf("snapshot JSON retained removed field %q: %s", removed, encoded)
		}
	}
	if !strings.Contains(encoded, `"action":{"provider":"openai","model":"action-model"`) || strings.Contains(encoded, `"plan":`) {
		t.Fatalf("action-only snapshot JSON = %s", encoded)
	}

	var decoded SessionModelProfileSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if decoded.Action.Model != "action-model" || decoded.Plan != nil || decoded.AppliedAt != 42 {
		t.Fatalf("decoded snapshot = %+v", decoded)
	}
}

func TestCloneSessionModelProfileSnapshotDeepCopiesSelections(t *testing.T) {
	original := &SessionModelProfileSnapshot{
		Source:           SessionModelProfileSourceSaved,
		ActionFavoriteID: "action-id",
		Action:           ModelProfileSelection{Provider: "openai", Model: "action"},
		PlanFavoriteID:   "plan-id",
		Plan:             &ModelProfileSelection{Provider: "codex", Model: "plan"},
	}
	cloned := CloneSessionModelProfileSnapshot(original)
	if cloned == original || cloned.Plan == original.Plan {
		t.Fatalf("clone shares mutable pointers: original=%p/%p clone=%p/%p", original, original.Plan, cloned, cloned.Plan)
	}
	cloned.Action.Model = "changed"
	cloned.Plan.Model = "changed"
	if original.Action.Model != "action" || original.Plan.Model != "plan" {
		t.Fatalf("clone mutation changed original: %+v", original)
	}
	if CloneSessionModelProfileSnapshot(nil) != nil || CloneModelProfileSelection(nil) != nil {
		t.Fatal("nil clone must stay nil")
	}
}

func assertSessionModelProfileSnapshot(t *testing.T, profile *SessionModelProfileSnapshot, ok bool, err error) {
	t.Helper()
	if err != nil || !ok || profile == nil {
		t.Fatalf("stored profile = %+v ok=%t err=%v", profile, ok, err)
	}
	if profile.Source != SessionModelProfileSourceSaved || !profile.UseAccountDefault || profile.ActionFavoriteID != "favorite-action" || profile.ActionFavoriteName != "Action Favorite" || profile.Action.Model != "action-model" || profile.PlanFavoriteID != "favorite-plan" || profile.PlanFavoriteName != "Plan Favorite" || profile.Plan == nil || profile.Plan.Model != "plan-model" || profile.AppliedAt != 100 {
		t.Fatalf("stored profile = %+v", profile)
	}
}
