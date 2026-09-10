package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: automationKey/ApplyAutomationMutation must reject identifiers that
// JSON would normalize and keep delimiter-bearing namespaces distinct. Real
// Pebble proves rejected writes leave neither heads nor successful receipts.
func TestAutomationIdentifierIntegrity(t *testing.T) {
	s := openTaskProgramTestStore(t)
	for _, field := range []string{"account", "workspace", "automation", "mutation"} {
		m := automationFixture()
		bad := string([]byte{'x', 0xff})
		switch field {
		case "account": m.Record.Scope.AccountID = bad
		case "workspace": m.Record.Scope.WorkspaceID = bad
		case "automation": m.Record.AutomationID, m.Record.ID = bad, bad
		case "mutation": m.MutationID = bad
		}
		if _, fresh, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationInvalid) || fresh { t.Fatalf("%s: %v %v", field, fresh, err) }
	}
	for _, scope := range []AutomationScope{{AccountID: "a:b", WorkspaceID: "c"}, {AccountID: "a", WorkspaceID: "b:c"}} {
		m := automationFixture()
		m.Record.Scope = scope
		if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh { t.Fatal("namespace collision", fresh, err) }
		rows, _, err := s.SearchAutomationRecords(AutomationSearch{Scope: scope, Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].Scope != scope { t.Fatal("namespace leak", rows, err) }
	}
	m := automationFixture()
	if _, fresh, err := s.ApplyAutomationMutation(m); err != nil || !fresh { t.Fatal("rejected mutation left state", fresh, err) }
}
