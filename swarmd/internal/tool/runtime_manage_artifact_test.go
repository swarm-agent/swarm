package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/uisettings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManagedCreateInfersHTMLMediaTypeFromFilename(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	source := pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 9}
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", SourceArtifact: &source})
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "managed-create-html", Name: "manage_artifact", Arguments: `{"action":"create","filename":"revision.html","content":"<h1>revision</h1>"}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.MediaType != "text/html" {
		t.Fatalf("managed create media type = %q", authority.created.MediaType)
	}
	if authority.reserveCalls != 0 {
		t.Fatalf("unprofiled HTML entered animation preflight gate: %d", authority.reserveCalls)
	}
	if strings.Contains(output, "trusted_animation_preflight") {
		t.Fatalf("unprofiled HTML claimed trusted animation preflight: %s", output)
	}
}

func TestManagedPackageProjectsShortAnimationDurationForVideoPlans(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{result: htmlcapture.AnimationResult{PreviewPNG: testAnimationFallbackPNG(t), DurationMS: 6000, FPS: 30, FrameCount: 180}})
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: reviewedMotionProfile(t)})
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":6000,"fps":30}</script>`
	arguments, err := json.Marshal(map[string]any{"action": "create_package", "filename": "candidate.zip", "entries": []any{map[string]any{"name": "index.html", "content": html}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "managed-package-animation", Name: "manage_artifact", Arguments: string(arguments)}); err != nil {
		t.Fatal(err)
	}
	if len(authority.packaged.Parts) != 1 || authority.packaged.Parts[0].Kind != "temporal" || authority.packaged.Parts[0].EndMs != 6000 {
		t.Fatalf("managed package animation parts = %+v, want canonical 6000ms duration metadata", authority.packaged.Parts)
	}
}

func TestManagedCreateProjectsShortAnimationDurationForVideoPlans(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{result: htmlcapture.AnimationResult{PreviewPNG: testAnimationFallbackPNG(t), DurationMS: 6000, FPS: 30, FrameCount: 180}})
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: reviewedMotionProfile(t)})
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":6000,"fps":30}</script>`
	arguments, err := json.Marshal(map[string]any{"action": "create", "filename": "candidate.html", "media_type": "text/html", "content": html})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "managed-create-animation", Name: "manage_artifact", Arguments: string(arguments)}); err != nil {
		t.Fatal(err)
	}
	if len(authority.created.Parts) != 1 || authority.created.Parts[0].Kind != "temporal" || authority.created.Parts[0].EndMs != 6000 {
		t.Fatalf("managed animation parts = %+v, want canonical 6000ms duration metadata", authority.created.Parts)
	}
}

func TestArtifactReviewTargetIDsPreservesBoundedOrder(t *testing.T) {
	targets := []pebblestore.SessionArtifactPart{{ID: "part-1"}, {ID: "part-3"}, {ID: "part-1"}}
	if got := artifactReviewTargetIDs(targets); got != "part-1,part-3" {
		t.Fatalf("artifactReviewTargetIDs = %q", got)
	}
}

type fakeManagedImageGenerator struct {
	calls        int
	req          imagegen.ManagedGenerateRequest
	image        imagegen.ManagedImage
	capabilities imagegen.ManagedImageCapabilities
}

func (f *fakeManagedImageGenerator) ManagedImageCapabilities(string) (imagegen.ManagedImageCapabilities, error) {
	if f.capabilities.Available || f.capabilities.CapabilityToken != "" || f.capabilities.Reason != "" {
		return f.capabilities, nil
	}
	return imagegen.ManagedImageCapabilities{Available: true}, nil
}

func (f *fakeManagedImageGenerator) GenerateManagedImage(_ context.Context, req imagegen.ManagedGenerateRequest) (imagegen.ManagedImage, error) {
	f.calls++
	f.req = req
	return f.image, nil
}

type fakeImageUISettings struct {
	settings uisettings.UISettings
}

func (f *fakeImageUISettings) Get() (uisettings.UISettings, error) { return f.settings, nil }
func (f *fakeImageUISettings) GetForAccount(string) (uisettings.UISettings, error) {
	return f.settings, nil
}
func (f *fakeImageUISettings) Set(settings uisettings.UISettings) (uisettings.UISettings, error) {
	f.settings = settings
	return settings, nil
}
func (f *fakeImageUISettings) SetForAccount(_ string, settings uisettings.UISettings) (uisettings.UISettings, error) {
	f.settings = settings
	return settings, nil
}

type fakeArtifactAuthority struct {
	principal       artifact.Principal
	created         artifact.CreateInput
	initial         artifact.CreateInitialCompositionInput
	packaged        artifact.CreatePackageInput
	readBody        []byte
	readErr         error
	packageReadErr  error
	packageManifest []artifact.PackageManifestEntry
	variant         pebblestore.SessionArtifactVariant
	reference       pebblestore.SessionArtifactSelectionReference
	referenceRead   bool
	deleted         string
	materializedRef pebblestore.SessionArtifactSelectionReference
	batchItems      []artifact.MaterializeBatchItem
	batchVariants   []pebblestore.SessionArtifactVariant
	workspaceRoot   string
	destination     string
	reserveCalls    int
	createCalls     int
	packageCalls    int
	publishCalls    int
	overwrite       bool
	createdFromFile artifact.CreateFileInput
	catalogOptions  pebblestore.SessionArtifactCatalogOptions
	catalogPage     pebblestore.SessionArtifactCatalogPage
}

func (f *fakeArtifactAuthority) Reserve(principal artifact.Principal, input artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	f.reserveCalls++
	f.principal, f.created = principal, input
	if f.variant.ID == input.VariantID && f.variant.CollectionID == input.CollectionID && (f.variant.Status == pebblestore.SessionArtifactStatusFailed || f.variant.Status == pebblestore.SessionArtifactStatusReady) {
		return f.variant, nil
	}
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusStaging, ProjectionReservation: true, Filename: input.Filename, MediaType: input.MediaType, Presentation: input.Presentation, OutputRequirements: input.OutputRequirements, AnimationProfile: input.AnimationProfile, Parts: append([]pebblestore.SessionArtifactPart(nil), input.Parts...)}
	return f.variant, nil
}

func (f *fakeArtifactAuthority) UpdateProgress(principal artifact.Principal, _ string, collectionID, variantID string, progress pebblestore.SessionArtifactProgress) (pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	f.variant.CollectionID, f.variant.ID, f.variant.Status, f.variant.Progress = collectionID, variantID, pebblestore.SessionArtifactStatusStaging, &progress
	f.variant.EventSeq++
	return f.variant, nil
}

func (f *fakeArtifactAuthority) MarkFailed(principal artifact.Principal, _ string, collectionID, variantID, failureCode string) (pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	f.variant.CollectionID, f.variant.ID, f.variant.Status, f.variant.FailureCode = collectionID, variantID, pebblestore.SessionArtifactStatusFailed, failureCode
	f.variant.EventSeq++
	return f.variant, nil
}

func (f *fakeArtifactAuthority) Create(_ context.Context, principal artifact.Principal, input artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	f.createCalls++
	f.principal, f.created = principal, input
	f.readBody = append([]byte(nil), input.Body...)
	digest := sha256.Sum256(input.Body)
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: input.MediaType, Size: int64(len(input.Body)), DigestSHA256: hex.EncodeToString(digest[:]), Presentation: input.Presentation, OutputRequirements: input.OutputRequirements, AnimationProfile: input.AnimationProfile, Parts: append([]pebblestore.SessionArtifactPart(nil), input.Parts...)}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) CreateInitialComposition(_ context.Context, principal artifact.Principal, input artifact.CreateInitialCompositionInput) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.initial = principal, input
	definitions := make([]pebblestore.SessionArtifactPartDefinition, 0, len(input.Parts))
	composition := pebblestore.SessionArtifactComposition{ID: input.CompositionID, ArtifactChainID: input.ArtifactChainID, OwnerSessionID: principal.SessionID}
	for _, part := range input.Parts {
		definitions = append(definitions, part.Definition)
		digest := sha256.Sum256(part.Body)
		composition.Parts = append(composition.Parts, pebblestore.SessionArtifactCompositionPart{PartID: part.Definition.ID, DefinitionOwnerSessionID: principal.SessionID, Revision: pebblestore.SessionArtifactPartRevisionReference{ArtifactChainID: input.ArtifactChainID, PartID: part.Definition.ID, PartRevisionID: part.RevisionID, OwnerSessionID: principal.SessionID, DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(part.Body)), MediaType: part.MediaType}})
	}
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: input.MediaType, ArtifactChainID: input.ArtifactChainID, PartGraphState: pebblestore.SessionArtifactGraphAuthoritative, PartDefinitions: definitions, Composition: &composition}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) CreatePackage(_ context.Context, principal artifact.Principal, input artifact.CreatePackageInput) (pebblestore.SessionArtifactVariant, error) {
	f.packageCalls++
	f.principal, f.packaged = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: "application/zip", Size: 1, Presentation: input.Presentation, OutputRequirements: input.OutputRequirements, AnimationProfile: input.AnimationProfile}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) List(principal artifact.Principal, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	f.principal = principal
	return []pebblestore.SessionArtifactCollection{{ID: "collection-1", Name: "Drafts"}}, nil
}
func (f *fakeArtifactAuthority) ListVariants(principal artifact.Principal, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	if f.variant.ID != "" && f.variant.CollectionID == collectionID {
		return []pebblestore.SessionArtifactVariant{f.variant}, nil
	}
	return []pebblestore.SessionArtifactVariant{{ID: "variant-1", CollectionID: collectionID}}, nil
}
func (f *fakeArtifactAuthority) SearchCatalog(principal artifact.Principal, options pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	f.principal, f.catalogOptions = principal, options
	return f.catalogPage, nil
}
func (f *fakeArtifactAuthority) Get(principal artifact.Principal, variantID string) (pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	if f.variant.ID == "" {
		f.variant = pebblestore.SessionArtifactVariant{ID: variantID, CollectionID: "collection-1", MediaType: "text/plain"}
	}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference = principal, ref
	return f.variant, nil
}
func (f *fakeArtifactAuthority) Read(_ context.Context, principal artifact.Principal, variantID string, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := f.Get(principal, variantID)
	if err == nil {
		err = f.readErr
	}
	return append([]byte(nil), f.readBody...), variant, err
}
func (f *fakeArtifactAuthority) ReadReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference, f.referenceRead = principal, ref, true
	return append([]byte(nil), f.readBody...), f.variant, f.readErr
}
func (f *fakeArtifactAuthority) ReadPackageReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ string, _ int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference, f.referenceRead = principal, ref, true
	return append([]artifact.PackageManifestEntry(nil), f.packageManifest...), append([]byte(nil), f.readBody...), f.variant, f.packageReadErr
}
func (f *fakeArtifactAuthority) MaterializeReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, workspaceRoot, destination string, overwrite bool) (artifact.Materialized, error) {
	f.principal, f.materializedRef, f.workspaceRoot, f.destination, f.overwrite = principal, ref, workspaceRoot, destination, overwrite
	return artifact.Materialized{Destination: destination, Files: 1, Bytes: 7, DigestSHA256: "digest", MediaType: "text/plain"}, nil
}
func (f *fakeArtifactAuthority) MaterializeBatchReferences(_ context.Context, principal artifact.Principal, items []artifact.MaterializeBatchItem, workspaceRoot, destination string, overwrite bool) ([]artifact.Materialized, []pebblestore.SessionArtifactVariant, error) {
	f.principal, f.batchItems, f.workspaceRoot, f.destination, f.overwrite = principal, append([]artifact.MaterializeBatchItem(nil), items...), workspaceRoot, destination, overwrite
	materialized := make([]artifact.Materialized, 0, len(items))
	variants := make([]pebblestore.SessionArtifactVariant, 0, len(items))
	for index := range items {
		materialized = append(materialized, artifact.Materialized{Destination: filepath.ToSlash(filepath.Join(destination, "item.txt")), Files: 1, Bytes: 7, DigestSHA256: "digest", MediaType: "text/plain"})
		variants = append(variants, pebblestore.SessionArtifactVariant{MediaType: "text/plain", DigestSHA256: "digest-" + string(rune('0'+index))})
	}
	f.batchVariants = variants
	return materialized, variants, nil
}
func (f *fakeArtifactAuthority) PublishWorkspace(_ context.Context, principal artifact.Principal, input artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	f.publishCalls++
	f.principal, f.createdFromFile = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 11, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: input.MediaType, DigestSHA256: "published-digest", Size: 7, Presentation: input.Presentation, AnimationProfile: input.AnimationProfile, Lineage: pebblestore.SessionArtifactLineage{SourceSessionID: input.SourceSessionID, SourceCollectionID: input.SourceCollectionID, SourceVariantID: input.SourceVariantID, SourceEventSeq: input.SourceEventSeq}}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) Select(principal artifact.Principal, _, collectionID, variantID string) (pebblestore.SessionArtifactSelectionReference, error) {
	f.principal = principal
	return pebblestore.SessionArtifactSelectionReference{SessionID: principal.SessionID, CollectionID: collectionID, VariantID: variantID}, nil
}
func (f *fakeArtifactAuthority) DeleteVariant(principal artifact.Principal, _, collectionID, variantID string) error {
	f.principal, f.deleted = principal, collectionID+"/"+variantID
	return nil
}
func (f *fakeArtifactAuthority) DeleteCollection(principal artifact.Principal, _, collectionID string) error {
	f.principal, f.deleted = principal, collectionID
	return nil
}

func artifactToolContext() (context.Context, WorkspaceScope) {
	scope := WorkspaceScope{PrimaryPath: ".", SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1", SessionID: "session-1"}}
	ctx := WithWorkspaceScope(context.Background(), scope)
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", RunID: "run-1", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "attempt-1"})
	return ctx, scope
}

func testPNGImage() []byte {
	decoded, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	return decoded
}

func TestManageArtifactListCrossSessionCatalogReturnsExactCandidatesAndPagination(t *testing.T) {
	authority := &fakeArtifactAuthority{catalogPage: pebblestore.SessionArtifactCatalogPage{
		Items: []pebblestore.SessionArtifactCatalogItem{
			{Collection: pebblestore.SessionArtifactCollection{ID: "collection-a", SessionID: "session-a", Name: "Campaign"}, Variant: pebblestore.SessionArtifactVariant{ID: "variant-a", CollectionID: "collection-a", SessionID: "session-a", Status: pebblestore.SessionArtifactStatusReady, Filename: "hero.png", MediaType: "image/png", EventSeq: 7}, Reference: &pebblestore.SessionArtifactSelectionReference{SessionID: "session-a", CollectionID: "collection-a", VariantID: "variant-a", EventSeq: 7}},
			{Collection: pebblestore.SessionArtifactCollection{ID: "collection-b", SessionID: "session-b", Name: "Campaign"}, Variant: pebblestore.SessionArtifactVariant{ID: "variant-b", CollectionID: "collection-b", SessionID: "session-b", Status: pebblestore.SessionArtifactStatusReady, Filename: "hero.png", MediaType: "image/png", EventSeq: 9}, Reference: &pebblestore.SessionArtifactSelectionReference{SessionID: "session-b", CollectionID: "collection-b", VariantID: "variant-b", EventSeq: 9}},
		},
		NextCursor: "opaque-next", HasMore: true,
	}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "catalog-list", map[string]any{"action": "search", "query": "hero", "status": "ready", "media_type": "image/png", "created_after": 10, "created_before": 20, "limit": 2})
	if err != nil {
		t.Fatalf("catalog list: %v", err)
	}
	if authority.catalogOptions.Query != "hero" || authority.catalogOptions.Status != "ready" || authority.catalogOptions.MediaType != "image/png" || authority.catalogOptions.CreatedAfter != 10 || authority.catalogOptions.CreatedBefore != 20 || authority.catalogOptions.Limit != 2 {
		t.Fatalf("catalog options = %+v", authority.catalogOptions)
	}
	for _, required := range []string{`"session_id":"session-a"`, `"collection_id":"collection-a"`, `"variant_id":"variant-a"`, `"event_seq":7`, `"session_id":"session-b"`, `"next_cursor":"opaque-next"`, `"has_more":true`} {
		if !strings.Contains(output, required) {
			t.Fatalf("catalog output lacks %s: %s", required, output)
		}
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "catalog-conflict", map[string]any{"action": "list", "collection_id": "collection-a", "query": "hero"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("collection/search ambiguity error = %v", err)
	}
}

func TestManageArtifactImageCapabilitiesExposeConfiguredSnapshotOptions(t *testing.T) {
	generator := &fakeManagedImageGenerator{capabilities: imagegen.ManagedImageCapabilities{
		Available: true, CapabilityToken: "snapshot-token",
		Settings: map[string]imagegen.ManagedImageSettingCapability{"image_size": {Status: "verified", DefaultValue: "1K", SupportedValues: []any{"1K", "2K"}}},
	}}
	runtime := NewRuntime(1)
	runtime.SetManagedImageGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{settings: uisettings.UISettings{Tools: uisettings.ToolSettings{Image: uisettings.ToolImageSettings{DefaultModel: "gemini-image"}}}}, nil)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "image-capabilities", map[string]any{"action": "image_capabilities"})
	if err != nil {
		t.Fatalf("image capabilities: %v", err)
	}
	if !strings.Contains(output, `"capability_token":"snapshot-token"`) || !strings.Contains(output, `"supported_values":["1K","2K"]`) || strings.Contains(output, "gemini-image") {
		t.Fatalf("image capabilities output = %s", output)
	}
}

func TestManageArtifactGenerateImageUsesCanonicalSettingAndTrustedDestination(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	generator := &fakeManagedImageGenerator{image: imagegen.ManagedImage{Bytes: testPNGImage(), MediaType: "image/png"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedImageGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{settings: uisettings.UISettings{Tools: uisettings.ToolSettings{Image: uisettings.ToolImageSettings{DefaultModel: "gemini-nano-banana-2"}}}}, nil)
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "auth-1", AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1"})
	output, err := runtime.executeManageArtifact(ctx, scope, "image-call", map[string]any{"action": "generate_image", "prompt": "A red square"})
	if err != nil {
		t.Fatalf("generate image: %v", err)
	}
	if generator.calls != 1 || generator.req.SelectionID != "gemini-nano-banana-2" || generator.req.Principal.AccountScopeID != "account-1" {
		t.Fatalf("generation calls=%d request=%#v", generator.calls, generator.req)
	}
	if authority.created.AutoAccept {
		t.Fatalf("managed swarm image unexpectedly requested auto-accept: %+v", authority.created)
	}
	if authority.created.CollectionID != "collection-1" || authority.created.VariantID != "variant-1" || authority.created.MediaType != "image/png" || string(authority.created.Body) != string(testPNGImage()) {
		t.Fatalf("artifact create = %#v", authority.created)
	}
	if !strings.Contains(output, `"session_id"`) || !strings.Contains(output, `"event_seq"`) {
		t.Fatalf("generation output lacks exact reference: %s", output)
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "image-call-redirect", map[string]any{"action": "generate_image", "prompt": "A red square", "collection_id": "other"}); err == nil || !strings.Contains(err.Error(), "must omit collection_id") {
		t.Fatalf("managed redirect error = %v", err)
	}
}

func TestManageArtifactGenerateImageResolvesExactRemixSourceAndPublishesLineage(t *testing.T) {
	imageBytes := testPNGImage()
	authority := &fakeArtifactAuthority{readBody: imageBytes, variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png"}}
	generator := &fakeManagedImageGenerator{image: imagegen.ManagedImage{Bytes: imageBytes, MediaType: "image/png"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedImageGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{settings: uisettings.UISettings{Tools: uisettings.ToolSettings{Image: uisettings.ToolImageSettings{DefaultModel: "gemini-image"}}}}, nil)
	ctx, scope := artifactToolContext()

	_, err := runtime.executeManageArtifact(ctx, scope, "remix", map[string]any{
		"action": "generate_image", "prompt": "make it warmer", "source_session_id": "source-session",
		"source_collection_id": "source-collection", "source_variant_id": "source-variant", "source_event_seq": 9,
	})
	if err != nil {
		t.Fatalf("generate image remix: %v", err)
	}
	if !authority.referenceRead || generator.req.Source == nil || string(generator.req.Source.Bytes) != string(imageBytes) || generator.req.Source.MediaType != "image/png" {
		t.Fatalf("remix resolution reference=%#v request=%#v", authority.reference, generator.req)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "source-collection" || authority.created.SourceVariantID != "source-variant" || authority.created.SourceEventSeq != 9 {
		t.Fatalf("published remix lineage = %#v", authority.created)
	}
	if !authority.created.AutoAccept {
		t.Fatalf("direct generated image did not request auto-accept: %+v", authority.created)
	}

	_, err = runtime.executeManageArtifact(ctx, scope, "remix-same-collection", map[string]any{
		"action": "generate_image", "prompt": "make it brighter", "collection_id": "source-collection",
		"collection_name": "Generated image", "collection_description": "model-authored defaults",
		"source_session_id": "source-session", "source_collection_id": "source-collection",
		"source_variant_id": "source-variant", "source_event_seq": 9,
	})
	if err != nil {
		t.Fatalf("generate image remix in source collection: %v", err)
	}
	if authority.created.CollectionID != "source-collection" || authority.created.CollectionName != "" || authority.created.CollectionDescription != "" {
		t.Fatalf("existing collection metadata was not omitted: %#v", authority.created)
	}

	if _, err := runtime.executeManageArtifact(ctx, scope, "partial-remix", map[string]any{"action": "generate_image", "prompt": "change it", "source_variant_id": "source-variant"}); err == nil || !strings.Contains(err.Error(), "source_session_id") {
		t.Fatalf("partial remix error = %v", err)
	}
	for name, invalidEventSeq := range map[string]any{"fractional": 1.5, "too-large": float64(1<<53) + 2} {
		t.Run(name+" event sequence", func(t *testing.T) {
			_, err := runtime.executeManageArtifact(ctx, scope, "invalid-remix-"+name, map[string]any{
				"action": "generate_image", "prompt": "change it", "source_session_id": "source-session",
				"source_collection_id": "source-collection", "source_variant_id": "source-variant", "source_event_seq": invalidEventSeq,
			})
			if err == nil || !strings.Contains(err.Error(), "source_event_seq") {
				t.Fatalf("invalid remix event sequence error = %v", err)
			}
		})
	}
}

func TestManageArtifactGenerateImageAcceptsPresentationAndProviderNeutralPixelAlias(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	generator := &fakeManagedImageGenerator{image: imagegen.ManagedImage{Bytes: testPNGImage(), MediaType: "image/png"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedImageGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{settings: uisettings.UISettings{Tools: uisettings.ToolSettings{Image: uisettings.ToolImageSettings{DefaultModel: "gemini-nano-banana-2"}}}}, nil)
	ctx, scope := artifactToolContext()

	_, err := runtime.executeManageArtifact(ctx, scope, "image-call-presentation", map[string]any{
		"action": "generate_image", "prompt": "A red square", "image_settings": map[string]any{"aspect_ratio": "1:1", "image_size": "2048x2048"},
		"presentation": map[string]any{"kind": "image", "label": "Red square", "previewable": true, "width": 2048, "height": 2048},
	})
	if err != nil {
		t.Fatalf("generate image with presentation: %v", err)
	}
	if generator.req.Settings["image_size"] != "2048x2048" || generator.req.Settings["aspect_ratio"] != "1:1" {
		t.Fatalf("provider-neutral generation settings = %#v", generator.req.Settings)
	}
	if authority.created.Presentation.Label != "Red square" || authority.created.Presentation.Kind != "image" || !authority.created.Presentation.Previewable || authority.created.Presentation.Width != 1 || authority.created.Presentation.Height != 1 {
		t.Fatalf("generated presentation = %#v", authority.created.Presentation)
	}
}

func TestManageArtifactGenerateImageAppliesTrustedExactOutputRequirements(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	generator := &fakeManagedImageGenerator{image: imagegen.ManagedImage{Bytes: testPNGImage(), MediaType: "image/png"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedImageGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{}, nil)
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "auth-1", AccountScopeID: "account-1", UserID: "user-1"}}
	requirements := &pebblestore.SessionArtifactOutputRequirements{Width: 2, Height: 1, AspectRatio: "2:1", Orientation: "landscape", ResolutionSource: "dimensions", RegistryVersion: artifact.OutputRequirementsRegistryVersion}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", OutputRequirements: requirements})

	if _, err := runtime.executeManageArtifact(ctx, scope, "image-call-requirements", map[string]any{"action": "generate_image", "prompt": "A wide red square"}); err != nil {
		t.Fatalf("generate exact image: %v", err)
	}
	if authority.created.Presentation.Width != 2 || authority.created.Presentation.Height != 1 || authority.created.OutputRequirements == nil {
		t.Fatalf("exact artifact create = %#v", authority.created)
	}
	if generator.req.Settings["aspect_ratio"] != "2:1" {
		t.Fatalf("provider-neutral generation settings = %#v", generator.req.Settings)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(authority.created.Body))
	if err != nil || config.Width != 2 || config.Height != 1 {
		t.Fatalf("published image config=%#v err=%v", config, err)
	}
}

func TestManageArtifactImageReadRequiresExactReferenceAndReturnsBoundedBase64(t *testing.T) {
	imageBytes := testPNGImage()
	authority := &fakeArtifactAuthority{readBody: imageBytes, variant: pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", SessionID: "session-1", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "image/png"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	if _, err := runtime.executeManageArtifact(ctx, scope, "image-read-inexact", map[string]any{"action": "read", "variant_id": "variant-1"}); err == nil || !strings.Contains(err.Error(), "exact ready") {
		t.Fatalf("inexact image read error = %v", err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "image-read", map[string]any{"action": "read", "session_id": "session-1", "collection_id": "collection-1", "variant_id": "variant-1", "event_seq": 7, "max_bytes": 1024})
	if err != nil {
		t.Fatalf("exact image read: %v", err)
	}
	if !strings.Contains(output, `"encoding":"base64"`) || !strings.Contains(output, base64.StdEncoding.EncodeToString(imageBytes)) || strings.Contains(output, "data_url") || strings.Contains(output, "storage") {
		t.Fatalf("image read output = %s", output)
	}
	authority.readBody = []byte("not an image")
	if _, err := runtime.executeManageArtifact(ctx, scope, "image-read-mismatch", map[string]any{"action": "read", "session_id": "session-1", "collection_id": "collection-1", "variant_id": "variant-1", "event_seq": 7}); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched image bytes error = %v", err)
	}
}

func TestManageArtifactReadQuotaErrorsAreActionableAndDoNotClaimUnavailability(t *testing.T) {
	ctx, scope := artifactToolContext()
	ref := map[string]any{"action": "read", "session_id": "session-1", "collection_id": "collection-1", "variant_id": "variant-1", "event_seq": 7}

	for _, test := range []struct {
		name      string
		mediaType string
		entry     string
		configure func(*fakeArtifactAuthority)
		code      string
	}{
		{name: "text", mediaType: "text/plain", configure: func(authority *fakeArtifactAuthority) { authority.readErr = artifact.ErrQuotaExceeded }, code: manageArtifactReadResponseQuotaCode},
		{name: "package entry", mediaType: "application/zip", entry: "index.html", configure: func(authority *fakeArtifactAuthority) { authority.packageReadErr = artifact.ErrQuotaExceeded }, code: manageArtifactPackageEntryResponseQuotaCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &fakeArtifactAuthority{variant: pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", SessionID: "session-1", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: test.mediaType}}
			test.configure(authority)
			runtime := NewRuntime(1)
			runtime.SetArtifactAuthority(authority)
			args := make(map[string]any, len(ref)+1)
			for key, value := range ref {
				args[key] = value
			}
			if test.entry != "" {
				args["entry"] = test.entry
			}
			_, err := runtime.executeManageArtifact(ctx, scope, "quota-"+test.name, args)
			if err == nil {
				t.Fatal("expected read response quota error")
			}
			message := err.Error()
			for _, required := range []string{"code=" + test.code, "does not mean the artifact is unavailable", "Use materialize", "complete exact reference"} {
				if !strings.Contains(message, required) {
					t.Fatalf("quota error lacks %q: %s", required, message)
				}
			}
		})
	}
}

func TestManageArtifactRetrievalContractSurfacesCompleteReadyReference(t *testing.T) {
	definition := manageArtifactDefinition()
	for _, requiredInstruction := range []string{
		"prior-session artifact library without scanning transcripts or storage folders",
		"next_cursor is an opaque continuation that must be passed back unchanged as cursor",
		"Never infer a selection when human names are ambiguous",
		"Collection-list results are not complete ready references",
		"call list again with collection_id",
		"obtain variant_id and event_seq",
		"copy session_id, collection_id, variant_id, and event_seq together",
		"prefer materialize or atomic materialize_batch over bulk read responses",
		"manipulate the imported files with normal workspace tools",
		"use publish_workspace to publish the finished file or package",
		"copy the original exact reference into source_session_id",
	} {
		if !strings.Contains(definition.Description, requiredInstruction) {
			t.Fatalf("definition does not explain %q: %s", requiredInstruction, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	for key, required := range map[string][]string{
		"action":      {"search", "materialize/materialize_batch", "publish_workspace"},
		"query":       {"cross-session", "complete exact references"},
		"cursor":      {"next_cursor", "unchanged", "never parse"},
		"limit":       {"next_cursor/cursor"},
		"references":  {"exact ready references", "atomically", "preflighted"},
		"source":      {"workspace-relative", "normal workspace tools"},
		"max_bytes":   {"response-quota error", "does not mean the artifact is unavailable", "materialize"},
		"destination": {"materialize_batch", "overwrite defaults to false"},
	} {
		description := properties[key].(map[string]any)["description"].(string)
		for _, fragment := range required {
			if !strings.Contains(description, fragment) {
				t.Fatalf("%s description lacks %q: %s", key, fragment, description)
			}
		}
	}
	for _, key := range []string{"session_id", "collection_id", "variant_id", "event_seq"} {
		field := properties[key].(map[string]any)
		description := field["description"].(string)
		for _, peer := range []string{"session_id", "collection_id", "variant_id", "event_seq"} {
			if peer != key && !strings.Contains(description, peer) {
				t.Fatalf("%s description does not name peer %s: %s", key, peer, description)
			}
		}
	}

	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "read without reference", args: map[string]any{"action": "read"}},
		{name: "read partial reference", args: map[string]any{"action": "read", "variant_id": "variant-1", "event_seq": 7}},
		{name: "get collection only", args: map[string]any{"action": "get", "collection_id": "collection-1"}},
		{name: "materialize partial reference", args: map[string]any{"action": "materialize", "variant_id": "variant-1", "destination": "artifact.txt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtime.executeManageArtifact(ctx, scope, "retrieval-contract", test.args)
			if err == nil {
				t.Fatal("expected incomplete reference error")
			}
			message := err.Error()
			for _, key := range []string{"session_id", "collection_id", "variant_id", "event_seq"} {
				if !strings.Contains(message, key) {
					t.Fatalf("error does not identify complete reference field %s: %s", key, message)
				}
			}
			if !strings.Contains(message, "same returned reference") {
				t.Fatalf("error does not explain how to recover: %s", message)
			}
		})
	}
}

func TestManageArtifactDefinitionDoesNotExposeProviderOrModel(t *testing.T) {
	raw, err := json.Marshal(manageArtifactDefinition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["provider"]; ok {
		t.Fatal("manage_artifact schema exposes provider")
	}
	if _, ok := properties["model"]; ok {
		t.Fatal("manage_artifact schema exposes model")
	}
	if imageSettings, ok := properties["image_settings"].(map[string]any); !ok {
		t.Fatal("manage_artifact schema lacks image settings")
	} else if nested, ok := imageSettings["properties"].(map[string]any); !ok {
		t.Fatal("manage_artifact image settings schema lacks properties")
	} else if _, provider := nested["provider"]; provider {
		t.Fatal("manage_artifact image settings expose provider")
	} else if _, model := nested["model"]; model {
		t.Fatal("manage_artifact image settings expose model")
	}
}

func TestManageArtifactDefinitionExplainsKindSpecificPartsContract(t *testing.T) {
	properties := manageArtifactDefinition().Parameters["properties"].(map[string]any)
	parts := properties["parts"].(map[string]any)
	partsDescription := parts["description"].(string)
	for _, want := range []string{
		"source-bound review/edit targets",
		"never create or prove independently replaceable bytes",
		"For text/html, omit parts",
		"server derive useful targets",
		"without splitting or rewriting the file",
		"explicitly supplied parts remain authoritative",
		"Use initial_parts only",
	} {
		if !strings.Contains(partsDescription, want) {
			t.Fatalf("parts description missing %q: %s", want, partsDescription)
		}
	}
	initialParts := properties["initial_parts"].(map[string]any)
	if initialParts["minItems"] != 2 {
		t.Fatalf("initial_parts schema = %#v", initialParts)
	}
	for _, want := range []string{"real independently byte-bearing", "server owns all chain", "Mutually exclusive"} {
		if !strings.Contains(initialParts["description"].(string), want) {
			t.Fatalf("initial_parts description missing %q: %s", want, initialParts["description"])
		}
	}
	partProperties := parts["items"].(map[string]any)["properties"].(map[string]any)
	kindDescription := partProperties["kind"].(map[string]any)["description"].(string)
	for _, want := range []string{"temporal requires start_ms/end_ms", "spatial requires normalized x/y/width/height", "page requires page", "state requires state_id", "selector requires selector"} {
		if !strings.Contains(kindDescription, want) {
			t.Fatalf("part kind description missing %q: %s", want, kindDescription)
		}
	}
}

func TestManageArtifactCreateCarriesReviewParts(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.executeManageArtifact(ctx, scope, "parts-create", map[string]any{
		"action": "create", "filename": "motion.html", "media_type": "text/html", "content": "animated",
		"parts": []any{
			map[string]any{"id": "intro", "label": "Intro", "kind": "temporal", "description": "Opening section", "start_ms": 0, "end_ms": 1200},
			map[string]any{"id": "hero", "label": "Hero", "kind": "spatial", "description": "Hero region", "x": 0.1, "y": 0.2, "width": 0.8, "height": 0.5},
		},
	})
	if err != nil {
		t.Fatalf("create artifact with parts: %v", err)
	}
	if len(authority.created.Parts) != 2 {
		t.Fatalf("created parts = %#v", authority.created.Parts)
	}
	if part := authority.created.Parts[0]; part.ID != "intro" || part.Kind != "temporal" || part.StartMs != 0 || part.EndMs != 1200 {
		t.Fatalf("temporal part = %#v", part)
	}
	if part := authority.created.Parts[1]; part.ID != "hero" || part.Kind != "spatial" || part.X != 0.1 || part.Y != 0.2 || part.Width != 0.8 || part.Height != 0.5 {
		t.Fatalf("spatial part = %#v", part)
	}
}

func TestManageArtifactCreateDerivesHTMLReviewTargetsWithoutMultipartPayloads(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	html := `<!doctype html><main id="hero" aria-label="Hero"></main><footer id="proof"></footer>`
	_, err := runtime.executeManageArtifact(ctx, scope, "derived-html-parts", map[string]any{
		"action": "create", "filename": "page.html", "media_type": "text/html", "content": html,
	})
	if err != nil {
		t.Fatalf("create monolithic HTML: %v", err)
	}
	if string(authority.created.Body) != html || len(authority.created.Parts) != 2 || authority.created.Parts[0].ID != "hero" || authority.created.Parts[1].ID != "proof" {
		t.Fatalf("monolithic HTML publication = %#v", authority.created)
	}
	if len(authority.initial.Parts) != 0 {
		t.Fatalf("derived review targets entered multipart composition: %#v", authority.initial)
	}
}

func TestManagedProfiledHTMLRejectsInitialPartsThatBypassTrustedPreflight(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: reviewedMotionProfile(t)})
	_, err := runtime.executeManageArtifact(ctx, scope, "profiled-initial-parts", map[string]any{
		"action": "create", "filename": "composed.html", "media_type": "text/html",
		"initial_parts": []any{
			map[string]any{"id": "shell", "label": "Shell", "media_type": "text/html", "content": `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`},
			map[string]any{"id": "runtime", "label": "Runtime", "media_type": "text/javascript", "content": "const animated = true;"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "animation_source_invalid") {
		t.Fatalf("profiled initial_parts error = %v", err)
	}
	if authority.reserveCalls != 0 || authority.createCalls != 0 || len(authority.initial.Parts) != 0 {
		t.Fatalf("profiled initial_parts bypassed preflight: reserve=%d create=%d initial=%#v", authority.reserveCalls, authority.createCalls, authority.initial)
	}
}

func TestProfiledNonHTMLInitialPartsRemainSupported(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: reviewedMotionProfile(t)})
	_, err := runtime.executeManageArtifact(ctx, scope, "profiled-text-parts", map[string]any{
		"action": "create", "filename": "composed.txt", "media_type": "text/plain",
		"initial_parts": []any{
			map[string]any{"id": "first", "label": "First", "media_type": "text/plain", "content": "first"},
			map[string]any{"id": "second", "label": "Second", "media_type": "text/plain", "content": "second"},
		},
	})
	if err != nil || len(authority.initial.Parts) != 2 || authority.reserveCalls != 0 {
		t.Fatalf("profiled non-HTML initial_parts: err=%v reserve=%d initial=%#v", err, authority.reserveCalls, authority.initial)
	}
}

func TestManageArtifactCreateInitialPartsUsesAuthoritativeCompositionContract(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "real-parts-create", map[string]any{
		"action": "create", "collection_name": "Composed", "filename": "composed.txt", "media_type": "text/plain",
		"initial_parts": []any{
			map[string]any{"id": "hero", "label": "Hero", "description": "Hero bytes", "media_type": "text/plain", "content": "hero", "locator": map[string]any{"kind": "semantic"}},
			map[string]any{"id": "footer", "label": "Footer", "media_type": "application/octet-stream", "content_base64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2})},
		},
	})
	if err != nil {
		t.Fatalf("create authoritative initial parts: %v", err)
	}
	if len(authority.initial.Parts) != 2 || string(authority.initial.Parts[0].Body) != "hero" || string(authority.initial.Parts[1].Body) != string([]byte{0, 1, 2}) {
		t.Fatalf("initial composition input = %#v", authority.initial)
	}
	if authority.initial.ArtifactChainID != pebblestore.RootSessionArtifactChainID("session-1", authority.initial.CollectionID, authority.initial.VariantID) || authority.initial.CompositionID == "" || authority.initial.Parts[0].RevisionID == "" || authority.initial.Parts[1].RevisionID == "" {
		t.Fatalf("server-owned composition identities = %#v", authority.initial)
	}
	for _, required := range []string{`"part_graph_state":"git_projection"`, `"part_definitions"`, `"composition"`, `"part_revision_id"`, `"status":"ready"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("authoritative create response lacks %s: %s", required, output)
		}
	}
	if authority.created.Body != nil || len(authority.created.Parts) != 0 {
		t.Fatalf("real parts fell through monolithic create: %#v", authority.created)
	}
}

func TestManageArtifactCreateInitialPartsRejectsAmbiguousOrMalformedPayloads(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(&fakeArtifactAuthority{})
	ctx, scope := artifactToolContext()
	valid := []any{
		map[string]any{"id": "hero", "label": "Hero", "media_type": "text/plain", "content": "hero"},
		map[string]any{"id": "footer", "label": "Footer", "media_type": "text/plain", "content": "footer"},
	}
	for _, test := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "monolithic conflict", args: map[string]any{"action": "create", "filename": "a.txt", "media_type": "text/plain", "content": "whole", "initial_parts": valid}, want: "cannot combine monolithic content"},
		{name: "locator conflict", args: map[string]any{"action": "create", "filename": "a.txt", "media_type": "text/plain", "parts": []any{map[string]any{"id": "hero", "label": "Hero", "kind": "semantic"}}, "initial_parts": valid}, want: "cannot combine locator-only parts"},
		{name: "too few", args: map[string]any{"action": "create", "filename": "a.txt", "media_type": "text/plain", "initial_parts": valid[:1]}, want: "2 to"},
		{name: "duplicate", args: map[string]any{"action": "create", "filename": "a.txt", "media_type": "text/plain", "initial_parts": []any{valid[0], valid[0]}}, want: "duplicate stable part id"},
		{name: "empty bytes", args: map[string]any{"action": "create", "filename": "a.txt", "media_type": "text/plain", "initial_parts": []any{map[string]any{"id": "hero", "label": "Hero", "media_type": "text/plain", "content": ""}, valid[1]}}, want: "between 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runtime.executeManageArtifact(ctx, scope, "invalid-"+test.name, test.args); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManageArtifactCreatePinsTrustedManagedDestinationAndLineage(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx := WithWorkspaceScope(context.Background(), WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}})
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{
		SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "call-1", ProgramID: "program-1", ProgramJobID: "job-1",
		IterationID: "iteration-1", IterationIndex: 1, CollectionID: "collection-trusted", VariantID: "variant-trusted",
	})
	_, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call", map[string]any{
		"action": "create", "collection_name": "redirected", "collection_description": "model-authored", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	})
	if err != nil {
		t.Fatalf("create trusted managed artifact: %v", err)
	}
	if authority.created.CollectionID != "collection-trusted" || authority.created.VariantID != "variant-trusted" {
		t.Fatalf("trusted destination not pinned: %#v", authority.created)
	}
	if authority.created.CollectionName != "" || authority.created.CollectionDescription != "" {
		t.Fatalf("managed create retained model-authored collection metadata: %#v", authority.created)
	}
	missingChild := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", TaskCallID: "call-1", CollectionID: "collection-trusted", VariantID: "variant-trusted"})
	if _, err := artifactPrincipal(missingChild, WorkspaceScope{SessionID: "parent-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}); err == nil || !strings.Contains(err.Error(), "requires trusted child session lineage") {
		t.Fatalf("missing managed child lineage error = %v", err)
	}
	if _, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "other-child", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call-mismatch", map[string]any{
		"action": "create", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	}); err == nil || !strings.Contains(err.Error(), "trusted session context is missing or inconsistent") {
		t.Fatalf("mismatched producer error = %v", err)
	}
	if _, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call-2", map[string]any{
		"action": "create", "collection_id": "redirected", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	}); err == nil || !strings.Contains(err.Error(), "managed create must omit collection_id") {
		t.Fatalf("trusted destination redirect error = %v", err)
	}
	principal, err := artifactPrincipal(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}})
	if err != nil || principal.SessionID != "parent-1" || principal.ChildSessionID != "child-1" || principal.TaskCallID != "call-1" || principal.ProgramJobID != "job-1" || principal.IterationIndex != 1 {
		t.Fatalf("trusted managed principal = %#v err=%v", principal, err)
	}
}

func TestManageArtifactAnimationProfileCreateContract(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "motion", Name: "manage_artifact", Arguments: `{"action":"create","filename":"motion.css","media_type":"text/css","content":"animated","animation_profile":{"profile":"motion_ui"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.AnimationProfile == nil || authority.created.AnimationProfile.ProfileID != "motion_ui" || authority.created.AnimationProfile.RuntimeKind != "native_css_waapi_svg" || authority.created.AnimationProfile.RuntimePackage != "" || authority.created.AnimationProfile.Budgets.NetworkAllowed || !strings.Contains(output, `"animation_profile"`) {
		t.Fatalf("created=%#v output=%s", authority.created, output)
	}
	if authority.reserveCalls != 0 {
		t.Fatalf("non-HTML profiled artifact entered animation publication gate: %+v", authority)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "override", Name: "manage_artifact", Arguments: `{"action":"create","filename":"motion.html","media_type":"text/html","content":"animated","animation_profile":{"profile":"motion_ui","runtime_version":"latest"}}`}); err == nil || !strings.Contains(err.Error(), "must contain only profile") {
		t.Fatalf("animation override error = %v", err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "unknown", Name: "manage_artifact", Arguments: `{"action":"create","filename":"motion.html","media_type":"text/html","content":"animated","animation_profile":{"profile":"unknown"}}`}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown animation profile error = %v", err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "bad-final", Name: "manage_artifact", Arguments: `{"action":"create","filename":"render.html","media_type":"text/html","content":"animated","animation_profile":{"profile":"final_render"}}`}); err == nil || !strings.Contains(err.Error(), "video/mp4") {
		t.Fatalf("final render media error = %v", err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "final", Name: "manage_artifact", Arguments: `{"action":"create","filename":"render.mp4","media_type":"video/mp4","content":"mp4","animation_profile":{"profile":"final_render"}}`}); err != nil {
		t.Fatalf("final render: %v", err)
	}
}

func TestManagedArtifactAnimationProfileIsInjectedAndOverrideRejected(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: profile})
	if _, err := runtime.executeManageArtifact(ctx, scope, "override", map[string]any{"action": "create", "filename": "motion.html", "media_type": "text/html", "content": "animated", "animation_profile": map[string]any{"profile": "spatial_3d"}}); err == nil || !strings.Contains(err.Error(), "must omit animation_profile") {
		t.Fatalf("managed override error = %v", err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "valid", map[string]any{"action": "create", "filename": "motion.css", "media_type": "text/css", "content": "animated"})
	if err != nil {
		t.Fatal(err)
	}
	profile.ProfileID = "mutated"
	if authority.created.AnimationProfile == nil || authority.created.AnimationProfile.ProfileID != "motion_ui" || !strings.Contains(output, `"animation_profile"`) {
		t.Fatalf("created=%#v output=%s", authority.created, output)
	}
}

func TestManageArtifactCreateUsesTrustedOwnershipAndFinalAuthorityResult(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-1", Name: "manage_artifact", Arguments: `{"action":"create","collection_name":"Drafts","filename":"note.txt","media_type":"text/plain","content":"managed","presentation":{"kind":"text","label":"Note","previewable":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.principal.SessionID != "session-1" || authority.principal.AccountScopeID != "account-1" || authority.principal.UserID != "user-1" || authority.principal.RunID != "run-1" {
		t.Fatalf("principal = %+v", authority.principal)
	}
	if authority.created.CollectionID == "" || authority.created.VariantID == "" || authority.created.RequestID == "" || string(authority.created.Body) != "managed" {
		t.Fatalf("create input = %+v", authority.created)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	artifactPayload := payload["artifact"].(map[string]any)
	if artifactPayload["status"] != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("output = %s", output)
	}
	if artifactPayload["session_id"] != "session-1" {
		t.Fatalf("output omitted exact artifact session identity: %s", output)
	}
	reference := payload["reference"].(map[string]any)
	if reference["session_id"] != "session-1" || reference["collection_id"] != artifactPayload["collection_id"] || reference["variant_id"] != artifactPayload["id"] || reference["event_seq"] != artifactPayload["event_seq"] {
		t.Fatalf("output reference does not match the exact ready artifact: %s", output)
	}
}

func TestManageArtifactRejectsMissingOrMismatchedTrustedSession(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(&fakeArtifactAuthority{})
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "other-session", RunID: "run-1"})
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-2", Name: "manage_artifact", Arguments: `{"action":"list"}`})
	if err == nil {
		t.Fatal("expected trusted session mismatch")
	}
}

func TestManageArtifactAcceptsDistinctAuthenticatedAndV3SessionIDs(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := WorkspaceScope{
		PrimaryPath: ".",
		SessionID:   "v3-session-1",
		Principal: identity.Principal{
			Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1", SessionID: "local-product-session-1",
		},
	}
	ctx := WithArtifactRunContext(WithWorkspaceScope(context.Background(), scope), ArtifactRunContext{SessionID: "v3-session-1", RunID: "run-1"})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-auth-session", Name: "manage_artifact", Arguments: `{"action":"list"}`}); err != nil {
		t.Fatalf("distinct authenticated and V3 session ids: %v", err)
	}
	if authority.principal.SessionID != "v3-session-1" || authority.principal.AccountScopeID != "account-1" || authority.principal.UserID != "user-1" {
		t.Fatalf("artifact principal = %#v", authority.principal)
	}
}

func TestManageArtifactListSelectAndDeleteUseOpaqueReferences(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-list", Name: "manage_artifact", Arguments: `{"action":"list","collection_id":"collection-1","limit":5}`})
	if err != nil {
		t.Fatal(err)
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(output), &listed); err != nil || listed["count"].(float64) != 1 {
		t.Fatalf("list output=%q err=%v", output, err)
	}
	output, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-select", Name: "manage_artifact", Arguments: `{"action":"select","collection_id":"collection-1","variant_id":"variant-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	var selected map[string]any
	if err := json.Unmarshal([]byte(output), &selected); err != nil {
		t.Fatal(err)
	}
	reference := selected["reference"].(map[string]any)
	if reference["variant_id"] != "variant-1" {
		t.Fatalf("selection output = %s", output)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-delete", Name: "manage_artifact", Arguments: `{"action":"delete","collection_id":"collection-1","variant_id":"variant-1"}`}); err != nil {
		t.Fatal(err)
	}
	if authority.deleted != "collection-1/variant-1" {
		t.Fatalf("deleted = %q", authority.deleted)
	}
}

func TestManageArtifactPackageAndBoundedTextRead(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("hello"), variant: pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", SessionID: "session-1", EventSeq: 7, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-package", Name: "manage_artifact", Arguments: `{"action":"create_package","collection_name":"UI","filename":"ui.zip","entries":[{"name":"index.html","content":"<h1>Hi</h1>"}]}`}); err != nil {
		t.Fatal(err)
	}
	if len(authority.packaged.Entries) != 1 || string(authority.packaged.Entries[0].Data) != "<h1>Hi</h1>" {
		t.Fatalf("package input = %+v", authority.packaged)
	}
	if !authority.packaged.AutoAccept {
		t.Fatalf("direct package did not request auto-accept: %+v", authority.packaged)
	}
	authority.variant.MediaType = "text/plain"
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read", Name: "manage_artifact", Arguments: `{"action":"read","variant_id":"variant-1","max_bytes":100}`})
	if err != nil || !json.Valid([]byte(output)) {
		t.Fatalf("read output=%q err=%v", output, err)
	}
	authority.variant.MediaType = "application/octet-stream"
	authority.variant.Presentation.Kind = "download"
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-binary", Name: "manage_artifact", Arguments: `{"action":"read","variant_id":"variant-1"}`})
	if err == nil {
		t.Fatal("expected binary read rejection")
	}
	if err.Error() != "manage_artifact read returns only UTF-8 text or supported image artifacts" {
		t.Fatalf("unexpected binary read error: %v", err)
	}
}

func TestManageArtifactReadsPackageManifestAndUTF8Entry(t *testing.T) {
	authority := &fakeArtifactAuthority{
		readBody:        []byte("<main>selected</main>"),
		packageManifest: []artifact.PackageManifestEntry{{Name: "assets/site.css", Size: 6}, {Name: "index.html", Size: 21}},
		variant:         pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", SessionID: "session-1", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "application/zip"},
	}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()

	refArgs := `"session_id":"session-1","collection_id":"collection-1","variant_id":"variant-1","event_seq":7`
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-manifest", Name: "manage_artifact", Arguments: `{"action":"read",` + refArgs + `}`})
	if err != nil || !strings.Contains(output, `"manifest":[{"name":"assets/site.css","size":6},{"name":"index.html","size":21}]`) || strings.Contains(output, `"content"`) {
		t.Fatalf("manifest output=%q err=%v", output, err)
	}
	output, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-entry", Name: "manage_artifact", Arguments: `{"action":"read",` + refArgs + `,"entry":"index.html"}`})
	if err != nil || !strings.Contains(output, `"entry":"index.html"`) || !strings.Contains(output, `"content":"\u003cmain\u003eselected\u003c/main\u003e"`) {
		t.Fatalf("entry output=%q err=%v", output, err)
	}
	authority.readBody = []byte{0xff}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-entry-binary", Name: "manage_artifact", Arguments: `{"action":"read",` + refArgs + `,"entry":"index.html"}`}); err == nil || !strings.Contains(err.Error(), "UTF-8 regular entries") {
		t.Fatalf("non-UTF-8 package entry error = %v", err)
	}
}

func TestManageArtifactReadsExplicitAttachedReference(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("selected design"), variant: pebblestore.SessionArtifactVariant{ID: "variant-source", CollectionID: "collection-source", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read-source", Name: "manage_artifact", Arguments: `{"action":"read","session_id":"source-session","collection_id":"collection-source","variant_id":"variant-source","event_seq":42,"max_bytes":64}`})
	if err != nil {
		t.Fatal(err)
	}
	if !authority.referenceRead || authority.reference.SessionID != "source-session" || authority.reference.CollectionID != "collection-source" || authority.reference.VariantID != "variant-source" || authority.reference.EventSeq != 42 {
		t.Fatalf("source reference = %+v read=%t", authority.reference, authority.referenceRead)
	}
	if !strings.Contains(output, `"content":"selected design"`) || strings.Contains(output, "workspace") {
		t.Fatalf("read output = %s", output)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read-incomplete", Name: "manage_artifact", Arguments: `{"action":"read","session_id":"source-session","variant_id":"variant-source"}`})
	if err == nil || !strings.Contains(err.Error(), "complete ready reference") || !strings.Contains(err.Error(), "session_id") || !strings.Contains(err.Error(), "collection_id") || !strings.Contains(err.Error(), "variant_id") || !strings.Contains(err.Error(), "event_seq") {
		t.Fatalf("incomplete source reference error = %v", err)
	}
}

func TestManageArtifactMaterializesExactReferenceIntoTrustedWorkspace(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(scope.PrimaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-materialize", Name: "manage_artifact", Arguments: `{"action":"materialize","session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":42,"destination":"designs/selected.txt","overwrite":true}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.workspaceRoot != scope.PrimaryPath || authority.destination != "designs/selected.txt" || !authority.overwrite || authority.materializedRef.EventSeq != 42 {
		t.Fatalf("materialize input: root=%q destination=%q overwrite=%t ref=%+v", authority.workspaceRoot, authority.destination, authority.overwrite, authority.materializedRef)
	}
	if strings.Contains(output, scope.PrimaryPath) || !strings.Contains(output, `"destination":"designs/selected.txt"`) {
		t.Fatalf("materialize output = %s", output)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-materialize-current", Name: "manage_artifact", Arguments: `{"action":"materialize","variant_id":"source-variant","destination":"selected.txt"}`})
	if err == nil || !strings.Contains(err.Error(), "complete ready reference") || !strings.Contains(err.Error(), "session_id") || !strings.Contains(err.Error(), "collection_id") || !strings.Contains(err.Error(), "variant_id") || !strings.Contains(err.Error(), "event_seq") {
		t.Fatalf("materialize without exact reference error = %v", err)
	}
	managedCtx := WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-call", CollectionID: "managed-collection", VariantID: "managed-variant"})
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(managedCtx, scope, Call{CallID: "call-managed-materialize", Name: "manage_artifact", Arguments: `{"action":"materialize","session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":42,"destination":"selected.txt"}`})
	if err == nil || !strings.Contains(err.Error(), "managed Designer runs cannot materialize") {
		t.Fatalf("managed child materialize error = %v", err)
	}
}

func TestManageArtifactMaterializeBatchUsesExactReferencesAndReportsDigests(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(scope.PrimaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "batch", map[string]any{
		"action": "materialize_batch", "destination": "artifacts/imported",
		"references": []any{
			map[string]any{"session_id": "source-a", "collection_id": "collection-a", "variant_id": "variant-a", "event_seq": 7},
			map[string]any{"session_id": "source-b", "collection_id": "collection-b", "variant_id": "variant-b", "event_seq": 9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(authority.batchItems) != 2 || authority.batchItems[1].Reference.EventSeq != 9 || authority.destination != "artifacts/imported" {
		t.Fatalf("batch inputs = %#v destination=%q", authority.batchItems, authority.destination)
	}
	for _, required := range []string{`"count":2`, `"files":2`, `"bytes":14`, `"digest_sha256":"digest-0"`, `"session_id":"source-a"`, `"event_seq":9`} {
		if !strings.Contains(output, required) {
			t.Fatalf("batch output lacks %s: %s", required, output)
		}
	}
	duplicate := map[string]any{"session_id": "same", "collection_id": "same", "variant_id": "same", "event_seq": 1}
	if _, err := runtime.executeManageArtifact(ctx, scope, "batch-duplicate", map[string]any{"action": "materialize_batch", "destination": "out", "references": []any{duplicate, duplicate}}); err == nil || !strings.Contains(err.Error(), "duplicate exact references") {
		t.Fatalf("duplicate reference error = %v", err)
	}
}

func TestManageArtifactPublishWorkspaceRejectsUnsafePrivateSourcesAndPreservesLineage(t *testing.T) {
	authority := &fakeArtifactAuthority{variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, ".gitignore"), []byte("private.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, "revision.txt"), []byte("revised"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "publish", map[string]any{
		"action": "publish_workspace", "source": "revision.txt", "collection_id": "collection-new", "filename": "revision.txt",
		"source_session_id": "source-session", "source_collection_id": "source-collection", "source_variant_id": "source-variant", "source_event_seq": 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if authority.createdFromFile.SourcePath != filepath.Join(scope.PrimaryPath, "revision.txt") || authority.createdFromFile.Package || authority.createdFromFile.SourceEventSeq != 42 || authority.createdFromFile.CollectionID != "collection-new" {
		t.Fatalf("workspace publication = %#v", authority.createdFromFile)
	}
	if !authority.createdFromFile.AutoAccept {
		t.Fatalf("workspace publication did not request auto-accept: %+v", authority.createdFromFile)
	}
	if !strings.Contains(output, `"digest_sha256":"published-digest"`) || !strings.Contains(output, `"event_seq":11`) || strings.Contains(output, scope.PrimaryPath) {
		t.Fatalf("publish output = %s", output)
	}
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, "private.env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "private", map[string]any{"action": "publish_workspace", "source": "private.env"}); err == nil || !strings.Contains(err.Error(), "ignored private") {
		t.Fatalf("ignored private source error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(scope.PrimaryPath, "link.txt")); err == nil {
		if _, err := runtime.executeManageArtifact(ctx, scope, "link", map[string]any{"action": "publish_workspace", "source": "link.txt"}); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink source error = %v", err)
		}
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "traversal", map[string]any{"action": "publish_workspace", "source": "../outside.txt"}); err == nil || !strings.Contains(err.Error(), "canonical workspace-relative") {
		t.Fatalf("traversal source error = %v", err)
	}
}

func TestManageArtifactDeriveTextPreservesUnmatchedBytesAndExactLineage(t *testing.T) {
	sourceBody := []byte("prefix\nconst duration=74920;\nconst treatment='base';\nsuffix\n")
	authority := &fakeArtifactAuthority{readBody: sourceBody, variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, Filename: "source.txt", MediaType: "text/plain", OutputRequirements: &pebblestore.SessionArtifactOutputRequirements{PresetID: "landscape_video"}}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	args := map[string]any{
		"action": "derive_text", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source", "event_seq": 9,
		"text_edits": []any{
			map[string]any{"old_string": "duration=74920", "new_string": "duration=10000"},
			map[string]any{"old_string": "treatment='base'", "new_string": "treatment='orbital'"},
		},
	}
	if _, err := runtime.executeManageArtifact(ctx, scope, "derive-exact", args); err != nil {
		t.Fatal(err)
	}
	want := "prefix\nconst duration=10000;\nconst treatment='orbital';\nsuffix\n"
	if string(authority.created.Body) != want {
		t.Fatalf("derived body = %q", authority.created.Body)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "source-collection" || authority.created.SourceVariantID != "source" || authority.created.SourceEventSeq != 9 {
		t.Fatalf("derived lineage = %+v", authority.created)
	}
	if authority.created.OutputRequirements == nil {
		t.Fatalf("derived snapshot metadata missing: %+v", authority.created)
	}
}

func TestManageArtifactDeriveTextFailsBeforePublishingAmbiguousEdit(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("same same"), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, Filename: "source.txt", MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.executeManageArtifact(ctx, scope, "derive-ambiguous", map[string]any{"action": "derive_text", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source", "event_seq": 9, "text_edits": []any{map[string]any{"old_string": "same", "new_string": "new"}}})
	if err == nil || !strings.Contains(err.Error(), "matched 2 times") {
		t.Fatalf("ambiguous derive error = %v", err)
	}
	if authority.created.RequestID != "" {
		t.Fatalf("ambiguous edit published: %+v", authority.created)
	}
}

func TestManageArtifactPublishWorkspaceAttachesReviewedAnimationProfileWithoutChangingSource(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{result: htmlcapture.AnimationResult{PreviewPNG: testAnimationFallbackPNG(t), DurationMS: 6000, FPS: 30, FrameCount: 180}})
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()
	body := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":6000,"fps":30}</script>`)
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, "intro.html"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "publish-profiled", map[string]any{"action": "publish_workspace", "source": "intro.html", "animation_profile": map[string]any{"profile": "motion_ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"trusted_animation_preflight":true`) {
		t.Fatalf("profiled workspace publication omitted trusted preflight evidence: %s", output)
	}
	if authority.created.AnimationProfile == nil || authority.created.AnimationProfile.ProfileID != "motion_ui" || authority.created.Body == nil || authority.publishCalls != 0 || authority.createCalls != 1 {
		t.Fatalf("profiled workspace publication = create=%#v file=%#v", authority.created, authority.createdFromFile)
	}
	if string(authority.created.Body) != string(body) {
		t.Fatalf("profiled workspace source bytes changed")
	}
	if len(authority.created.Parts) != 1 || authority.created.Parts[0].Kind != "temporal" || authority.created.Parts[0].EndMs != 6000 {
		t.Fatalf("profiled workspace animation parts = %+v, want canonical 6000ms duration metadata", authority.created.Parts)
	}
}

func TestManageArtifactPublishWorkspaceInheritsOnlyAuthenticatedAnimationProfile(t *testing.T) {
	authority := &fakeArtifactAuthority{variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/css", AnimationProfile: reviewedMotionProfile(t)}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, "motion.css"), []byte("motion"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "publish_workspace", "source": "motion.css", "source_session_id": "source-session", "source_collection_id": "source-collection", "source_variant_id": "source-variant", "source_event_seq": 42}
	if _, err := runtime.executeManageArtifact(ctx, scope, "publish-motion", args); err != nil {
		t.Fatal(err)
	}
	if authority.createdFromFile.AnimationProfile == nil || authority.createdFromFile.AnimationProfile.ProfileID != "motion_ui" || authority.reference.EventSeq != 42 || authority.reserveCalls != 0 || authority.publishCalls != 1 {
		t.Fatalf("workspace profile inheritance = %#v ref=%#v", authority.createdFromFile.AnimationProfile, authority.reference)
	}
	badProfile := *reviewedMotionProfile(t)
	badProfile.RegistryVersion = "untrusted"
	authority.variant.AnimationProfile = &badProfile
	if _, err := runtime.executeManageArtifact(ctx, scope, "publish-bad-profile", args); err == nil || !strings.Contains(err.Error(), "incompatible animation profile snapshot") {
		t.Fatalf("incompatible profile error = %v", err)
	}
	delete(args, "source_event_seq")
	if _, err := runtime.executeManageArtifact(ctx, scope, "publish-incomplete", args); err == nil || !strings.Contains(err.Error(), "all four fields") {
		t.Fatalf("incomplete source error = %v", err)
	}
}

func TestManageArtifactCreateCarriesAuthenticatedSourceLineage(t *testing.T) {
	authority := &fakeArtifactAuthority{variant: pebblestore.SessionArtifactVariant{ID: "variant-source", CollectionID: "collection-source", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-derive", Name: "manage_artifact", Arguments: `{"action":"create","collection_name":"Derived","filename":"derived.txt","media_type":"text/plain","content":"revision","source_session_id":"source-session","source_collection_id":"collection-source","source_variant_id":"variant-source","source_event_seq":42}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "collection-source" || authority.created.SourceVariantID != "variant-source" || authority.created.SourceEventSeq != 42 {
		t.Fatalf("derived input = %+v", authority.created)
	}
}
