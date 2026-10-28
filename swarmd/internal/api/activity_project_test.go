package api

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a machine's activity feed reports that a project changed
// (project.updated: a task write, a workflow delivery transition, a job-end
// evaluation), so a linked wait following the feed reads the task again when
// the machine's workflow ends without session activity.
// Requirement: the record is shown as metadata only (kind, project_id,
// state) to an explicit user of the project's account, as the project routes
// allow; it is not shown to another account, to a non-user principal, or when
// project records are not admitted (an application gateway). A payload whose
// project is not the record's own project session, and the project order
// record (no project), are not activity. Threat: a linked wait never woken
// after a run-step/blocked/failed workflow end; project IDs or payload content
// leaking across accounts or to a project-confined gateway. Authority:
// activityPage / projectActivityMetadata / authorizeActivityEvent over the
// real outbox of a temporary store; the narrowest layer that owns both the
// feed's filtering and its authorization.
func TestActivityFeedShowsProjectUpdatesToTheAccount(t *testing.T) {
	f := newFleetTestSwarm(t, "local")
	t.Cleanup(f.cleanup)
	db := f.s.sessions.Store()
	if err := db.PutProject(f.account, &pebblestore.ProjectRecord{ID: "Proj-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	head, err := f.s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.NotifyProjectTaskChanged(f.account, "Proj-1", "task-1"); err != nil {
		t.Fatal(err)
	}
	owner := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.user, AccountScopeID: f.account}
	projectEvents := func(p identity.Principal, projects bool) []activityEvent {
		t.Helper()
		events, _, gap, err := f.s.activityPage(p, head, "", "", projects)
		if err != nil || gap {
			t.Fatalf("page: gap=%v err=%v", gap, err)
		}
		out := []activityEvent{}
		for _, e := range events {
			if e.Kind == pebblestore.ProjectUpdatedEventType {
				out = append(out, e)
			}
		}
		return out
	}
	got := projectEvents(owner, true)
	if len(got) != 1 || got[0].ProjectID != "Proj-1" || got[0].State != "changed" || got[0].SessionID != "" || got[0].Summary != pebblestore.ProjectUpdatedEventType {
		t.Fatalf("owner's project activity: %+v", got)
	}
	// Another explicit user of the same account reads the account's projects.
	if got := projectEvents(identity.Principal{Type: identity.PrincipalTypeUser, UserID: "someone-else", AccountScopeID: f.account}, true); len(got) != 1 {
		t.Fatalf("same-account user: %+v", got)
	}
	for name, read := range map[string]func() []activityEvent{
		"another account": func() []activityEvent {
			return projectEvents(identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.user, AccountScopeID: "other-account"}, true)
		},
		"not a user": func() []activityEvent {
			return projectEvents(identity.Principal{Type: "service", UserID: f.user, AccountScopeID: f.account}, true)
		},
		"projects not admitted": func() []activityEvent { return projectEvents(owner, false) },
	} {
		if got := read(); len(got) != 0 {
			t.Fatalf("%s saw project activity: %+v", name, got)
		}
	}
	// The project order record carries no project: not activity.
	if err := db.SetProjectOrder(f.account, []string{"Proj-1"}); err != nil {
		t.Fatal(err)
	}
	if got := projectEvents(owner, true); len(got) != 1 {
		t.Fatalf("order record shown as project activity: %+v", got)
	}
	// A payload naming a project other than the record's own session.
	forged := pebblestore.V3RealtimeOutboxRecord{AccountScopeID: f.account, SessionID: pebblestore.ProjectRealtimeSessionID(f.account, "Proj-1"), Event: pebblestore.V3SessionEvent{EventType: pebblestore.ProjectUpdatedEventType}}
	forged.Event.Payload, _ = json.Marshal(map[string]any{"project_id": "proj-2"})
	if _, ok := activityMetadata(forged); ok {
		t.Fatal("payload bound to another project's session accepted")
	}
	forged.Event.Payload, _ = json.Marshal(map[string]any{"project_id": "Proj-1", "summary": "secret"})
	if event, ok := activityMetadata(forged); !ok || event.ProjectID != "Proj-1" || event.Summary != pebblestore.ProjectUpdatedEventType {
		t.Fatalf("bound payload: %+v %v", event, ok)
	}
}
