package run

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type workerPinProvider struct{ provideriface.Runner }

func (workerPinProvider) ID() string { return "fixture-provider" }
func (workerPinProvider) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{ID: "fixture-provider", Ready: true}, nil
}

// Purpose: native model proposals must not change approved execution policy until
// acceptance, and invalid catalog choices must leave durable state unchanged.
// executeManageWorkersTool, ValidateWorkerModelPin and WorkerStore.AcceptWorker
// own these boundaries; real temporary stores with a readiness-only adapter are
// the narrowest deterministic layer (no provider execution or benchmark).
func TestManageWorkersModelPinProposal(t *testing.T) {
	s, settings, db := taskFallbackFixture(t)
	taskFallbackSettings(t, settings, "account-a", "specialist", "account-a-action")
	adapter := workerPinProvider{}
	s.providers = registry.New(adapter)
	s.providers.RegisterRunner(adapter)
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner", WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "folder", Kind: store.WorkspaceGrantPrimary, Available: store.BoolPtr(true)}}, Metadata: map[string]any{"agent_name": "orchestrator"}}); err != nil {
		t.Fatal(err)
	}
	invoke := func(args map[string]any) (store.WorkerRecord, error) {
		raw, _ := json.Marshal(args)
		call := tool.Call{Name: "manage_workers", Arguments: string(raw)}
		if !workerProposalCall(call) {
			t.Fatal("model change not classified as a proposal")
		}
		output, err := s.executeWorkerProposalTool("author", call)
		if err != nil {
			return store.WorkerRecord{}, err
		}
		var result struct {
			Worker store.WorkerRecord `json:"worker"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		return result.Worker, nil
	}
	choice := map[string]any{"provider": "fixture-provider", "model": "specialist", "thinking": "low"}
	w, err := invoke(map[string]any{"action": "propose", "name": "Pinned work", "instructions": "Review work", "model": choice})
	if err != nil {
		t.Fatal(err)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending || w.ModelProfile.UseAccountDefault || w.ModelProfile.Action.Model != "specialist" {
		t.Fatalf("not a pinned proposal: %+v", w)
	}
	w, err = s.sessions.AcceptWorker("account-a", "owner", w.ID, w.Revision, map[string]string{"primary": "folder"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ModelProfile.UseAccountDefault || w.ModelProfile.Action.Model != "specialist" {
		t.Fatal("accept lost pin")
	}
	for _, invalid := range []map[string]any{
		{"provider": "fixture-provider", "model": "missing", "thinking": "low"},
		{"provider": "disconnected", "model": "specialist", "thinking": "low"},
		{"provider": "fixture-provider", "model": "specialist", "thinking": "unsupported"},
		{"provider": "fixture-provider", "model": "specialist", "thinking": "low", "fast": true},
	} {
		_, err := invoke(map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": w.Revision, "model": invalid})
		principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-a", UserID: "owner"}
		_, sharedErr := ValidateWorkerModelPin(identity.ContextWithPrincipal(context.Background(), principal), s.providers, s.model, invalid)
		if err == nil || sharedErr == nil || err.Error() != sharedErr.Error() {
			t.Fatalf("validation mismatch: %v / %v", err, sharedErr)
		}
		saved, _, getErr := s.sessions.GetWorker("account-a", w.ID)
		if getErr != nil || saved.Revision != w.Revision || saved.PendingReview != nil || saved.ModelProfile.Action.Model != "specialist" {
			t.Fatal("invalid proposal changed worker")
		}
	}
	pending, err := invoke(map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": w.Revision, "model": nil})
	if err != nil {
		t.Fatal(err)
	}
	if pending.ModelProfile.UseAccountDefault || pending.PendingReview == nil || !pending.PendingReview.ModelProfile.UseAccountDefault {
		t.Fatal("clear applied before acceptance or was not staged")
	}
	cleared, err := s.sessions.AcceptWorker("account-a", "owner", w.ID, pending.Revision, map[string]string{"primary": "folder"})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.ModelProfile.UseAccountDefault || cleared.PendingReview != nil {
		t.Fatal("accept did not clear pin")
	}
	// A direct MCP/API edit must fence an older proposal even if a caller uses
	// the latest revision when accepting it.
	staged, err := invoke(map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": cleared.Revision, "model": choice})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := sessions.WorkerStore().SetWorkerModelProfile("account-a", "owner", w.ID, staged.Revision, cleared.ModelProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessions.AcceptWorker("account-a", "owner", w.ID, direct.Revision, map[string]string{"primary": "folder"}); err == nil {
		t.Fatal("stale proposal accepted after direct model edit")
	}
	saved, _, err := s.sessions.GetWorker("account-a", w.ID)
	if err != nil || saved.Revision != direct.Revision || !saved.ModelProfile.UseAccountDefault {
		t.Fatal("stale acceptance changed policy")
	}
	// Purpose: workerProposalToolOutput must not advertise stale reviews as
	// acceptable after UpdateWorker directly saves a model-free native edit.
	// Exercise the tool and durable store together to prove both the response
	// and the applied instructions, rather than only testing output formatting.
	t.Run("StaleReviewReportsUpdated", func(t *testing.T) {
		staged, err := invoke(map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": saved.Revision, "model": choice})
		if err != nil {
			t.Fatal(err)
		}
		if staged.PendingReview == nil || staged.PendingReviewStale {
			t.Fatal("model proposal is not fresh")
		}
		raw, _ := json.Marshal(map[string]any{"action": "propose", "worker_id": w.ID, "expected_revision": staged.Revision, "instructions": "Updated review instructions"})
		output, err := s.executeWorkerProposalTool("author", tool.Call{Name: "manage_workers", Arguments: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Status     string `json:"status"`
			NextAction string `json:"next_action"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "updated" || result.NextAction != "" {
			t.Fatalf("direct edit incorrectly awaits acceptance: %s", output)
		}
		updated, _, err := s.sessions.GetWorker("account-a", w.ID)
		if err != nil || updated.PendingReview == nil || !updated.PendingReviewStale || updated.Instructions != "Updated review instructions" || !updated.ModelProfile.UseAccountDefault {
			t.Fatalf("direct edit did not preserve approved policy and stale review: %+v, %v", updated, err)
		}
	})
	for _, action := range []string{"get", "list"} {
		raw, _ := json.Marshal(map[string]any{"action": action, "worker_id": w.ID})
		output, err := s.executeManageWorkersTool("author", string(raw))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		view, _ := result["worker"].(map[string]any)
		if action == "list" {
			view = result["workers"].([]any)[0].(map[string]any)
		}
		summary := view["model"].(map[string]any)
		if summary["source"] != "role_default" || summary["pinned"] != false || summary["model"] != "account-a-action" {
			t.Fatalf("missing model summary: %s", output)
		}
	}
}
