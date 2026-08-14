package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageArtifactListPresets(t *testing.T) {
	runtime := NewRuntime(1)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "presets", Name: "manage_artifact", Arguments: `{"action":"list_presets"}`})
	if err != nil { t.Fatal(err) }
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil { t.Fatal(err) }
	if payload["registry_version"] == "" || payload["reviewed_source"] == "" || payload["reviewed_date"] == "" || payload["count"].(float64) != 6 { t.Fatalf("payload = %#v", payload) }
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "presets-extra", Name: "manage_artifact", Arguments: `{"action":"list_presets","limit":1}`}); err == nil {
		t.Fatal("list_presets accepted unrelated arguments")
	}
}

func TestManagedArtifactRequirementsAreInjectedAndOverridesRejected(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithWorkspaceScope(context.Background(), scope)
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", OutputRequirements: requirements})
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "override", Name: "manage_artifact", Arguments: `{"action":"create","filename":"header.svg","media_type":"image/svg+xml","content":"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>","output_requirements":{"preset":"square_1080"}}`})
	if err == nil || !strings.Contains(err.Error(), "must omit output_requirements") { t.Fatalf("override err = %v", err) }
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "dimensions", Name: "manage_artifact", Arguments: `{"action":"create","filename":"header.svg","media_type":"image/svg+xml","content":"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>","presentation":{"width":1080,"height":1080}}`})
	if err == nil || !strings.Contains(err.Error(), "conflicts with output requirement") { t.Fatalf("dimension err = %v", err) }
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "valid", Name: "manage_artifact", Arguments: `{"action":"create","filename":"header.svg","media_type":"image/svg+xml","content":"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"}`})
	if err != nil { t.Fatal(err) }
	if authority.created.OutputRequirements == nil || authority.created.Presentation.Width != 1500 || authority.created.Presentation.Height != 500 || !strings.Contains(output, "output_requirements") { t.Fatalf("created=%#v output=%s", authority.created, output) }
	requirements.Width = 1
	if authority.created.OutputRequirements.Width != 1500 {
		t.Fatalf("trusted requirements were not cloned: %#v", authority.created.OutputRequirements)
	}
}

func TestManagedArtifactPackageRequirementsAreInjected(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithWorkspaceScope(context.Background(), scope)
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "square_1080", Width: 1080, Height: 1080, AspectRatio: "1:1", Orientation: "square", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", OutputRequirements: requirements})
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "package", Name: "manage_artifact", Arguments: `{"action":"create_package","filename":"design.zip","entries":[{"name":"index.html","content":"ready"}]}`})
	if err != nil { t.Fatal(err) }
	if authority.packaged.OutputRequirements == nil || authority.packaged.OutputRequirements.PresetID != "square_1080" || authority.packaged.Presentation.Width != 1080 || authority.packaged.Presentation.Height != 1080 || !strings.Contains(output, "output_requirements") {
		t.Fatalf("packaged=%#v output=%s", authority.packaged, output)
	}
}

func TestOrdinaryArtifactCreateResolvesRequirements(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "ordinary", Name: "manage_artifact", Arguments: `{"action":"create","filename":"header.svg","media_type":"image/svg+xml","content":"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>","output_requirements":{"preset":"twitter_header"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.OutputRequirements == nil || authority.created.OutputRequirements.PresetID != "x_header" || authority.created.Presentation.Width != 1500 || authority.created.Presentation.Height != 500 {
		t.Fatalf("created = %#v", authority.created)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "ordinary-conflict", Name: "manage_artifact", Arguments: `{"action":"create","filename":"header.svg","media_type":"image/svg+xml","content":"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>","presentation":{"width":1080,"height":1080},"output_requirements":{"preset":"twitter_header"}}`})
	if err == nil || !strings.Contains(err.Error(), "conflicts with output requirement") {
		t.Fatalf("ordinary conflict err = %v", err)
	}
}

func TestArtifactOutputRequirementsRemainImmutableAtFinalization(t *testing.T) {
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	current := pebblestore.SessionArtifactVariant{ID: "variant", Status: pebblestore.SessionArtifactStatusStaging, OutputRequirements: requirements, Presentation: pebblestore.SessionArtifactPresentation{Width: 1500, Height: 500}}
	incoming := pebblestore.SessionArtifactVariant{ID: "variant", Filename: "header.svg", MediaType: "image/svg+xml", Presentation: pebblestore.SessionArtifactPresentation{Label: "Header"}}
	if incoming.OutputRequirements != nil {
		t.Fatal("test precondition failed")
	}
	incoming.OutputRequirements = cloneArtifactOutputRequirements(current.OutputRequirements)
	if err := enforceArtifactPresentationRequirements(&incoming.Presentation, incoming.OutputRequirements); err != nil {
		t.Fatal(err)
	}
	if *incoming.OutputRequirements != *current.OutputRequirements || incoming.Presentation.Width != 1500 || incoming.Presentation.Height != 500 {
		t.Fatalf("incoming = %#v", incoming)
	}
}

func TestOldArtifactOutputRequirementsCompatibility(t *testing.T) {
	var variant pebblestore.SessionArtifactVariant
	if err := json.Unmarshal([]byte(`{"version":1,"id":"v","collection_id":"c","account_scope_id":"a","session_id":"s","status":"ready","created_at":1,"updated_at":1,"event_seq":1}`), &variant); err != nil { t.Fatal(err) }
	if variant.OutputRequirements != nil { t.Fatalf("legacy requirements = %#v", variant.OutputRequirements) }
}
