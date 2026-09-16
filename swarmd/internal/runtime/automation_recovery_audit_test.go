package runtime

import (
	"context"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: automationSweepAt must visit recovery and outcome reconciliation after
// an execution-affecting edit clears the grant. Real catalog/definition storage
// and a recording boundary isolate the daemon skip regression; these callbacks
// do not prove provider execution or replace domain ownership/approval tests.
// A paused definition must never reach Tick, and the trusted identity must use
// store attribution, not a caller-provided role or a fabricated approval.
func TestAutomationSweepRecoversWithoutCurrentGrant(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	if err := db.PutJSON(store.AccountScopePrefix()+scope.AccountID, store.AccountScopeRecord{ID: scope.AccountID}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.WorkspaceEntryPrefixForAccount(scope.AccountID)+"fixture", store.WorkspaceEntry{AccountScopeID: scope.AccountID, WorkspaceID: scope.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	def := store.AutomationDefinition{Name: "Harmless recovery fixture", Schedule: store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 60, MissedPolicy: "skip", OverlapPolicy: "independent"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 900000}, Plans: []store.AutomationPlanBinding{{ID: "instructions", Plan: store.AutomationPlanReference{SessionID: "source", PlanID: "plan", Revision: 1, DocumentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	row, _, err := db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: "owner", WrittenAt: 100000, MutationID: "save", Record: store.AutomationRecord{Scope: scope, AutomationID: "fixture", Kind: "definition", ID: "fixture", Definition: &def}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &auditRecoveryRecorder{}
	outcomes := 0
	for page := 0; page < 8 && outcomes == 0; page++ {
		err = automationSweepAt(context.Background(), db, recorder, time.UnixMilli(120000), func(ctx context.Context, p automation.Principal, got store.AutomationRecord) error {
			outcomes++
			actual, identityErr := automation.RuntimePrincipal(ctx)
			if identityErr != nil || actual != p || p.SubjectID != "owner" || p.Role != "system" || got.ID != row.ID {
				t.Fatalf("wrong recovery identity: %+v %v", p, identityErr)
			}
			return nil
		})
		if err := automationSweepFailure(err); err != nil {
			t.Fatal(err)
		}
	}
	if recorder.ticks != 0 || recorder.recoveries != 1 || outcomes != 1 {
		t.Fatalf("ticks=%d recovery=%d outcomes=%d", recorder.ticks, recorder.recoveries, outcomes)
	}
	unchanged, found, err := db.GetAutomationRecord(scope, row.ID, "definition", row.ID, 0)
	if err != nil || !found || unchanged.Revision != row.Revision || unchanged.Definition.Enabled || unchanged.Definition.Authorization.ApprovalReference != "" {
		t.Fatal("recovery altered paused policy", err)
	}
}

type auditRecoveryRecorder struct{ ticks, recoveries int }

func (r *auditRecoveryRecorder) Tick(context.Context, automation.Principal, store.AutomationScope, string, uint64) error {
	r.ticks++
	return nil
}
func (r *auditRecoveryRecorder) TickAt(context.Context, automation.Principal, store.AutomationScope, string, uint64, time.Time) error {
	r.ticks++
	return nil
}
func (r *auditRecoveryRecorder) RecoverPage(context.Context, automation.Principal, store.AutomationScope, string, string) (string, error) {
	r.recoveries++
	return "", nil
}
