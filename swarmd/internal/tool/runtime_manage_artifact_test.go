package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/uisettings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
	packaged        artifact.CreatePackageInput
	readBody        []byte
	packageManifest []artifact.PackageManifestEntry
	variant         pebblestore.SessionArtifactVariant
	reference       pebblestore.SessionArtifactSelectionReference
	referenceRead   bool
	deleted         string
	materializedRef pebblestore.SessionArtifactSelectionReference
	workspaceRoot   string
	destination     string
	overwrite       bool
}

func (f *fakeArtifactAuthority) Create(_ context.Context, principal artifact.Principal, input artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.created = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: input.MediaType, Size: int64(len(input.Body)), Presentation: input.Presentation, OutputRequirements: input.OutputRequirements}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) CreatePackage(_ context.Context, principal artifact.Principal, input artifact.CreatePackageInput) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.packaged = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, SessionID: principal.SessionID, EventSeq: 1, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: "application/zip", Size: 1, Presentation: input.Presentation, OutputRequirements: input.OutputRequirements}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) List(principal artifact.Principal, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	f.principal = principal
	return []pebblestore.SessionArtifactCollection{{ID: "collection-1", Name: "Drafts"}}, nil
}
func (f *fakeArtifactAuthority) ListVariants(principal artifact.Principal, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	return []pebblestore.SessionArtifactVariant{{ID: "variant-1", CollectionID: collectionID}}, nil
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
	return append([]byte(nil), f.readBody...), variant, err
}
func (f *fakeArtifactAuthority) ReadReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference, f.referenceRead = principal, ref, true
	return append([]byte(nil), f.readBody...), f.variant, nil
}
func (f *fakeArtifactAuthority) ReadPackageReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ string, _ int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference, f.referenceRead = principal, ref, true
	return append([]artifact.PackageManifestEntry(nil), f.packageManifest...), append([]byte(nil), f.readBody...), f.variant, nil
}
func (f *fakeArtifactAuthority) MaterializeReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, workspaceRoot, destination string, overwrite bool) (artifact.Materialized, error) {
	f.principal, f.materializedRef, f.workspaceRoot, f.destination, f.overwrite = principal, ref, workspaceRoot, destination, overwrite
	return artifact.Materialized{Destination: destination, Files: 1, Bytes: 7}, nil
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
	if _, err := runtime.executeManageArtifact(ctx, scope, "partial-remix", map[string]any{"action": "generate_image", "prompt": "change it", "source_variant_id": "source-variant"}); err == nil || !strings.Contains(err.Error(), "source_session_id") {
		t.Fatalf("partial remix error = %v", err)
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

func TestManageArtifactRetrievalContractSurfacesCompleteReadyReference(t *testing.T) {
	definition := manageArtifactDefinition()
	if !strings.Contains(definition.Description, "copy session_id, collection_id, variant_id, and event_seq together") {
		t.Fatalf("definition does not explain exact ready reference retrieval: %s", definition.Description)
	}
	properties := definition.Parameters["properties"].(map[string]any)
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
	if err == nil || !strings.Contains(err.Error(), "requires session_id, collection_id, variant_id, and event_seq") {
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
	if err == nil || !strings.Contains(err.Error(), "requires session_id, collection_id, variant_id, and event_seq") {
		t.Fatalf("materialize without exact reference error = %v", err)
	}
	managedCtx := WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-call", CollectionID: "managed-collection", VariantID: "managed-variant"})
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(managedCtx, scope, Call{CallID: "call-managed-materialize", Name: "manage_artifact", Arguments: `{"action":"materialize","session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":42,"destination":"selected.txt"}`})
	if err == nil || !strings.Contains(err.Error(), "managed Designer runs cannot materialize") {
		t.Fatalf("managed child materialize error = %v", err)
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
