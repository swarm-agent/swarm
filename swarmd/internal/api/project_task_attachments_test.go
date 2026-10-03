package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func projectAttachmentFixture(t *testing.T) (*routedMediaTestFixture, *pebblestore.ProjectRecord, tool.ProjectTaskCreateInput) {
	t.Helper()
	f := newRoutedMediaTestFixture(t)
	ws := f.server.worktrees.(*routedWorktreeServiceStub).allocation.RepoRoot
	project := &pebblestore.ProjectRecord{Name: "Attachment delivery", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: ws, Role: "primary_code"}}}
	if err := f.sessions.Store().PutProject(f.principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	return f, project, tool.ProjectTaskCreateInput{ID: "attachment-task", Title: "Inspect attachment", Prompt: "Implement the attached design", Agent: "coder", FeatureSize: "small", WorkspacePath: ws, Provider: "openai", Model: "gpt-media", AttachedMedia: []pebblestore.ProjectTaskMediaRef{{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(mediaStagingAPIPNG), Kind: "image", MediaType: "image/png", Filename: "design.png"}}}
}

// Purpose: CreateProjectTask and deployProjectTaskExecution must bind accepted
// Coder images to the V3 seed, not just mention them in prompt text. The real
// API/store/provider-input boundary is the narrowest proof of byte delivery,
// selected-model stability, durable replay, and no duplicate launch on retry.
func TestProjectTaskAttachmentCoderDelivery(t *testing.T) {
	f, project, input := projectAttachmentFixture(t)
	task, err := f.server.CreateProjectTask(context.Background(), f.principal, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	session, found, err := f.sessions.GetSession(task.SessionID)
	if err != nil || !found || session.Preference.Provider != input.Provider || session.Preference.Model != input.Model {
		t.Fatalf("selected model changed: %+v %v", session.Preference, err)
	}
	profile, err := sessionV3AgentProfileFromMetadata(session.Metadata)
	if err != nil || !strings.Contains(profile.Name, "coder") {
		t.Fatalf("not a Coder session: %+v %v", profile, err)
	}
	messages, err := f.sessions.ListSessionMessages(session.ID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 {
		t.Fatalf("seed lost attachment: %+v %v", messages, err)
	}
	ref := messages[0].Media[0]
	_, payload, err := f.sessions.ReadSessionMediaAsset(f.principal.AccountScopeID, session.ID, ref.AssetID)
	if err != nil || !bytes.Equal(payload, mediaStagingAPIPNG) {
		t.Fatalf("attachment bytes changed: %v", err)
	}
	contract, err := f.server.routedSessionMediaContract(context.Background(), f.principal, session)
	if err != nil {
		t.Fatal(err)
	}
	providerInput, err := f.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(sessionV3ResolvedRuntime{Session: session, AgentProfile: profile, MediaContract: contract}, messages, sessionsV3ProviderInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, providerInput))
	if !strings.Contains(encoded, `"type":"session_media"`) || !strings.Contains(encoded, base64.StdEncoding.EncodeToString(mediaStagingAPIPNG)) {
		t.Fatalf("provider input missing image bytes: %s", encoded)
	}
	replayed, err := f.server.CreateProjectTask(context.Background(), f.principal, project.ID, input)
	if err != nil || replayed.SessionID != task.SessionID {
		t.Fatalf("retry changed session: %+v %v", replayed, err)
	}
	intents, err := f.sessions.Store().ListRunIntents(session.ID, 10)
	if err != nil || len(intents) != 1 || f.server.worktrees.(*routedWorktreeServiceStub).allocationCalls != 1 {
		t.Fatalf("retry duplicated launch: %+v %v", intents, err)
	}
}

// Purpose: admission must intersect selected catalog and adapter capabilities
// before any reservation, session, worktree, or run is created. Mixed batches,
// unsupported documents, forged metadata and cross-account references must
// reject without changing model settings or retaining a partial attachment batch.
// The API/store layer observes all admission side effects without a live model.
func TestProjectTaskAttachmentRejectionBeforeLaunch(t *testing.T) {
	for _, name := range []string{"pdf", "text", "audio", "catalog-denied", "adapter-denied", "count", "bytes", "spoof", "digest", "foreign", "conflicting-source"} {
		t.Run(name, func(t *testing.T) {
			f, project, input := projectAttachmentFixture(t)
			switch name {
			case "pdf":
				input.AttachedMedia = append(input.AttachedMedia, pebblestore.ProjectTaskMediaRef{Data: base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\nfixture\n")), Kind: "document", MediaType: "application/pdf", Filename: "spec.pdf"})
			case "text":
				input.AttachedMedia = append(input.AttachedMedia, pebblestore.ProjectTaskMediaRef{Data: "requirements", Kind: "doc", MediaType: "text/plain", Filename: "spec.txt"})
			case "audio":
				input.AttachedMedia = append(input.AttachedMedia, pebblestore.ProjectTaskMediaRef{URL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString([]byte("RIFF0000WAVEfmt ")), Kind: "audio", MediaType: "audio/wav"})
			case "catalog-denied":
				catalog := pebblestore.NewModelCatalogStore(f.store)
				if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: "openai", Model: "gpt-media", Source: "test", SourceSnapshotID: "media-snapshot", SourceSnapshotVersion: "v1"}); err != nil {
					t.Fatal(err)
				}
			case "adapter-denied":
				f.runner.declaration.Inputs = nil
			case "count":
				input.AttachedMedia = append(input.AttachedMedia, input.AttachedMedia[0], input.AttachedMedia[0])
			case "bytes":
				f.runner.declaration.Inputs[0].MaxBytes = 1
			case "spoof":
				input.AttachedMedia[0].MediaType = "application/pdf"
			case "digest":
				input.AttachedMedia[0].DigestSHA256 = strings.Repeat("0", 64)
			case "foreign":
				asset, _, err := f.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{AccountScopeID: "foreign", SessionID: "foreign-session", Reader: bytes.NewReader(mediaStagingAPIPNG)})
				if err != nil {
					t.Fatal(err)
				}
				input.AttachedMedia[0].URL = "/v3/sessions/foreign-session/media/" + asset.ID
			case "conflicting-source":
				input.AttachedMedia[0].Data = base64.StdEncoding.EncodeToString(mediaStagingAPIPNG)
			}
			settings, err := f.server.agentModelSettings.GetForAccount(f.principal.AccountScopeID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.server.CreateProjectTask(context.Background(), f.principal, project.ID, input)
			if err == nil || !strings.Contains(err.Error(), "attachment") {
				t.Fatalf("expected clear attachment rejection, got %v", err)
			}
			if task, found, err := f.sessions.Store().GetProjectTask(f.principal.AccountScopeID, project.ID, input.ID); err != nil || found {
				t.Fatalf("rejected task reserved: %+v %v", task, err)
			}
			sum := sha256.Sum256([]byte("task-session:" + f.principal.AccountScopeID + ":" + project.ID + ":" + input.ID))
			sessionID := hex.EncodeToString(sum[:16])
			if _, found, err := f.sessions.GetSession(sessionID); err != nil || found {
				t.Fatalf("rejected task created session: %v", err)
			}
			intents, err := f.sessions.Store().ListRunIntents(sessionID, 10)
			if err != nil || len(intents) != 0 || f.server.worktrees.(*routedWorktreeServiceStub).allocationCalls != 0 {
				t.Fatalf("rejected task launched: %+v %v", intents, err)
			}
			current, _, err := f.sessions.Store().GetProject(f.principal.AccountScopeID, project.ID)
			if err != nil || len(current.ActiveTaskIDs) != 0 {
				t.Fatalf("rejected task changed project: %+v %v", current, err)
			}
			after, err := f.server.agentModelSettings.GetForAccount(f.principal.AccountScopeID)
			if err != nil || !bytes.Equal(mustJSON(t, settings), mustJSON(t, after)) {
				t.Fatalf("rejection changed model settings: %v", err)
			}
		})
	}
}

// Purpose: PDFs must be admitted only when both catalog and adapter declare
// document support; no image-only heuristic may deny a supported document or
// switch models. This tests the same preflight used by CreateProjectTask.
func TestProjectTaskAttachmentPDFCapabilityIntersection(t *testing.T) {
	f, project, input := projectAttachmentFixture(t)
	catalog := pebblestore.NewModelCatalogStore(f.store)
	if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: "openai", Model: "gpt-media", Source: "test", SourceSnapshotID: "media-snapshot", SourceSnapshotVersion: "v1", Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses, CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey, Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "document", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"application/pdf"}}}}}); err != nil {
		t.Fatal(err)
	}
	f.runner.declaration.Inputs = []provideriface.MediaAdapterCapability{{Modality: "document", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"application/pdf"}, ContentTypes: []string{"input_file"}, MaxBytes: 1024, MaxCount: 2}}
	input.AttachedMedia = []pebblestore.ProjectTaskMediaRef{{Data: base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\nfixture\n")), Kind: "document", MediaType: "application/pdf", Filename: "spec.pdf"}}
	task, err := f.server.CreateProjectTask(context.Background(), f.principal, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.sessions.ListSessionMessages(task.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 || messages[0].Media[0].MIMEType != "application/pdf" || task.Model != input.Model {
		t.Fatalf("supported PDF lost or model changed: %+v %v", messages, err)
	}
}

// Purpose: a crash after session creation but before seed publication must not
// turn a retry into text-only execution. reconcileProjectTaskSession is the
// narrowest durable recovery boundary; run creation follows successful media
// retention and repeated recovery must not duplicate the seed or run. Stale
// source bytes and missing/mismatched seed references must fail without a run.
func TestProjectTaskAttachmentRecovery(t *testing.T) {
	f, project, input := projectAttachmentFixture(t)
	// Use the existing create mutation to retain an owned session, then exercise
	// the explicit recovery path with the initially absent seed.
	source, err := f.server.resolveProjectTaskSource(f.principal, project, input.WorkspacePath, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: input.ID, ProjectID: project.ID, AccountID: f.principal.AccountScopeID, SessionID: "attachment-recovery", Agent: "swarm", Title: input.Title, Description: input.Prompt, WorkspacePath: source.Path, SourceWorkspace: source, Model: input.Model, Provider: input.Provider, AttachedMedia: input.AttachedMedia}
	if err := f.server.preflightProjectTaskAttachments(context.Background(), f.principal, task); err != nil {
		t.Fatal(err)
	}
	profile, err := f.server.agents.ResolveSystemAgent("swarm", pebblestore.AgentProfile{Provider: task.Provider, Model: task.Model})
	if err != nil {
		t.Fatal(err)
	}
	available := true
	owned := pebblestore.SessionSnapshot{ID: task.SessionID, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID, WorkspacePath: source.Path, Mode: "auto", Preference: pebblestore.ModelPreference{Provider: task.Provider, Model: task.Model}, WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, Path: source.Path, WorkspaceID: source.WorkspaceID, WorkspaceGeneration: source.WorkspaceGeneration, Available: &available}}, Metadata: map[string]any{"agent_profile": cloneSessionsV3AgentProfile(profile), "project_id": project.ID, "task_id": task.ID, "swarm_v3_source_workspace_path": source.Path, "swarm_v3_source_workspace_id": source.WorkspaceID, "swarm_v3_source_workspace_generation": source.WorkspaceGeneration}}
	_, err = f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, UserID: owned.UserID, AccountScopeID: owned.AccountScopeID, ClientRequestID: "recovery-create", IdempotencyKey: "recovery-create", PayloadHash: "recovery-create", RequestHash: "recovery-create", Kind: sessionruntime.SessionMutationCreateSession, Session: &owned})
	if err != nil {
		t.Fatal(err)
	}
	digest := task.AttachedMedia[0].DigestSHA256
	task.AttachedMedia[0].DigestSHA256 = strings.Repeat("0", 64)
	if err := f.server.reconcileProjectTaskSession(f.principal, project, task, owned, "in_progress"); err == nil || !strings.Contains(err.Error(), "immutable reference mismatch") {
		t.Fatalf("stale source accepted: %v", err)
	}
	if messages, err := f.sessions.ListSessionMessages(owned.ID, 0, 10); err != nil || len(messages) != 0 {
		t.Fatalf("stale source published a seed: %+v %v", messages, err)
	}
	if intents, err := f.sessions.Store().ListRunIntents(owned.ID, 10); err != nil || len(intents) != 0 {
		t.Fatalf("stale source launched: %+v %v", intents, err)
	}
	task.AttachedMedia[0].DigestSHA256 = digest
	for i := 0; i < 2; i++ {
		if err := f.server.reconcileProjectTaskSession(f.principal, project, task, owned, "pending_approval"); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := f.sessions.ListSessionMessages(owned.ID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 {
		t.Fatalf("recovered seed lost or duplicated media: %+v %v", messages, err)
	}
	_, payload, err := f.sessions.ReadSessionMediaAsset(owned.AccountScopeID, owned.ID, messages[0].Media[0].AssetID)
	if err != nil || !bytes.Equal(payload, mediaStagingAPIPNG) {
		t.Fatalf("recovery changed bytes: %v", err)
	}
	intents, err := f.sessions.Store().ListRunIntents(owned.ID, 10)
	if err != nil || len(intents) != 0 {
		t.Fatalf("recovery bypassed approval: %+v %v", intents, err)
	}
	for _, mismatch := range []string{"count", "digest"} {
		t.Run(mismatch, func(t *testing.T) {
			changed := *task
			changed.AttachedMedia = append([]pebblestore.ProjectTaskMediaRef(nil), task.AttachedMedia...)
			if mismatch == "count" {
				changed.AttachedMedia = append(changed.AttachedMedia, changed.AttachedMedia[0])
			} else {
				changed.AttachedMedia[0].DigestSHA256 = strings.Repeat("0", 64)
			}
			if err := f.server.reconcileProjectTaskSession(f.principal, project, &changed, owned, "in_progress"); err == nil || !strings.Contains(err.Error(), "task seed attachment") {
				t.Fatalf("mismatched seed accepted: %v", err)
			}
			after, err := f.sessions.ListSessionMessages(owned.ID, 0, 10)
			if err != nil || !bytes.Equal(mustJSON(t, messages), mustJSON(t, after)) {
				t.Fatalf("rejection changed seed: %+v %v", after, err)
			}
			if intents, err := f.sessions.Store().ListRunIntents(owned.ID, 10); err != nil || len(intents) != 0 {
				t.Fatalf("mismatched seed launched: %+v %v", intents, err)
			}
		})
	}
}

// Purpose: media admission must use the account's Coder assignment when no task
// override exists, then freeze it through deployment. This API/store test guards
// against using the Swarm model or mutating account settings to obtain vision.
func TestProjectTaskAttachmentUsesConfiguredCoderModel(t *testing.T) {
	f, project, input := projectAttachmentFixture(t)
	settingsStore := pebblestore.NewAgentModelSettingsStore(f.store)
	settings, err := f.server.agentModelSettings.GetForAccount(f.principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	settings.SystemAgents.Coder = pebblestore.AgentModelAssignment{Provider: input.Provider, Model: input.Model, Thinking: "low"}
	if _, err := settingsStore.PutForAccount(settings); err != nil {
		t.Fatal(err)
	}
	input.Provider, input.Model = "", ""
	task, err := f.server.CreateProjectTask(context.Background(), f.principal, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	session, found, err := f.sessions.GetSession(task.SessionID)
	if err != nil || !found || session.Preference.Model != "gpt-media" || session.Preference.Provider != "openai" || task.Model != session.Preference.Model {
		t.Fatalf("configured Coder model not preserved: %+v %v", session.Preference, err)
	}
}
