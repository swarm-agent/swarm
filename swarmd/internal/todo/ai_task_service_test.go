package todo

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAITaskAuthorityIsAccountScopedIdempotentAndMergeSafe(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "todo.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	store := pebblestore.NewWorkspaceTodoStore(db)
	svc := NewService(store, nil, nil, nil)
	const workspace = "/shared/workspace"
	create := func(account, request, key string) (TodoItem, error) {
		item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: account, UserID: "user-" + account, WorkspaceID: "workspace-1", WorkspacePath: workspace, OriginSessionID: "origin-1", Request: request, IdempotencyKey: key})
		return item, err
	}

	first, err := create("account-a", "repair queue", "stable-key")
	if err != nil {
		t.Fatalf("create account-a task: %v", err)
	}
	other, err := create("account-b", "repair queue", "stable-key")
	if err != nil {
		t.Fatalf("create account-b task: %v", err)
	}
	if first.ID == other.ID || first.AccountScopeID == other.AccountScopeID {
		t.Fatalf("account isolation failed: first=%#v other=%#v", first, other)
	}
	if _, ok, err := store.GetForAccount("account-b", workspace, first.ID); err != nil || ok {
		t.Fatalf("cross-account lookup: ok=%t err=%v", ok, err)
	}
	if first.AIState != pebblestore.WorkspaceTodoAIStateQueued || first.AIStateVersion != 1 || first.AIIdempotencyKeyHash == "" || first.AIRequestHash == "" || first.Group != "" {
		t.Fatalf("initial authority = %#v", first)
	}

	const goroutines = 12
	results := make(chan TodoItem, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := create("account-a", "repair queue", "stable-key")
			results <- item
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
	}
	for item := range results {
		if item.ID != first.ID {
			t.Fatalf("duplicate task created: %q != %q", item.ID, first.ID)
		}
	}
	if _, err := create("account-a", "different request", "stable-key"); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting key error = %v", err)
	}

	priority := "urgent"
	updated, _, _, err := svc.Update(UpdateInput{AccountScopeID: "account-a", WorkspacePath: workspace, ID: first.ID, Priority: &priority})
	if err != nil {
		t.Fatalf("cosmetic update: %v", err)
	}
	if updated.Priority != priority || updated.AIState != first.AIState || updated.AIIdempotencyKeyHash != first.AIIdempotencyKeyHash {
		t.Fatalf("cosmetic update erased authority: %#v", updated)
	}
	text := "counterfeit"
	if _, _, _, err := svc.Update(UpdateInput{AccountScopeID: "account-a", WorkspacePath: workspace, ID: first.ID, Text: &text}); err == nil {
		t.Fatal("ordinary update changed active AI authority")
	}
	if _, _, _, _, err := svc.ApplyBatch(workspace, []BatchOperation{{Action: "update", ID: first.ID, Text: &text}}, ListOptions{AccountScopeID: "account-a"}); err == nil {
		t.Fatal("batch update changed active AI authority")
	}
	if _, _, _, _, err := svc.ApplyBatch(workspace, []BatchOperation{{Action: "delete", ID: first.ID}}, ListOptions{AccountScopeID: "account-a"}); err == nil {
		t.Fatal("batch delete removed active AI authority")
	}

	claimed, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: "account-a", WorkspacePath: workspace, ID: first.ID, ExpectedState: "queued", ExpectedVersion: 1, State: "preparing", PreparationSessionID: "prep-1", PreparationRunID: "prep-run-1", PreparationAttemptID: "attempt-1", Disposition: "claimed"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.AIStateVersion != 2 || claimed.AIClaimedAt == 0 || claimed.PreparationSessionID != "prep-1" {
		t.Fatalf("claim authority = %#v", claimed)
	}
	if _, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: "account-a", WorkspacePath: workspace, ID: first.ID, ExpectedState: "preparing", ExpectedVersion: 1, State: "failed"}); err == nil {
		t.Fatal("stale state version was accepted")
	}
	recovered, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: "account-a", WorkspacePath: workspace, ID: first.ID, ExpectedState: "preparing", ExpectedVersion: 2, State: "queued", Disposition: "stale_recovery"})
	if err != nil || recovered.AIStateVersion != 3 {
		t.Fatalf("stale recovery: item=%#v err=%v", recovered, err)
	}

	audit, err := store.ListAITaskAudit("account-a", first.ID, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	seen := map[string]bool{}
	for _, record := range audit {
		if record.AccountScopeID != "account-a" || record.TaskID != first.ID {
			t.Fatalf("mis-scoped audit record: %#v", record)
		}
		seen[record.Stage] = true
	}
	for _, stage := range []string{"queued", "replayed", "preparing"} {
		if !seen[stage] {
			t.Fatalf("missing audit stage %q in %#v", stage, audit)
		}
	}
}
