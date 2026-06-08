package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

const (
	sessionsV3LoadBenchmarkSessionCount       = 5
	sessionsV3LoadBenchmarkMessagesPerSession = 500
	sessionsV3LoadBenchmarkWorkspacePath      = "/workspace/v3-load-baseline"
	sessionsV3LoadBenchmarkWorkspaceName      = "v3-load-baseline"
	sessionsV3LoadBenchmarkSessionTitlePrefix = "V3 Load Baseline"
)

type sessionsV3LoadBenchmarkHydrateResponse struct {
	OK            bool                             `json:"ok"`
	Session       pebblestore.SessionSnapshot      `json:"session"`
	Projection    sessionruntime.SessionProjection `json:"projection"`
	Messages      []pebblestore.MessageSnapshot    `json:"messages"`
	Events        []sessionruntime.SessionEvent    `json:"events"`
	AppliedSeq    uint64                           `json:"applied_seq"`
	HighWatermark uint64                           `json:"high_watermark"`
}

type sessionsV3LoadBenchmarkMessagesPageResponse struct {
	OK            bool                          `json:"ok"`
	SessionID     string                        `json:"session_id"`
	Messages      []pebblestore.MessageSnapshot `json:"messages"`
	Count         int                           `json:"count"`
	OldestSeq     uint64                        `json:"oldest_seq"`
	NewestSeq     uint64                        `json:"newest_seq"`
	NextBeforeSeq uint64                        `json:"next_before_seq"`
	NextAfterSeq  uint64                        `json:"next_after_seq"`
	HasMore       bool                          `json:"has_more"`
	HasMoreOlder  bool                          `json:"has_more_older"`
	HasMoreNewer  bool                          `json:"has_more_newer"`
	BeforeSeq     uint64                        `json:"before_seq"`
	AfterSeq      uint64                        `json:"after_seq"`
}

func TestSessionsV3PrimaryHydrateRouteLoadBaselineFive500MessageSessions(t *testing.T) {
	server, sessionIDs := newSessionsV3PrimaryLoadBenchmarkFixture(t)
	for _, sessionID := range sessionIDs {
		resp, bodyBytes := hydrateSessionsV3PrimaryLoadBenchmarkRoute(t, server, sessionID)
		assertSessionsV3LoadBenchmarkHydrateResponse(t, resp)
		t.Logf("baseline hydrate route session_id=%s body_bytes=%d messages=%d events=%d projection_last_seq=%d applied_seq=%d high_watermark=%d", sessionID, bodyBytes, len(resp.Messages), len(resp.Events), resp.Projection.LastEventSeq, resp.AppliedSeq, resp.HighWatermark)
	}
}

func BenchmarkSessionsV3PrimaryHydrateRouteFive500MessageSessions(b *testing.B) {
	server, sessionIDs := newSessionsV3PrimaryLoadBenchmarkFixture(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, bodyBytes := hydrateSessionsV3PrimaryLoadBenchmarkRoute(b, server, sessionIDs[i%len(sessionIDs)])
		assertSessionsV3LoadBenchmarkHydrateResponse(b, resp)
		b.ReportMetric(float64(bodyBytes), "body_bytes/op")
		b.ReportMetric(float64(len(resp.Messages)), "messages/op")
		b.ReportMetric(float64(len(resp.Events)), "events/op")
	}
}

func TestSessionsV3PrimaryMessageHistoryPaginationFetchesFullSeededHistory(t *testing.T) {
	server, sessionIDs := newSessionsV3PrimaryLoadBenchmarkFixture(t)
	for _, sessionID := range sessionIDs {
		hydrate, _ := hydrateSessionsV3PrimaryLoadBenchmarkRoute(t, server, sessionID)
		assertSessionsV3LoadBenchmarkHydrateResponse(t, hydrate)
		messages, bodyBytes := fetchAllSessionsV3PrimaryLoadBenchmarkMessages(t, server, sessionID, hydrate.Messages)
		if len(messages) != sessionsV3LoadBenchmarkMessagesPerSession {
			t.Fatalf("paginated messages for %s = %d, want %d", sessionID, len(messages), sessionsV3LoadBenchmarkMessagesPerSession)
		}
		for i := 1; i < len(messages); i++ {
			if messages[i].GlobalSeq <= messages[i-1].GlobalSeq {
				t.Fatalf("messages are not ascending at %d: prev=%d current=%d", i, messages[i-1].GlobalSeq, messages[i].GlobalSeq)
			}
		}
		t.Logf("history pagination session_id=%s pages=%d body_bytes=%d messages=%d first_seq=%d last_seq=%d", sessionID, (len(messages)+sessionsV3PrimaryDefaultMessageTailLimit-1)/sessionsV3PrimaryDefaultMessageTailLimit, bodyBytes, len(messages), messages[0].GlobalSeq, messages[len(messages)-1].GlobalSeq)
	}
}

func BenchmarkSessionsV3PrimaryMessageHistoryPaginationFive500MessageSessions(b *testing.B) {
	server, sessionIDs := newSessionsV3PrimaryLoadBenchmarkFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hydrate, hydrateBytes := hydrateSessionsV3PrimaryLoadBenchmarkRoute(b, server, sessionIDs[i%len(sessionIDs)])
		messages, pageBytes := fetchAllSessionsV3PrimaryLoadBenchmarkMessages(b, server, sessionIDs[i%len(sessionIDs)], hydrate.Messages)
		if len(messages) != sessionsV3LoadBenchmarkMessagesPerSession {
			b.Fatalf("paginated messages = %d, want %d", len(messages), sessionsV3LoadBenchmarkMessagesPerSession)
		}
		b.ReportMetric(float64(hydrateBytes+pageBytes), "full_history_body_bytes/op")
		b.ReportMetric(float64(len(messages)), "full_history_messages/op")
	}
}

func newSessionsV3PrimaryLoadBenchmarkFixture(tb testing.TB) (*Server, []string) {
	tb.Helper()

	server, sessionSvc := newSessionsV3PrimaryLoadBenchmarkServer(tb)
	principal := testPrincipal()
	sessionIDs := make([]string, 0, sessionsV3LoadBenchmarkSessionCount)
	baseMs := int64(1_900_000_000_000)
	for sessionIndex := 1; sessionIndex <= sessionsV3LoadBenchmarkSessionCount; sessionIndex++ {
		sessionID := fmt.Sprintf("v3-load-baseline-%02d", sessionIndex)
		startMs := baseMs + int64(sessionIndex)*86_400_000
		seedSessionsV3PrimaryLoadBenchmarkSession(tb, sessionSvc, principal, sessionID, sessionIndex, sessionsV3LoadBenchmarkMessagesPerSession, startMs)
		sessionIDs = append(sessionIDs, sessionID)
	}
	return server, sessionIDs
}

func newSessionsV3PrimaryLoadBenchmarkServer(tb testing.TB) (*Server, *sessionruntime.Service) {
	tb.Helper()
	tb.Setenv("SWARM_API_NO_AUTH", "1")
	store, err := pebblestore.Open(filepath.Join(tb.TempDir(), "sessions-v3-load-baseline.pebble"))
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		tb.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	return server, sessionSvc
}

func seedSessionsV3PrimaryLoadBenchmarkSession(tb testing.TB, sessionSvc *sessionruntime.Service, principal identity.Principal, sessionID string, ordinal, messageCount int, startMs int64) {
	tb.Helper()
	title := fmt.Sprintf("%s %02d - %d messages", sessionsV3LoadBenchmarkSessionTitlePrefix, ordinal, messageCount)
	runID := sessionID + "-run-0001"
	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  sessionsV3LoadBenchmarkWorkspacePath,
		WorkspaceName:  sessionsV3LoadBenchmarkWorkspaceName,
		Title:          title,
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"load_test":      true,
			"seeded_by":      "sessions_v3_primary_load_benchmark_test",
			"scenario":       "five-500-message-route-hydration-baseline",
			"message_target": messageCount,
			"synthetic":      true,
		},
		CreatedAt: startMs,
		UpdatedAt: startMs,
	}
	applySessionsV3LoadBenchmarkMutation(tb, sessionSvc, sessionID, principal, sessionID+"-create", sessionruntime.SessionMutationCreateSession, startMs, &session, nil, nil, sessionsV3LoadBenchmarkPayloadHash("session.create", sessionID, title))
	recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentPendingExecutor, startMs+100)
	recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentRunning, startMs+200)
	for i := 1; i <= messageCount; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		content := sessionsV3LoadBenchmarkSyntheticContent(messageCount, i, role)
		message := pebblestore.MessageSnapshot{
			ID:             fmt.Sprintf("%s-msg-%05d", sessionID, i),
			SessionID:      sessionID,
			UserID:         principal.UserID,
			AccountScopeID: principal.AccountScopeID,
			Role:           role,
			Content:        content,
			Metadata: map[string]any{
				"load_test": true,
				"index":     i,
				"turn":      (i + 1) / 2,
			},
			CreatedAt: startMs + int64(i)*1000,
		}
		requestID := fmt.Sprintf("%s-message-%05d", sessionID, i)
		applySessionsV3LoadBenchmarkMutation(tb, sessionSvc, sessionID, principal, requestID, sessionruntime.SessionMutationAppendMessage, message.CreatedAt, nil, &message, nil, sessionsV3LoadBenchmarkPayloadHash("message.append", sessionID, strconv.Itoa(i), role, content))
	}
	recordSessionsV3LoadBenchmarkRunIntent(tb, sessionSvc, principal, sessionID, runID, sessionruntime.RunIntentCompleted, startMs+int64(messageCount+2)*1000)
}

func recordSessionsV3LoadBenchmarkRunIntent(tb testing.TB, sessionSvc *sessionruntime.Service, principal identity.Principal, sessionID, runID, status string, now int64) {
	tb.Helper()
	intent := pebblestore.V3SessionRunIntent{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, RunID: runID, Status: status, CreatedAt: now, UpdatedAt: now}
	applySessionsV3LoadBenchmarkMutation(tb, sessionSvc, sessionID, principal, fmt.Sprintf("%s-run-%s", sessionID, strings.ReplaceAll(status, "_", "-")), sessionruntime.SessionMutationRecordRunIntent, now, nil, nil, &intent, sessionsV3LoadBenchmarkPayloadHash("run_intent.record", sessionID, runID, status))
}

func applySessionsV3LoadBenchmarkMutation(tb testing.TB, sessionSvc *sessionruntime.Service, sessionID string, principal identity.Principal, clientRequestID, kind string, now int64, session *pebblestore.SessionSnapshot, message *pebblestore.MessageSnapshot, runIntent *pebblestore.V3SessionRunIntent, payloadHash string) {
	tb.Helper()
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: kind, Session: session, Message: message, RunIntent: runIntent, NowUnixMs: now}); err != nil {
		tb.Fatalf("apply %s %s: %v", kind, clientRequestID, err)
	}
}

func hydrateSessionsV3PrimaryLoadBenchmarkRoute(tb testing.TB, server *Server, sessionID string) (sessionsV3LoadBenchmarkHydrateResponse, int) {
	tb.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+sessionID, nil)
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		tb.Fatalf("hydrate %s status = %d, want %d, body=%s", sessionID, rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp sessionsV3LoadBenchmarkHydrateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		tb.Fatalf("decode hydrate %s response: %v", sessionID, err)
	}
	return resp, rec.Body.Len()
}

func fetchAllSessionsV3PrimaryLoadBenchmarkMessages(tb testing.TB, server *Server, sessionID string, tail []pebblestore.MessageSnapshot) ([]pebblestore.MessageSnapshot, int) {
	tb.Helper()
	all := append([]pebblestore.MessageSnapshot(nil), tail...)
	totalBodyBytes := 0
	beforeSeq := uint64(0)
	if len(tail) > 0 {
		beforeSeq = tail[0].GlobalSeq
	}
	for beforeSeq > 1 {
		page, bodyBytes := fetchSessionsV3PrimaryLoadBenchmarkMessagePageBefore(tb, server, sessionID, beforeSeq, sessionsV3PrimaryDefaultMessageTailLimit)
		totalBodyBytes += bodyBytes
		if !page.OK {
			tb.Fatalf("message page for %s before %d was not ok: %+v", sessionID, beforeSeq, page)
		}
		if len(page.Messages) == 0 {
			break
		}
		all = append(page.Messages, all...)
		beforeSeq = page.Messages[0].GlobalSeq
	}
	return all, totalBodyBytes
}

func fetchSessionsV3PrimaryLoadBenchmarkMessagePageBefore(tb testing.TB, server *Server, sessionID string, beforeSeq uint64, limit int) (sessionsV3LoadBenchmarkMessagesPageResponse, int) {
	tb.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v3/sessions/%s/messages?before_seq=%d&limit=%d", sessionID, beforeSeq, limit), nil)
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		tb.Fatalf("messages %s before=%d status = %d, want %d, body=%s", sessionID, beforeSeq, rec.Code, http.StatusOK, rec.Body.String())
	}
	var page sessionsV3LoadBenchmarkMessagesPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		tb.Fatalf("decode messages %s before=%d response: %v", sessionID, beforeSeq, err)
	}
	return page, rec.Body.Len()
}

func assertSessionsV3LoadBenchmarkHydrateResponse(tb testing.TB, resp sessionsV3LoadBenchmarkHydrateResponse) {
	tb.Helper()
	if !resp.OK || strings.TrimSpace(resp.Session.ID) == "" {
		tb.Fatalf("hydrate response missing session: %+v", resp)
	}
	if resp.Session.MessageCount != sessionsV3LoadBenchmarkMessagesPerSession {
		tb.Fatalf("hydrate session message_count = %d, want %d", resp.Session.MessageCount, sessionsV3LoadBenchmarkMessagesPerSession)
	}
	if len(resp.Messages) != sessionsV3PrimaryDefaultMessageTailLimit {
		tb.Fatalf("hydrate tail messages = %d, want %d", len(resp.Messages), sessionsV3PrimaryDefaultMessageTailLimit)
	}
	if len(resp.Events) != sessionsV3PrimaryDefaultEventLimit {
		tb.Fatalf("hydrate events = %d, want route-critical default of %d", len(resp.Events), sessionsV3PrimaryDefaultEventLimit)
	}
	if resp.Projection.LastEventSeq == 0 || resp.Projection.ProjectionHighWatermarkSeq == 0 {
		tb.Fatalf("hydrate response missing projection cursor: %+v", resp.Projection)
	}
	if resp.AppliedSeq != resp.Projection.LastEventSeq || resp.HighWatermark != resp.Projection.ProjectionHighWatermarkSeq {
		tb.Fatalf("hydrate durable cursors applied=%d high=%d projection=%+v", resp.AppliedSeq, resp.HighWatermark, resp.Projection)
	}
	if len(resp.Messages) > 0 && resp.Messages[0].GlobalSeq <= 10 {
		tb.Fatalf("hydrate returned oldest history instead of latest tail: first message seq=%d", resp.Messages[0].GlobalSeq)
	}
}

func sessionsV3LoadBenchmarkSyntheticContent(total, index int, role string) string {
	turn := (index + 1) / 2
	if role == "user" {
		return fmt.Sprintf("Load-test user prompt %04d in a synthetic %d-message AI session. Please analyze the current project state, reference earlier decisions, and continue with the next implementation step. This message exercises scrollback, cache hydration, and session switching.", turn, total)
	}
	if index%50 == 0 {
		return fmt.Sprintf("Assistant response %04d for the %d-message load-test session.\n\nSummary:\n- Preserved durable V3 session history ordering.\n- Referenced earlier context without assuming stream completion from message events.\n- Included a moderate code block so rendering and scrollback measure realistic content.\n\n```ts\nconst checkpoint = { turn: %d, durable: true, source: 'pebble-v3' }\nconsole.log(checkpoint)\n```\n\nNext: continue validating long-history loading and route switching behavior.", turn, total, turn)
	}
	return fmt.Sprintf("Assistant response %04d in a synthetic %d-message AI session. The durable Pebble V3 message record is the source of truth for this history item. It contains enough prose to look like a normal AI answer while remaining deterministic for performance comparisons.", turn, total)
}

func sessionsV3LoadBenchmarkPayloadHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestSessionsV3PrimaryLoadBenchmarkFixtureSeedTime(t *testing.T) {
	started := time.Now()
	_, sessionIDs := newSessionsV3PrimaryLoadBenchmarkFixture(t)
	if len(sessionIDs) != sessionsV3LoadBenchmarkSessionCount {
		t.Fatalf("seeded sessions = %d, want %d", len(sessionIDs), sessionsV3LoadBenchmarkSessionCount)
	}
	t.Logf("seeded %d sessions x %d messages in %s", len(sessionIDs), sessionsV3LoadBenchmarkMessagesPerSession, time.Since(started))
}
