package todo

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCreateAITaskPersistsDigestBoundModelProfileSnapshot(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "ai-task-profile.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewWorkspaceTodoStore(db)
	svc := NewService(store, nil, nil, nil)
	workspace := t.TempDir()
	profile := &pebblestore.SessionModelProfileSnapshot{
		Source: pebblestore.SessionModelProfileSourceSaved, SavedProfileID: "saved-profile", Name: "standard", ModelMode: pebblestore.ModelProfileModeSplit, AppliedAt: 42,
		Plan: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "plan-model", Thinking: "high"},
		Auto: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "auto-model", Thinking: "medium"},
	}
	item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account-profile", UserID: "user-profile", WorkspaceID: "workspace-profile", WorkspacePath: workspace, OriginSessionID: "origin-profile", ModelProfile: profile, Request: "preserve profile", Mode: "plan", IdempotencyKey: "profile-key"})
	if err != nil {
		t.Fatalf("create AI task: %v", err)
	}
	profile.Plan.Model = "mutated-after-create"
	stored, ok, err := store.GetForAccount(item.AccountScopeID, workspace, item.ID)
	if err != nil || !ok || stored.AIModelProfile == nil || stored.AIModelProfile.Plan == nil || stored.AIModelProfile.Plan.Model != "plan-model" || stored.AIModelProfile.Auto == nil || stored.AIModelProfile.Auto.Model != "auto-model" {
		t.Fatalf("stored AI task profile = %#v ok=%t err=%v", stored.AIModelProfile, ok, err)
	}
	_, _, _, _, err = svc.CreateAITaskWithReplay(CreateAITaskInput{AccountScopeID: "account-profile", UserID: "user-profile", WorkspaceID: "workspace-profile", WorkspacePath: workspace, OriginSessionID: "origin-profile", ModelProfile: &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, SavedProfileID: "different", ModelMode: pebblestore.ModelProfileModeSingle, Single: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "different"}}, Request: "preserve profile", Mode: "plan", IdempotencyKey: "profile-key"})
	if err == nil || !strings.Contains(err.Error(), "different AI task model profile") {
		t.Fatalf("model-profile digest conflict error = %v", err)
	}
}
