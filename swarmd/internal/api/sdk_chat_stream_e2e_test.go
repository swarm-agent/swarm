package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: prove the published TypeScript SDK (packages/sdk/dist, driven by
// packages/sdk/e2e/chat-stream.e2e.mjs) streams a V3 chat turn end to end against this
// API server: real HTTP + WebSocket transport, /v3/sync/hydrate, /v3/realtime/stream
// replay and live patches, the real executor, durable progress sink and Pebble store.
// Requirement: assistant text reaches the SDK incrementally, a dropped socket mid-turn
// reconnects and resumes without lost or repeated text while durable checkpoints lag
// live output, completion and failure are reported once, sessions can be listed and
// switched, and context clear follows its project-only contract.
// Threat: SDK/daemon protocol drift that unit fixtures cannot see.
// Boundary and gaps: only the provider runner is replaced (scripted deltas below, not a
// model) and request authentication is the package's test principal, not a minted
// token; tool calls and permission prompts are not exercised here. The swarmd binary
// cannot run a turn without a verified provider credential, so this in-process server is
// the closest hermetic boundary. Requires node and a built SDK; opt in with
// SWARM_SDK_E2E=1 (never part of the hermetic critical tiers).
func TestSDKChatStreamE2E(t *testing.T) {
	if os.Getenv("SWARM_SDK_E2E") != "1" {
		t.Skip("set SWARM_SDK_E2E=1 after `npm run build` in packages/sdk to run the SDK end-to-end test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("SWARM_SDK_E2E=1 requires node on PATH: %v", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "packages", "sdk", "e2e", "chat-stream.e2e.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(script), "..", "dist", "index.js")); err != nil {
		t.Fatalf("build the SDK first (npm run build in packages/sdk): %v", err)
	}

	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3LivePatchEnabled = true
	configureAssistantOrderTestProvider(t, server)
	workspace := t.TempDir()
	seedSessionsV3PrimaryAuthority(t, server, workspace)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "e2e-project", Name: "E2E Project"}); err != nil {
		t.Fatal(err)
	}
	projectConversation := projectConversationRequest(t, server, p, http.MethodPost, ProjectsPath+"/e2e-project/sessions", map[string]any{"client_request_id": "e2e-project-conversation"})
	var project struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(projectConversation.Body.Bytes(), &project); err != nil || project.SessionID == "" {
		t.Fatalf("create project conversation: %s %v", projectConversation.Body.String(), err)
	}

	// Scripted provider output: fixed test data, emitted in two halves around a gate so the
	// SDK can drop its socket mid-turn. Multi-byte text exercises UTF-8 byte offsets.
	first := []string{"Stream", "ing ", "über ", "the ", "wire, "}
	rest := []string{"then ", "resum", "ing ", "after ", "a drop ", "— done."}
	continueTurn := make(chan struct{})
	var continueOnce, releaseOnce sync.Once
	runner := installSessionsV3TestProvider(server, "")
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "deterministic session titles") {
			return provideriface.Response{Text: "E2E Conversation", StopReason: "stop"}, nil
		}
		emit := func(tokens []string) {
			for _, token := range tokens {
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: token})
				}
			}
		}
		switch sdkE2ELatestMarker(req) {
		case "E2E_STREAM":
			emit(first)
			select {
			case <-continueTurn:
			case <-ctx.Done():
				return provideriface.Response{}, ctx.Err()
			case <-time.After(60 * time.Second):
				return provideriface.Response{}, errors.New("e2e driver never continued the turn")
			}
			emit(rest)
			return provideriface.Response{Text: strings.Join(first, "") + strings.Join(rest, ""), StopReason: "stop"}, nil
		case "E2E_FAIL":
			return provideriface.Response{}, errors.New("scripted provider failure")
		default:
			emit([]string{"short ", "reply"})
			return provideriface.Response{Text: "short reply", StopReason: "stop"}, nil
		}
	}
	executor := newSessionV3Executor(server)
	executor.startDelay = 0
	// Hold every durable progress write of the first turn until the driver releases it,
	// so live text is provably ahead of durable checkpoints when the socket reconnects.
	writer := newSessionsV3BlockingDurableProgressWriter(sessionV3ExecutorDurableProgressWriter{exec: executor})
	executor.durableProgressWriterForTest = writer
	server.v3SessionExecutor = executor

	mux := http.NewServeMux()
	mux.HandleFunc("/__e2e/script", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"first": first, "rest": rest})
	})
	mux.HandleFunc("/__e2e/continue", func(w http.ResponseWriter, r *http.Request) {
		continueOnce.Do(func() { close(continueTurn) })
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/__e2e/release-durable", func(w http.ResponseWriter, r *http.Request) {
		releaseOnce.Do(func() { close(writer.release) })
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	defer releaseOnce.Do(func() { close(writer.release) })
	defer continueOnce.Do(func() { close(continueTurn) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(),
		"SWARM_E2E_BASE_URL="+httpServer.URL,
		"SWARM_E2E_WORKSPACE="+workspace,
		"SWARM_E2E_PROJECT_SESSION="+project.SessionID,
	)
	output, err := cmd.CombinedOutput()
	t.Logf("SDK e2e output:\n%s", output)
	if err != nil {
		t.Fatalf("SDK e2e failed: %v", err)
	}
}

// sdkE2ELatestMarker returns the scenario marker of the newest user turn in the request.
func sdkE2ELatestMarker(req provideriface.Request) string {
	raw, _ := json.Marshal(req.Input)
	text := string(raw)
	latest, at := "", -1
	for _, marker := range []string{"E2E_STREAM", "E2E_FAIL", "E2E_SHORT"} {
		if index := strings.LastIndex(text, marker); index > at {
			latest, at = marker, index
		}
	}
	return latest
}
