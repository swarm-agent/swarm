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
	measurements := make([]time.Duration, 0, 30)
	for i := 0; i < 30; i++ {
		started := time.Now()
		result := createV3RealtimeTestSessionResult(t, server, fmt.Sprintf("activity-latency-%d", i), fmt.Sprintf("activity-request-%d", i))
		response, err := client.Get(fmt.Sprintf("%s/v3/activity/stream?cursor=%d", httpServer.URL, result.RealtimeOutbox.EndpointSeq-1))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(response.Body)
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
		response.Body.Close()
		if !received {
			t.Fatalf("event not received: %v", scanner.Err())
		}
	}
	sort.Slice(measurements, func(i, j int) bool { return measurements[i] < measurements[j] })
	t.Logf("durable commit-to-client latency samples=%d p50=%s p99=%s", len(measurements), measurements[14], measurements[29])
}
