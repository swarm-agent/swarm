package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: projects-only usage worksets must receive committed scope totals and
// policy invalidations, never session discovery/projections/transcripts. Owners:
// v3RealtimeProcessOutboxRecord, v3RealtimeCatchUpEndpointCursor and
// usageScopesForRealtimeSubscriptions. Temporary Pebble plus bounded local test
// sockets is the narrowest layer proving identical live/replay wire selection.
func TestUsageScopeProjectsOnlyLiveAndReplayDelivery(t *testing.T) {
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureAutomationRealtime(db)
	created := createV3RealtimeTestSessionResult(t, server, "usage-chat", "usage-create")
	p := testPrincipal()
	snapshot := *created.Session
	snapshot.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	if err := db.PutJSON(pebblestore.KeySession(snapshot.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyProjectTask(p.AccountScopeID, "project", "task"), pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, SessionID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyWorker(p.AccountScopeID, "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: p.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer func() { conn.Close() }()
	workset := v3RealtimeGlobalWorksetRequestForTest()
	workset.Resources = []string{"projects"}
	workset.AutoSubscribeSessions = false
	resume := func(cursor uint64) {
		writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, cursor), Worksets: []V3RealtimeWorksetSubscriptionRequest{workset}})
	}
	readResource := func(kind string) V3RealtimeMessage {
		t.Helper()
		for i := 0; i < 8; i++ {
			frame := readV3RealtimeFrame(t, conn)
			if frame.SessionID != "" || frame.Session != nil || frame.Projection != nil || frame.CurrentRunState != nil || frame.ActivePlan != nil {
				t.Fatalf("unrequested session resource leaked: %+v", frame)
			}
			if frame.Kind == V3RealtimeKindEndpointWatermark && kind != V3RealtimeKindEndpointWatermark {
				continue
			}
			if frame.Kind != kind {
				t.Fatalf("unrequested frame: %+v, want %s", frame, kind)
			}
			return frame
		}
		t.Fatalf("missing %s", kind)
		return V3RealtimeMessage{}
	}
	resume(created.RealtimeOutbox.EndpointSeq - 1)
	readResource(V3RealtimeKindEndpointWatermark)
	appendV3RealtimeTestMessage(t, server, snapshot.ID, "private-message", "private transcript sentinel")
	usage, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		IdempotencyKey: "usage-receipt", PayloadHash: "usage-receipt", Kind: sessionruntime.SessionMutationRecordUsage,
		EventType: "run.usage.updated", NowUnixMs: 3000,
		TurnUsage: &pebblestore.SessionTurnUsageSnapshot{RunID: "receipt", Provider: "fixture", Model: "fixture", TotalTokens: 7, BilledTokens: 40, PriceStatus: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.RealtimeOutbox == nil || len(usageScopesFromRealtimeRecord(*usage.RealtimeOutbox)) != 1 {
		t.Fatal("receipt did not commit its canonical usage scope")
	}
	assertUsage := func() {
		t.Helper()
		frame := readResource(V3RealtimeKindUsageScopeUpdated)
		assertV3RealtimeSignedCursorSeq(t, server, frame.EndpointCursor, usage.RealtimeOutbox.EndpointSeq)
		var payload struct {
			ScopeTotals []pebblestore.UsageScopeTotal `json:"scope_totals"`
		}
		if frame.Event == nil || json.Unmarshal(frame.Event.Payload, &payload) != nil || len(payload.ScopeTotals) != 1 || payload.ScopeTotals[0].Kind != "task" || payload.ScopeTotals[0].ID != "task" || payload.ScopeTotals[0].TotalTokens != 40 {
			t.Fatalf("missing committed bounded total: %+v", frame)
		}
		raw, err := json.Marshal(frame)
		if err != nil || strings.Contains(string(raw), "private transcript sentinel") || strings.Contains(string(raw), "turn_usage") || strings.Contains(string(raw), "fixture") {
			t.Fatalf("private receipt/transcript leaked: %s %v", raw, err)
		}
	}
	assertUsage()
	// Store-backed selection rejects foreign receipts and unmatched selectors
	// without changing the durable task total.
	record := *usage.RealtimeOutbox
	for _, field := range []string{"account", "principal"} {
		foreign := record
		if field == "account" {
			foreign.AccountScopeID = "foreign-account"
		} else {
			foreign.UserID = "foreign-user"
		}
		if totals := server.usageScopesForRealtimeSubscriptions(p, foreign, nil, map[string]v3RealtimeWorksetSubscription{"usage": {Resources: []string{"projects"}, Selector: workset.Selector}}); len(totals) != 0 {
			t.Fatalf("foreign %s receipt selected: %+v", field, totals)
		}
	}
	for _, selector := range []V3RealtimeWorksetSelector{{Kind: "workspace", WorkspacePath: "/workspace/other"}, {Kind: "session_ids", SessionIDs: []string{"other-session"}}} {
		if totals := server.usageScopesForRealtimeSubscriptions(p, record, nil, map[string]v3RealtimeWorksetSubscription{"usage": {Resources: []string{"projects"}, Selector: selector}}); len(totals) != 0 {
			t.Fatalf("unselected receipt admitted: %+v", totals)
		}
	}
	total, found, err := server.sessions.Store().GetUsageScope(p.AccountScopeID, "task", "project", "task")
	if err != nil || !found || total.TotalTokens != 40 || total.ReceiptCount != 1 {
		t.Fatalf("selection changed durable usage: %+v %v", total, err)
	}
	if _, err := server.sessions.Store().SetWorkerBudget(p.AccountScopeID, "worker", 0, 1, 100, p.UserID); err != nil {
		t.Fatal(err)
	}
	policyFrame := readResource(V3RealtimeKindWorkerChanged)
	if policyFrame.Event != nil || policyFrame.EndpointCursor == "" {
		t.Fatalf("policy invalidation leaked content: %+v", policyFrame)
	}
	conn.Close()
	conn = dialV3RealtimeStream(t, httpServer.URL)
	resume(created.RealtimeOutbox.EndpointSeq)
	assertUsage()
	replayedPolicy := readResource(V3RealtimeKindWorkerChanged)
	policySeq, _, err := server.parseV3SyncEndpointCursor(policyFrame.EndpointCursor, v3SyncCursorScopeForRealtime(p, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	assertV3RealtimeSignedCursorSeq(t, server, replayedPolicy.EndpointCursor, policySeq)
	if replayedPolicy.Event != nil {
		t.Fatalf("policy replay changed or leaked: %+v", replayedPolicy)
	}
}

// Purpose: explicit resource-only scopes cannot auto-subscribe chat, even on
// resume classification; session/sidebar resources retain their contract.
// Owners: v3RealtimeWorksetIncludesRecordResource and
// v3RealtimeMatchedWorksetIDsForSnapshot. Pure boundary tests are sufficient for
// resource classification, with delivery independently tested above.
func TestUsageScopeResourceOnlySessionClassification(t *testing.T) {
	p := testPrincipal()
	snapshot := pebblestore.SessionSnapshot{ID: "session", UserID: p.UserID, AccountScopeID: p.AccountScopeID}
	for _, resources := range [][]string{{"projects"}, {"auth"}, {"notifications"}, {"tasks"}, {"permission_summaries"}, {"projects", "tasks"}} {
		workset := v3RealtimeWorksetSubscription{WorksetID: "resource", Resources: resources, Selector: V3RealtimeWorksetSelector{Kind: "global", Global: true}, AutoSubscribeSessions: true}
		for _, eventType := range []string{"session.created", "session.message.appended", "session.archived", "session.deleted", "run.usage.updated", "session.plan.saved"} {
			if v3RealtimeWorksetIncludesRecordResource(workset, sessionruntime.RealtimeOutboxRecord{Event: pebblestore.V3SessionEvent{EventType: eventType}}) {
				t.Fatalf("%v admitted %s", resources, eventType)
			}
		}
		if ids, auto := v3RealtimeMatchedWorksetIDsForSnapshot(p, snapshot, map[string]v3RealtimeWorksetSubscription{"resource": workset}); len(ids) != 0 || auto {
			t.Fatalf("resource-only auto subscription: %v %v", ids, auto)
		}
	}
	for _, resource := range []string{"", "sessions", "projections", "events", "messages", "current_run_state", "active_plan", "membership", "tombstones"} {
		workset := v3RealtimeWorksetSubscription{WorksetID: "session", Selector: V3RealtimeWorksetSelector{Kind: "global", Global: true}, AutoSubscribeSessions: true}
		if resource != "" {
			workset.Resources = []string{resource}
		}
		if !v3RealtimeWorksetIncludesRecordResource(workset, sessionruntime.RealtimeOutboxRecord{Event: pebblestore.V3SessionEvent{EventType: "session.message.appended"}}) {
			t.Fatalf("session resource %q rejected", resource)
		}
		if ids, auto := v3RealtimeMatchedWorksetIDsForSnapshot(p, snapshot, map[string]v3RealtimeWorksetSubscription{"session": workset}); len(ids) != 1 || !auto {
			t.Fatalf("session subscription lost: %v %v", ids, auto)
		}
	}
}

// Purpose: usage selectors retain principal/account isolation, hidden resource
// linkage and 320-element UI compatibility without authorizing other scopes.
// Owners: usageScopeSubscriptionMatches/canonicalV3RealtimeWorksetSelector.
// This boundary matrix isolates selector behavior from receipt persistence.
func TestUsageScopeSelectorsIsolationAndCompatibility(t *testing.T) {
	p := testPrincipal()
	snapshot := pebblestore.SessionSnapshot{ID: "session", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: "/workspace/usage", Metadata: map[string]any{"navigation_hidden": true}}
	for _, selector := range []V3RealtimeWorksetSelector{{Kind: "global", Global: true}, {Kind: "workspace", WorkspacePath: snapshot.WorkspacePath}, {Kind: "session_ids", SessionIDs: []string{snapshot.ID}}} {
		worksets := map[string]v3RealtimeWorksetSubscription{"usage": {Resources: []string{"projects"}, Selector: selector}}
		if !usageScopeSubscriptionMatches(p, snapshot, "projects", nil, worksets) {
			t.Fatalf("hidden linked resource rejected: %+v", selector)
		}
		foreign := snapshot
		foreign.AccountScopeID = "foreign-account"
		if usageScopeSubscriptionMatches(p, foreign, "projects", nil, worksets) {
			t.Fatal("foreign account admitted")
		}
		foreign = snapshot
		foreign.UserID = "foreign-user"
		if usageScopeSubscriptionMatches(p, foreign, "projects", nil, worksets) {
			t.Fatal("foreign principal admitted")
		}
	}
	for _, selector := range []V3RealtimeWorksetSelector{{Kind: "workspace", WorkspacePath: "/workspace/other"}, {Kind: "session_ids", SessionIDs: []string{"other-session"}}} {
		if usageScopeSubscriptionMatches(p, snapshot, "projects", nil, map[string]v3RealtimeWorksetSubscription{"usage": {Resources: []string{"projects"}, Selector: selector}}) {
			t.Fatalf("unselected scope admitted: %+v", selector)
		}
	}
	for _, selector := range []V3RealtimeWorksetSelector{{Kind: "global", SessionIDs: []string{"session"}}, {Kind: "workspace", WorkspacePath: "relative"}, {Kind: "session_ids", SessionIDs: []string{"session"}, WorkspacePath: snapshot.WorkspacePath}} {
		if _, err := canonicalV3RealtimeWorksetSelector(selector); err == nil {
			t.Fatalf("invalid selector accepted: %+v", selector)
		}
	}
	ids, paths := make([]string, 320), make([]string, 320)
	for i := range ids {
		ids[i], paths[i] = fmt.Sprintf("session-%03d", i), fmt.Sprintf("/workspace/%03d", i)
	}
	for _, selector := range []V3RealtimeWorksetSelector{{Kind: "session_ids", SessionIDs: ids}, {Kind: "workspace", WorkspacePaths: paths}} {
		canonical, err := canonicalV3RealtimeWorksetSelector(selector)
		if err != nil || len(canonical.SessionIDs)+len(canonical.WorkspacePaths) != 320 {
			t.Fatalf("320-element selector truncated/rejected: %+v %v", canonical, err)
		}
	}
}

// Purpose: excluding projects-only chat must not suppress explicit session
// events. Owner v3RealtimeProcessOutboxRecord; store-backed replay is the
// narrowest wire layer proving the positive session-delivery counterpart.
func TestUsageScopeExplicitSessionReplayRetained(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "explicit-usage-chat", "explicit-usage-create")
	message := appendV3RealtimeTestMessage(t, server, created.SessionID, "explicit-message", "requested transcript")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	workset := v3RealtimeGlobalWorksetRequestForTest()
	workset.Resources = []string{"projects"}
	workset.AutoSubscribeSessions = false
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{
		Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume,
		EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq),
		Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "explicit"}},
		Worksets: []V3RealtimeWorksetSubscriptionRequest{workset},
	})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindEvent, created.SessionID, message.Event.Seq)
	if frame.Event.EventType != "session.message.appended" || !strings.Contains(string(frame.Event.Payload), "requested transcript") {
		t.Fatalf("explicit session content suppressed: %+v", frame)
	}
	assertV3RealtimeSignedCursorSeq(t, server, frame.EndpointCursor, message.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, message.Event.Seq)
}
