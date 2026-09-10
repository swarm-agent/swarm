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

// Purpose: ApplyAutomationMutation derives immutable record attribution from the
// trusted mutation envelope, rejects missing/forged metadata, and returns original
// writer/time on replay. Real storage proves both history and no-write rejection.
func TestAutomationAttributionReplay(t *testing.T) {
	s := openTaskProgramTestStore(t)
	m := automationFixture()
	for _, bad := range []string{"subject", "time", "record"} {
		invalid := m
		switch bad {
		case "subject": invalid.SubjectID = ""
		case "time": invalid.WrittenAt = 0
		case "record": invalid.Record.SubjectID = "forged"
		}
		if _, fresh, err := s.ApplyAutomationMutation(invalid); err == nil || fresh { t.Fatal("invalid attribution accepted", bad) }
	}
	first, fresh, err := s.ApplyAutomationMutation(m)
	if err != nil || !fresh { t.Fatal("invalid writes left state", err) }
	update := automationFixture(); update.ExpectedRevision = 1; update.MutationID = "update"
	update.SubjectID = "second-writer"; update.WrittenAt = 200000
	if _, _, err := s.ApplyAutomationMutation(update); err != nil { t.Fatal(err) }
	m.WrittenAt = 300000
	replay, fresh, err := s.ApplyAutomationMutation(m)
	if err != nil || fresh || replay.SubjectID != first.SubjectID || replay.Actor != first.Actor || replay.WrittenAt != 100000 { t.Fatal("replay attribution changed", replay, err) }
	m.SubjectID = "impersonator"
	if _, _, err := s.ApplyAutomationMutation(m); !errors.Is(err, ErrAutomationConflict) { t.Fatal("writer collision accepted", err) }
	for rev, subject := range map[uint64]string{1:"writer", 2:"second-writer"} {
		r, found, err := s.GetAutomationRecord(m.Record.Scope, "check", "definition", "check", rev)
		if err != nil || !found || r.SubjectID != subject || r.WrittenAt != int64(rev)*100000 || r.Actor != "user" { t.Fatal("historical attribution lost", r, err) }
	}
}
