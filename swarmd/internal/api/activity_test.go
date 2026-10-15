package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: activityMetadata is the narrow serialization boundary; hostile payload
// content must never become transcript/output/argument content in the fleet feed.
func TestActivityMetadataPrivacy(t *testing.T) {
	row := sessionruntime.RealtimeOutboxRecord{EndpointSeq: 1}
	row.Event.EventType = "session.tool.started"
	row.Event.Payload = json.RawMessage(`{"tool_name":"write","arguments":"{\"path\":\"src/a.go\",\"content\":\"SECRET\"}","output":"SECRET","summary":"SECRET","prompt":"SECRET"}`)
	event, ok := activityMetadata(row)
	raw, err := json.Marshal(event)
	if err != nil || !ok || event.FilePath != "src/a.go" || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("unsafe metadata: %s %v", raw, err)
	}
	for _, kind := range []string{"session.tool.completed", "session.tool.failed"} {
		row.Event.EventType = kind
		event, ok := activityMetadata(row)
		raw, err := json.Marshal(event)
		if err != nil || !ok || strings.Contains(string(raw), "SECRET") {
			t.Fatalf("completion leaked content: %s", raw)
		}
	}
	row.Event.EventType = "worker.updated"
	row.Event.Payload = json.RawMessage(`{"activity_kind":"review.recorded","worker_id":"worker","run_id":"run","state":"ok","change_summary":"SECRET","output":"SECRET","reason":"SECRET"}`)
	event, ok = activityMetadata(row)
	raw, err = json.Marshal(event)
	if err != nil || !ok || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("review leaked content: %s", raw)
	}
	row.Event.EventType = "session.message.appended"
	if _, ok := activityMetadata(row); ok {
		t.Fatal("message emitted")
	}
}

// Purpose: handleActivity must enforce the session principal/read boundary and a
// hard stream cap before subscribing; this HTTP layer exercises actual admission.
func TestActivityAdmission(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	request := httptest.NewRequest(http.MethodGet, "/v3/activity/stream", nil)
	rec := httptest.NewRecorder()
	server.handleActivity(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	server.handleActivity(rec, requestWithScopedToken(withTestPrincipal(request), &pebblestore.ScopedTokenRecord{Scopes: []string{"signals:read"}}))
	if rec.Code != http.StatusForbidden || server.activityStreams.Load() != 0 {
		t.Fatalf("missing sessions scope admitted: %d", rec.Code)
	}
	server.activityStreams.Store(activityStreamLimit)
	rec = httptest.NewRecorder()
	server.handleActivity(rec, withTestPrincipal(request))
	if rec.Code != http.StatusTooManyRequests || server.activityStreams.Load() != activityStreamLimit {
		t.Fatalf("cap status %d streams %d", rec.Code, server.activityStreams.Load())
	}
}

// Purpose: activityPage must replay committed outbox rows, reject cross-account
// access, and explicitly mark invalid/expired cursors rather than returning silence.
func TestActivityReplayAndAccountIsolation(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	result := createV3RealtimeTestSessionResult(t, server, "activity-session", "activity-create")
	principal := testPrincipal()
	events, cursor, gap, err := server.activityPage(principal, 0, "", "")
	if err != nil || gap || len(events) != 1 || cursor != result.RealtimeOutbox.EndpointSeq {
		t.Fatalf("replay %+v %d %v %v", events, cursor, gap, err)
	}
	events, next, gap, err := server.activityPage(principal, cursor, "", "")
	if err != nil || gap || len(events) != 0 || next != cursor {
		t.Fatal("resume duplicated event")
	}
	principal.AccountScopeID = "other-account"
	events, _, _, err = server.activityPage(principal, 0, "", "")
	if err != nil || len(events) != 0 {
		t.Fatal("cross-account disclosure")
	}
	_, _, gap, err = server.activityPage(testPrincipal(), cursor+100, "", "")
	if err != nil || !gap {
		t.Fatal("ahead cursor silently accepted")
	}
}

// Purpose: the reused outbox hub must never wait on a slow SSE consumer. This
// narrow hub test fills the exact bounded queue and requires a gap notice.
func TestActivitySlowConsumer(t *testing.T) {
	hub := newV3RealtimeOutboxHub()
	sub := hub.subscribe()
	done := make(chan struct{})
	go func() {
		for i := 1; i <= v3RealtimeSubscriberBufSize+1; i++ {
			hub.publish(sessionruntime.RealtimeOutboxRecord{EndpointSeq: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked")
	}
	select {
	case notice := <-sub.slow:
		if notice.EndpointSeq != v3RealtimeSubscriberBufSize+1 {
			t.Fatal("wrong missing range")
		}
	default:
		t.Fatal("overflow not signaled")
	}
	if len(sub.send) != v3RealtimeSubscriberBufSize {
		t.Fatal("queue not bounded")
	}
}

// Purpose: measure real durable ApplySessionMutation commit-to-SSE delivery in
// process (no provider workload simulation). HTTP parsing is the narrow client boundary.
func TestActivityCommitToClientLatency(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.handleActivity(w, withTestPrincipal(r)) }))
	defer httpServer.Close()
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Get(httpServer.URL + "/v3/activity/stream?cursor=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	measurements := make([]time.Duration, 0, 30)
	for i := 0; i < 30; i++ {
		started := time.Now()
		result := createV3RealtimeTestSessionResult(t, server, fmt.Sprintf("activity-latency-%d", i), fmt.Sprintf("activity-request-%d", i))
		if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
			t.Fatal(err)
		}
		received := false
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				var event activityEvent
				if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) == nil && event.SessionID == result.SessionID {
					received = true
					break
				}
			}
		}
		measurements = append(measurements, time.Since(started))
		if !received {
			t.Fatalf("event not received: %v", scanner.Err())
		}
	}
	sort.Slice(measurements, func(i, j int) bool { return measurements[i] < measurements[j] })
	t.Logf("durable commit-to-client latency samples=%d p50=%s p99=%s", len(measurements), measurements[14], measurements[29])
}

// Purpose: retained-history expiration must produce an explicit HTTP gap and
// close, not leave an authorized client waiting forever on unavailable history.
func TestActivityExpiredCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "expired-activity", "expired-create")
	if err := server.sessions.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{UpdatedAtUnixMs: time.Now().UnixMilli(), OldestRetainedRealtimeEndpointSeq: created.RealtimeOutbox.EndpointSeq + 10, RealtimePrunedThroughEndpointSeq: created.RealtimeOutbox.EndpointSeq + 9}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	server.handleActivity(rec, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/activity/stream?cursor=0", nil)))
	if !strings.Contains(rec.Body.String(), "event: gap\n") || server.activityStreams.Load() != 0 {
		t.Fatalf("missing gap/cleanup: %s", rec.Body.String())
	}
}

// Purpose: an otherwise idle established HTTP stream must emit the specified
// heartbeat without polling or activity producers, and release admission on close.
func TestActivityHeartbeat(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.handleActivity(w, withTestPrincipal(r)) }))
	defer httpServer.Close()
	client := &http.Client{Timeout: 18 * time.Second}
	response, err := client.Get(httpServer.URL + "/v3/activity/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	started := time.Now()
	for scanner.Scan() {
		if scanner.Text() == ": heartbeat" {
			elapsed := time.Since(started)
			if elapsed < 14*time.Second || elapsed > 17*time.Second {
				t.Fatalf("heartbeat timing %s", elapsed)
			}
			return
		}
	}
	t.Fatalf("missing heartbeat: %v", scanner.Err())
}

// activityBlockedWriter blocks only the initial connection flush, allowing a
// deterministic slow-client test without sleeps, kernel buffer-size assumptions,
// or a simulated performance measurement.
type activityBlockedWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (w *activityBlockedWriter) Flush() {
	if !w.once {
		w.once = true
		close(w.entered)
		<-w.release
	}
	w.ResponseRecorder.Flush()
}

// Purpose: handleActivity must turn the bounded hub overflow into a gap frame
// and terminate/release its slot; testing only the hub cannot prove HTTP cleanup.
func TestActivityOverflowClosesStream(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	writer := &activityBlockedWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		server.handleActivity(writer, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/activity/stream", nil)))
		close(done)
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("subscription not established")
	}
	for i := 1; i <= v3RealtimeSubscriberBufSize+1; i++ {
		server.v3RealtimeOutbox.publish(sessionruntime.RealtimeOutboxRecord{EndpointSeq: uint64(i)})
	}
	close(writer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("overflow did not close")
	}
	body := writer.Body.String()
	if !strings.Contains(body, "event: gap\n") || !strings.Contains(body, `"from":1`) || !strings.Contains(body, `"to":257`) || server.activityStreams.Load() != 0 {
		t.Fatalf("bad gap/cleanup %s", body)
	}
}

// Purpose: actual delivery receipt producers must commit replayable metadata,
// remain idempotent, and reject another principal without appending a receipt.
func TestActivityDeliveryProducer(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "delivery-activity", "delivery-create")
	call := &controlMCPCall{server: server, request: withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil))}
	refs := controlMCPJobRefs{sessionID: created.SessionID, projectID: "project"}
	for i := 0; i < 2; i++ {
		if err := call.recordJobActivity(controlMCPJobWorker, "worker", "run", refs, "job.landed", "head"); err != nil {
			t.Fatal(err)
		}
	}
	events, _, gap, err := server.activityPage(testPrincipal(), created.RealtimeOutbox.EndpointSeq, "project", "")
	if err != nil || gap || len(events) != 1 || events[0].State != "job.landed" || events[0].JobID != "worker/worker/run" {
		t.Fatalf("delivery replay %+v %v", events, err)
	}
	refs.sessionID = "not-owned"
	if err := call.recordJobActivity(controlMCPJobWorker, "worker", "run", refs, "job.landed", "head"); err == nil {
		t.Fatal("unowned receipt accepted")
	}
}

// Purpose: MCP since_cursor must use the V3 metadata page, not legacy signals,
// preserve the next cursor, and reject malformed cursors before dispatch.
func TestActivityMCPCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createV3RealtimeTestSessionResult(t, server, "mcp-activity", "mcp-create")
	call := &controlMCPCall{server: server, request: withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), next: http.HandlerFunc(server.handleActivity)}
	call.request = requestWithScopedToken(call.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	result, err := controlMCPActivity(call, map[string]any{"since_cursor": "0"})
	if err != nil {
		t.Fatal(err)
	}
	page := result.(map[string]any)
	if page["cursor"] != "1" || len(page["events"].([]any)) != 1 {
		t.Fatalf("bad MCP page %+v", page)
	}
	if _, err := controlMCPActivity(call, map[string]any{"since_cursor": "bad"}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

// Purpose: durable membership must retain filter identities across session and
// tool envelopes; the HTTP stream proves cursor advancement cannot hide either.
func TestActivityFilteredSessionAndToolStream(t *testing.T) {
	s, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	_, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: "filtered", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationCreateSession, ClientRequestID: "filtered-create", IdempotencyKey: "filtered-create", PayloadHash: "create", Session: &pebblestore.SessionSnapshot{ID: "filtered", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Metadata: map[string]any{"project_id": "project", "worker_id": "worker", "worker_name": "Worker", "worker_run_id": "run", "job_id": "job"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: "filtered", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationRecordDiagnostic, EventType: "session.tool.started", EventPayload: json.RawMessage(`{"tool_name":"read"}`), ClientRequestID: "filtered-tool", IdempotencyKey: "filtered-tool", PayloadHash: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleActivity(w, withTestPrincipal(r)) }))
	defer httpServer.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(httpServer.URL + "/v3/activity/stream?project=project&worker=worker")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	kinds := []string{}
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event activityEvent
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil || event.Kind == "" {
			continue
		}
		if event.ProjectID != "project" || event.WorkerID != "worker" || event.WorkerName != "Worker" || event.JobID != "worker/worker/run" {
			t.Fatalf("lost identity %+v", event)
		}
		kinds = append(kinds, event.Kind)
		if len(kinds) == 2 {
			break
		}
	}
	if strings.Join(kinds, ",") != "session.created,session.tool.started" {
		t.Fatalf("filtered events %v: %v", kinds, scanner.Err())
	}
}

// Purpose: admission replay must use durable run ownership before its session
// exists, but never bypass existing-session ACLs or disclose to another owner.
func TestActivityPreSessionAdmissionReplay(t *testing.T) {
	s, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	ws := s.sessions.Store().WorkerStore()
	worker, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Activity", WorkspaceRequirements: []pebblestore.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = ws.ActivateWorker(p.AccountScopeID, p.UserID, worker.ID, worker.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.AdmitWorkerRun(p.AccountScopeID, pebblestore.WorkerRunAdmission{WorkerID: worker.ID, UserID: p.UserID, RequestSource: "direct", IdempotencyKey: "activity-admit", Input: map[string]any{"prompt": "private"}})
	if err != nil {
		t.Fatal(err)
	}
	events, next, gap, err := s.activityPage(p, cursor, "", worker.ID)
	if err != nil || gap || len(events) != 1 || events[0].SessionID != run.SessionID || next <= cursor {
		t.Fatalf("lost admission %+v %v", events, err)
	}
	foreign := p
	foreign.UserID = "foreign"
	events, _, _, err = s.activityPage(foreign, cursor, "", worker.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("foreign disclosure %+v %v", events, err)
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: run.SessionID, UserID: foreign.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationCreateSession, ClientRequestID: "foreign-create", IdempotencyKey: "foreign-create", PayloadHash: "foreign", Session: &pebblestore.SessionSnapshot{ID: run.SessionID, UserID: foreign.UserID, AccountScopeID: p.AccountScopeID}})
	if err != nil {
		t.Fatal(err)
	}
	events, _, _, err = s.activityPage(p, cursor, "", worker.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("session ACL bypass %+v %v", events, err)
	}
}

// Purpose: project-filtered SSE must retain admission and review receipts when
// input omits project_id. The execution resolver is tested in run; this HTTP
// boundary verifies its pinned admission result survives both durable producers.
func TestActivityResolvedProjectAdmissionAndReview(t *testing.T) {
	s, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	ws := s.sessions.Store().WorkerStore()
	worker, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Activity", WorkspaceRequirements: []pebblestore.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = ws.ActivateWorker(p.AccountScopeID, p.UserID, worker.ID, worker.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.AdmitWorkerRun(p.AccountScopeID, pebblestore.WorkerRunAdmission{WorkerID: worker.ID, UserID: p.UserID, RequestSource: "direct", IdempotencyKey: "resolved-project", ResolvedProjectID: "project", Input: map[string]any{"prompt": "private"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Input["project_id"] != nil {
		t.Fatal("fixture unexpectedly supplied project input")
	}
	reviewer, err := ws.RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{WorkerID: worker.ID, Role: pebblestore.WorkerRoleReviewer, UserID: p.UserID, WorkspaceID: run.WorkspaceID, Input: map[string]any{"review_job": "worker/" + worker.ID + "/" + run.ID}, Status: "succeeded", RequestSource: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.RecordJobReview(p.AccountScopeID, worker.ID, run.ID, pebblestore.JobReview{Head: "head", RecordedAt: 1, Verdict: "ok", ReviewerJobID: "worker/" + worker.ID + "/" + reviewer.ID}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleActivity(w, withTestPrincipal(r)) }))
	defer httpServer.Close()
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(httpServer.URL + "/v3/activity/stream?project=project")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	kinds := []string{}
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event activityEvent
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil || event.Kind == "" {
			continue
		}
		if event.ProjectID != "project" || event.JobID != "worker/"+worker.ID+"/"+run.ID {
			t.Fatalf("incorrect identity %+v", event)
		}
		kinds = append(kinds, event.Kind)
		if len(kinds) == 2 {
			break
		}
	}
	if strings.Join(kinds, ",") != "job.started,review.recorded" {
		t.Fatalf("lost filtered receipts %v: %v", kinds, scanner.Err())
	}
}
