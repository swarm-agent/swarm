package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Requirement: Orchestrator's explicit media grant admits image bytes only at
// the catalog/adapter intersection. CompileSessionMediaContract and
// sessionsV3ProviderInputWithMedia own admission and durable asset assembly.
// This hermetic API-boundary test prevents metadata-only vision regressions and
// capability bypasses, and checks ownership/digest rejection without providers.
func TestOrchestratorProviderInputUsesCapabilityGatedDurableImages(t *testing.T) {
	fixture := newRoutedMediaTestFixture(t)
	staged := fixture.stage(t, fixture.principal.AccountScopeID, "orchestrator-media")
	response := fixture.post(t, fixture.principal.AccountScopeID, "orchestrator-media", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if response.Code != http.StatusOK {
		t.Fatalf("retain image: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	session, found, err := fixture.sessions.GetSession(body.SessionID)
	if err != nil || !found {
		t.Fatalf("get session: found=%v err=%v", found, err)
	}
	// Retention must follow real isolated-lane admission, never a fixture-only
	// bypass of Git provenance or the durable session owner/account binding.
	if !session.WorktreeEnabled || session.AccountScopeID != fixture.principal.AccountScopeID || session.UserID != fixture.principal.UserID || session.Metadata["swarm_v3_worktree_owner_session_id"] != session.ID {
		t.Fatalf("invalid owned session binding: %+v", session)
	}
	base, _ := session.Metadata["base_commit"].(string)
	if err := worktreeruntime.ValidateOwnedIdentity(session.WorkspacePath, session.WorktreeRootPath, session.WorktreeBranch, base); err != nil {
		t.Fatalf("invalid isolated Git lane: %v", err)
	}
	messages, err := fixture.sessions.ListSessionMessages(body.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 {
		t.Fatalf("durable messages: %+v err=%v", messages, err)
	}
	reference := messages[0].Media[0]
	_, originalBytes, err := fixture.sessions.ReadSessionMediaAsset(session.AccountScopeID, session.ID, reference.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agentruntime.BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	legacy := agentruntime.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	delete(legacy.ToolContract.Tools, "media_inspect")
	profile, err := registry.ReconcileSnapshot(agentruntime.SwarmOrchestratorAgentID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai vision", "codex vision", "unknown model", "text-only model", "surface mismatch", "unsupported adapter", "unauthorized profile"} {
		t.Run(name, func(t *testing.T) {
			adapter := fixture.runner.declaration
			if name == "codex vision" {
				adapter.AdapterID = provideriface.MediaAdapterIDCodexChatGPTV1
				adapter.ProviderID = "codex"
				adapter.ProviderSurface = provideriface.MediaProviderSurfaceCodexChatGPT
				adapter.CredentialSurface = provideriface.MediaCredentialSurfaceCodexOAuth
			}
			catalog := &pebblestore.ModelCatalogRecord{
				Provider: adapter.ProviderID, Model: "fixture-model", SourceSnapshotID: "snapshot", SourceSnapshotVersion: "v1",
				Media: &pebblestore.ModelCatalogMediaCapabilities{
					State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: adapter.ProviderSurface, CredentialSurface: adapter.CredentialSurface,
					Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}},
				},
			}
			authorized := runruntime.AgentProfileAuthorizesMedia(profile)
			switch name {
			case "unknown model":
				catalog = nil
			case "text-only model":
				catalog.Media.Inputs[0].State = pebblestore.ModelCatalogMediaStateUnsupported
			case "surface mismatch":
				catalog.Media.CredentialSurface = "different-surface"
			case "unsupported adapter":
				adapter.AdapterID = "unreviewed-adapter"
			case "unauthorized profile":
				authorized = runruntime.AgentProfileAuthorizesMedia(legacy)
			}
			contract := runruntime.CompileSessionMediaContract(runruntime.SessionMediaContractInput{
				ProviderID: adapter.ProviderID, Model: "fixture-model", Catalog: catalog,
				CatalogMeta: &pebblestore.ModelCatalogMeta{SnapshotID: "snapshot", SnapshotVersion: "v1"},
				Adapter: adapter, AgentAuthorized: authorized, ExecutionMode: "auto", WorkspaceScope: session.WorkspacePath, SessionScope: session.ID,
			})
			resolved := sessionV3ResolvedRuntime{Session: session, AgentProfile: profile, MediaContract: contract}
			input, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(resolved, messages, sessionsV3ProviderInputOptions{})
			if err != nil || len(input) != 1 {
				t.Fatalf("assemble input: %+v err=%v", input, err)
			}
			content, ok := input[0]["content"].([]map[string]any)
			if !ok {
				t.Fatalf("unexpected content: %+v", input)
			}
			var payloads []provideriface.SessionMediaPayload
			for _, item := range content {
				if item["type"] == "session_media" {
					payloads = append(payloads, item["media"].(provideriface.SessionMediaPayload))
				}
			}
			allowed := name == "openai vision" || name == "codex vision"
			encoded := string(mustJSON(t, input))
			if !allowed {
				if len(payloads) != 0 || !strings.Contains(encoded, "model perception not supported") || !strings.Contains(encoded, reference.AssetID) {
					t.Fatalf("denied model must receive retained metadata only: %s", encoded)
				}
				return
			}
			if len(payloads) != 1 || !bytes.Equal(payloads[0].Bytes, originalBytes) || payloads[0].AssetID != reference.AssetID || payloads[0].DigestSHA256 != reference.DigestSHA256 || payloads[0].Size != reference.Size || payloads[0].MIMEType != "image/png" || strings.Contains(encoded, "model perception not supported") {
				t.Fatalf("supported model did not receive exact image payload: %+v", input)
			}
			if !runruntime.SessionMediaContractAllows(contract, "image", "image/png", "png") || runruntime.SessionMediaContractAllows(contract, "audio", "audio/mpeg", "") || runruntime.SessionMediaContractAllows(contract, "image", "image/jpeg", "") {
				t.Fatal("capability intersection was broadened or lost")
			}
			for _, capability := range contract.Capabilities {
				if capability.State == provideriface.MediaCapabilityStateAllowed && (capability.MaxBytes != 1024 || capability.MaxCount != 2) {
					t.Fatalf("adapter limits lost: %+v", capability)
				}
			}
			wrongAccount := resolved
			wrongAccount.Session.AccountScopeID = "different-account"
			if rejected, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(wrongAccount, messages, sessionsV3ProviderInputOptions{}); err == nil || rejected != nil {
				t.Fatalf("cross-account image admitted: %+v err=%v", rejected, err)
			}
			wrongSession := resolved
			wrongSession.Session.ID = "different-session"
			if rejected, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(wrongSession, messages, sessionsV3ProviderInputOptions{}); err == nil || rejected != nil {
				t.Fatalf("cross-session image admitted: %+v err=%v", rejected, err)
			}
			tampered := append([]pebblestore.MessageSnapshot(nil), messages...)
			tampered[0].Media = append([]pebblestore.SessionMediaReference(nil), messages[0].Media...)
			tampered[0].Media[0].DigestSHA256 = "forged-digest"
			if rejected, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(resolved, tampered, sessionsV3ProviderInputOptions{}); err == nil || rejected != nil {
				t.Fatalf("tampered image admitted: %+v err=%v", rejected, err)
			}
		})
	}
	after, err := fixture.sessions.ListSessionMessages(session.ID, 0, 10)
	if err != nil || !bytes.Equal(mustJSON(t, messages), mustJSON(t, after)) {
		t.Fatalf("provider assembly mutated durable references: %+v err=%v", after, err)
	}
}
