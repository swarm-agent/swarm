package automation

import (
	"context"
	"errors"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: session identity is part of the reviewed policy digest, and a bound
// automation cannot be silently moved or detached via ordinary definition edits.
// Service/ApprovalPolicyDigest are the narrowest domain authority for this rule.
func TestPersistentAutomationSessionBindingCannotMove(t *testing.T) {
	s, repo, _, _, p, scope, d := fixture(t)
	d.SessionID = "conversation"
	before, err := ApprovalPolicyDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "create", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"other-conversation", ""} {
		changed := d
		changed.SessionID = id
		digest, err := ApprovalPolicyDigest(changed)
		if err != nil || digest == before {
			t.Fatalf("session not reviewed: %v", err)
		}
		if _, _, err := s.SaveDefinition(context.Background(), p, scope, "check", "move-"+id, saved.Revision, changed); !errors.Is(err, ErrDenied) {
			t.Fatalf("binding moved: %v", err)
		}
	}
	row, ok, err := repo.GetAutomationRecord(scope, "check", "definition", "check", 0)
	if err != nil || !ok || row.Definition.SessionID != "conversation" || repo.writes != 1 {
		t.Fatalf("history changed: %+v %v", row, err)
	}
}

// Purpose: historical definitions omit session_id; adding the optional locator
// must not alter their policy digest through unrelated runtime attribution.
func TestPersistentAutomationPolicyPinsOnlyDefinition(t *testing.T) {
	d := store.AutomationDefinition{Name: "historical"}
	before, _ := ApprovalPolicyDigest(d)
	d.Enabled = true
	d.Authorization.Mode = "approved_policy"
	d.Authorization.ApprovalReference = "grant"
	after, _ := ApprovalPolicyDigest(d)
	if before != after {
		t.Fatal("approval linking changed historical digest")
	}
}
