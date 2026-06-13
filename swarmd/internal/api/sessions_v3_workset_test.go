package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3WorksetEndpointSupportsPaginationAndManifests(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "workset-a", "Workset A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "workset-b", "Workset B")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "first")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "second")

	body := `{"session_ids":["` + createdB.ID + `"],"workspace":{"workspace_path":"/workspace/cp6"},"recent":{"limit":1},"history":{"mode":"full","max_messages_per_session":1,"manifest_policy":"manifest"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                        bool                                                     `json:"ok"`
		Rev                       uint64                                                   `json:"rev"`
		SessionsByID              map[string]pebblestore.SessionSnapshot                   `json:"sessions_by_id"`
		MessagesBySession         map[string][]pebblestore.MessageSnapshot                 `json:"messages_by_session"`
		EventsBySession           map[string][]pebblestore.V3SessionEvent                  `json:"events_by_session"`
		HistoryManifestsBySession map[string][]pebblestore.V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
		HistoryChunksByID         map[string]pebblestore.V3SessionHistoryChunk             `json:"history_chunks_by_id"`
		Omissions                 []pebblestore.V3SessionWorksetOmission                   `json:"omissions"`
		Pagination                pebblestore.V3SessionWorksetPagination                   `json:"pagination"`
		SessionOrder              []string                                                 `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	_ = createdA
	if !payload.OK || payload.SessionsByID[createdB.ID].ID != createdB.ID {
		t.Fatalf("sessions_by_id = %+v", payload.SessionsByID)
	}
	if payload.Rev == 0 {
		t.Fatalf("workset rev = %d, want non-zero daemon revision", payload.Rev)
	}
	if len(payload.MessagesBySession[createdB.ID]) != 1 {
		t.Fatalf("messages_by_session = %+v", payload.MessagesBySession)
	}
	if len(payload.EventsBySession[createdB.ID]) != 0 {
		t.Fatalf("events should be omitted by default: %+v", payload.EventsBySession)
	}
	if len(payload.HistoryChunksByID) != 0 {
		t.Fatalf("history_chunks_by_id should be metadata-only for manifests: %+v", payload.HistoryChunksByID)
	}
	if len(payload.HistoryManifestsBySession[createdB.ID]) == 0 || len(payload.Omissions) == 0 {
		t.Fatalf("manifest/omissions missing: %+v %+v", payload.HistoryManifestsBySession, payload.Omissions)
	}
	if !payload.Pagination.HasMore || payload.Pagination.NextBeforeUpdatedAt == nil || payload.Pagination.NextBeforeSessionID == "" {
		t.Fatalf("pagination = %+v", payload.Pagination)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != createdB.ID {
		t.Fatalf("session_order = %+v", payload.SessionOrder)
	}
}

func TestSessionsV3WorksetEndpointOmitsPlansByDefault(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-plan-default", "Workset Plan Default")
	if _, _, err := server.sessions.SavePlan(created.ID, "plan-workset-default", "Workset Plan", "# Plan", "draft", "draft", true); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		PlansBySession         map[string]pebblestore.SessionPlanSnapshot   `json:"plans_by_session"`
		PlanRevisionsBySession map[string][]pebblestore.SessionPlanSnapshot `json:"plan_revisions_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	if len(payload.PlansBySession) != 0 {
		t.Fatalf("routine workset should not include plans_by_session: %+v", payload.PlansBySession)
	}
	if len(payload.PlanRevisionsBySession) != 0 {
		t.Fatalf("routine workset should not include plan_revisions_by_session: %+v", payload.PlanRevisionsBySession)
	}
}

func TestSessionsV3WorksetEndpointReturnsActivePlansAndRevisionsWhenRequested(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-plan", "Workset Plan")
	plan, _, err := server.sessions.SavePlan(created.ID, "plan-workset", "Workset Plan", "# Plan", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"none"},"resources":{"active_plan":true,"plan_revisions":true}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		PlansBySession         map[string]pebblestore.SessionPlanSnapshot   `json:"plans_by_session"`
		PlanRevisionsBySession map[string][]pebblestore.SessionPlanSnapshot `json:"plan_revisions_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	if payload.PlansBySession[created.ID].ID != plan.ID || payload.PlansBySession[created.ID].Plan != "# Plan" {
		t.Fatalf("plans_by_session = %+v", payload.PlansBySession)
	}
	if len(payload.PlanRevisionsBySession[created.ID]) == 0 || payload.PlanRevisionsBySession[created.ID][0].ID != plan.ID {
		t.Fatalf("plan_revisions_by_session = %+v", payload.PlanRevisionsBySession)
	}
}

func TestSessionsV3WorksetEndpointReturnsPersistedUsage(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-usage", "Workset Usage")
	now := time.Now().UnixMilli()
	turnUsage := pebblestore.SessionTurnUsageSnapshot{
		SessionID:     created.ID,
		RunID:         "run-workset-usage",
		Provider:      "codex",
		Model:         "gpt-5.5",
		Source:        "codex_api_usage",
		ContextWindow: 1000,
		InputTokens:   200,
		OutputTokens:  50,
		TotalTokens:   250,
	}
	payloadHash, err := sessionV3UsagePayloadHash(created.ID, turnUsage.RunID, turnUsage)
	if err != nil {
		t.Fatalf("hash usage payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "workset-usage-record",
		IdempotencyKey:  "workset-usage-record",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordUsage,
		EventType:       "run.usage.updated",
		TurnUsage:       &turnUsage,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"none"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		UsageBySession map[string]pebblestore.SessionUsageSummary `json:"usage_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	usage, ok := payload.UsageBySession[created.ID]
	if !ok {
		t.Fatalf("usage_by_session missing session %q: %+v", created.ID, payload.UsageBySession)
	}
	if usage.ContextWindow != 1000 || usage.TotalTokens != 250 || usage.RemainingTokens != 750 {
		t.Fatalf("usage summary = %+v", usage)
	}
}

func TestSessionsV3WorksetEndpointOmittedHistoryIsMetadataOnly(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-default-history", "Workset Default History")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "first")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "second")

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(`{"session_ids":["`+created.ID+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		MessagesBySession map[string][]pebblestore.MessageSnapshot `json:"messages_by_session"`
		EventsBySession   map[string][]pebblestore.V3SessionEvent  `json:"events_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	if got := len(payload.MessagesBySession[created.ID]); got != 0 {
		t.Fatalf("omitted history should be metadata-only messages, got %d: %+v", got, payload.MessagesBySession[created.ID])
	}
	if got := len(payload.EventsBySession[created.ID]); got != 0 {
		t.Fatalf("omitted history should be metadata-only events, got %d: %+v", got, payload.EventsBySession[created.ID])
	}
}

func TestSessionsV3WorksetEndpointRejectsFullHistoryWithoutCap(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-full-history", "Workset Full History")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "first")

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"full","manifest_policy":"manifest","include_events":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestSessionsV3WorksetEndpointCapsExplicitHistoryAtTwoHundred(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-bounded-history", "Workset Bounded History")
	for i := 0; i < 205; i++ {
		appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "message-"+strconv.Itoa(i))
	}

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"tail","manifest_policy":"manifest","include_events":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		MessagesBySession map[string][]pebblestore.MessageSnapshot `json:"messages_by_session"`
		Omissions         []pebblestore.V3SessionWorksetOmission   `json:"omissions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	messages := payload.MessagesBySession[created.ID]
	if len(messages) != 200 {
		t.Fatalf("bounded tail history returned %d messages, want 200", len(messages))
	}
	if messages[0].Content != "message-5" || messages[len(messages)-1].Content != "message-204" {
		t.Fatalf("bounded tail messages = first %q last %q", messages[0].Content, messages[len(messages)-1].Content)
	}
	if len(payload.Omissions) == 0 {
		t.Fatalf("bounded history should report omitted older messages")
	}
}

type sessionsV3MessagesPageTestPayload struct {
	Messages      []pebblestore.MessageSnapshot `json:"messages"`
	Count         int                           `json:"count"`
	Limit         int                           `json:"limit"`
	OldestSeq     uint64                        `json:"oldest_seq"`
	NewestSeq     uint64                        `json:"newest_seq"`
	NextBeforeSeq uint64                        `json:"next_before_seq"`
	NextAfterSeq  uint64                        `json:"next_after_seq"`
	HasMoreOlder  bool                          `json:"has_more_older"`
	HasMoreNewer  bool                          `json:"has_more_newer"`
	HasMore       bool                          `json:"has_more"`
	Tail          bool                          `json:"tail"`
}

func TestSessionsV3MessagesEndpointSupportsBoundedTailBeforeAndAfterPages(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "messages-bounded-pagination", "Messages Bounded Pagination")
	for i := 0; i < 405; i++ {
		appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "message-"+strconv.Itoa(i))
	}

	tailPage := fetchSessionsV3MessagesPageForWorksetTest(t, server, "/v3/sessions/"+created.ID+"/messages?tail=true&limit=200")
	if len(tailPage.Messages) != 200 || tailPage.Count != 200 || tailPage.Limit != 200 {
		t.Fatalf("tail page count/limit = count:%d len:%d limit:%d", tailPage.Count, len(tailPage.Messages), tailPage.Limit)
	}
	if tailPage.Messages[0].Content != "message-205" || tailPage.Messages[len(tailPage.Messages)-1].Content != "message-404" {
		t.Fatalf("tail page messages = first %q last %q", tailPage.Messages[0].Content, tailPage.Messages[len(tailPage.Messages)-1].Content)
	}
	if !tailPage.Tail || !tailPage.HasMoreOlder || tailPage.HasMoreNewer || !tailPage.HasMore || tailPage.NextBeforeSeq != tailPage.OldestSeq || tailPage.NextAfterSeq != tailPage.NewestSeq {
		t.Fatalf("tail page cursor metadata = %+v", tailPage)
	}

	olderPage := fetchSessionsV3MessagesPageForWorksetTest(t, server, "/v3/sessions/"+created.ID+"/messages?before_seq="+strconv.FormatUint(tailPage.NextBeforeSeq, 10)+"&limit=200")
	if len(olderPage.Messages) != 200 || olderPage.Count != 200 {
		t.Fatalf("older page count = count:%d len:%d", olderPage.Count, len(olderPage.Messages))
	}
	if olderPage.Messages[0].Content != "message-5" || olderPage.Messages[len(olderPage.Messages)-1].Content != "message-204" {
		t.Fatalf("older page messages = first %q last %q", olderPage.Messages[0].Content, olderPage.Messages[len(olderPage.Messages)-1].Content)
	}
	if !olderPage.HasMoreOlder || !olderPage.HasMoreNewer || !olderPage.HasMore || olderPage.NextBeforeSeq != olderPage.OldestSeq || olderPage.NextAfterSeq != olderPage.NewestSeq {
		t.Fatalf("older page cursor metadata = %+v", olderPage)
	}

	newerPage := fetchSessionsV3MessagesPageForWorksetTest(t, server, "/v3/sessions/"+created.ID+"/messages?after_seq="+strconv.FormatUint(olderPage.NewestSeq, 10)+"&limit=3")
	if len(newerPage.Messages) != 3 || newerPage.Messages[0].Content != "message-205" || newerPage.Messages[2].Content != "message-207" {
		t.Fatalf("newer page messages = %+v", newerPage.Messages)
	}
	if !newerPage.HasMoreOlder || !newerPage.HasMoreNewer || !newerPage.HasMore || newerPage.NextAfterSeq != newerPage.NewestSeq || newerPage.NextBeforeSeq != newerPage.OldestSeq {
		t.Fatalf("newer page cursor metadata = %+v", newerPage)
	}
}

func TestSessionsV3MessagesEndpointDefaultsAndCapsLimitAtTwoHundred(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "messages-default-cap", "Messages Default Cap")
	for i := 0; i < 205; i++ {
		appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "message-"+strconv.Itoa(i))
	}

	defaultPage := fetchSessionsV3MessagesPageForWorksetTest(t, server, "/v3/sessions/"+created.ID+"/messages")
	if len(defaultPage.Messages) != 200 || defaultPage.Limit != 200 {
		t.Fatalf("default page count/limit = len:%d limit:%d", len(defaultPage.Messages), defaultPage.Limit)
	}
	if defaultPage.Messages[0].Content != "message-0" || defaultPage.Messages[len(defaultPage.Messages)-1].Content != "message-199" {
		t.Fatalf("default page messages = first %q last %q", defaultPage.Messages[0].Content, defaultPage.Messages[len(defaultPage.Messages)-1].Content)
	}
	if defaultPage.HasMoreOlder || !defaultPage.HasMoreNewer {
		t.Fatalf("default page cursor metadata = %+v", defaultPage)
	}

	cappedPage := fetchSessionsV3MessagesPageForWorksetTest(t, server, "/v3/sessions/"+created.ID+"/messages?tail=true&limit=500")
	if len(cappedPage.Messages) != 200 || cappedPage.Limit != 200 {
		t.Fatalf("capped page count/limit = len:%d limit:%d", len(cappedPage.Messages), cappedPage.Limit)
	}
	if cappedPage.Messages[0].Content != "message-5" || cappedPage.Messages[len(cappedPage.Messages)-1].Content != "message-204" {
		t.Fatalf("capped tail messages = first %q last %q", cappedPage.Messages[0].Content, cappedPage.Messages[len(cappedPage.Messages)-1].Content)
	}
}

func TestSessionsV3MessagesEndpointRejectsAmbiguousPageModes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "messages-ambiguous-pagination", "Messages Ambiguous Pagination")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "first")

	paths := []string{
		"/v3/sessions/" + created.ID + "/messages?after_seq=1&before_seq=2",
		"/v3/sessions/" + created.ID + "/messages?tail=true&after_seq=1",
		"/v3/sessions/" + created.ID + "/messages?tail=true&before_seq=2",
		"/v3/sessions/" + created.ID + "/messages?limit=0",
		"/v3/sessions/" + created.ID + "/messages?tail=maybe",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d, body=%s", path, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
}

func fetchSessionsV3MessagesPageForWorksetTest(t *testing.T, server *Server, path string) sessionsV3MessagesPageTestPayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("messages status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload sessionsV3MessagesPageTestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode messages response: %v", err)
	}
	return payload
}

func appendSessionsV3PrimaryMessageForWorksetTest(t *testing.T, server *Server, sessionID, content string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"client_request_id":"`+sessionID+content+`","role":"user","content":"`+content+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("append message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestSessionsV3TUIWorksetEndpointScopesWorkspaceAndCWD(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	cwd := filepath.Join(t.TempDir(), "cwd-only")
	for _, path := range []string{workspaceA, workspaceB, cwd} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	workspaceA = mustEvalSymlinkForWorksetTest(t, workspaceA)
	workspaceB = mustEvalSymlinkForWorksetTest(t, workspaceB)
	cwd = mustEvalSymlinkForWorksetTest(t, cwd)

	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "tui-workset-a", "TUI Workset A", workspaceA)
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "tui-workset-b", "TUI Workset B", workspaceB)
	createdCWD := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "tui-workset-cwd", "TUI Workset CWD", cwd)

	workspacePayload := postSessionsV3TUIWorksetForTest(t, server, http.StatusOK, map[string]any{
		"scope":   map[string]any{"workspace_path": workspaceA},
		"recent":  map[string]any{"limit": 20},
		"history": map[string]any{"mode": "none"},
	})
	assertSessionsV3WorksetIDs(t, workspacePayload, createdA.ID)

	multiPayload := postSessionsV3TUIWorksetForTest(t, server, http.StatusOK, map[string]any{
		"scope":   map[string]any{"workspace_paths": []string{workspaceA, workspaceB}},
		"recent":  map[string]any{"limit": 20},
		"history": map[string]any{"mode": "none"},
	})
	assertSessionsV3WorksetIDs(t, multiPayload, createdB.ID, createdA.ID)

	cwdPayload := postSessionsV3TUIWorksetForTest(t, server, http.StatusOK, map[string]any{
		"scope":   map[string]any{"cwd_path": cwd},
		"recent":  map[string]any{"limit": 20},
		"history": map[string]any{"mode": "none"},
	})
	assertSessionsV3WorksetIDs(t, cwdPayload, createdCWD.ID)
}

func TestSessionsV3TUIWorksetEndpointRejectsEmptyAndNonCanonicalSelectors(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	postSessionsV3TUIWorksetForTest(t, server, http.StatusBadRequest, map[string]any{
		"scope":  map[string]any{},
		"recent": map[string]any{"limit": 10},
	})
	postSessionsV3TUIWorksetForTest(t, server, http.StatusBadRequest, map[string]any{
		"scope":  map[string]any{"workspace_path": "relative/path"},
		"recent": map[string]any{"limit": 10},
	})
	postSessionsV3TUIWorksetForTest(t, server, http.StatusBadRequest, map[string]any{
		"scope":  map[string]any{"workspace_path": "/tmp/../tmp"},
		"recent": map[string]any{"limit": 10},
	})
}

func TestSessionsV3TUIWorksetEndpointFiltersSessionIDsByExplicitScopeAndAccount(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	for _, path := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	workspaceA = mustEvalSymlinkForWorksetTest(t, workspaceA)
	workspaceB = mustEvalSymlinkForWorksetTest(t, workspaceB)

	createdA := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "tui-scope-a", "TUI Scope A", workspaceA)
	createdB := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "tui-scope-b", "TUI Scope B", workspaceB)
	createSessionsV3WorksetForeignAccountSession(t, sessionSvc, "foreign-session", workspaceA)

	payload := postSessionsV3TUIWorksetForTest(t, server, http.StatusOK, map[string]any{
		"session_ids": []string{createdA.ID, createdB.ID, "foreign-session"},
		"scope":       map[string]any{"workspace_path": workspaceA},
		"history":     map[string]any{"mode": "none"},
	})
	assertSessionsV3WorksetIDs(t, payload, createdA.ID)
}

func TestSessionsV3WorksetEndpointRejectsUnscopedRecentSelector(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "main-workset-unscoped", "Main Workset Unscoped")

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(`{"session_ids":["`+created.ID+`"],"recent":{"limit":10},"history":{"mode":"none"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped recent status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func postSessionsV3TUIWorksetForTest(t *testing.T, server *Server, wantStatus int, body map[string]any) map[string]pebblestore.SessionSnapshot {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal tui workset body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/tui/sessions:workset", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != wantStatus {
		t.Fatalf("tui workset status = %d, want %d, body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	if wantStatus != http.StatusOK {
		return nil
	}
	var payload struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tui workset response: %v", err)
	}
	return payload.SessionsByID
}

func assertSessionsV3WorksetIDs(t *testing.T, sessions map[string]pebblestore.SessionSnapshot, want ...string) {
	t.Helper()
	if len(sessions) != len(want) {
		t.Fatalf("sessions_by_id len = %d, want %d: %+v", len(sessions), len(want), sessions)
	}
	for _, id := range want {
		if sessions[id].ID != id {
			t.Fatalf("sessions_by_id missing %q: %+v", id, sessions)
		}
	}
}

func mustEvalSymlinkForWorksetTest(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return resolved
}

func createSessionsV3WorksetForeignAccountSession(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, workspacePath string) {
	t.Helper()
	_, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:      sessionID,
		UserID:         "foreign-user",
		AccountScopeID: "foreign-account",
		IdempotencyKey: "create-" + sessionID,
		PayloadHash:    "hash-create-" + sessionID,
		RequestHash:    "hash-create-" + sessionID,
		Kind:           sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         "foreign-user",
			AccountScopeID: "foreign-account",
			WorkspacePath:  workspacePath,
			WorkspaceName:  filepath.Base(workspacePath),
			Title:          sessionID,
			CreatedAt:      time.Now().UnixMilli(),
			UpdatedAt:      time.Now().UnixMilli(),
		},
		NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("create foreign account session: %v", err)
	}
}
