package todo

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAITaskV2AcceptanceAtomicallyCreatesDurableRecoveryRecord(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "v2-queue.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewWorkspaceTodoStore(db)
	svc := NewService(store, nil, nil, nil)
	workspace := t.TempDir()

	item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account-v2", UserID: "user-v2", WorkspaceID: "workspace-v2", WorkspacePath: workspace, Request: "durably queue this", Mode: "plan", IdempotencyKey: "key-v2"})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := store.LoadAITaskV2RecoveryQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery) != 1 || recovery[0].Task.ID != item.ID || recovery[0].Task.AIState != pebblestore.WorkspaceTodoAIStateQueued || recovery[0].Task.AIMode != "plan" {
		t.Fatalf("recovery records=%#v", recovery)
	}
	preparing, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: workspace, ID: item.ID, ExpectedState: item.AIState, ExpectedVersion: item.AIStateVersion, State: pebblestore.WorkspaceTodoAIStatePreparing})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err = store.LoadAITaskV2RecoveryQueue(10)
	if err != nil || len(recovery) != 1 || recovery[0].Task.AIState != pebblestore.WorkspaceTodoAIStatePreparing || recovery[0].Task.AIStateVersion != preparing.AIStateVersion || recovery[0].Task.AIMode != "plan" {
		t.Fatalf("preparing recovery=%#v err=%v", recovery, err)
	}
	if _, err := svc.BindAITaskLifecycle(item.AccountScopeID, workspace, item.ID, preparing.AIState, pebblestore.WorkspaceTodoAIStateInProgress, "auto", true, "session-v2", "Task", "run-v2", "", ""); err != nil {
		t.Fatal(err)
	}
	recovery, err = store.LoadAITaskV2RecoveryQueue(10)
	if err != nil || len(recovery) != 0 {
		t.Fatalf("terminal dispatcher handoff retained recovery=%#v err=%v", recovery, err)
	}
}

func TestAITaskPreparedMetadataAndOriginalRequestAreDurable(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "prepared-metadata.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewWorkspaceTodoStore(db)
	svc := NewService(store, nil, nil, nil)
	workspace := t.TempDir()
	const request = "  preserve this exact request\nwith spacing  "
	item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace", WorkspacePath: workspace, Request: request, IdempotencyKey: "metadata-key"})
	if err != nil {
		t.Fatal(err)
	}
	preparing, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: workspace, ID: item.ID, ExpectedState: item.AIState, ExpectedVersion: item.AIStateVersion, State: pebblestore.WorkspaceTodoAIStatePreparing})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: workspace, ID: item.ID, ExpectedState: preparing.AIState, ExpectedVersion: preparing.AIStateVersion, State: pebblestore.WorkspaceTodoAIStatePreparing, Mode: "auto", Worktree: true, WorktreeName: "stable-seed", DisplayTitle: "Stable title"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.AIRequest != request || prepared.AIDisplayTitle != "Stable title" || prepared.AIWorktreeName != "stable-seed" {
		t.Fatalf("prepared authority=%#v", prepared)
	}
	recovery, err := store.LoadAITaskV2RecoveryQueue(10)
	if err != nil || len(recovery) != 1 || recovery[0].Task.AIRequest != request || recovery[0].Task.AIWorktreeName != "stable-seed" {
		t.Fatalf("prepared recovery=%#v err=%v", recovery, err)
	}
}

func TestAITaskTerminalStatesAndPreparedTitleAreDurable(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "terminal.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewWorkspaceTodoStore(db)
	svc := NewService(store, nil, nil, nil)
	workspace := t.TempDir()
	item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account-a", UserID: "user-a", WorkspaceID: "workspace-a", WorkspacePath: workspace, Request: "do work", IdempotencyKey: "key-a"})
	if err != nil {
		t.Fatal(err)
	}
	preparing, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: workspace, ID: item.ID, ExpectedState: item.AIState, ExpectedVersion: item.AIStateVersion, State: pebblestore.WorkspaceTodoAIStatePreparing})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.BindAITaskLifecycle(item.AccountScopeID, workspace, item.ID, preparing.AIState, pebblestore.WorkspaceTodoAIStateInProgress, "auto", false, "session-a", "Prepared title", "run-a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := svc.BindAITaskLifecycle(item.AccountScopeID, workspace, item.ID, started.AIState, pebblestore.WorkspaceTodoAIStateCompleted, "auto", false, "session-a", "Prepared title", "run-a", "done", "")
	if err != nil {
		t.Fatal(err)
	}
	if completed.AIDisplayTitle != "Prepared title" || completed.AIResult != "done" || !completed.Done || completed.CompletedAt == 0 {
		t.Fatalf("completed task = %+v", completed)
	}
	if _, err := svc.BindAITaskLifecycle(item.AccountScopeID, workspace, item.ID, completed.AIState, pebblestore.WorkspaceTodoAIStateFailed, "auto", false, "session-a", "", "run-a", "", "late failure"); err == nil {
		t.Fatal("terminal task must reject a second terminal transition")
	}
}

func TestAITaskLifecyclePublisherReceivesEveryDurableTransition(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "lifecycle.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(pebblestore.NewWorkspaceTodoStore(db), nil, nil, nil)
	workspace := t.TempDir()
	var published []TodoItem
	svc.SetAITaskLifecyclePublisher(func(item TodoItem) error {
		published = append(published, item)
		return nil
	})

	queued, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace", WorkspacePath: workspace, Request: "ship", IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAITaskLifecycle(queued); err != nil {
		t.Fatal(err)
	}
	preparing, err := svc.TransitionAITaskAuthority(AITaskTransitionInput{AccountScopeID: queued.AccountScopeID, WorkspacePath: workspace, ID: queued.ID, ExpectedState: queued.AIState, ExpectedVersion: queued.AIStateVersion, State: pebblestore.WorkspaceTodoAIStatePreparing})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.BindAITaskLifecycle(queued.AccountScopeID, workspace, queued.ID, preparing.AIState, pebblestore.WorkspaceTodoAIStateInProgress, "auto", false, "session", "Prepared title", "run", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindAITaskLifecycle(queued.AccountScopeID, workspace, queued.ID, started.AIState, pebblestore.WorkspaceTodoAIStateCompleted, "auto", false, "session", "Prepared title", "run", "done", ""); err != nil {
		t.Fatal(err)
	}
	if len(published) != 4 {
		t.Fatalf("published states=%#v, want queued plus three transitions", published)
	}
	for i, want := range []string{pebblestore.WorkspaceTodoAIStateQueued, pebblestore.WorkspaceTodoAIStatePreparing, pebblestore.WorkspaceTodoAIStateInProgress, pebblestore.WorkspaceTodoAIStateCompleted} {
		if published[i].AIState != want || published[i].AIStateVersion != uint64(i+1) {
			t.Fatalf("published[%d]=%#v, want state=%s version=%d", i, published[i], want, i+1)
		}
	}
}

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
		item, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: account, UserID: "user-" + account, WorkspaceID: "workspace-1", WorkspacePath: workspace, OriginSessionID: "origin-1", Request: request, Mode: "auto", IdempotencyKey: key})
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
	if _, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account-a", UserID: "user-account-a", WorkspaceID: "workspace-1", WorkspacePath: workspace, OriginSessionID: "origin-1", Request: "repair queue", Mode: "plan", IdempotencyKey: "stable-key"}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting mode error = %v", err)
	}
	if _, _, _, err := svc.CreateAITask(CreateAITaskInput{AccountScopeID: "account-a", UserID: "user-account-a", WorkspaceID: "workspace-1", WorkspacePath: workspace, Request: "bad mode", Mode: "manual", IdempotencyKey: "invalid-mode"}); err == nil || !strings.Contains(err.Error(), "mode must be plan or auto") {
		t.Fatalf("invalid mode error = %v", err)
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
