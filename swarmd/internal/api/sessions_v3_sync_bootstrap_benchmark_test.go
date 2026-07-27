package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	v3SyncCheckpoint0TotalSessions      = 5000
	v3SyncCheckpoint0RecentSessions     = 50
	v3SyncCheckpoint0ActiveSessions     = 10
	v3SyncCheckpoint0HydrateMessages    = 200
	v3SyncCheckpoint0WorkspacePath      = "/workspace/v3-sync-checkpoint-0"
	v3SyncCheckpoint0HistoricalSessions = v3SyncCheckpoint0TotalSessions - v3SyncCheckpoint0RecentSessions - v3SyncCheckpoint0ActiveSessions
)

type v3SyncCheckpoint0BenchmarkFixture struct {
	server         *Server
	sessionSvc     *sessionruntime.Service
	principal      identity.Principal
	hydrateSession string
	activeIDs      []string
	recentIDs      []string
}

func BenchmarkV3SyncBootstrapRecent50WithActiveIndex(b *testing.B) {
	fixture := newV3SyncCheckpoint0BenchmarkFixture(b)
	body := []byte(`{"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":50}},"history":{"mode":"none"},"resources":{"current_run_state":true},"include_active":true}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload, bodyBytes := postV3SyncCheckpoint0Snapshot(b, fixture.server, V3SyncBootstrapPath, body)
		if len(payload.SessionsByID) != v3SyncCheckpoint0RecentSessions+v3SyncCheckpoint0ActiveSessions {
			b.Fatalf("bootstrap selected sessions = %d, want %d", len(payload.SessionsByID), v3SyncCheckpoint0RecentSessions+v3SyncCheckpoint0ActiveSessions)
		}
		if len(payload.ActiveSessionIDs) != v3SyncCheckpoint0ActiveSessions {
			b.Fatalf("active_session_ids = %d, want %d: %+v", len(payload.ActiveSessionIDs), v3SyncCheckpoint0ActiveSessions, payload.ActiveSessionIDs)
		}
		if countV3SyncCheckpoint0RunIntents(payload.RunIntentsBySession) != 0 {
			b.Fatalf("bootstrap emitted historical run intents without explicit run_intents resource: %+v", payload.RunIntentsBySession)
		}
		b.ReportMetric(float64(bodyBytes), "response_bytes/op")
		b.ReportMetric(float64(len(payload.SessionsByID)), "sessions/op")
		b.ReportMetric(float64(len(payload.ActiveSessionIDs)), "active_sessions/op")
	}
}

func BenchmarkV3SyncHydrateTail200(b *testing.B) {
	fixture := newV3SyncCheckpoint0BenchmarkFixture(b)
	body := []byte(fmt.Sprintf(`{"surface":"desktop","session_ids":[%q],"history":{"mode":"tail","max_messages_per_session":200,"manifest_policy":"manifest"},"resources":{"messages":true,"run_intents":true,"current_run_state":true,"session_view":true},"include_active":true}`, fixture.hydrateSession))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload, bodyBytes := postV3SyncCheckpoint0Snapshot(b, fixture.server, V3SyncHydratePath, body)
		messages := payload.MessagesBySession[fixture.hydrateSession]
		if len(messages) != v3SyncCheckpoint0HydrateMessages {
			b.Fatalf("hydrate messages = %d, want %d", len(messages), v3SyncCheckpoint0HydrateMessages)
		}
		if payload.SessionsByID[fixture.hydrateSession].ID != fixture.hydrateSession {
			b.Fatalf("hydrate missing requested session: %+v", payload.SessionsByID)
		}
		if view, ok := payload.SessionViewsByID[fixture.hydrateSession]; !ok || strings.TrimSpace(view.AgenticSettings.Mode) == "" {
			b.Fatalf("hydrate missing session view for requested session: %+v", payload.SessionViewsByID)
		}
		b.ReportMetric(float64(bodyBytes), "response_bytes/op")
		b.ReportMetric(float64(len(messages)), "messages/op")
		b.ReportMetric(float64(countV3SyncCheckpoint0RunIntents(payload.RunIntentsBySession)), "run_intents/op")
		b.ReportMetric(float64(len(payload.SessionViewsByID)), "session_views/op")
	}
}

func BenchmarkV3ListActiveRunStatesFromIndex(b *testing.B) {
	fixture := newV3SyncCheckpoint0BenchmarkFixture(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		states, err := fixture.sessionSvc.ListActiveSessionRunStates(fixture.principal.AccountScopeID, 0)
		if err != nil {
			b.Fatalf("list active run states: %v", err)
		}
		if len(states) != v3SyncCheckpoint0ActiveSessions {
			b.Fatalf("active run states = %d, want %d: %+v", len(states), v3SyncCheckpoint0ActiveSessions, states)
		}
		b.ReportMetric(float64(len(states)), "active_states/op")
	}
}

func newV3SyncCheckpoint0BenchmarkFixture(tb testing.TB) v3SyncCheckpoint0BenchmarkFixture {
	tb.Helper()
	server, sessionSvc := newSessionsV3PrimaryLoadBenchmarkServer(tb)
	principal := testPrincipal()
	fixture := v3SyncCheckpoint0BenchmarkFixture{
		server:     server,
		sessionSvc: sessionSvc,
		principal:  principal,
		activeIDs:  make([]string, 0, v3SyncCheckpoint0ActiveSessions),
		recentIDs:  make([]string, 0, v3SyncCheckpoint0RecentSessions),
	}

	baseMs := int64(1_900_000_000_000)
	for i := 0; i < v3SyncCheckpoint0HistoricalSessions; i++ {
		sessionID := fmt.Sprintf("v3-sync-cp0-historical-%04d", i+1)
		seedV3SyncCheckpoint0Session(tb, sessionSvc, principal, sessionID, fmt.Sprintf("V3 Sync CP0 Historical %04d", i+1), baseMs+int64(i), 0, false)
	}

	for i := 0; i < v3SyncCheckpoint0ActiveSessions; i++ {
		sessionID := fmt.Sprintf("v3-sync-cp0-active-%02d", i+1)
		seedV3SyncCheckpoint0Session(tb, sessionSvc, principal, sessionID, fmt.Sprintf("V3 Sync CP0 Active %02d", i+1), baseMs+100_000+int64(i), 0, true)
		fixture.activeIDs = append(fixture.activeIDs, sessionID)
	}

	recentBaseMs := baseMs + 10_000_000_000
	for i := 0; i < v3SyncCheckpoint0RecentSessions; i++ {
		sessionID := fmt.Sprintf("v3-sync-cp0-recent-%02d", i+1)
		messageCount := 0
		if i == 0 {
			fixture.hydrateSession = sessionID
			messageCount = v3SyncCheckpoint0HydrateMessages
		}
		seedV3SyncCheckpoint0Session(tb, sessionSvc, principal, sessionID, fmt.Sprintf("V3 Sync CP0 Recent %02d", i+1), recentBaseMs+int64(i)*10_000, messageCount, false)
		fixture.recentIDs = append(fixture.recentIDs, sessionID)
	}

	if fixture.hydrateSession == "" {
		tb.Fatalf("checkpoint 0 fixture did not create hydrate session")
	}
	return fixture
}

func seedV3SyncCheckpoint0Session(tb testing.TB, sessionSvc *sessionruntime.Service, principal identity.Principal, sessionID, title string, startMs int64, messageCount int, leaveActive bool) {
	tb.Helper()
	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  v3SyncCheckpoint0WorkspacePath,
		WorkspaceName:  "v3-sync-checkpoint-0",
		Title:          title,
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"benchmark":  "v3-sync-checkpoint-0",
			"synthetic":  true,
			"message_ct": messageCount,
		},
		CreatedAt: startMs,
		UpdatedAt: startMs,
	}
	applySessionsV3LoadBenchmarkMutation(tb, sessionSvc, sessionID, principal, sessionID+"-create", sessionruntime.SessionMutationCreateSession, startMs, &session, nil, nil, sessionsV3LoadBenchmarkPayloadHash("sync-cp0-session.create", sessionID, title))

	runID := sessionID + "-run-0001"
	if leaveActive || messageCount > 0 {
		recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentPendingExecutor, startMs+100)
		recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentRunning, startMs+200)
	}

	for i := 1; i <= messageCount; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		metadata := map[string]any{"benchmark": "v3-sync-checkpoint-0", "index": i}
		content := v3SyncCheckpoint0MessageContent(messageCount, i, role)
		if i%25 == 0 {
			role = "tool"
			metadata["tool_call_id"] = fmt.Sprintf("tool-call-%03d", i)
			metadata["tool_name"] = "benchmark_tool"
			content = v3SyncCheckpoint0ToolOutput(i)
		}
		message := pebblestore.MessageSnapshot{
			ID:             fmt.Sprintf("%s-msg-%05d", sessionID, i),
			SessionID:      sessionID,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			Role:           role,
			Content:        content,
			Metadata:       metadata,
			CreatedAt:      startMs + int64(i)*1000,
		}
		applySessionsV3LoadBenchmarkMutation(tb, sessionSvc, sessionID, principal, fmt.Sprintf("%s-message-%05d", sessionID, i), sessionruntime.SessionMutationAppendMessage, message.CreatedAt, nil, &message, nil, sessionsV3LoadBenchmarkPayloadHash("sync-cp0-message.append", sessionID, fmt.Sprint(i), role, content))
	}

	if messageCount > 0 && !leaveActive {
		recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentCompleted, startMs+int64(messageCount+2)*1000)
	}
}

type v3SyncCheckpoint0SnapshotPayload struct {
	OK                       bool                                        `json:"ok"`
	SessionsByID             map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
	MessagesBySession        map[string][]pebblestore.MessageSnapshot    `json:"messages_by_session"`
	RunIntentsBySession      map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
	CurrentRunStateBySession map[string]pebblestore.V3SessionRunState    `json:"current_run_state_by_session"`
	ActiveSessionIDs         []string                                    `json:"active_session_ids"`
	SessionViewsByID         map[string]sessionsV3SessionView            `json:"session_views_by_id"`
	SessionOrder             []string                                    `json:"session_order"`
}

func postV3SyncCheckpoint0Snapshot(tb testing.TB, server *Server, path string, body []byte) (v3SyncCheckpoint0SnapshotPayload, int) {
	tb.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		tb.Fatalf("%s status = %d, want %d, body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload v3SyncCheckpoint0SnapshotPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		tb.Fatalf("decode %s response: %v", path, err)
	}
	if !payload.OK {
		tb.Fatalf("%s response ok=false: %+v", path, payload)
	}
	return payload, rec.Body.Len()
}

func countV3SyncCheckpoint0RunIntents(intents map[string][]pebblestore.V3SessionRunIntent) int {
	total := 0
	for _, values := range intents {
		total += len(values)
	}
	return total
}

func v3SyncCheckpoint0MessageContent(total, index int, role string) string {
	turn := (index + 1) / 2
	if role == "user" {
		return fmt.Sprintf("Checkpoint 0 benchmark prompt %03d/%03d. Summarize the active V3 sync contract, include enough normal prose to exercise transcript paint, and preserve session-local context.", turn, total/2)
	}
	return fmt.Sprintf("Checkpoint 0 benchmark assistant response %03d. The response references bootstrap, targeted hydrate, and realtime responsibilities with deterministic text for comparable payload and reducer timing.", turn)
}

func v3SyncCheckpoint0ToolOutput(index int) string {
	return strings.Join([]string{
		fmt.Sprintf("tool output chunk for checkpoint 0 message %03d", index),
		"status: ok",
		"files: swarmd/internal/api/sessions_v3_sync_bootstrap.go, swarmd/internal/store/pebble/session_sync_snapshot.go",
		"note: synthetic but shaped like compact command output with multiple lines for renderer and JSON payload measurement",
	}, "\n")
}
