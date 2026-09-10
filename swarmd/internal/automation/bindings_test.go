package automation

import (
	"context"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type bindingPlans map[string]store.SessionPlanSnapshot
func (b bindingPlans) GetPlanRevision(session, id string, revision int) (store.SessionPlanSnapshot, bool, error) {
	p, ok := b[session]
	return p, ok, nil
}

// Purpose: SaveDefinition rejects malformed dependency graphs before writes or
// authorization. This domain layer exercises the same validator used by storage.
func TestAutomationBindingGraph(t *testing.T) {
	for _, kind := range []string{"empty", "duplicate", "missing", "self", "cycle", "duplicate-edge", "bound"} {
		t.Run(kind, func(t *testing.T) {
			s, r, a, _, p, scope, d := fixture(t)
			second := d.Plans[0]; second.ID = "second"; second.DependsOn = []string{"primary"}
			d.Plans = append(d.Plans, second)
			switch kind {
			case "empty": d.Plans = nil
			case "duplicate": d.Plans[1].ID = "primary"
			case "missing": d.Plans[1].DependsOn = []string{"missing"}
			case "self": d.Plans[1].DependsOn = []string{"second"}
			case "cycle": d.Plans[0].DependsOn = []string{"second"}
			case "duplicate-edge": d.Plans[1].DependsOn = []string{"primary", "primary"}
			case "bound": d.Plans = make([]store.AutomationPlanBinding, 17)
			}
			if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "create", 0, d); err == nil { t.Fatal("invalid graph accepted") }
			if r.writes != 0 || a.executions != 0 { t.Fatal("invalid graph reached write/grant") }
		})
	}
}

// Purpose: every plan, including a later dependency, must have canonical account
// ownership and exact content on save/run. Domain fakes isolate second-plan
// replacement; rejection must leave both stored revisions and grant counts intact.
func TestAutomationSecondPlanAuthority(t *testing.T) {
	for _, failure := range []string{"account", "ownership", "pin", "missing"} {
		t.Run(failure, func(t *testing.T) {
			s, r, a, plans, p, scope, d := fixture(t)
			second := plans.plan; second.SessionID = "second-session"; second.Document = &store.SessionPlanDocument{}
			canonical := bindingPlans{"session": plans.plan, "second-session": second}; s.plans = canonical
			d.Plans = append(d.Plans, store.AutomationPlanBinding{ID:"second", Plan:store.AutomationPlanReference{SessionID:second.SessionID, PlanID:second.ID, Revision:1}, DependsOn:[]string{"primary"}})
			d.Enabled = true
			d.Authorization = store.AutomationAuthorizationPolicy{Mode:"approved_policy", ApprovalReference:"approval", ExpiresAt:100001}
			created, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "create", 0, d)
			if err != nil { t.Fatal(err) }
			if created.SubjectID != p.SubjectID || created.Actor != "user" || created.WrittenAt != 100000 || len(created.Definition.Plans[1].Plan.DocumentSHA256) != 64 { t.Fatal("missing attribution/pin", created) }
			switch failure {
			case "ownership": a.deniedSession = second.SessionID
			case "account": second.AccountScopeID = "foreign"; canonical[second.SessionID] = second
			case "pin": second.Document.Title = "replacement"
			case "missing": delete(canonical, second.SessionID)
			}
			if _, err := s.CheckRun(context.Background(), p, scope, "check", 1); err == nil { t.Fatal("second plan accepted on run") }
			if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "update", 1, d); err == nil { t.Fatal("second plan accepted on save") }
			if r.writes != 1 || a.executions != 1 { t.Fatal("second plan rejection reached write/grant") }
		})
	}
}

// Purpose: history is authenticated and bounded before repository access, not a
// cross-account revision oracle. The domain fake observes absence of reads.
func TestAutomationHistoryAuthority(t *testing.T) {
	s, r, _, _, p, scope, _ := fixture(t)
	if _, _, err := s.History(context.Background(), p, scope, "check", "context", "check", 0, 51); err == nil { t.Fatal("unbounded history") }
	scope.AccountID = "foreign"
	if _, _, err := s.History(context.Background(), p, scope, "check", "context", "check", 0, 1); err == nil { t.Fatal("foreign history") }
	if r.reads != 0 { t.Fatal("rejected history reached store") }
}

// Purpose: History must expose immutable context authorship in bounded pages,
// after a later write, without leaking another account. Real
// Pebble plus the service is the narrowest layer for this end-to-end read contract.
func TestAutomationContextHistory(t *testing.T) {
	s, _, _, _, p, scope, d := fixture(t)
	repo, err := store.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer repo.Close()
	s.repo = repo
	ctx := context.Background()
	if _, _, err := s.SaveDefinition(ctx, p, scope, "check", "create", 0, d); err != nil { t.Fatal(err) }
	if _, _, err := s.UpdateContext(ctx, p, scope, "check", "first", 0, map[string]string{"rule":"original"}, nil); err != nil { t.Fatal(err) }
	p.SubjectID = "second-human"
	if _, _, err := s.UpdateContext(ctx, p, scope, "check", "second", 1, map[string]string{"rule":"updated"}, nil); err != nil { t.Fatal(err) }
	rows, next, err := s.History(ctx, p, scope, "check", "context", "check", 0, 1)
	if err != nil || len(rows) != 1 || next != 2 || rows[0].SubjectID != "second-human" || rows[0].Revision != 2 { t.Fatal("first page", rows, next, err) }
	rows, next, err = s.History(ctx, p, scope, "check", "context", "check", next, 1)
	if err != nil || len(rows) != 1 || next != 0 || rows[0].SubjectID != "human" || rows[0].WrittenAt != 100000 || rows[0].Context.UserLocked["rule"] != "original" { t.Fatal("immutable history", rows, next, err) }
	scope.AccountID = "foreign"; p.AccountID = "foreign"
	rows, next, err = s.History(ctx, p, scope, "check", "context", "check", 0, 1)
	if err != nil || len(rows) != 0 || next != 0 { t.Fatal("history scope leak", rows, err) }
}
