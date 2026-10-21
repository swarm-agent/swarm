package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func recoverySnapshot(t *testing.T, f *committedValidationFixture) store.SessionSnapshot {
	t.Helper()
	p, ok, err := f.sessions.GetSession(f.scope.SessionID)
	if err != nil || !ok {
		t.Fatalf("parent: %v", err)
	}
	return p
}

func recordRecoveryRun(t *testing.T, f *committedValidationFixture, runID, parentID, status string) {
	t.Helper()
	i := store.V3SessionRunIntent{SessionID: f.request.ChildSessionID, RunID: runID, EpochID: "epoch-00000000000000000001", ParentSessionID: parentID, AccountScopeID: f.scope.Principal.AccountScopeID, UserID: f.scope.Principal.UserID, Status: store.V3RunIntentPendingExecutor}
	for _, state := range []string{store.V3RunIntentPendingExecutor, status} {
		i.Status = state
		res, err := f.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: i.SessionID, UserID: i.UserID, AccountScopeID: i.AccountScopeID, Kind: store.V3SessionMutationRecordRunIntent, RunIntent: &i, IdempotencyKey: runID + state, PayloadHash: runID + state, NowUnixMs: 10})
		if err != nil || res.Error != nil || res.Conflict != nil {
			t.Fatalf("run %s: %v %#v", state, err, res)
		}
	}
}

func newHandoffRecoveryFixture(t *testing.T) *committedValidationFixture {
	t.Helper()
	f := newCommittedValidationFixture(t)
	p := recoverySnapshot(t, f)
	row := p.Metadata["task_launches"].(map[string]any)[f.request.TaskCallID].(map[string]any)["launches"].([]any)[0].(map[string]any)
	row["phase"], row["error"], row["head_commit"] = "failed", "interrupted before commit", ""
	res, err := f.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: p.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: store.V3SessionMutationUpdateMetadata, Session: &p, IdempotencyKey: "failed-launch", PayloadHash: "failed-launch", NowUnixMs: 2})
	if err != nil || res.Error != nil || res.Conflict != nil {
		t.Fatalf("failed launch: %v %#v", err, res)
	}
	// Reproduce the real continuation boundary: V3 advances its current run,
	// while the original native task lifecycle remains historical.
	life, ok, err := f.sessions.GetLifecycle(f.request.ChildSessionID)
	if err != nil || !ok {
		t.Fatalf("native lifecycle: %v", err)
	}
	life.Phase, life.Error = "failed", "interrupted before commit"
	if err = f.sessions.UpsertLifecycle(life); err != nil {
		t.Fatal(err)
	}
	return f
}

func callHandoffRecovery(t *testing.T, f *committedValidationFixture, runID string) (string, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"action": "recover_handoff", "task_call_id": f.request.TaskCallID, "child_session_id": f.request.ChildSessionID, "child_run_id": runID, "source_head": f.request.HeadCommit})
	return f.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), f.scope, tool.Call{CallID: "recover-call", Name: "manage-worktree", Arguments: string(b)})
}

func TestRecoveredCoderHandoffValidatesAndPreservesFailure(t *testing.T) {
	// Requirement: a parent's successfully resumed regular Coder can be validated
	// without rewriting the failed task launch or integrating the source branch.
	// Owners: recover_handoff -> V3 metadata mutation -> ResolveCommittedSource.
	// Real Pebble, Git and shell validation prove the narrow complete boundary.
	f := newHandoffRecoveryFixture(t)
	before, _ := json.Marshal(recoverySnapshot(t, f).Metadata["task_launches"])
	beforeLife, _, _ := f.sessions.GetLifecycle(f.request.ChildSessionID)
	if _, err := f.runtime.ResolveCommittedSource(f.scope, f.request); err == nil {
		t.Fatal("failed launch accepted")
	}
	recordRecoveryRun(t, f, "resumed-run", f.scope.SessionID, store.V3RunIntentCompleted)
	for i := 0; i < 2; i++ {
		if out, err := callHandoffRecovery(t, f, "resumed-run"); err != nil {
			t.Fatalf("recovery %d: %v %s", i, err, out)
		}
	}
	after := recoverySnapshot(t, f)
	old, _ := json.Marshal(after.Metadata["task_launches"])
	if string(old) != string(before) {
		t.Fatal("original failed attempt changed")
	}
	afterLife, _, _ := f.sessions.GetLifecycle(f.request.ChildSessionID)
	if handoffBefore, handoffAfter := mustRecoveryJSON(t, beforeLife), mustRecoveryJSON(t, afterLife); handoffBefore != handoffAfter {
		t.Fatal("original native lifecycle history changed")
	}
	if len(after.Metadata["recovered_coder_handoffs"].(map[string]any)) != 1 {
		t.Fatal("duplicate recovery receipts")
	}
	recalled, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), f.scope, tool.Call{CallID: "recall-recovered", Name: "manage-worktree", Arguments: `{"action":"recall","task_call_id":"fixture-call"}`})
	if err != nil || !strings.Contains(recalled, `"committed_source"`) {
		t.Fatalf("recovered handoff unavailable to recall: %s %v", recalled, err)
	}
	payload, err := f.execute(t, "test \"$(cat tracked.txt)\" = committed-child", nil)
	if err != nil || validationReceipt(t, payload)["verified"] != true {
		t.Fatalf("validation: %v %#v", err, payload)
	}
	if got := validationGit(t, f.source, "rev-parse", "HEAD"); got != f.base {
		t.Fatal("recovery integrated Git")
	}
	// A later continuation invalidates the old receipt even if HEAD is unchanged.
	recordRecoveryRun(t, f, "later-run", f.scope.SessionID, store.V3RunIntentCompleted)
	if _, err = f.runtime.ResolveCommittedSource(f.scope, f.request); err == nil {
		t.Fatal("stale recovered run accepted")
	}
}

func TestRecoveredCoderHandoffRejectsChangedCommitAfterRecording(t *testing.T) {
	// Requirement: an accepted receipt never follows a changed HEAD. Threat:
	// validating unreviewed code with an old recovered success. The public tool
	// and command preflight use real Git and Pebble; assert no command executes.
	f := newHandoffRecoveryFixture(t)
	recordRecoveryRun(t, f, "resumed-run", f.scope.SessionID, store.V3RunIntentCompleted)
	if _, err := callHandoffRecovery(t, f, "resumed-run"); err != nil {
		t.Fatal(err)
	}
	validationWrite(t, f.child+"/tracked.txt", "changed after receipt\n")
	validationGit(t, f.child, "commit", "-am", "changed after receipt")
	if _, err := f.runtime.ResolveCommittedSource(f.scope, f.request); err == nil {
		t.Fatal("old commit accepted after HEAD changed")
	}
	f.request.HeadCommit = validationGit(t, f.child, "rev-parse", "HEAD")
	if _, err := callHandoffRecovery(t, f, "resumed-run"); err == nil {
		t.Fatal("same run rebound to another commit")
	}
}

func TestRecoveredCoderHandoffRejectsInvalidEvidenceWithoutMutation(t *testing.T) {
	// Requirement: recovery requires current successful ownership and exact Git
	// evidence. Threat: stale/cross-parent/failed/changed source laundering.
	// Public tool admission + real durable state is the narrowest proof; reject
	// before recording anything, and preserve both parent history and Git HEAD.
	for _, name := range []string{"other-parent", "wrong-run", "failed-run", "active-run", "competing-native-run", "changed-head", "dirty-tree", "wrong-call", "other-principal"} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffRecoveryFixture(t)
			owner, status := f.scope.SessionID, store.V3RunIntentCompleted
			if name == "other-parent" {
				owner = "different-parent"
			}
			if name == "failed-run" {
				status = store.V3RunIntentFailed
			}
			if name == "active-run" {
				status = store.V3RunIntentPendingExecutor
			}
			recordRecoveryRun(t, f, "resumed-run", owner, status)
			runID := "resumed-run"
			switch name {
			case "competing-native-run":
				life, _, _ := f.sessions.GetLifecycle(f.request.ChildSessionID)
				life.RunID = "another-native-attempt"
				if err := f.sessions.UpsertLifecycle(life); err != nil {
					t.Fatal(err)
				}
			case "wrong-run":
				runID = "stale-run"
			case "changed-head":
				validationWrite(t, f.child+"/tracked.txt", "changed\n")
				validationGit(t, f.child, "commit", "-am", "changed")
			case "dirty-tree":
				validationWrite(t, f.child+"/tracked.txt", "dirty\n")
			case "wrong-call":
				f.request.TaskCallID = "not-owned"
			case "other-principal":
				f.scope.Principal.UserID = "someone-else"
			}
			before, _ := json.Marshal(recoverySnapshot(t, f).Metadata)
			if out, err := callHandoffRecovery(t, f, runID); err == nil {
				t.Fatalf("accepted: %s", out)
			}
			after, _ := json.Marshal(recoverySnapshot(t, f).Metadata)
			if string(before) != string(after) {
				t.Fatal("rejected recovery mutated parent")
			}
		})
	}
}

func mustRecoveryJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type failedRecoveryStore struct{ *sessionruntime.Service }

func (s failedRecoveryStore) ApplySessionMutation(input store.V3SessionMutationInput) (store.V3SessionMutationResult, error) {
	if strings.HasPrefix(input.IdempotencyKey, "coder-handoff-recovery:") {
		return store.V3SessionMutationResult{}, errors.New("injected persistence failure")
	}
	return s.Service.ApplySessionMutation(input)
}

func TestRecoveredCoderHandoffPersistenceFailureDoesNotAuthorize(t *testing.T) {
	// Requirement: failed persistence cannot authorize a recovered handoff.
	// Threat: partial metadata effects or success based solely on an in-memory
	// overlay. Inject at ApplySessionMutation; all Git/store reads remain real.
	f := newHandoffRecoveryFixture(t)
	recordRecoveryRun(t, f, "resumed-run", f.scope.SessionID, store.V3RunIntentCompleted)
	f.runtime.SetManageWorktreeServices(failedRecoveryStore{f.sessions}, f.workspace, f.worktrees)
	before, _ := json.Marshal(recoverySnapshot(t, f).Metadata)
	if out, err := callHandoffRecovery(t, f, "resumed-run"); err == nil || !strings.Contains(err.Error(), "injected persistence failure") {
		t.Fatalf("failure not reached: %s %v", out, err)
	}
	after, _ := json.Marshal(recoverySnapshot(t, f).Metadata)
	if string(before) != string(after) {
		t.Fatal("partial recovery persisted")
	}
	if _, err := f.runtime.ResolveCommittedSource(f.scope, f.request); err == nil {
		t.Fatal("failed persistence authorized validation")
	}
}
