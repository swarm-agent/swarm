package run

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestProviderManagedMediaInspectReadsExactExportedHTMLStillPNG(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "artifact-state"))

	session, ok, err := svc.sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	authority := artifact.NewAuthority(artifact.NewRegistry(svc.sessions, artifact.Limits{}), svc.sessions)
	svc.tools.SetArtifactAuthority(authority)

	modelStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "models.pebble"))
	if err != nil {
		t.Fatalf("open model store: %v", err)
	}
	defer modelStore.Close()
	catalogStore := pebblestore.NewModelCatalogStore(modelStore)
	catalog := model.NewCatalogService(catalogStore)
	svc.model = model.NewService(pebblestore.NewModelStore(modelStore), nil, catalog)
	const snapshotID = "media-artifact-snapshot"
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{
		Provider: "codex", Model: "test-model", Source: "live", SourceSnapshotID: snapshotID, SourceSnapshotVersion: "v1",
		Media: &pebblestore.ModelCatalogMediaCapabilities{
			State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceCodexChatGPT, CredentialSurface: provideriface.MediaCredentialSurfaceCodexOAuth,
			Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}},
		},
	}); err != nil {
		t.Fatalf("set media model catalog: %v", err)
	}
	if err := catalogStore.SetMeta(pebblestore.ModelCatalogMeta{Source: "live", SnapshotID: snapshotID, SnapshotVersion: "v1", RecordCount: 1}); err != nil {
		t.Fatalf("set media model catalog metadata: %v", err)
	}
	runner := &artifactMediaTestRunner{}
	providers := registry.New()
	providers.RegisterRunner(runner)
	svc.providers = providers

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID, SessionID: session.ID}
	artifactPrincipal := artifact.Principal{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	source, err := authority.Create(context.Background(), artifactPrincipal, artifact.CreateInput{
		RequestID: "source-html", CollectionID: "source-html", CollectionName: "Source HTML", VariantID: "source-html", Filename: "storyboard.html", MediaType: "text/html", AutoAccept: true,
		Body: []byte(`<!doctype html><script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"opening"}]}</script><body></body>`),
	})
	if err != nil {
		t.Fatalf("create HTML source: %v", err)
	}
	exactPNG := exactExportedStillPNG(t)
	svc.tools.SetHTMLCaptureRenderer(artifactStillRenderer{png: exactPNG})
	scope := tool.WorkspaceScope{PrimaryPath: workspace, SessionID: session.ID, Principal: principal}
	artifactCtx := tool.WithArtifactRunContext(context.Background(), tool.ArtifactRunContext{SessionID: session.ID, RunID: "run-export"})
	exportOutput, err := svc.tools.ExecuteForWorkspaceScopeWithRuntime(artifactCtx, scope, tool.Call{
		CallID: "export-still", Name: "manage_artifact", Arguments: mustProviderToolInvokerJSON(t, map[string]any{
			"action": "export_html_stills", "session_id": source.SessionID, "collection_id": source.CollectionID, "variant_id": source.ID, "event_seq": source.EventSeq,
		}),
	})
	if err != nil {
		t.Fatalf("export HTML still: %v", err)
	}
	var exported struct {
		Exports []struct {
			StateID   string                                        `json:"state_id"`
			Reference pebblestore.SessionArtifactSelectionReference `json:"reference"`
		} `json:"exports"`
	}
	if err := json.Unmarshal([]byte(exportOutput), &exported); err != nil || len(exported.Exports) != 1 {
		t.Fatalf("decode exported still reference: exports=%+v err=%v output=%s", exported.Exports, err, exportOutput)
	}
	if exported.Exports[0].StateID != "opening" {
		t.Fatalf("export state = %q, want opening", exported.Exports[0].StateID)
	}

	readyBytes, readyVariant, err := authority.ReadReference(context.Background(), artifactPrincipal, exported.Exports[0].Reference, pebblestore.SessionMediaDefaultMaxBytes)
	if err != nil || !bytes.Equal(readyBytes, exactPNG) || readyVariant.Size != int64(len(exactPNG)) {
		t.Fatalf("exported ready PNG authority mismatch: size=%d bytes=%d exact=%d equal=%t err=%v", readyVariant.Size, len(readyBytes), len(exactPNG), bytes.Equal(readyBytes, exactPNG), err)
	}

	profile := agentruntime.SwarmAgentProfileForContext(pebblestore.AgentProfile{})
	catalogRecord, catalogMeta, err := modelCatalogLookupWithMeta(svc.model, "codex", "test-model")
	if err != nil || catalogRecord == nil {
		t.Fatalf("load media catalog: record=%+v meta=%+v err=%v", catalogRecord, catalogMeta, err)
	}
	contract := CompileSessionMediaContract(SessionMediaContractInput{
		ProviderID: "codex", Model: "test-model", Catalog: catalogRecord, CatalogMeta: catalogMeta,
		Adapter: ResolveMediaAdapterDeclaration(context.Background(), "codex", runner), AgentAuthorized: AgentProfileAuthorizesMedia(profile),
		ExecutionMode: sessionruntime.ModeAuto, WorkspaceScope: workspace, SessionScope: session.ID,
	})
	if !SessionMediaContractAllows(contract, "image", "image/png", "png") {
		t.Fatalf("compiled media contract denied exact exported PNG: %+v", contract)
	}
	capability, err := validateMediaInspectInvocation(contract, "image", "image/png", "png")
	if err != nil || capability.MaxBytes < int64(len(exactPNG)) {
		t.Fatalf("compiled media capability cannot admit exact exported PNG: capability=%+v png_bytes=%d err=%v contract=%+v", capability, len(exactPNG), err, contract)
	}
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID: session.ID, permissionSessionID: session.ID, runID: "run-inspect-export", step: 1,
		sessionMode: sessionruntime.ModeAuto, workspacePath: workspace, workspaceRoots: []string{workspace}, workspaceOriginPath: workspace, workspaceOriginRoots: []string{workspace},
		principal: principal, providerManagedV3: true, applySessionMutation: providerManagedV3NoopMutation,
		agentProfile: profile, providerID: "codex", model: "test-model", mediaContract: contract,
	})
	args := mustProviderToolInvokerJSON(t, map[string]any{"artifact_reference": exported.Exports[0].Reference})
	result, err := invoker.ExecuteTool(context.Background(), toolInvocation("inspect-export", mediaInspectToolName, args))
	if err != nil {
		t.Fatalf("inspect exported exact PNG: %v", err)
	}
	if result.Error != "" || result.Media == nil {
		t.Fatalf("media inspection result = %+v", result)
	}
	if result.Media.MIMEType != "image/png" || !bytes.Equal(result.Media.Bytes, exactPNG) {
		t.Fatalf("native media payload did not preserve exact exported PNG: mime=%q bytes_equal=%t", result.Media.MIMEType, bytes.Equal(result.Media.Bytes, exactPNG))
	}
	if result.Media.AssetID == "" || !strings.Contains(result.TextForModel, result.Media.AssetID) {
		t.Fatalf("provider-facing native injection metadata missing: result=%+v", result)
	}
	pending, err := permissions.ListPending(session.ID, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("exact artifact inspection created permission records: pending=%+v err=%v", pending, err)
	}

	staleRef := exported.Exports[0].Reference
	staleRef.EventSeq++
	staleResult, err := invoker.ExecuteTool(context.Background(), toolInvocation("inspect-stale", mediaInspectToolName, mustProviderToolInvokerJSON(t, map[string]any{"artifact_reference": staleRef})))
	if err != nil || !strings.Contains(staleResult.Error, "artifact source reference is stale") {
		t.Fatalf("stale exact artifact reference did not fail closed: result=%+v err=%v", staleResult, err)
	}
	nonImage, err := authority.Create(context.Background(), artifactPrincipal, artifact.CreateInput{RequestID: "not-image", CollectionID: "not-image", CollectionName: "Not image", VariantID: "not-image", Filename: "notes.txt", MediaType: "text/plain", Body: []byte("not an image"), AutoAccept: true})
	if err != nil {
		t.Fatalf("create non-image artifact: %v", err)
	}
	nonImageRef := pebblestore.SessionArtifactSelectionReference{SessionID: nonImage.SessionID, CollectionID: nonImage.CollectionID, VariantID: nonImage.ID, EventSeq: nonImage.EventSeq}
	nonImageResult, err := invoker.ExecuteTool(context.Background(), toolInvocation("inspect-not-image", mediaInspectToolName, mustProviderToolInvokerJSON(t, map[string]any{"artifact_reference": nonImageRef})))
	if err != nil || !strings.Contains(nonImageResult.Error, "artifact reference is not an image") {
		t.Fatalf("non-image artifact reference did not fail closed: result=%+v err=%v", nonImageResult, err)
	}
	foreign, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: "foreign-user", AccountScopeID: session.AccountScopeID, Title: "Foreign", WorkspacePath: workspace, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "off"}})
	if err != nil {
		t.Fatalf("create foreign session: %v", err)
	}
	foreignResult, err := invoker.ExecuteTool(context.Background(), toolInvocation("inspect-foreign", mediaInspectToolName, mustProviderToolInvokerJSON(t, map[string]any{"artifact_reference": map[string]any{"session_id": foreign.ID, "collection_id": "foreign", "variant_id": "foreign", "event_seq": 1}})))
	if err != nil || !strings.Contains(foreignResult.Error, "artifact session ownership does not match") {
		t.Fatalf("foreign-session artifact reference did not fail closed: result=%+v err=%v", foreignResult, err)
	}
	smallRunner := &artifactMediaTestRunner{maxBytes: int64(len(exactPNG) - 1)}
	smallProviders := registry.New()
	smallProviders.RegisterRunner(smallRunner)
	svc.providers = smallProviders
	smallContract := CompileSessionMediaContract(SessionMediaContractInput{
		ProviderID: "codex", Model: "test-model", Catalog: catalogRecord, CatalogMeta: catalogMeta,
		Adapter: ResolveMediaAdapterDeclaration(context.Background(), "codex", smallRunner), AgentAuthorized: AgentProfileAuthorizesMedia(profile),
		ExecutionMode: sessionruntime.ModeAuto, WorkspaceScope: workspace, SessionScope: session.ID,
	})
	smallInvoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID: session.ID, permissionSessionID: session.ID, runID: "run-inspect-oversize", step: 1,
		sessionMode: sessionruntime.ModeAuto, workspacePath: workspace, workspaceRoots: []string{workspace}, workspaceOriginPath: workspace, workspaceOriginRoots: []string{workspace},
		principal: principal, providerManagedV3: true, applySessionMutation: providerManagedV3NoopMutation,
		agentProfile: profile, providerID: "codex", model: "test-model", mediaContract: smallContract,
	})
	oversizeResult, err := smallInvoker.ExecuteTool(context.Background(), toolInvocation("inspect-oversize", mediaInspectToolName, args))
	if err != nil || !strings.Contains(oversizeResult.Error, "artifact exceeds read byte bound") {
		t.Fatalf("oversized artifact did not fail at bounded authority read: result=%+v err=%v", oversizeResult, err)
	}
	svc.providers = providers
	svc.tools.SetArtifactAuthority(nil)
	missingAuthority, err := invoker.ExecuteTool(context.Background(), toolInvocation("inspect-missing-authority", mediaInspectToolName, args))
	if err != nil || !strings.Contains(missingAuthority.Error, "requires the authenticated artifact authority") {
		t.Fatalf("missing artifact authority did not fail closed: result=%+v err=%v", missingAuthority, err)
	}
}

func TestProviderManagedMediaInspectArtifactReferenceReadLimitFailsClosed(t *testing.T) {
	contract := provideriface.SessionMediaContract{Hash: "contract", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, MIMETypes: []string{"image/png"}, FileTypes: []string{"png"}, MaxBytes: 1024, MaxCount: 1}}}
	if _, err := mediaInspectImageReadLimit(contract); err != nil {
		t.Fatalf("valid image read limit rejected: %v", err)
	}
	for _, denied := range []provideriface.SessionMediaContract{
		{Hash: "empty"},
		{Hash: "invalid-count", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, MIMETypes: []string{"image/png"}, MaxBytes: 1024, MaxCount: 0}}},
		{Hash: "invalid-bytes", Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, MIMETypes: []string{"image/png"}, MaxBytes: 0, MaxCount: 1}}},
	} {
		if _, err := mediaInspectImageReadLimit(denied); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("invalid image read contract did not fail closed: contract=%+v err=%v", denied, err)
		}
	}
}

type artifactMediaTestRunner struct{ maxBytes int64 }

func (*artifactMediaTestRunner) ID() string { return "codex" }
func (*artifactMediaTestRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (*artifactMediaTestRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (r *artifactMediaTestRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	maxBytes := r.maxBytes
	if maxBytes <= 0 {
		maxBytes = pebblestore.SessionMediaDefaultMaxBytes
	}
	return provideriface.MediaAdapterDeclaration{
		AdapterID: provideriface.MediaAdapterIDCodexChatGPTV1, ProviderID: "codex", ProviderSurface: provideriface.MediaProviderSurfaceCodexChatGPT,
		CredentialSurface: provideriface.MediaCredentialSurfaceCodexOAuth, CredentialFingerprint: "artifact-media-test-credential",
		Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: maxBytes, MaxCount: 1}},
	}, nil
}

type artifactStillRenderer struct{ png []byte }

func (r artifactStillRenderer) Capture(_ context.Context, request htmlcapture.Request) ([]htmlcapture.Result, error) {
	results := make([]htmlcapture.Result, len(request.StateIDs))
	for index, stateID := range request.StateIDs {
		results[index] = htmlcapture.Result{StateID: stateID, PNG: append([]byte(nil), r.png...)}
	}
	return results, nil
}

func exactExportedStillPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, htmlcapture.Width, htmlcapture.Height))
	for index := 0; index < len(img.Pix); index += 4 {
		img.Pix[index], img.Pix[index+1], img.Pix[index+2], img.Pix[index+3] = 18, 52, 86, 255
	}
	img.SetRGBA(0, 0, color.RGBA{R: 255, G: 128, B: 32, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatalf("encode exact exported still: %v", err)
	}
	return output.Bytes()
}
