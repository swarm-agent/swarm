package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Only the scope authority is exposed, deliberately not the tool compiler.
type capabilityScopeOnly struct {
	runService
	resolve func(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error)
}

func (r capabilityScopeOnly) ResolveRuntimeWorkspaceScope(s pebblestore.SessionSnapshot, p identity.Principal) (tool.WorkspaceScope, error) {
	return r.resolve(s, p)
}

type capabilityPoisonExecution struct{ capabilityScopeOnly }

func (capabilityPoisonExecution) CompileStoredV3AgentToolContract(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error) {
	panic("capability read compiled execution tools")
}
func (capabilityPoisonExecution) ComposeRuntimeInstructions(tool.WorkspaceScope, string, bool, pebblestore.AgentProfile, string) string {
	panic("capability read composed prompts")
}
func (capabilityPoisonExecution) ComposeDurableRunStateInstructions(string, string, string, *runruntime.RunPlanCheckpointContext) (string, error) {
	panic("capability read composed durable instructions")
}

func capabilityReadFixture(t *testing.T) (*Server, pebblestore.SessionSnapshot, *sessionsV3RecordingProviderRunner) {
	t.Helper()
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	declaration := provideriface.MediaAdapterDeclaration{
		AdapterID: provideriface.MediaAdapterIDCodexChatGPTV1, ProviderID: "codex",
		ProviderSurface: provideriface.MediaProviderSurfaceCodexChatGPT, CredentialSurface: provideriface.MediaCredentialSurfaceCodexOAuth,
		CredentialFingerprint: "capability-test",
		Inputs:                []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}},
	}
	provider := &sessionsV3RecordingProviderRunner{id: "codex", mediaDeclaration: &declaration}
	server.providers = registry.New()
	server.providers.RegisterRunner(provider)
	// Swarm creation captures the canonical account slots, not the request's
	// preference. Configure both slots before creating the session.
	for _, slot := range []string{"action", "plan"} {
		if _, err := server.agentModelSettings.UpdateSwarmSlot(identity.ContextWithPrincipal(context.Background(), testPrincipal()), slot, pebblestore.AgentModelAssignment{Provider: "codex", Model: "gpt-5.6-sol", Thinking: "high"}); err != nil {
			t.Fatal(err)
		}
	}
	// EnsureBootDefaults in the server fixture seeds the checked-in catalog.
	// Assert the exact selected model exists before testing its intersection.
	catalog, err := server.model.GetCatalog("codex", "gpt-5.6-sol")
	if err != nil || !catalog.Found || catalog.Meta == nil {
		t.Fatalf("fixture catalog missing: %+v err=%v", catalog, err)
	}
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "capability-read", "capability", t.TempDir(), pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.6-sol", Thinking: "high"})
	return server, created, provider
}

// Requirement: sessionsV3MediaContract must use the same current contract/hash as
// resolveSessionV3Runtime, without execution setup or durable mutations. Threat:
// expensive compiler/prompt work on GET and divergence in attachment authority.
// The API resolver with a real temporary store is the narrowest shared boundary;
// poison methods prove absence of execution calls, not a timing approximation.
func TestSessionsV3CapabilitiesReadParityWithoutExecution(t *testing.T) {
	server, created, _ := capabilityReadFixture(t)
	exec := &sessionV3Executor{server: server}
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: "capability-parity"}
	full, err := exec.resolveSessionV3Runtime(job)
	if err != nil {
		t.Fatal(err)
	}
	if full.Preference.Provider != "codex" || full.Preference.Model != "gpt-5.6-sol" || full.MediaContract.ProviderID != "codex" || full.MediaContract.Model != "gpt-5.6-sol" {
		t.Fatalf("canonical model decision changed: %+v %+v", full.Preference, full.MediaContract)
	}
	if !runruntime.SessionMediaContractAllows(full.MediaContract, "image", "image/png", "") {
		t.Fatalf("fixture must admit an image: %+v", full.MediaContract)
	}
	before, _, err := server.sessions.GetSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := server.runner
	scope := base.(interface {
		ResolveRuntimeWorkspaceScope(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error)
	})
	withoutCompiler := capabilityScopeOnly{runService: base, resolve: scope.ResolveRuntimeWorkspaceScope}
	for _, runner := range []runService{withoutCompiler, capabilityPoisonExecution{withoutCompiler}} {
		server.runner = runner
		server.v3SessionExecutor = nil
		read, err := server.sessionsV3MediaContract(testPrincipal(), created)
		if err != nil || !reflect.DeepEqual(read, full.MediaContract) {
			t.Fatalf("read/execution contract mismatch: err=%v read=%+v execution=%+v", err, read, full.MediaContract)
		}
	}
	server.runner = withoutCompiler
	if _, err := exec.resolveSessionV3Runtime(job); err == nil || !strings.Contains(err.Error(), "compiler") {
		t.Fatalf("execution without compiler must fail: %v", err)
	}
	after, _, err := server.sessions.GetSession(created.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("capability resolution mutated session: %v", err)
	}
	if len(full.Tools) == 0 || full.Instructions == "" {
		t.Fatal("execution no longer constructs tools/instructions")
	}
}

// Requirement: account, scope and provider authority failures must fail closed
// before exposure, with no contract or durable mutation. The resolver/handler
// boundary proves negative postconditions without a provider request or daemon.
func TestSessionsV3CapabilitiesAuthorityFailures(t *testing.T) {
	server, created, _ := capabilityReadFixture(t)
	exec := &sessionV3Executor{server: server}
	before, _, _ := server.sessions.GetSession(created.ID)
	for _, account := range []string{"foreign-account", ""} {
		principal := testPrincipal()
		principal.AccountScopeID = account
		got, err := exec.resolveSessionV3Capabilities(sessionV3ExecutorJob{Principal: principal, SessionID: created.ID})
		if err == nil || got.MediaContract.Hash != "" {
			t.Fatalf("account %q got authority: %+v %v", account, got, err)
		}
	}
	base := server.runner
	server.runner = capabilityScopeOnly{runService: base, resolve: func(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error) {
		return tool.WorkspaceScope{}, errors.New("stale workspace generation")
	}}
	if got, err := server.sessionsV3MediaContract(testPrincipal(), created); err == nil || got.Hash != "" {
		t.Fatalf("stale scope admitted: %+v %v", got, err)
	}
	server.runner = nil
	recorder := httptest.NewRecorder()
	server.handleSessionV3MediaCapability(recorder, httptest.NewRequest("GET", "/", nil), testPrincipal(), created.ID)
	if recorder.Code != 503 || !strings.Contains(recorder.Body.String(), "scope resolver") {
		t.Fatalf("resolution failure hidden: %d %s", recorder.Code, recorder.Body.String())
	}
	server.runner = base
	server.providers = registry.New()
	if got, err := server.sessionsV3MediaContract(testPrincipal(), created); err == nil || got.Hash != "" {
		t.Fatalf("unregistered provider admitted: %+v %v", got, err)
	}
	server.providers = nil
	if got, err := server.sessionsV3MediaContract(testPrincipal(), created); err == nil || got.Hash != "" {
		t.Fatalf("missing registry admitted: %+v %v", got, err)
	}
	after, _, err := server.sessions.GetSession(created.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed resolution mutated session: %v", err)
	}
}

// Requirement: catalog/adapter contradictions and agent denial intersect rather
// than grant authority; mutable inputs must not reuse a cached contract. This
// shared compiler test isolates intersection from unrelated execution tools.
func TestSessionsV3CapabilitiesIntersectionAndFreshness(t *testing.T) {
	server, created, provider := capabilityReadFixture(t)
	exec := &sessionV3Executor{server: server}
	resolved, err := exec.resolveSessionV3Capabilities(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	original := resolved.MediaContract
	if !runruntime.SessionMediaContractAllows(original, "image", "image/png", "") {
		t.Fatalf("intersection fixture must first grant image authority: %+v", original)
	}
	for _, kind := range []string{"agent", "catalog", "adapter", "model"} {
		t.Run(kind, func(t *testing.T) {
			input := resolved
			declaration := *provider.mediaDeclaration
			defer func() { provider.mediaDeclaration = &declaration }()
			switch kind {
			case "agent":
				input.AgentProfile.ToolContract = &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"media_inspect": {Enabled: pebblestore.BoolPtr(false)}}}
			case "catalog":
				record := input.ModelCatalog.(pebblestore.ModelCatalogRecord)
				record.Provider = "foreign-provider"
				input.ModelCatalog = record
			case "adapter":
				bad := declaration
				bad.ProviderSurface = "foreign-surface"
				provider.mediaDeclaration = &bad
			case "model":
				input.Preference.Model = "different-model"
			}
			got, err := exec.compileSessionV3MediaContract(testPrincipal(), input)
			if err != nil {
				t.Fatal(err)
			}
			if runruntime.SessionMediaContractAllows(got, "image", "image/png", "") || len(got.DenialReasons) == 0 || got.Hash == original.Hash {
				t.Fatalf("%s reused/granted authority: %+v", kind, got)
			}
		})
	}
}

// Requirement: mutable saved-agent media authorization is read per request and
// account, without EnsureDefaults writes; malformed/reserved snapshots must not
// become capability authority. Direct reconciliation tests the narrowest owner
// of this boundary and compares saved profiles before/after the read.
func TestSessionsV3CapabilitiesCurrentProfileReadOnly(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	account := testPrincipal().AccountScopeID
	input := agentruntime.UpsertInput{
		Name: "capability-custom", Mode: agentruntime.ModeSubagent, Prompt: "test profile",
		RuntimeMode: pebblestore.AgentRuntimeModeReadWrite, ExitPlanModeEnabled: pebblestore.BoolPtr(false), Enabled: pebblestore.BoolPtr(true),
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"media_inspect": {Enabled: pebblestore.BoolPtr(true)}}},
	}
	snapshot, _, _, err := server.agents.UpsertForAccount(account, input)
	if err != nil {
		t.Fatal(err)
	}
	exec := &sessionV3Executor{server: server}
	for _, enabled := range []bool{true, false} {
		input.ToolContract.Tools["media_inspect"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(enabled)}
		if _, _, _, err := server.agents.UpsertForAccount(account, input); err != nil {
			t.Fatal(err)
		}
		before, _, _ := server.agents.GetProfileForAccount(account, input.Name)
		got, err := exec.resolveSessionV3CurrentAgentToolContract(account, nil, snapshot)
		if err != nil || runruntime.AgentProfileAuthorizesMedia(got) != enabled {
			t.Fatalf("current authorization not observed: enabled=%v got=%+v err=%v", enabled, got, err)
		}
		after, _, _ := server.agents.GetProfileForAccount(account, input.Name)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("profile resolution mutated saved authority")
		}
	}
	if _, err := exec.resolveSessionV3CurrentAgentToolContract("foreign-account", nil, snapshot); err == nil {
		t.Fatal("foreign account obtained saved profile")
	}
	input.Enabled = pebblestore.BoolPtr(false)
	if _, _, _, err := server.agents.UpsertForAccount(account, input); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.resolveSessionV3CurrentAgentToolContract(account, nil, snapshot); err == nil {
		t.Fatal("disabled current profile admitted stale enabled snapshot")
	}
	for _, name := range []string{"", "system-unknown"} {
		invalid := snapshot
		invalid.Name = name
		if _, err := exec.resolveSessionV3CurrentAgentToolContract(account, nil, invalid); err == nil {
			t.Fatalf("invalid snapshot %q admitted", name)
		}
	}
}

// Requirement: a previously hydrated snapshot must not pin media authority after
// a canonical preference mutation. Threat: stale model capability tokens after
// model switching. The real session mutation plus both resolvers is the narrowest
// layer proving reads reload the preference and agree with execution.
func TestSessionsV3CapabilitiesReloadPreference(t *testing.T) {
	server, created, _ := capabilityReadFixture(t)
	first, err := server.sessionsV3MediaContract(testPrincipal(), created)
	if err != nil {
		t.Fatal(err)
	}
	provider, model := "test-provider", "test-model"
	server.providers.RegisterRunner(&sessionsV3RecordingProviderRunner{})
	current, ok, err := server.sessions.GetSession(created.ID)
	if err != nil || !ok || current.ModelProfile == nil {
		t.Fatalf("missing canonical model profile: %v", err)
	}
	current.ModelProfile.Action = pebblestore.ModelProfileSelection{Provider: provider, Model: model}
	key := "capability-model-switch"
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: current.ID, UserID: current.UserID, AccountScopeID: current.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: sessionruntime.SessionMutationUpdateModelProfile, Session: &current,
	}); err != nil {
		t.Fatal(err)
	}
	// Deliberately supply the old snapshot; only its identity may be consumed.
	read, err := server.sessionsV3MediaContract(testPrincipal(), created)
	if err != nil {
		t.Fatal(err)
	}
	full, err := (&sessionV3Executor{server: server}).resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, full.MediaContract) || read.ProviderID != provider || read.Model != model || read.Hash == first.Hash || runruntime.SessionMediaContractAllows(read, "image", "image/png", "") {
		t.Fatalf("stale preference authority: first=%+v read=%+v execution=%+v", first, read, full.MediaContract)
	}
}

// Requirement: media decisions retain their model, mode and workspace/session
// bindings across permission bypass and Plan/Action selection. Threat: reads
// granting authority that the pre-extraction execution compiler would reject,
// or bypass stripping media scope. Real scope/profile resolution plus the old
// tool compiler is the narrowest independent authority comparison; no run starts.
func TestSessionsV3CapabilitiesModeScopeAndBypassParity(t *testing.T) {
	server, created, _ := capabilityReadFixture(t)
	exec := &sessionV3Executor{server: server}
	compiler := server.runner.(interface {
		CompileStoredV3AgentToolContract(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error)
	})
	for _, mode := range []string{sessionruntime.ModeAuto, sessionruntime.ModePlan} {
		for _, bypass := range []bool{false, true} {
			server.perm.(*permission.Service).SetBypassPermissions(bypass)
			if _, _, err := server.sessions.SetMode(created.ID, mode); err != nil {
				t.Fatal(err)
			}
			before, _, err := server.sessions.GetSession(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID}
			resolved, err := exec.resolveSessionV3Capabilities(job)
			if err != nil {
				t.Fatal(err)
			}
			legacy, _, err := compiler.CompileStoredV3AgentToolContract(created.AccountScopeID, resolved.AgentProfile)
			if err != nil || !legacy.Tools["media_inspect"].Enabled {
				t.Fatalf("independent execution authority denied fixture: %+v %v", legacy, err)
			}
			if resolved.Scope.PrimaryPath == "" || resolved.Session.ID != created.ID || resolved.Session.Mode != mode || resolved.Preference.Model != "gpt-5.6-sol" {
				t.Fatalf("lost model/mode/scope binding: %+v", resolved)
			}
			read, err := server.sessionsV3MediaContract(testPrincipal(), created)
			if err != nil || !runruntime.SessionMediaContractAllows(read, "image", "image/png", "") {
				t.Fatalf("mode=%s bypass=%v read denied: %+v %v", mode, bypass, read, err)
			}
			full, err := exec.resolveSessionV3Runtime(job)
			if err != nil || !reflect.DeepEqual(read, full.MediaContract) {
				t.Fatalf("mode=%s bypass=%v parity: %+v %v", mode, bypass, full.MediaContract, err)
			}
			after, _, err := server.sessions.GetSession(created.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("resolvers mutated canonical state: %v", err)
			}
		}
	}
}
