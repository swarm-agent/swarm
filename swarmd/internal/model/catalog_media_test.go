package model

import (
	"encoding/json"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: snapshotModelMediaCapabilities and rekeyCatalogRecord must preserve
// Claude image facts while using the CLI surface, without mutating API records
// or inventing support for unknown models. Unit tests isolate this translation.
func TestClaudeCodeCatalogMediaFacts(t *testing.T) {
	yes := true
	snapshot := swarmSnapshot{Definitions: map[string]swarmSnapshotProviderDefinition{"anthropic": {Multimodal: json.RawMessage(`{"image_input_media_types":["image/png","image/jpeg","image/gif","image/webp"]}`)}}}
	model := swarmSnapshotModel{}
	model.Capabilities.SupportsImageInput = &yes
	media, err := snapshotModelMediaCapabilities(snapshot, "claudecode", model)
	if err != nil {
		t.Fatal(err)
	}
	if media == nil || media.ProviderSurface != provideriface.MediaProviderSurfaceClaudeCodeMCP || media.CredentialSurface != provideriface.MediaCredentialSurfaceClaudeCodeCLI || len(media.Inputs) != 1 || media.Inputs[0].State != pebblestore.ModelCatalogMediaStateSupported || len(media.Inputs[0].MIMETypes) != 4 {
		t.Fatalf("invalid media facts: %+v", media)
	}
	original := pebblestore.ModelCatalogRecord{Provider: "anthropic", Media: &pebblestore.ModelCatalogMediaCapabilities{ProviderSurface: provideriface.MediaProviderSurfaceAnthropicMessages, CredentialSurface: provideriface.MediaCredentialSurfaceAnthropicAPIKey}}
	mapped := rekeyCatalogRecord(original, "claudecode")
	if mapped.Media.ProviderSurface != provideriface.MediaProviderSurfaceClaudeCodeMCP || original.Media.ProviderSurface != provideriface.MediaProviderSurfaceAnthropicMessages {
		t.Fatal("surface translation missing or source mutated")
	}
	unknown, err := snapshotModelMediaCapabilities(swarmSnapshot{}, "claudecode", swarmSnapshotModel{})
	if err != nil || unknown.State != pebblestore.ModelCatalogMediaStateUnknown || len(unknown.Inputs) != 0 {
		t.Fatalf("unknown facts granted: %+v, %v", unknown, err)
	}
}
