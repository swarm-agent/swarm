package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: continuity is maintained automatically through the daemon's real
// paths and its freshness is visible on the supported state read.
// Requirement: the person's messages posted through POST
// /v3/sessions/{id}/messages and the Companion's replies written by the V3
// executor (a scripted provider, no live model) are folded by background
// maintenance (the commit nudge, as wired on the daemon), with no
// manage_companion record call and no explicit maintenance call, into
// continuity: once the draft reaches CompanionSegmentMaxItems items, GET
// /v3/companion/state lists a sealed segment whose items are the person's
// requests (authority person) and the replies about them, the open draft is
// the rest, and maintenance reads current, not running, with no failure.
// Forgetting a request through DELETE /v3/companion/context removes its ask
// and the reply about it from the sealed segment at a new segment revision
// on the next read. Account memory is unchanged. Threat: continuity that
// only appears through manual saving, a state read that hides staleness, or
// a forget that leaves the forgotten request in the maintained summary.
// Boundary: the message route, sessionV3Executor, the session commit
// observer, MaintainCompanionState, companionStateRead,
// companionMaintenanceStatus, companionContextForget,
// propagateCompanionRecordChange. Narrowest layer: the daemon's HTTP
// handlers and executor over a real Pebble store.
func TestCompanionContinuityMaintainedAutomaticallyAndForgetVisibleOnStateRead(t *testing.T) {
	f := newCompanionRunFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "Understood, I will look into that.", StopReason: "stop"}
	})
	store := f.sessions.Store()
	f.server.ConfigureCompanionMaintenance(store.Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	principal := testPrincipal()
	memoryBefore, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	topics := []string{"laptop backup drive", "printer wifi drops", "router firmware update", "calendar invites", "photo library duplicates", "keyboard shortcuts", "battery calibration", "email filters", "vpn reconnects"}
	for i, topic := range topics {
		f.say(f.sessionID, fmt.Sprintf("continuity-%d", i), fmt.Sprintf("Please sort out the %s problem for me", topic))
	}
	view := companionStateNow(t, f.confirmationFixture, f.sessionID)
	if view.Maintenance.Freshness != "current" || view.Maintenance.Running || view.Maintenance.LastFailure != nil || view.Pending != 0 {
		t.Fatalf("maintenance = %+v pending %d", view.Maintenance, view.Pending)
	}
	if len(view.Segments) != 1 || len(view.Segments[0].Items) != pebblestore.CompanionSegmentMaxItems || view.State.Continuity.Sealed != 1 {
		t.Fatalf("segments = %+v, continuity %+v", view.Segments, view.State.Continuity)
	}
	seg := view.Segments[0]
	first := seg.Items[0]
	if first.Kind != pebblestore.CompanionItemAsk || first.Authority != pebblestore.CompanionAuthorityPerson || !strings.Contains(first.Text, topics[0]) || first.RecordID == "" {
		t.Fatalf("first item = %+v", first)
	}
	if reply := seg.Items[1]; reply.Kind != pebblestore.CompanionItemReply || !reflect.DeepEqual(reply.About, []string{first.RecordID}) {
		t.Fatalf("reply item = %+v", reply)
	}
	if open := view.State.Continuity.Open; len(open) != 2*len(topics)-pebblestore.CompanionSegmentMaxItems {
		t.Fatalf("open draft = %+v", open)
	}

	record, found, err := store.GetCompanionContext(principal.AccountScopeID, principal.UserID, first.RecordID)
	if err != nil || !found {
		t.Fatalf("first request's topic: %v %v", found, err)
	}
	body, _ := json.Marshal(companionContextDeleteRequest{ID: record.ID, ExpectedRevision: record.Revision})
	if code, out := f.do(http.MethodDelete, CompanionContextPath, string(body)); code != http.StatusOK {
		t.Fatalf("forget: %d %s", code, out)
	}
	after := companionStateNow(t, f.confirmationFixture, f.sessionID)
	if len(after.Segments) != 1 {
		t.Fatalf("segments after forget = %+v", after.Segments)
	}
	got := after.Segments[0]
	if got.Revision != seg.Revision+1 || got.Removed != 2 || len(got.Items) != len(seg.Items)-2 {
		t.Fatalf("segment after forget: revision %d removed %d items %d", got.Revision, got.Removed, len(got.Items))
	}
	raw, _ := json.Marshal(after)
	if strings.Contains(string(raw), topics[0]) {
		t.Fatalf("the forgotten request is still on the state read: %s", raw)
	}
	memoryAfter, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memoryBefore, memoryAfter) {
		t.Fatal("continuity maintenance changed explicit account memory")
	}
}
