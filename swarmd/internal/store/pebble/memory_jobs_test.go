package pebblestore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Purpose: exercise MemoryStore job transitions and canonical session authority
// against a real temporary DB. Threats: foreign/excluded reads, stale/cancelled
// publication, forged provenance, partial commits, restart billing replay and
// automated replacement of explicit rules. This is the narrowest atomic layer.
func memoryJobFixture(t *testing.T) (*Store, *MemoryStore, MemoryJob) {
	db, s, d := memoryTest(t)
	model := AgentModelAssignment{Provider: "codex", Model: "test-model", Thinking: "medium"}
	models := AgentModelSettingsRecord{AccountScopeID: "a", Swarm: SwarmAgentModelAssignments{Action: model, Plan: model}, SystemAgents: SystemAgentModelAssignments{Compact: model, Finder: model, Coder: model, Designer: model, Router: model}}
	if _, err := NewAgentModelSettingsStore(db).PutForAccount(models); err != nil {
		t.Fatal(err)
	}
	settings := d.Settings
	settings.AutomationEnabled = true
	settings.IncludedWorkspaces = []string{"w"}
	settings.ReviewBeforeApply = false
	memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "opt in", Operation: "settings", Settings: &settings})
	for _, w := range []string{"w", "other"} {
		if err := db.PutJSON(KeyWorkspaceEntryByIDForAccount("a", w), WorkspaceEntry{AccountScopeID: "a", WorkspaceID: w, State: "active", Path: "/workspace"}); err != nil {
			t.Fatal(err)
		}
	}
	sessions := NewSessionStore(db)
	for _, item := range []struct{ id, account, workspace string }{{"allowed", "a", "w"}, {"foreign", "b", "w"}, {"excluded", "a", "other"}} {
		_, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: item.id, UserID: "user", AccountScopeID: item.account, IdempotencyKey: "create", RequestHash: "create-hash", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: item.id, WorkspacePath: "/workspace", WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: item.workspace, Path: "/workspace"}}}, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		_, err = sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: item.id, UserID: "user", AccountScopeID: item.account, IdempotencyKey: "message", RequestHash: "message-hash", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "Project uses Go"}, RunIntent: &V3SessionRunIntent{Status: V3RunIntentDispatchBlocked, BlockedReason: "fixture"}, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.QueueMemoryJob("a", "user", "job", false)
	if err != nil {
		t.Fatal(err)
	}
	return db, s, j
}
func claimMemory(t *testing.T, s *MemoryStore) MemoryJob {
	t.Helper()
	j, in, err := s.ClaimMemoryJob(context.Background(), "a", "user", "job")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Source.SessionID != "allowed" || j.Model.Model != "test-model" {
		t.Fatalf("source/model authority: %+v %+v", j, in)
	}
	if err = s.ReserveMemorySpend("a", "user", "job", 10); err != nil {
		t.Fatal(err)
	}
	return j
}
func learnedProposal(j MemoryJob) *MemoryEntry {
	return &MemoryEntry{Kind: "learned", Content: "Project uses Go", WorkspaceID: "w", Sources: j.Sources}
}
func TestMemoryJobsAtomicScopeAndIdempotency(t *testing.T) {
	_, s, queued := memoryJobFixture(t)
	again, err := s.QueueMemoryJob("a", "user", "job", false)
	if err != nil || !reflect.DeepEqual(again, queued) {
		t.Fatal("idempotency", err)
	}
	if _, err = s.QueueMemoryJob("a", "intruder", "job", false); err == nil {
		t.Fatal("foreign owner")
	}
	j := claimMemory(t, s)
	bad := learnedProposal(j)
	bad.Sources = []MemorySource{{WorkspaceID: "w", SessionID: "foreign", EventSeq: 1}}
	before, _ := s.GetForAccount("a")
	if _, err = s.FinishMemoryJob(context.Background(), "a", "user", "job", bad, 10, 10, false); err == nil {
		t.Fatal("forged source")
	}
	after, _ := s.GetForAccount("a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial rejected publication")
	}
	done, err := s.FinishMemoryJob(context.Background(), "a", "user", "job", learnedProposal(j), 10, 10, false)
	if err != nil || done.Status != "completed" {
		t.Fatal(done, err)
	}
	d, _ := s.GetForAccount("a")
	if len(d.Entries) != 1 || d.JobCursors["allowed"] == 0 || done.ResultRevision != d.Revision {
		t.Fatal("incomplete atomic result")
	}
	if _, err = s.FinishMemoryJob(context.Background(), "a", "user", "job", learnedProposal(j), 10, 10, false); err == nil {
		t.Fatal("double publish")
	}
}
func TestMemoryJobsCancellationRevocationRestart(t *testing.T) {
	for _, mode := range []string{"cancel", "settings", "restart", "source-delete", "workspace-revoke"} {
		t.Run(mode, func(t *testing.T) {
			db, s, _ := memoryJobFixture(t)
			j := claimMemory(t, s)
			switch mode {
			case "cancel":
				if _, err := s.UpdateMemoryJob("a", "user", "job", "cancelled", 0, 0); err != nil {
					t.Fatal(err)
				}
			case "settings":
				d, _ := s.GetForAccount("a")
				settings := d.Settings
				settings.AutomationEnabled = false
				memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "pause", Operation: "settings", Settings: &settings})
			case "restart":
				if err := NewMemoryStore(db).RecoverMemoryJobs("a", "user"); err != nil {
					t.Fatal(err)
				}
			case "workspace-revoke":
				if err := db.PutJSON(KeyWorkspaceEntryByIDForAccount("a", "w"), WorkspaceEntry{AccountScopeID: "a", WorkspaceID: "w", State: "inactive"}); err != nil {
					t.Fatal(err)
				}
			case "source-delete":
				if err := NewSessionStore(db).DeleteSession("allowed"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.FinishMemoryJob(context.Background(), "a", "user", "job", learnedProposal(j), 10, 10, false); err == nil {
				t.Fatal("revoked job published")
			}
			d, _ := s.GetForAccount("a")
			if len(d.Entries) != 0 || len(d.JobCursors) != 0 {
				t.Fatal("unauthorized partial result")
			}
		})
	}
}
func TestMemoryJobsReviewAndFailureAtomicity(t *testing.T) {
	db, s, _ := memoryJobFixture(t)
	j := claimMemory(t, s)
	d, _ := s.GetForAccount("a")
	d.Jobs[0].Settings.ReviewBeforeApply = true
	if err := s.persist(d); err != nil {
		t.Fatal(err)
	}
	review, err := s.FinishMemoryJob(context.Background(), "a", "user", "job", learnedProposal(j), 10, 10, false)
	if err != nil || review.Status != "review" {
		t.Fatal(review, err)
	}
	d, _ = s.GetForAccount("a")
	if len(d.Entries) != 0 {
		t.Fatal("unapproved apply")
	}
	// Inject an invalid oversized document into the private loaded transaction;
	// the synchronous publication must reject without changing persisted bytes.
	before, _, _ := db.GetBytes(memoryKey("a"))
	d.Jobs = append(d.Jobs, make([]MemoryJob, MemoryMaxJobs)...)
	if err = s.persist(d); err == nil {
		t.Fatal("capacity accepted")
	}
	after, _, _ := db.GetBytes(memoryKey("a"))
	if string(before) != string(after) {
		t.Fatal("partial failure")
	}
	if _, err = s.FinishMemoryJob(context.Background(), "a", "user", "job", nil, 0, 0, true); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetForAccount("a")
	if len(d.Entries) != 1 {
		t.Fatal("approval lost")
	}
	encoded, _ := json.Marshal(d)
	if len(encoded) == 0 {
		t.Fatal("missing inspectable result")
	}
}

func TestMemoryJobsConflictProtectionAndIncrementalRead(t *testing.T) {
	db, s, _ := memoryJobFixture(t)
	j := claimMemory(t, s)
	if _, err := s.FinishMemoryJob(context.Background(), "a", "user", "job", learnedProposal(j), 10, 10, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueMemoryJob("a", "user", "empty", false); err != nil {
		t.Fatal(err)
	}
	empty, in, err := s.ClaimMemoryJob(context.Background(), "a", "user", "empty")
	if err != nil || len(in) != 0 {
		t.Fatal("reread old messages", err)
	}
	if _, err = s.FinishMemoryJob(context.Background(), "a", "user", empty.ID, nil, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	_, err = NewSessionStore(db).ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "allowed", UserID: "user", AccountScopeID: "a", IdempotencyKey: "new-message", RequestHash: "new-message", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "Project now uses Rust"}, RunIntent: &V3SessionRunIntent{Status: V3RunIntentDispatchBlocked, BlockedReason: "fixture"}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueMemoryJob("a", "user", "conflict", false); err != nil {
		t.Fatal(err)
	}
	next, in, err := s.ClaimMemoryJob(context.Background(), "a", "user", "conflict")
	if err != nil || len(in) != 1 || in[0].Content != "Project now uses Rust" {
		t.Fatal(in, err)
	}
	if err = s.ReserveMemorySpend("a", "user", "conflict", 10); err != nil {
		t.Fatal(err)
	}
	proposal := learnedProposal(next)
	proposal.Content = "Project uses Rust"
	review, err := s.FinishMemoryJob(context.Background(), "a", "user", "conflict", proposal, 10, 10, false)
	if err != nil || review.Status != "review" || !review.Conflict {
		t.Fatal(review, err)
	}
	d, _ := s.GetForAccount("a")
	if d.Entries[0].Content != "Project uses Go" {
		t.Fatal("conflict silently applied")
	}
	// Explicit user pin invalidates pending reconciliation and cannot be overwritten.
	pinned := d.Entries[0]
	pinned.Pinned = true
	memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "pin", Operation: "put", Entry: pinned})
	if _, err = s.FinishMemoryJob(context.Background(), "a", "user", "conflict", nil, 0, 0, true); err == nil {
		t.Fatal("pinned replacement")
	}
	d, _ = s.GetForAccount("a")
	if !d.Entries[0].Pinned || d.Entries[0].Content != "Project uses Go" {
		t.Fatal("rule protection failed")
	}
}

// Purpose: memorySessionSource must accept the canonical ID-less execution lane
// of an isolated session without treating arbitrary paths as catalog authority.
// This store-layer regression reproduces live V3 grants and rejects mismatched,
// unavailable, foreign and excluded scope with no document mutation.
func TestMemoryIsolatedSessionSourceScope(t *testing.T) {
	_, s, _ := memoryJobFixture(t)
	d, err := s.GetForAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	base := SessionSnapshot{ID: "allowed", AccountScopeID: "a", UserID: "user", WorktreeEnabled: true, WorktreeRootPath: "/isolated/lane", WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: "w", Path: "/workspace"}, {Kind: WorkspaceGrantWorktree, Path: "/isolated/lane"}}}
	for _, mode := range []string{"valid", "wrong-path", "disabled", "unavailable", "unknown-kind", "no-catalog", "foreign", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			sess := base
			sess.WorkspaceGrants = append([]WorkspaceGrant(nil), base.WorkspaceGrants...)
			doc := d
			switch mode {
			case "wrong-path":
				sess.WorkspaceGrants[1].Path = "/unowned"
			case "disabled":
				sess.WorktreeEnabled = false
			case "unavailable":
				available := false
				sess.WorkspaceGrants[1].Available = &available
			case "unknown-kind":
				sess.WorkspaceGrants[1].Kind = WorkspaceGrantTemporary
			case "no-catalog":
				sess.WorkspaceGrants = sess.WorkspaceGrants[1:]
			case "foreign":
				sess.AccountScopeID = "foreign"
			case "excluded":
				doc.Settings.ExcludedWorkspaces = []string{"w"}
			}
			src, err := s.memorySessionSource(doc, sess, "user")
			if mode == "valid" {
				if err != nil || src.WorkspaceID != "w" || src.SessionID != "allowed" {
					t.Fatal(src, err)
				}
			} else if err == nil {
				t.Fatal("unauthorized source accepted")
			}
			after, err := s.GetForAccount("a")
			if err != nil || !reflect.DeepEqual(d, after) {
				t.Fatal("source validation mutated memory", err)
			}
		})
	}
}
