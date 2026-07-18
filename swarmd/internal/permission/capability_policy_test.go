package permission

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCapabilityPoliciesDefaultToAskAndRemainIsolated(t *testing.T) {
	policy := DefaultPolicy()
	deployArgs := `{"action":"deploy","proposals":[{"prompt":"work"}]}`
	planArgs := `{"action":"request_new_plan","document":{"title":"Plan","info":{"goal":"ship"},"checkpoints":[{"id":"cp-1","title":"One","status":"pending","tasks":["ship"],"acceptance_criteria":["done"]}]}}`
	if got := ExplainPolicy("auto", "manage_sessions", deployArgs, policy).Decision; got != PolicyDecisionAsk {
		t.Fatalf("fresh deploy decision = %q, want ask", got)
	}
	if got := ExplainPolicy("auto", "plan_manage", planArgs, policy).Decision; got != PolicyDecisionAsk {
		t.Fatalf("fresh plan decision = %q, want ask", got)
	}
	policy.SessionDeploy.Mode = CapabilityModeAlwaysAllow
	if got := ExplainPolicy("auto", "manage_sessions", deployArgs, policy).Decision; got != PolicyDecisionAllow {
		t.Fatalf("deploy always-allow decision = %q", got)
	}
	if got := ExplainPolicy("auto", "plan_manage", planArgs, policy).Decision; got != PolicyDecisionAsk {
		t.Fatalf("deploy policy leaked into plan acceptance: %q", got)
	}
	policy.SessionDeploy.Mode = CapabilityModeAsk
	policy.PlanAcceptance.Mode = CapabilityModeAlwaysAllow
	if got := ExplainPolicy("auto", "plan_manage", planArgs, policy).Decision; got != PolicyDecisionAllow {
		t.Fatalf("plan always-allow decision = %q", got)
	}
	if got := ExplainPolicy("auto", "manage_sessions", deployArgs, policy).Decision; got != PolicyDecisionAsk {
		t.Fatalf("plan policy leaked into deployment: %q", got)
	}
}

func TestCapabilityPoliciesPersistByAccount(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "capabilities.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewService(pebblestore.NewPermissionStore(store), events, nil)
	reader := NewService(pebblestore.NewPermissionStore(store), events, nil)
	deploy := SessionDeployPolicy{Mode: CapabilityModeBounded, AutomaticDeploymentsPerParentRun: 2, OverLimitAction: SessionDeployOverLimitDeny}
	plan := PlanAcceptancePolicy{Mode: CapabilityModeAlwaysAllow}
	if _, err := writer.UpdateCapabilityPoliciesForAccount("account-a", deploy, plan); err != nil {
		t.Fatal(err)
	}
	got, err := reader.CurrentPolicyForAccount("account-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionDeploy != deploy || got.PlanAcceptance != plan {
		t.Fatalf("persisted capabilities = %#v %#v", got.SessionDeploy, got.PlanAcceptance)
	}
	other, err := reader.CurrentPolicyForAccount("account-b")
	if err != nil {
		t.Fatal(err)
	}
	if other.SessionDeploy.Mode != CapabilityModeAsk || other.PlanAcceptance.Mode != CapabilityModeAsk {
		t.Fatalf("account policy leaked: %#v", other)
	}
}

func TestMarkSessionDeployApprovedUsesFinalApprovedSelection(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "deploy-approved-selection.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	reserved, err := svc.ReserveSessionDeploy(SessionDeployReservationRequest{SessionID: "parent", AccountScopeID: "account", RunID: "run", CallID: "call", ManifestHash: "hash", DeployCount: 1})
	if err != nil || reserved.Decision != SessionDeployReservationAsk {
		t.Fatalf("reservation = %#v, %v", reserved, err)
	}
	if err := svc.MarkSessionDeployApproved("parent", "run", "call", 3); err != nil {
		t.Fatal(err)
	}
	record, ok, err := svc.store.GetSessionDeployReservation("parent", "run", "call")
	if err != nil || !ok {
		t.Fatalf("approved reservation lookup = %#v, %v", record, err)
	}
	if record.Status != string(SessionDeployReservationApprove) || record.DeployCount != 3 {
		t.Fatalf("approved reservation = %#v", record)
	}
}

func TestBoundedSessionDeployReservationCountsPerParentRun(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "deploy-reservations.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	deploy := SessionDeployPolicy{Mode: CapabilityModeBounded, AutomaticDeploymentsPerParentRun: 2, OverLimitAction: SessionDeployOverLimitAsk}
	if _, err := svc.UpdateCapabilityPoliciesForAccount("account", deploy, DefaultPlanAcceptancePolicy()); err != nil {
		t.Fatal(err)
	}
	reserve := func(call string) SessionDeployReservationResult {
		result, err := svc.ReserveSessionDeploy(SessionDeployReservationRequest{SessionID: "parent", AccountScopeID: "account", RunID: "run", CallID: call, ManifestHash: "hash-" + call, DeployCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := reserve("one").Decision; got != SessionDeployReservationApprove {
		t.Fatalf("first decision = %q", got)
	}
	if got := reserve("two").Decision; got != SessionDeployReservationApprove {
		t.Fatalf("second decision = %q", got)
	}
	if got := reserve("three").Decision; got != SessionDeployReservationAsk {
		t.Fatalf("over-limit decision = %q, want ask", got)
	}
	deploy.OverLimitAction = SessionDeployOverLimitDeny
	if _, err := svc.UpdateCapabilityPoliciesForAccount("account", deploy, DefaultPlanAcceptancePolicy()); err != nil {
		t.Fatal(err)
	}
	if got := reserve("four").Decision; got != SessionDeployReservationDeny {
		t.Fatalf("over-limit deny decision = %q", got)
	}
	otherRun, err := svc.ReserveSessionDeploy(SessionDeployReservationRequest{SessionID: "parent", AccountScopeID: "account", RunID: "run-2", CallID: "one", ManifestHash: "other", DeployCount: 1})
	if err != nil || otherRun.Decision != SessionDeployReservationApprove {
		t.Fatalf("new parent run reservation = %#v, %v", otherRun, err)
	}
}
