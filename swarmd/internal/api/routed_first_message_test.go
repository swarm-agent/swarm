package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: a routed start must retain the original user request in its response,
// durable transcript, and executor provider input. This catches loss between
// handleRoutedSessionStart's atomic create and sessionV3ProviderContextMessages;
// the API fixture exercises the real store with a deterministic fake Router.
func TestRoutedFirstMessageSurvivesProviderContext(t *testing.T) {
	for _, planRequested := range []bool{false, true} {
		t.Run(map[bool]string{false: "auto", true: "plan"}[planRequested], func(t *testing.T) {
			testRoutedFirstMessageSurvivesProviderContext(t, planRequested)
		})
	}
}

func testRoutedFirstMessageSurvivesProviderContext(t *testing.T, planRequested bool) {
	t.Helper()
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"First Message","worktree_name":"first-message"}`}}
	server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	const prompt = "Keep this original request.\nDo not replace it with the Router title."
	response := postRoutedSessionAtomicityRequest(t, server, principal, map[string]any{"input": prompt, "client_request_id": "first-message-proof", "plan_mode_requested": planRequested})
	if response.Code != http.StatusOK {
		t.Fatalf("routed start status=%d body=%s", response.Code, response.Body.String())
	}
	result := decodeRoutedSessionAtomicityResponse(t, response)
	// Hydration replaces the session record before the Desktop reducer decides
	// whether an omitted initiating message belongs to a routed session.
	body, err := json.Marshal(map[string]any{"surface": "desktop", "session_ids": []string{result.SessionID}})
	if err != nil {
		t.Fatal(err)
	}
	hydrateRequest := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	hydrateRequest = hydrateRequest.WithContext(identity.ContextWithPrincipal(hydrateRequest.Context(), principal))
	hydrateResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateResponse, hydrateRequest)
	if hydrateResponse.Code != http.StatusOK {
		t.Fatalf("hydrate: %d %s", hydrateResponse.Code, hydrateResponse.Body.String())
	}
	var snapshot struct {
		SessionsByID map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
	}
	if err := json.Unmarshal(hydrateResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	shell := snapshot.SessionsByID[result.SessionID]
	if shell.Metadata["routed_start"] != true {
		t.Fatal("sync hydration strips routed_start, disabling first-user-message retention")
	}
	if _, leaked := shell.Metadata["routed_start_request_hash"]; leaked {
		t.Fatal("sync hydration leaked internal routed request hash")
	}
	if result.FirstMessage.Content != prompt {
		t.Fatalf("routed response lost first message: %q", result.FirstMessage.Content)
	}
	messages, err := sessions.ListSessionMessages(result.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Content != prompt {
		t.Fatalf("durable transcript lost first message: messages=%+v err=%v", messages, err)
	}
	server.providers.RegisterRunner(&sessionsV3RecordingProviderRunner{id: "codex"})
	executor := &sessionV3Executor{server: server}
	contextMessages, err := executor.sessionV3ProviderContextMessages(sessionV3ExecutorJob{SessionID: result.SessionID, RunID: result.Mutation.RunIntent.RunID})
	if err != nil || len(contextMessages) != 1 || contextMessages[0].Content != prompt {
		t.Fatalf("provider context lost first message: messages=%+v err=%v", contextMessages, err)
	}
	job := sessionV3ExecutorJob{Principal: principal, SessionID: result.SessionID, RunID: result.Mutation.RunIntent.RunID, EpochID: result.Mutation.RunIntent.EpochID}
	// Allocation is stubbed by this fixture; supply resolved workspace context
	// while retaining the real routed session, epoch, and request construction.
	resolved := sessionV3ResolvedRuntime{
		Session:      result.Session,
		AgentProfile: pebblestore.AgentProfile{Name: "swarm", Mode: "primary", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExecutionSetting: "auto"},
		Preference:   pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"},
		Scope:        tool.WorkspaceScope{PrimaryPath: t.TempDir()},
		Instructions: "Test instructions", ToolChoice: "none",
	}
	input, selection, err := executor.sessionV3ProviderCheckpointStartupInput(job, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if selection != sessionV3ProviderContextCheckpointStartup {
		input, err = executor.sessionsV3ProviderInput(resolved, contextMessages)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := executor.sessionV3ProviderBaseRequest(job, resolved, input)
	if err != nil {
		t.Fatalf("build routed provider request: %v", err)
	}
	input, err = executor.sessionV3ProviderInitialContextInput(job, resolved, contextMessages, request, input, selection)
	if err != nil {
		t.Fatalf("select routed provider input: %v", err)
	}
	if len(input) != 1 || input[0]["role"] != "user" {
		t.Fatalf("provider input lost first message: %+v", input)
	}
	if content, ok := input[0]["content"].([]map[string]any); !ok || len(content) != 1 || content[0]["text"] != prompt {
		t.Fatalf("provider input changed first message: %+v", input)
	}
	// Retrying the routed request must neither replace nor duplicate the user
	// message once the provider epoch has been initialized.
	replay := postRoutedSessionAtomicityRequest(t, server, principal, map[string]any{"input": prompt, "client_request_id": "first-message-proof", "plan_mode_requested": planRequested})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	replayed := decodeRoutedSessionAtomicityResponse(t, replay)
	if replayed.SessionID != result.SessionID || replayed.FirstMessage.ID != result.FirstMessage.ID || replayed.FirstMessage.Content != prompt {
		t.Fatalf("replay replaced original message: %+v", replayed.FirstMessage)
	}
	messages, err = sessions.ListSessionMessages(result.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Content != prompt {
		t.Fatalf("replay changed durable transcript: messages=%+v err=%v", messages, err)
	}
}
