package run

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: native budget proposals must remain inert until AcceptWorker commits
// the policy through SetWorkerBudget's shared persistence boundary. Real store
// and native tool calls are the narrowest layer proving create/update approval,
// null clearing, and invalid/stale/foreign requests without policy mutation.
func TestManageWorkersBudgetProposalAcceptance(t *testing.T) {
	s, settings, db := taskFallbackFixture(t)
	taskFallbackSettings(t, settings, "account-a", "specialist", "account-a-action")
	st := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(st, nil)
	for _, v := range []struct{ id, account, agent string }{{"author", "account-a", "orchestrator"}, {"foreign", "account-b", "orchestrator"}, {"worker", "account-a", "coder"}} {
		if err := st.CreateSession(store.SessionSnapshot{ID: v.id, AccountScopeID: v.account, UserID: "owner", WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "folder", Kind: store.WorkspaceGrantPrimary, Available: store.BoolPtr(true)}}, Metadata: map[string]any{"agent_name": v.agent}}); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(session string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return s.executeManageWorkersTool(session, string(raw))
	}
	output, err := invoke("author", map[string]any{"action": "propose", "name": "Budget worker", "instructions": "Review work", "budget": map[string]any{"daily_usd": 10, "daily_tokens": 100}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Worker store.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	w := result.Worker
	// Pending updates preserve the proposal and still cannot apply a policy.
	pendingArgs := map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": w.Revision, "name": "Revised budget worker", "budget": map[string]any{"daily_usd": 10, "daily_tokens": 100}}
	if _, err := invoke("author", pendingArgs); err != nil {
		t.Fatal(err)
	}
	w, _, err = s.sessions.GetWorker("account-a", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "update", "worker_id": w.ID, "expected_revision": w.Revision, "budget": map[string]any{"daily_usd": 10, "daily_tokens": 100}}
	before, err := st.GetWorkerBudget("account-a", w.ID)
	if err != nil || before.Revision != 0 || w.Budget == nil {
		t.Fatalf("proposal applied early: %+v %v", before, err)
	}
	if _, err := s.sessions.AcceptWorker("account-a", "owner", w.ID, w.Revision+1, map[string]string{"primary": "folder"}); err == nil {
		t.Fatal("stale acceptance succeeded")
	}
	if _, err := s.sessions.AcceptWorker("account-b", "owner", w.ID, w.Revision, map[string]string{"primary": "folder"}); err == nil {
		t.Fatal("foreign acceptance succeeded")
	}
	if _, err := s.sessions.AcceptWorker("account-a", "owner", w.ID, w.Revision, map[string]string{"primary": "other"}); err == nil {
		t.Fatal("invalid binding accepted")
	}
	unchanged, _ := st.GetWorkerBudget("account-a", w.ID)
	if unchanged != before {
		t.Fatal("failed acceptance changed budget")
	}
	w, err = s.sessions.AcceptWorker("account-a", "owner", w.ID, w.Revision, map[string]string{"primary": "folder"})
	if err != nil {
		t.Fatal(err)
	}
	args["expected_revision"] = w.Revision
	policy, err := st.GetWorkerBudget("account-a", w.ID)
	if err != nil || policy.DailyCostLimitUSD != 10 || policy.DailyTokensLimit != 100 {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	for _, tc := range []struct {
		session string
		key     string
		value   any
	}{{"foreign", "budget", nil}, {"worker", "budget", nil}, {"author", "expected_revision", w.Revision + 1}, {"author", "budget", map[string]any{"daily_usd": -1}}, {"author", "budget", map[string]any{"daily_usd": 1, "daily_tokens": 1.5}}, {"author", "budget", map[string]any{"daily_tokens": 1}}} {
		bad := map[string]any{}
		for k, v := range args {
			bad[k] = v
		}
		bad[tc.key] = tc.value
		if _, err := invoke(tc.session, bad); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
		got, _ := st.GetWorkerBudget("account-a", w.ID)
		saved, _, _ := s.sessions.GetWorker("account-a", w.ID)
		if got != policy || saved.Revision != w.Revision || saved.Name != w.Name {
			t.Fatal("rejected request mutated state")
		}
	}
	args["budget"] = nil
	output, err = invoke("author", args)
	if err != nil {
		t.Fatal(err)
	}
	var proposal struct {
		NextAction string `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(output), &proposal); err != nil || proposal.NextAction != "await_worker_acceptance" {
		t.Fatalf("budget update must await acceptance: %s (%v)", output, err)
	}
	staged, _, err := s.sessions.GetWorker("account-a", w.ID)
	if err != nil || staged.PendingReview == nil || staged.PendingReview.Budget == nil {
		t.Fatalf("reset not proposed: %+v %v", staged, err)
	}
	unchanged, _ = st.GetWorkerBudget("account-a", w.ID)
	if unchanged != policy {
		t.Fatal("update applied before acceptance")
	}
	if _, err := s.sessions.AcceptWorker("account-a", "owner", w.ID, staged.Revision, map[string]string{"primary": "folder"}); err != nil {
		t.Fatal(err)
	}
	cleared, _ := st.GetWorkerBudget("account-a", w.ID)
	if cleared.DailyCostLimitUSD != 0 || cleared.DailyTokensLimit != 0 || cleared.Revision != policy.Revision+1 {
		t.Fatalf("clear: %+v", cleared)
	}
}
