package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: the Companion can change the work it delegates. Requirement: a
// follow-up (manage_projects message_task) whose origin is the account user's
// current Companion is admitted for a task of any existing project of the
// account, by the same rule as the Companion's task watches
// (companionWatchesProject), and records the Companion as the task's origin;
// an origin that is neither the task's project conversation nor the current
// Companion (an unmarked chat, a session with a copied marker, the Companion
// after the person deleted it, a missing session) is refused with the
// existing reason and changes nothing. Threat: the Companion's contract tells
// it to send a change to the exact task, but the store refused every
// follow-up from a non-project session ("follow-up origin conversation
// unavailable"), so revisions failed; widening that rule to arbitrary
// sessions or forged markers. Boundary:
// SessionStore.ReserveTaskFollowupWithRepositories,
// companionWatchesProject/IsCurrentCompanion. Narrowest layer: the store
// reservation every follow-up path (native tool, HTTP, linked) goes through.
func TestCompanionSendsFollowUpsToItsDelegatedTasks(t *testing.T) {
	f, _ := newSettledWaitFixture(t, filepath.Join(t.TempDir(), "store"), "task")
	identity, _, err := f.db.EnsureCompanion("account", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	companion := identity.SessionID
	f.companionWatchSession(companion, "c-run", true)
	f.companionWatchSession("plain", "p-run", false)
	f.companionWatchSession("forged", "f-run", true)
	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	before, _, _ := f.db.GetProjectTask("account", "project", "task")

	for i, origin := range []string{"plain", "forged", "missing-session"} {
		_, err := f.db.ReserveTaskFollowupWithRepositories("account", "project", "task", "user", "refused-"+origin, "Revise it.", int(before.Revision), int64(10+i), nil, nil, origin)
		if err == nil || !strings.Contains(err.Error(), "follow-up origin conversation unavailable") {
			t.Fatalf("origin %s: %v, want the origin refusal", origin, err)
		}
	}
	if after, _, _ := f.db.GetProjectTask("account", "project", "task"); after.Revision != before.Revision || len(after.Attempts) != len(before.Attempts) || after.OriginSessionID != before.OriginSessionID {
		t.Fatalf("a refused follow-up changed the task: %+v", after)
	}

	task, err := f.db.ReserveTaskFollowupWithRepositories("account", "project", "task", "user", "companion-1", "Revise it.", int(before.Revision), 20, nil, nil, companion)
	if err != nil {
		t.Fatalf("Companion follow-up refused: %v", err)
	}
	if task.OriginSessionID != companion || task.Status != "in_progress" || task.ActiveAttemptID == before.ActiveAttemptID {
		t.Fatalf("Companion follow-up: origin %s status %s attempt %s", task.OriginSessionID, task.Status, task.ActiveAttemptID)
	}

	// After the person deletes the Companion, its old session no longer
	// qualifies.
	if _, err := f.db.RetireCompanion("account", "user", companion, 30); err != nil {
		t.Fatal(err)
	}
	current, _, _ := f.db.GetProjectTask("account", "project", "task")
	if _, err := f.db.ReserveTaskFollowupWithRepositories("account", "project", "task", "user", "retired-1", "Again.", int(current.Revision), 40, nil, nil, companion); err == nil || !strings.Contains(err.Error(), "follow-up origin conversation unavailable") {
		t.Fatalf("retired Companion follow-up: %v", err)
	}
}
