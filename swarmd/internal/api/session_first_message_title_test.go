package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the deterministic title is the first-line, bounded label apps show
// for a conversation before (or instead of) any model-generated title.
// Invariant: same input, same title; first non-empty line only, Markdown
// markers and runs of whitespace removed, at most 60 runes plus an ellipsis,
// cut at a word boundary when one is near; empty input yields no title.
// firstMessageSessionTitle is a pure function, the narrowest layer.
func TestFirstMessageSessionTitleIsDeterministicAndBounded(t *testing.T) {
	long := "Build a booking page for the dentist office with calendar slots and email reminders"
	cases := []struct{ in, want string }{
		{"Fix the login bug", "Fix the login bug"},
		{"\n\n  ## Add   dark mode \nwith more detail on line two", "Add dark mode"},
		{"> quoted ask\nrest", "quoted ask"},
		{long, "Build a booking page for the dentist office with calendar…"},
		{strings.Repeat("x", 80), strings.Repeat("x", 60) + "…"},
		{"   \n\t\n", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := firstMessageSessionTitle(tc.in)
		if got != tc.want {
			t.Errorf("firstMessageSessionTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if again := firstMessageSessionTitle(tc.in); again != got {
			t.Errorf("non-deterministic title for %q: %q then %q", tc.in, got, again)
		}
		if n := len([]rune(got)); n > sessionV3FirstMessageTitleMaxRunes+1 {
			t.Errorf("title %q has %d runes, over the bound", got, n)
		}
	}
}

// Purpose: every SDK app had to store its own conversation labels because
// sessions stayed "New Session"/"Orchestrator AI" forever (the model title flow
// can fail, and the SDK's placeholder titles disabled it).
// Invariant: when the first user message is accepted on a session that still
// has a placeholder title, the session is titled from that message before the
// message request returns, with no model call, as an unlocked
// title_source=first_message title; a client-given title is never replaced;
// POST /v3/sessions/{id}/title renames for the owner only, locks the title,
// and another principal can neither rename nor observe the session.
// The HTTP handlers + committed-mutation hook backed by Pebble are the
// narrowest layer that proves the durable title and owner scoping.
func TestSessionsV3FirstMessageTitleAndOwnerRename(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "Title-only case") {
			return provideriface.Response{}, errors.New("title model unavailable")
		}
		return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	pref := pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"}

	sessionIDs := []string{}
	for _, placeholder := range []string{"", "Orchestrator AI", "Chat Session"} {
		created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "first-title-create-"+placeholder, placeholder, pref)
		sessionIDs = append(sessionIDs, created.ID)
		postSessionsV3PrimaryTestMessage(t, server, created.ID, "first-title-message-"+placeholder, "Plan the launch checklist\nwith details below")
		stored, ok, err := sessionSvc.GetSession(created.ID)
		if err != nil || !ok {
			t.Fatalf("load session ok=%v err=%v", ok, err)
		}
		if stored.Title != "Plan the launch checklist" {
			t.Fatalf("placeholder %q: title right after the first message = %q", placeholder, stored.Title)
		}
		if stored.Metadata["title_source"] != sessionV3TitleSourceFirstMessage || sessionV3TitleGenerationLocked(stored.Metadata) {
			t.Fatalf("provisional title metadata = %#v", stored.Metadata)
		}
	}

	explicit := createSessionsV3PrimaryTestSessionWithPreference(t, server, "first-title-explicit", "Client Title", pref)
	postSessionsV3PrimaryTestMessage(t, server, explicit.ID, "first-title-explicit-message", "this must not rename it")
	sessionIDs = append(sessionIDs, explicit.ID)

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "rename-create", "", pref)
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "rename-first", "Initial ask")
	for _, id := range append(sessionIDs, created.ID) {
		waitForSessionsV3MessageCount(t, sessionSvc, id, 2)
		waitForSessionsV3TerminalRun(t, sessionSvc, id)
	}
	if stored, _, _ := sessionSvc.GetSession(explicit.ID); stored.Title != "Client Title" {
		t.Fatalf("client-given title replaced: %q", stored.Title)
	}

	rename := func(req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}
	newRenameRequest := func(title, clientRequestID string) *http.Request {
		body, _ := json.Marshal(map[string]any{"title": title, "client_request_id": clientRequestID})
		return httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/title", bytes.NewReader(body))
	}

	if rec := rename(requestWithTestPrincipalForAccount(newRenameRequest("Stolen", "rename-other"), "user-2", "account-2")); rec.Code == http.StatusOK {
		t.Fatalf("another principal renamed the session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rename(withTestPrincipal(newRenameRequest("   ", "rename-empty"))); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty title status = %d", rec.Code)
	}
	if rec := rename(withTestPrincipal(newRenameRequest(strings.Repeat("y", 201), "rename-long"))); rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long title status = %d", rec.Code)
	}
	if stored, _, _ := sessionSvc.GetSession(created.ID); stored.Title != "Initial ask" {
		t.Fatalf("rejected renames changed the title to %q", stored.Title)
	}

	if rec := rename(withTestPrincipal(newRenameRequest("  Launch plan  ", "rename-owner"))); rec.Code != http.StatusOK {
		t.Fatalf("owner rename status = %d body=%s", rec.Code, rec.Body.String())
	}
	stored, _, _ := sessionSvc.GetSession(created.ID)
	if stored.Title != "Launch plan" || !sessionV3TitleGenerationLocked(stored.Metadata) || stored.Metadata["title_source"] != "manual" {
		t.Fatalf("renamed session title=%q metadata=%#v", stored.Title, stored.Metadata)
	}
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "rename-second", "A later message")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 4)
	waitForSessionsV3TerminalRun(t, sessionSvc, created.ID)
	if stored, _, _ := sessionSvc.GetSession(created.ID); stored.Title != "Launch plan" {
		t.Fatalf("manual title was replaced by %q", stored.Title)
	}
}

// waitForSessionsV3TerminalRun waits until no run of the session is pending or
// running. The assistant outcome itself is irrelevant to title ownership.
func waitForSessionsV3TerminalRun(t *testing.T, sessionSvc *sessionruntime.Service, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		intents, err := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("list run intents: %v", err)
		}
		active := len(intents) == 0
		for _, intent := range intents {
			if intent.Status == sessionruntime.RunIntentPendingExecutor || intent.Status == sessionruntime.RunIntentRunning {
				active = true
			}
		}
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %q run did not finish", sessionID)
}
