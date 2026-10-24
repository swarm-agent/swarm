package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/anthropic"
	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/codex"
	"swarm/packages/swarmd/internal/provider/defaults"
	exaprovider "swarm/packages/swarmd/internal/provider/exa"
	"swarm/packages/swarmd/internal/provider/fireworks"
	"swarm/packages/swarmd/internal/provider/google"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/openai"
	"swarm/packages/swarmd/internal/provider/openrouter"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: when no model provider is connected, Swarm's onboarding offers
// every provider a fresh install can run agents with as an equal option and
// lets the person choose; ChatGPT is one option, not the default. The safety
// rules stay: no key in chat, the person signs in or adds keys themselves,
// Swarm never handles Claude credentials.
// Threat (regression): the MCP server instructions and swarm_list_providers'
// next told the delegating AI to "first offer" ChatGPT sign-in, so a person
// signed in to Claude Code with no ChatGPT credits was offered only ChatGPT.
// Owners: controlMCPCoreInstructionLines / controlMCPCall.instructions,
// controlMCPProviderOptionsText, controlMCPListProviders,
// controlMCPProviderConnect and controlMCPNoProviderNext.
// Layer: instructions are a pure function of the caller's key levels, so a
// controlMCPCall with a scoped token is the narrowest proof. The list test runs
// the real /v1/providers handler over the daemon's real provider adapters in a
// temporary store, so the connect data comes from each adapter's advertised
// auth methods, not from a fixture.

// controlMCPAgentProviders is every provider a fresh install can run agents
// with: the supported onboarding providers (Copilot stays hidden).
func controlMCPAgentProviders() []string {
	return defaults.SupportedProviders()
}

func controlMCPProviderTestCall(t *testing.T, server *Server, scopes ...string) *controlMCPCall {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request = requestWithTestPrincipalForAccount(request, testUserID, testAccountScopeID)
	request = requestWithScopedToken(request, &pebblestore.ScopedTokenRecord{Scopes: scopes})
	var next http.Handler = http.NotFoundHandler()
	if server != nil {
		next = http.HandlerFunc(server.handleProviders)
	}
	return &controlMCPCall{request: request, next: next, server: server}
}

var controlMCPChatGPTDefaultPhrases = []string{"first offer", "alternative", "only as", "instead of chatgpt"}

func assertControlMCPEqualProviderOptions(t *testing.T, what, text string, providers []string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, provider := range providers {
		if !strings.Contains(text, provider) {
			t.Fatalf("%s does not name provider %q: %s", what, provider, text)
		}
	}
	for _, phrase := range controlMCPChatGPTDefaultPhrases {
		if strings.Contains(lower, phrase) {
			t.Fatalf("%s singles out one provider (%q): %s", what, phrase, text)
		}
	}
	if !strings.Contains(lower, "ask the person which") {
		t.Fatalf("%s does not ask the person which option they have: %s", what, text)
	}
	if !strings.Contains(lower, "never ask for a key in chat") {
		t.Fatalf("%s lost the no-key-in-chat rule: %s", what, text)
	}
}

func TestControlMCPInstructionsOfferEveryProviderEqually(t *testing.T) {
	for _, tc := range []struct {
		name        string
		scopes      []string
		chatGPTTool bool
	}{
		{name: "write key", scopes: []string{"sessions:read", "swarm:read", "swarm:write"}, chatGPTTool: true},
		{name: "read key", scopes: []string{"sessions:read", "swarm:read"}, chatGPTTool: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := controlMCPProviderTestCall(t, nil, tc.scopes...).instructions()
			assertControlMCPEqualProviderOptions(t, "instructions", text, controlMCPAgentProviders())
			if !strings.Contains(text, "claude auth login") || !strings.Contains(text, "never handles Claude credentials") {
				t.Fatalf("instructions do not say how Claude Code is connected without Swarm handling Claude credentials: %s", text)
			}
			if !strings.Contains(text, "connect field") {
				t.Fatalf("instructions do not point to swarm_list_providers' connect data: %s", text)
			}
			if got := strings.Contains(text, "swarm_connect_chatgpt"); got != tc.chatGPTTool {
				t.Fatalf("swarm_connect_chatgpt named = %v, want %v (a key never sees a tool it cannot call): %s", got, tc.chatGPTTool, text)
			}
			if !tc.chatGPTTool && !strings.Contains(text, "swarmctl setup codex-login") {
				t.Fatalf("read key lost the ChatGPT option: %s", text)
			}
		})
	}
	// A key without the provider tool gets no provider line at all.
	if text := controlMCPProviderTestCall(t, nil, "sessions:read", "swarm:approve").instructions(); strings.Contains(text, "swarm_list_providers") {
		t.Fatalf("instructions name swarm_list_providers to a key that cannot call it: %s", text)
	}
}

// controlMCPConnectTestRunner stands in for each provider's registered
// runner, as the daemon registers one per agent provider (runtime/daemon.go);
// it is never asked to run a turn.
type controlMCPConnectTestRunner struct{ id string }

func (r controlMCPConnectTestRunner) ID() string { return r.id }
func (r controlMCPConnectTestRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, errors.New("not used")
}
func (r controlMCPConnectTestRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, errors.New("not used")
}

func newControlMCPProviderConnectServer(t *testing.T) (*Server, *pebblestore.AuthStore) {
	t.Helper()
	// Claude Code readiness probes a local CLI; point it at a missing file so
	// the test never runs a host binary.
	t.Setenv(claudecode.CLIPathEnv, filepath.Join(t.TempDir(), "missing-claude"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "provider-connect"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	authStore := pebblestore.NewAuthStore(store)
	// The same adapters the daemon registers (runtime/daemon.go), with one
	// runner per agent provider and none for Exa (web search only).
	providers := registry.New(
		anthropic.NewAdapter(authStore),
		claudecode.NewAdapter(authStore, claudecode.NewCLI()),
		codex.NewAdapter(authStore),
		fireworks.NewAdapter(authStore),
		google.NewAdapter(authStore),
		openai.NewAdapter(authStore),
		openrouter.NewAdapter(authStore),
		google.NewVertexAdapter(authStore, google.NewApplicationDefaultCredentials()),
		exaprovider.NewAdapter(authStore),
	)
	for _, id := range controlMCPAgentProviders() {
		providers.RegisterRunner(controlMCPConnectTestRunner{id: id})
	}
	return &Server{auth: auth.NewService(authStore, events), providers: providers}, authStore
}

type controlMCPListedProviders struct {
	Next      string           `json:"next"`
	Providers []map[string]any `json:"providers"`
}

func controlMCPListProvidersForTest(t *testing.T, server *Server, scopes ...string) (controlMCPListedProviders, map[string]map[string]any) {
	t.Helper()
	result, err := controlMCPListProviders(controlMCPProviderTestCall(t, server, scopes...), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var out controlMCPListedProviders
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, entry := range out.Providers {
		byID[entry["provider"].(string)] = entry
	}
	return out, byID
}

func controlMCPConnectSteps(t *testing.T, provider string, connect map[string]any) string {
	t.Helper()
	raw, _ := connect["steps"].([]any)
	if len(raw) == 0 {
		t.Fatalf("%s connect has no steps: %v", provider, connect)
	}
	steps := make([]string, 0, len(raw))
	for _, step := range raw {
		text, _ := step.(string)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("%s connect has an empty step: %v", provider, connect)
		}
		steps = append(steps, text)
	}
	return strings.Join(steps, "\n")
}

func TestControlMCPListProvidersReturnsConnectMethodForEveryUnconnectedProvider(t *testing.T) {
	server, _ := newControlMCPProviderConnectServer(t)
	agentProviders := controlMCPAgentProviders()
	wantMethod := map[string]string{
		"anthropic":  controlMCPConnectAPIKey,
		"claudecode": controlMCPConnectClaudeCodeLogin,
		"codex":      controlMCPConnectSignIn,
		"fireworks":  controlMCPConnectAPIKey,
		"google":     controlMCPConnectAPIKey,
		"openai":     controlMCPConnectAPIKey,
		"openrouter": controlMCPConnectAPIKey,
		"vertex":     controlMCPConnectMachineIdentity,
	}
	wantIDs := make([]string, 0, len(wantMethod))
	for id := range wantMethod {
		wantIDs = append(wantIDs, id)
	}
	sort.Strings(wantIDs)
	if !slices.Equal(agentProviders, wantIDs) {
		t.Fatalf("agent providers = %v, this test covers %v; cover every connectable provider", agentProviders, wantIDs)
	}

	for _, tc := range []struct {
		name     string
		scopes   []string
		keyTool  bool
		chatTool bool
	}{
		// Write may request a human-entered key; levelAllows must keep read clients out.
		{name: "write key", scopes: []string{"sessions:read", "swarm:read", "swarm:write"}, chatTool: true, keyTool: true},
		{name: "read key", scopes: []string{"sessions:read", "swarm:read"}},
		{name: "full key", scopes: []string{"sessions:read", "swarm:read", "swarm:write", "swarm:approve", "swarm:manage"}, chatTool: true, keyTool: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, byID := controlMCPListProvidersForTest(t, server, tc.scopes...)
			assertControlMCPEqualProviderOptions(t, "next", out.Next, agentProviders)
			if strings.Contains(out.Next, "exa (") {
				t.Fatalf("next offers exa, which cannot run agents: %s", out.Next)
			}
			for _, id := range agentProviders {
				entry := byID[id]
				if entry == nil {
					t.Fatalf("provider %s missing from %v", id, out.Providers)
				}
				if ready, _ := entry["ready"].(bool); ready {
					t.Fatalf("%s ready on an empty store", id)
				}
				if runs, _ := entry["runs_agents"].(bool); !runs {
					t.Fatalf("%s runs_agents = false: %v", id, entry)
				}
				connect, _ := entry["connect"].(map[string]any)
				if connect == nil {
					t.Fatalf("unconnected provider %s has no connect method: %v", id, entry)
				}
				if connect["method"] != wantMethod[id] {
					t.Fatalf("%s connect method = %v, want %s", id, connect["method"], wantMethod[id])
				}
				if needs, _ := connect["needs"].(string); strings.TrimSpace(needs) == "" {
					t.Fatalf("%s connect does not say what it needs: %v", id, connect)
				}
				steps := controlMCPConnectSteps(t, id, connect)
				tool, _ := connect["tool"].(string)
				switch wantMethod[id] {
				case controlMCPConnectSignIn:
					if want := map[bool]string{true: "swarm_connect_chatgpt", false: ""}[tc.chatTool]; tool != want {
						t.Fatalf("codex tool = %q, want %q", tool, want)
					}
					if !strings.Contains(steps, "swarmctl setup codex-login") {
						t.Fatalf("codex steps lack the on-machine sign-in: %s", steps)
					}
				case controlMCPConnectClaudeCodeLogin:
					if want := map[bool]string{true: "swarm_connect_claude_code", false: ""}[tc.chatTool]; tool != want || strings.Contains(steps, "swarm_connect_claude_code") != tc.chatTool {
						t.Fatalf("claudecode tool = %q, want %q (a key never sees a tool it cannot call): %s", tool, want, steps)
					}
					if !strings.Contains(steps, "install.sh claude-code") || !strings.Contains(steps, "claude auth login") || !strings.Contains(steps, "Claude Code CLI login") {
						t.Fatalf("claudecode connect must be the person's own Claude Code sign-in plus the account opt-in: %v", connect)
					}
					if strings.Contains(strings.ToLower(steps), "api key") || strings.Contains(steps, "--api-key-stdin") {
						t.Fatalf("claudecode connect asks for a key: %s", steps)
					}
				case controlMCPConnectMachineIdentity:
					if tool != "" || !strings.Contains(steps, "--provider vertex --project PROJECT") || strings.Contains(steps, "--api-key-stdin") {
						t.Fatalf("vertex connect = %v", connect)
					}
				case controlMCPConnectAPIKey:
					if want := map[bool]string{true: "swarm_request_provider_key", false: ""}[tc.keyTool]; tool != want {
						t.Fatalf("%s tool = %q, want %q", id, tool, want)
					}
					// The provider-connect boundary must offer both first-run and
					// additional-key routes, without asking for secrets in chat.
					if !strings.Contains(steps, "setup credential --provider "+id+" --api-key-stdin") || !strings.Contains(steps, "Next to another provider: sudo swarmctl setup add-credential --provider "+id+" --api-key-stdin < key-file (on a box from install.sh: install.sh key "+id+").") {
						t.Fatalf("%s steps lack the person's own on-machine key entry: %s", id, steps)
					}
				}
			}
			exa := byID["exa"]
			if exa == nil {
				t.Fatal("exa missing")
			}
			if runs, _ := exa["runs_agents"].(bool); runs {
				t.Fatalf("exa runs_agents = true")
			}
			if connect, _ := exa["connect"].(map[string]any); connect == nil || connect["note"] == nil {
				t.Fatalf("exa connect does not say it cannot run agents: %v", exa)
			}
		})
	}
}

func TestControlMCPListProvidersOmitsConnectForAConnectedProvider(t *testing.T) {
	server, authStore := newControlMCPProviderConnectServer(t)
	// Obviously fake key; no provider is contacted (Status reads the store).
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		Provider: "anthropic", AccountScopeID: testAccountScopeID, Type: pebblestore.AuthTypeAPI,
		APIKey: "fake-test-key-not-real-0001", SetActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	out, byID := controlMCPListProvidersForTest(t, server, "sessions:read", "swarm:read", "swarm:write")
	if ready, _ := byID["anthropic"]["ready"].(bool); !ready {
		t.Fatalf("anthropic not ready with an active key: %v", byID["anthropic"])
	}
	if _, ok := byID["anthropic"]["connect"]; ok {
		t.Fatalf("connected provider still offers a connect method: %v", byID["anthropic"])
	}
	if out.Next != "" {
		t.Fatalf("next = %q with a provider ready", out.Next)
	}
	if connect, _ := byID["claudecode"]["connect"].(map[string]any); connect == nil {
		t.Fatalf("an unconnected provider lost its connect method once another was ready: %v", byID["claudecode"])
	}
	if strings.Contains(controlMCPProviderTestJSON(t, out), "fake-test-key-not-real-0001") {
		t.Fatal("list_providers leaked key material")
	}
}

func controlMCPProviderTestJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Purpose: controlMCPProviderConnectMethod and controlMCPProviderOptionsText must
// distinguish headless Providers from desktop Auth. Pure output tests are the
// narrowest layer to catch misleading navigation in every settings-based method.
func TestProviderConnectSettingsNavigation(t *testing.T) {
	const want = "Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth)"
	for _, tc := range []struct {
		id    string
		types []string
	}{
		{"openai", []string{"api"}},
		{"codex", []string{"oauth"}},
		{"claudecode", []string{"cli"}},
		{"unknown", nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			connect := controlMCPProviderConnectMethod(tc.id, tc.types, func(string) bool { return false })
			steps := strings.Join(connect["steps"].([]string), "\n")
			if !strings.Contains(steps, want) || strings.Contains(steps, "setup page or") {
				t.Fatalf("incorrect settings navigation: %s", steps)
			}
		})
	}
	if text := controlMCPProviderOptionsText("sign in"); strings.Count(text, want) != 2 {
		t.Fatalf("overview lacks headless and desktop navigation: %s", text)
	}
}

// Purpose: handleProviders and Registry.ListStatuses must serialize an explicit
// runs_agents capability, including false for search-only providers, independently
// of readiness. The HTTP handler with real adapters is the narrowest layer that
// catches omitted JSON fields and prevents unconnected model providers being
// mislabeled as search providers; no provider calls or credentials are needed.
func TestProvidersReturnsRunsAgents(t *testing.T) {
	server, _ := newControlMCPProviderConnectServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/providers", nil)
	req = requestWithTestPrincipalForAccount(req, testUserID, testAccountScopeID)
	response := httptest.NewRecorder()
	server.handleProviders(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, status := range body.Providers {
		id := status["id"].(string)
		seen[id] = true
		runs, present := status["runs_agents"].(bool)
		if !present || runs != (id != "exa") {
			t.Fatalf("%s runs_agents = %v (boolean present: %v)", id, status["runs_agents"], present)
		}
		if status["ready"] != false || status["runnable"] != false {
			t.Fatalf("unconnected provider unexpectedly ready/runnable: %v", status)
		}
	}
	for _, id := range append(controlMCPAgentProviders(), "exa") {
		if !seen[id] {
			t.Fatalf("provider %s missing", id)
		}
	}
}
