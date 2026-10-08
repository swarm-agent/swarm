package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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
				Adapter:     adapter, AgentAuthorized: authorized, ExecutionMode: "auto", WorkspaceScope: session.WorkspacePath, SessionScope: session.ID,
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

// Requirement: authenticated project conversations have no filesystem scope but
// must perceive retained images at the selected catalog/adapter intersection.
// resolveSessionV3Runtime owns profile reconciliation, scope authorization and
// tool materialization; provider input assembly owns durable byte verification.
// This API integration layer catches the missing-project-scope regression that
// direct contract fixtures with a workspace cannot exercise.
func TestProjectOrchestratorRuntimeMediaWithoutWorkspace(t *testing.T) {
	for _, provider := range []string{"openai", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newRoutedMediaTestFixture(t)
			if provider == "codex" {
				f.runner.id = provider
				f.runner.declaration.ProviderID = provider
				f.runner.declaration.AdapterID = provideriface.MediaAdapterIDCodexChatGPTV1
				f.runner.declaration.ProviderSurface = provideriface.MediaProviderSurfaceCodexChatGPT
				f.runner.declaration.CredentialSurface = provideriface.MediaCredentialSurfaceCodexOAuth
				f.server.providers.RegisterRunner(f.runner)
			}
			settings := testAgentModelSettingsRecord(f.principal.AccountScopeID)
			settings.Swarm.Action = pebblestore.AgentModelAssignment{Provider: provider, Model: "gpt-media", Thinking: "medium"}
			settings.Swarm.Plan = settings.Swarm.Action
			if _, err := pebblestore.NewAgentModelSettingsStore(f.store).PutForAccount(settings); err != nil {
				t.Fatal(err)
			}
			catalog := pebblestore.NewModelCatalogStore(f.store)
			record := pebblestore.ModelCatalogRecord{
				Provider: provider, Model: "gpt-media", SourceSnapshotID: "media-snapshot", SourceSnapshotVersion: "v1",
				Media: &pebblestore.ModelCatalogMediaCapabilities{
					State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: f.runner.declaration.ProviderSurface, CredentialSurface: f.runner.declaration.CredentialSurface,
					Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}},
				},
			}
			if err := catalog.SetRecord(record); err != nil {
				t.Fatal(err)
			}
			if err := f.sessions.Store().PutProject(f.principal.AccountScopeID, &pebblestore.ProjectRecord{ID: "vision-project", Name: "Vision"}); err != nil {
				t.Fatal(err)
			}
			created := projectConversationRequest(t, f.server, f.principal, http.MethodPost, ProjectsPath+"/vision-project/sessions", map[string]any{"client_request_id": "vision"})
			var body struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil || created.Code != http.StatusOK || body.SessionID == "" {
				t.Fatalf("create project conversation: %d %s (%v)", created.Code, created.Body.String(), err)
			}
			session, found, err := f.sessions.GetSession(body.SessionID)
			if err != nil || !found || session.WorkspacePath != "" || session.WorktreeEnabled {
				t.Fatalf("project unexpectedly has workspace authority: %+v %v", session, err)
			}
			legacy, err := sessionV3AgentProfileFromMetadata(session.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			delete(legacy.ToolContract.Tools, "media_inspect")
			session.Metadata["agent_profile"] = legacy
			if _, _, err := f.sessions.UpdateMetadata(session.ID, session.Metadata); err != nil {
				t.Fatal(err)
			}
			asset, _, err := f.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
				AccountScopeID: f.principal.AccountScopeID, SessionID: session.ID, Modality: "image", DeclaredMIMEType: "image/png", FileName: "image.png", Reader: bytes.NewReader(mediaStagingAPIPNG),
			})
			if err != nil {
				t.Fatal(err)
			}
			reference := pebblestore.SessionMediaReference{AssetID: asset.ID, Modality: asset.Modality, MIMEType: asset.DetectedMIMEType, FileType: asset.FileType, Size: asset.Size, DigestSHA256: asset.DigestSHA256, ContractHash: asset.ContractHash}
			if _, err := f.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
				SessionID: session.ID, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID,
				ClientRequestID: "image-message", PayloadHash: "image-message", Kind: pebblestore.V3SessionMutationAppendMessage,
				Message: &pebblestore.MessageSnapshot{Role: "user", Content: "inspect", Media: []pebblestore.SessionMediaReference{reference}},
			}); err != nil {
				t.Fatal(err)
			}
			messages, err := f.sessions.ListSessionMessages(session.ID, 0, 10)
			if err != nil || len(messages) != 1 {
				t.Fatalf("retained messages: %+v %v", messages, err)
			}
			job := sessionV3ExecutorJob{SessionID: session.ID, RunID: "vision-run", Principal: f.principal}
			executor := f.server.v3SessionExecutor
			for _, state := range []string{"supported", "text-only", "unknown", "credential mismatch"} {
				t.Run(state, func(t *testing.T) {
					record := record
					media := *record.Media
					media.Inputs = append([]pebblestore.ModelCatalogMediaDirection(nil), media.Inputs...)
					record.Media = &media
					switch state {
					case "text-only":
						record.Media.Inputs[0].State = pebblestore.ModelCatalogMediaStateUnsupported
					case "unknown":
						record.Media = nil
					case "credential mismatch":
						record.Media = &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: f.runner.declaration.ProviderSurface, CredentialSurface: "wrong"}
					}
					if err := catalog.SetRecord(record); err != nil {
						t.Fatal(err)
					}
					resolved, err := executor.resolveSessionV3Runtime(job)
					if err != nil {
						t.Fatal(err)
					}
					allowed := state == "supported"
					if resolved.Preference.Provider != provider || resolved.Preference.Model != "gpt-media" || !runruntime.AgentProfileAuthorizesMedia(resolved.AgentProfile) || resolved.MediaContract.ProjectScope != "vision-project" || resolved.Scope.PrimaryPath != "" || len(resolved.Scope.Roots) != 0 {
						t.Fatalf("incorrect resolved authority: %+v", resolved)
					}
					if sessionsV3ProviderRequestHasTool(resolved.Tools, "media_inspect") != allowed || runruntime.SessionMediaContractAllows(resolved.MediaContract, "audio", "audio/mpeg", "") || runruntime.SessionMediaContractAllows(resolved.MediaContract, "video", "video/mp4", "") {
						t.Fatalf("incorrect media capabilities: %+v", resolved.MediaContract)
					}
					input, err := executor.sessionsV3ProviderInputWithMedia(resolved, messages, sessionsV3ProviderInputOptions{})
					if err != nil {
						t.Fatal(err)
					}
					request, err := executor.sessionV3ProviderBaseRequest(job, resolved, input)
					if err != nil || sessionsV3ProviderRequestHasTool(request.Tools, "media_inspect") != allowed {
						t.Fatalf("provider request: %+v %v", request, err)
					}
					if allowed {
						invoker, err := executor.newSessionV3ProviderToolInvoker(resolved, job, 1, nil, nil, resolved.Tools)
						if err != nil {
							t.Fatal(err)
						}
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						inspected, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "inspect-image", Name: "media_inspect", Arguments: string(mustJSON(t, map[string]string{"asset_id": asset.ID}))})
						if err != nil || inspected.Error != "" || inspected.Media == nil || !bytes.Equal(inspected.Media.Bytes, mediaStagingAPIPNG) {
							t.Fatalf("retained image inspection failed: %+v %v", inspected, err)
						}
						denied, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "inspect-path", Name: "media_inspect", Arguments: `{"path":"image.png"}`})
						if (err == nil && denied.Error == "") || denied.Media != nil {
							t.Fatalf("project scope granted filesystem perception: %+v %v", denied, err)
						}
						stale := resolved
						stale.MediaContract.Hash = "forged"
						staleInvoker, err := executor.newSessionV3ProviderToolInvoker(stale, job, 1, nil, nil, stale.Tools)
						if err != nil {
							t.Fatal(err)
						}
						denied, err = staleInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "inspect-stale", Name: "media_inspect", Arguments: string(mustJSON(t, map[string]string{"asset_id": asset.ID}))})
						if (err == nil && denied.Error == "") || denied.Media != nil {
							t.Fatalf("forged project contract admitted: %+v %v", denied, err)
						}
					}
					payloadCount := 0
					for _, item := range input[0]["content"].([]map[string]any) {
						if item["type"] == "session_media" {
							payloadCount++
							payload := item["media"].(provideriface.SessionMediaPayload)
							if !bytes.Equal(payload.Bytes, mediaStagingAPIPNG) || payload.DigestSHA256 != asset.DigestSHA256 {
								t.Fatal("provider received incorrect image bytes")
							}
						}
					}
					if (payloadCount == 1) != allowed || strings.Contains(string(mustJSON(t, input)), "model perception not supported") == allowed {
						t.Fatalf("incorrect perception payload: %+v", input)
					}
				})
			}
			for _, foreign := range []string{"account", "user"} {
				denied := job
				if foreign == "account" {
					denied.Principal.AccountScopeID = "foreign"
				} else {
					denied.Principal.UserID = "foreign"
				}
				if _, err := executor.resolveSessionV3Runtime(denied); err == nil {
					t.Fatal("foreign project principal admitted")
				}
			}
			if len(f.runner.requests) != 0 {
				t.Fatal("runtime resolution unexpectedly called a provider")
			}
		})
	}
}
