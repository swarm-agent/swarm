package videogen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Written purpose / invariant:
// Video operation constraint projection must accurately reflect authoritative videogen execution policy:
// Google Veo 3.1 standard and fast models support extend (locked to 720p and 8s, source must be Google Veo standard/fast)
// and reject edit; Veo Lite models reject edit and reject extend; Google Stable Omni (gemini-omni-1.1-flash) supports
// edit and extend (no duration selection allowed, source must be stable Omni interaction); Preview Omni
// (gemini-omni-flash-preview) rejects both edit and extend; OpenRouter video models reject edit and extend.

func TestCheckVideoEditSupport(t *testing.T) {
	optsWithEdit := &ParsedVideoOptions{ConversationalEditingSupported: true}
	optsWithoutEdit := &ParsedVideoOptions{ConversationalEditingSupported: false}

	// 1. Veo 3.1 Standard rejected for editing
	if err := CheckVideoEditSupport(ProviderGoogleGemini, "veo-3.1-generate-preview", optsWithEdit); err == nil || !strings.Contains(err.Error(), "Veo models do not support video editing") {
		t.Fatalf("expected Veo edit rejection, got: %v", err)
	}

	// 2. Veo Lite rejected for editing
	if err := CheckVideoEditSupport(ProviderGoogleGemini, "veo-3.1-lite-generate-preview", optsWithEdit); err == nil || !strings.Contains(err.Error(), "Veo models do not support video editing") {
		t.Fatalf("expected Veo Lite edit rejection, got: %v", err)
	}

	// 3. Preview Omni rejected for editing (stable Omni required)
	if err := CheckVideoEditSupport(ProviderGoogleGemini, "gemini-omni-flash-preview", optsWithEdit); err == nil || !strings.Contains(err.Error(), "only supported on stable Gemini Omni") {
		t.Fatalf("expected Preview Omni edit rejection, got: %v", err)
	}

	// 4. OpenRouter Omni rejected for editing (Google Gemini provider required)
	if err := CheckVideoEditSupport(ProviderOpenRouter, "gemini-omni-1.1-flash", optsWithEdit); err == nil || !strings.Contains(err.Error(), "not supported on provider") {
		t.Fatalf("expected OpenRouter edit rejection, got: %v", err)
	}

	// 5. Stable Omni with conversational editing supported
	if err := CheckVideoEditSupport(ProviderGoogleGemini, "gemini-omni-1.1-flash", optsWithEdit); err != nil {
		t.Fatalf("expected Stable Omni with editing supported, got: %v", err)
	}

	// 6. Stable Omni without conversational editing rejected
	if err := CheckVideoEditSupport(ProviderGoogleGemini, "gemini-omni-1.1-flash", optsWithoutEdit); err == nil || !strings.Contains(err.Error(), "does not support conversational video editing") {
		t.Fatalf("expected failure for Omni without conversational editing, got: %v", err)
	}
}

func TestCheckVideoExtendSupport(t *testing.T) {
	optsWithExtend := &ParsedVideoOptions{VideoExtensionSupported: true}
	optsWithoutExtend := &ParsedVideoOptions{VideoExtensionSupported: false}

	// 1. Veo 3.1 Standard with extension supported
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "veo-3.1-generate-preview", optsWithExtend); err != nil {
		t.Fatalf("expected Veo 3.1 Standard extend supported, got: %v", err)
	}

	// 2. Veo 3.1 Fast with extension supported
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "veo-3.1-fast-generate-preview", optsWithExtend); err != nil {
		t.Fatalf("expected Veo 3.1 Fast extend supported, got: %v", err)
	}

	// 3. Veo Lite with extension rejected (Lite excluded)
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "veo-3.1-lite-generate-preview", optsWithExtend); err == nil || !strings.Contains(err.Error(), "only supported on Veo 3.1 standard or fast") {
		t.Fatalf("expected Veo Lite extend rejection, got: %v", err)
	}

	// 4. Veo 3.1 Standard without extension in metadata rejected
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "veo-3.1-generate-preview", optsWithoutExtend); err == nil || !strings.Contains(err.Error(), "does not support video extension in catalog metadata") {
		t.Fatalf("expected failure without catalog extension flag, got: %v", err)
	}

	// 5. OpenRouter Veo 3.1 rejected (Google Gemini provider required)
	if err := CheckVideoExtendSupport(ProviderOpenRouter, "google/veo-3.1", optsWithExtend); err == nil || !strings.Contains(err.Error(), "only supported directly via Google Gemini") {
		t.Fatalf("expected OpenRouter Veo rejection, got: %v", err)
	}

	// 6. Stable Omni with extension supported
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "gemini-omni-1.1-flash", optsWithExtend); err != nil {
		t.Fatalf("expected Stable Omni extend supported, got: %v", err)
	}

	// 7. Preview Omni rejected (stable Omni required)
	if err := CheckVideoExtendSupport(ProviderGoogleGemini, "gemini-omni-flash-preview", optsWithExtend); err == nil || !strings.Contains(err.Error(), "only supported on stable model") {
		t.Fatalf("expected Preview Omni extend rejection, got: %v", err)
	}
}

func TestSupportsVideoIteration(t *testing.T) {
	optsFull := &ParsedVideoOptions{ConversationalEditingSupported: true, VideoExtensionSupported: true}

	if !SupportsVideoIteration(ProviderGoogleGemini, "veo-3.1-generate-preview", optsFull) {
		t.Errorf("veo-3.1-generate-preview should support video iteration (via extension)")
	}
	if !SupportsVideoIteration(ProviderGoogleGemini, "veo-3.1-fast-generate-preview", optsFull) {
		t.Errorf("veo-3.1-fast-generate-preview should support video iteration (via extension)")
	}
	if SupportsVideoIteration(ProviderGoogleGemini, "veo-3.1-lite-generate-preview", optsFull) {
		t.Errorf("veo-3.1-lite-generate-preview must NOT support video iteration")
	}
	if !SupportsVideoIteration(ProviderGoogleGemini, "gemini-omni-1.1-flash", optsFull) {
		t.Errorf("gemini-omni-1.1-flash should support video iteration (via editing & extension)")
	}
	if SupportsVideoIteration(ProviderGoogleGemini, "gemini-omni-flash-preview", optsFull) {
		t.Errorf("gemini-omni-flash-preview must NOT support video iteration")
	}
	if SupportsVideoIteration(ProviderOpenRouter, "google/veo-3.1", optsFull) {
		t.Errorf("openrouter google/veo-3.1 must NOT support video iteration")
	}
}

func TestBuildVideoOperationConstraints(t *testing.T) {
	veoRec := pebblestore.ModelCatalogRecord{
		Provider: ProviderGoogleGemini,
		Model:    "veo-3.1-generate-preview",
		CatalogModalities: pebblestore.ModelCatalogModalities{
			Outputs: []string{"video"},
		},
		ProviderSpecific: []byte(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p","1080p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}},"features":{"video_extension":{"status":"verified","supported":true},"initial_image":{"status":"verified","supported":true,"max_inputs":1}}}}}`),
	}

	c := BuildVideoOperationConstraints(ProviderGoogleGemini, "veo-3.1-generate-preview", veoRec)
	if c == nil {
		t.Fatalf("expected non-nil constraints for veo-3.1")
	}

	// Create constraint
	if !c.Create.Supported || !c.Create.SupportsDuration || !c.Create.InitialImageSupported || c.Create.DefaultDuration != 8 {
		t.Errorf("veo create constraint mismatch: %#v", c.Create)
	}

	// Edit constraint
	if c.Edit.Supported {
		t.Errorf("veo edit must NOT be supported")
	}
	if c.Edit.Reason == "" {
		t.Errorf("veo edit reason must be populated")
	}

	// Extend constraint
	if !c.Extend.Supported {
		t.Fatalf("veo extend should be supported")
	}
	if c.Extend.LockedDurationSeconds != 8 {
		t.Errorf("locked duration = %d, want 8", c.Extend.LockedDurationSeconds)
	}
	if c.Extend.LockedResolution != "720p" {
		t.Errorf("locked resolution = %q, want 720p", c.Extend.LockedResolution)
	}
	if !c.Extend.LockedAspectRatioMatchesSource {
		t.Errorf("locked aspect ratio matches source should be true")
	}
	if !c.Extend.RequiresVeoSource || !c.Extend.DisallowsVeoLiteSource || c.Extend.MaxExtensionCount != 20 {
		t.Errorf("veo extend source constraints mismatch: %#v", c.Extend)
	}
	if c.Extend.RequiredSourceProvider != ProviderGoogleGemini {
		t.Errorf("veo extend required provider = %q, want %q", c.Extend.RequiredSourceProvider, ProviderGoogleGemini)
	}
	if c.Extend.RequiredSourceTransport != pebblestore.VideoTransportGooglePredictLongRunning {
		t.Errorf("veo extend required transport = %q, want %q", c.Extend.RequiredSourceTransport, pebblestore.VideoTransportGooglePredictLongRunning)
	}
	if !c.Extend.RequiresProviderResource || !c.Extend.RequiresOutputDigest || !c.Extend.RequiresKnownExtensionCount {
		t.Errorf("veo extend required flags mismatch: resource=%v, digest=%v, count=%v",
			c.Extend.RequiresProviderResource, c.Extend.RequiresOutputDigest, c.Extend.RequiresKnownExtensionCount)
	}
	if c.Extend.MaxReferenceAgeMs != 48*3600*1000 {
		t.Errorf("veo extend max reference age = %d, want 48h", c.Extend.MaxReferenceAgeMs)
	}
	if len(c.Extend.ObservedDimensionPairs) != 2 {
		t.Errorf("veo extend observed dimension pairs = %#v, want 2 pairs", c.Extend.ObservedDimensionPairs)
	}

	// Omni constraint checks: must NOT have global 20 count
	omniRec := pebblestore.ModelCatalogRecord{
		Provider: ProviderGoogleGemini,
		Model:    "gemini-omni-1.1-flash",
		CatalogModalities: pebblestore.ModelCatalogModalities{
			Outputs: []string{"video"},
		},
		ProviderSpecific: []byte(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}},"features":{"conversational_editing":{"status":"verified","supported":true},"video_extension":{"status":"verified","supported":true}}}}}`),
	}
	omniC := BuildVideoOperationConstraints(ProviderGoogleGemini, "gemini-omni-1.1-flash", omniRec)
	if omniC == nil {
		t.Fatalf("expected non-nil constraints for stable omni")
	}
	if omniC.Extend.MaxExtensionCount != 0 {
		t.Errorf("omni extend must NOT have global 20 extension count, got %d", omniC.Extend.MaxExtensionCount)
	}
	if !omniC.Extend.RequiresInteractionHandle {
		t.Errorf("omni extend must require interaction handle")
	}
	if omniC.Edit.MaxExternalDurationSec != 10.0 {
		t.Errorf("omni edit max external duration = %f, want 10.0", omniC.Edit.MaxExternalDurationSec)
	}
	if !omniC.Edit.RequiresHandleForLongVideo {
		t.Errorf("omni edit requires_handle_for_long_video must be true")
	}
}

func TestCheckSourceCompatibility(t *testing.T) {
	now := time.Now().UnixMilli()

	validVeoProv := &pebblestore.VideoProvenance{
		AccountScopeID:      "acc-1",
		Provider:            ProviderGoogleGemini,
		Model:               "veo-3.1-generate-preview",
		Transport:           pebblestore.VideoTransportGooglePredictLongRunning,
		ProviderResource:    "projects/123/locations/us-central1/publishers/google/models/veo-3.1:predictLongRunning/operations/op-1",
		OutputDigestSHA256:  strings.Repeat("a", 64),
		CreatedAt:           now - 1000,
		ExpiresAt:           now + 3600*1000,
		ExtensionCount:      0,
		ExtensionCountKnown: true,
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ObservedDurationMs:  8000,
	}

	veoLiteProv := &pebblestore.VideoProvenance{
		AccountScopeID:      "acc-1",
		Provider:            ProviderGoogleGemini,
		Model:               "veo-3.1-lite-generate-preview",
		Transport:           pebblestore.VideoTransportGooglePredictLongRunning,
		ProviderResource:    "projects/123/operations/op-2",
		OutputDigestSHA256:  strings.Repeat("b", 64),
		CreatedAt:           now - 1000,
		ExpiresAt:           now + 3600*1000,
		ExtensionCount:      0,
		ExtensionCountKnown: true,
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ObservedDurationMs:  8000,
	}

	validOmniProv := &pebblestore.VideoProvenance{
		AccountScopeID:      "acc-1",
		Provider:            ProviderGoogleGemini,
		Model:               "gemini-omni-1.1-flash",
		Transport:           pebblestore.VideoTransportGoogleInteractions,
		InteractionID:       "interaction-123",
		CreatedAt:           now - 1000,
		ExpiresAt:           now + 3600*1000,
		ExtensionCount:      0,
		ExtensionCountKnown: true,
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ObservedDurationMs:  10000,
	}

	// 1. Extend Veo 3.1 Standard with valid Veo provenance -> compatible
	res := CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", validVeoProv, 1280, 720, 8.0)
	if !res.Compatible {
		t.Fatalf("expected compatible, got reason: %s", res.Reason)
	}

	// 2. Extend Veo 3.1 Standard with Veo Lite provenance -> rejected
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", veoLiteProv, 1280, 720, 8.0)
	if res.Compatible || !strings.Contains(res.Reason, "cannot extend videos generated by Veo Lite") {
		t.Fatalf("expected Veo Lite source rejection, got: %#v", res)
	}

	// 3. Extend Veo 3.1 Standard with Omni provenance -> rejected
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", validOmniProv, 1280, 720, 10.0)
	if res.Compatible || !strings.Contains(res.Reason, "requires a source generated by Google Veo") {
		t.Fatalf("expected Omni source rejection for Veo extend, got: %#v", res)
	}

	// 4. Extend Omni with Veo provenance -> rejected
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "extend", validVeoProv, 1280, 720, 8.0)
	if res.Compatible || !strings.Contains(res.Reason, "requires source generated by stable Gemini Omni") {
		t.Fatalf("expected Veo source rejection for Omni extend, got: %#v", res)
	}

	// 5. Extend Veo with wrong dimensions (e.g. 1920x1080) -> rejected
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", validVeoProv, 1920, 1080, 8.0)
	if res.Compatible || !strings.Contains(res.Reason, "requires observed 720p dimensions") {
		t.Fatalf("expected dimension rejection for Veo extend, got: %#v", res)
	}

	// 6. Edit with Veo target model -> rejected
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "edit", validVeoProv, 1280, 720, 8.0)
	if res.Compatible || !strings.Contains(res.Reason, "Veo models do not support video editing") {
		t.Fatalf("expected edit rejection with Veo model, got: %#v", res)
	}

	// 7. Edit with Stable Omni and valid handle -> compatible
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "edit", validOmniProv, 1280, 720, 10.0)
	if !res.Compatible {
		t.Fatalf("expected edit compatible with stable Omni, got reason: %s", res.Reason)
	}

	// 8. Sanitized provenance (interaction_id stripped, HasInteraction=true) -> compatible for Omni extend
	sanitizedOmniProv := &pebblestore.VideoProvenance{
		AccountScopeID:      "acc-1",
		Provider:            ProviderGoogleGemini,
		Model:               "gemini-omni-1.1-flash",
		Transport:           pebblestore.VideoTransportGoogleInteractions,
		HasInteraction:      true,
		CreatedAt:           now - 1000,
		ExpiresAt:           now + 3600*1000,
		ExtensionCountKnown: true,
	}
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "extend", sanitizedOmniProv, 1280, 720, 10.0)
	if !res.Compatible {
		t.Fatalf("expected sanitized Omni provenance (HasInteraction=true) to be compatible for extend, got reason: %s", res.Reason)
	}

	// 9. Sanitized provenance without handle -> rejected for Omni extend
	missingHandleOmniProv := sanitizedOmniProv.Clone()
	missingHandleOmniProv.HasInteraction = false
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "extend", missingHandleOmniProv, 1280, 720, 10.0)
	if res.Compatible || !strings.Contains(res.Reason, "missing interaction handle") {
		t.Fatalf("expected rejection for missing interaction handle, got: %#v", res)
	}

	// 10. Sanitized Veo provenance (ProviderResource stripped, HasProviderResource=true) -> compatible for Veo extend
	sanitizedVeoProv := &pebblestore.VideoProvenance{
		AccountScopeID:      "acc-1",
		Provider:            ProviderGoogleGemini,
		Model:               "veo-3.1-generate-preview",
		Transport:           pebblestore.VideoTransportGooglePredictLongRunning,
		HasProviderResource: true,
		OutputDigestSHA256:  strings.Repeat("a", 64),
		CreatedAt:           now - 1000,
		ExpiresAt:           now + 3600*1000,
		ExtensionCountKnown: true,
		ObservedWidth:       1280,
		ObservedHeight:      720,
	}
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", sanitizedVeoProv, 1280, 720, 8.0)
	if !res.Compatible {
		t.Fatalf("expected sanitized Veo provenance (HasProviderResource=true) to be compatible for extend, got reason: %s", res.Reason)
	}

	// 11. Expired provenance -> rejected
	expiredProv := validVeoProv.Clone()
	expiredProv.ExpiresAt = now - 1000
	res = CheckSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", expiredProv, 1280, 720, 8.0)
	if res.Compatible || !strings.Contains(res.Reason, "expired") {
		t.Fatalf("expected rejection for expired provenance, got: %#v", res)
	}

	// 12. Non-finite duration (NaN or negative) -> rejected
	if err := ValidateSourceCompatibility(ProviderGoogleGemini, "veo-3.1-generate-preview", "extend", validVeoProv, 1280, 720, -5.0); err == nil {
		t.Fatal("expected rejection for negative duration")
	}

	// 13. External edit without handle up to 10s allowed, >10s rejected
	externalEditProv := &pebblestore.VideoProvenance{
		AccountScopeID: "acc-1",
		Provider:       ProviderGoogleGemini,
		Model:          "gemini-omni-1.1-flash",
		Transport:      pebblestore.VideoTransportGoogleInteractions,
	}
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "edit", externalEditProv, 1280, 720, 10.0)
	if !res.Compatible {
		t.Fatalf("expected external edit up to 10s to be compatible, got: %s", res.Reason)
	}
	res = CheckSourceCompatibility(ProviderGoogleGemini, "gemini-omni-1.1-flash", "edit", externalEditProv, 1280, 720, 10.5)
	if res.Compatible || !strings.Contains(res.Reason, "exceeds maximum allowed for external video editing (10s)") {
		t.Fatalf("expected external edit >10s to be rejected, got: %#v", res)
	}
}

func init() {
	_ = json.Marshal
}
