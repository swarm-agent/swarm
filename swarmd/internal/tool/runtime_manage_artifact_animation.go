package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	animationManifestID   = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-animation-manifest["'](?:\s|$)`)
	animationManifestType = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
)

type animationManifest struct {
	Version    string `json:"version"`
	DurationMS int    `json:"duration_ms"`
	FPS        int    `json:"fps"`
}

func (r *Runtime) exportHTMLAnimation(ctx context.Context, principal artifact.Principal, callID string, args map[string]any) (pebblestore.SessionArtifactVariant, pebblestore.SessionArtifactSelectionReference, *pebblestore.SessionArtifactOutputRequirements, error) {
	for key := range args {
		switch key {
		case "action", "session_id", "collection_id", "variant_id", "event_seq":
		default:
			return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactSelectionReference{}, nil, animationError("animation_source_reference_invalid", fmt.Sprintf("export_html_animation contains unsupported field %q", key))
		}
	}
	if r.htmlAnimationCapture == nil {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactSelectionReference{}, nil, animationError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	if err := validateArtifactRetrievalIdentity(args, "export_html_animation", true); err != nil {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactSelectionReference{}, nil, animationError("animation_source_reference_invalid", "complete exact ready HTML reference is required")
	}
	ref, explicit, err := parseArtifactReadReference(args, strings.TrimSpace(asString(args["variant_id"])))
	if err != nil || !explicit {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactSelectionReference{}, nil, animationError("animation_source_reference_invalid", "complete exact ready HTML reference is required")
	}
	variant, err := r.artifactAuthority.GetReference(principal, ref)
	if err != nil || variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, ref, nil, animationError("animation_source_reference_invalid", "exact ready HTML reference could not be authenticated")
	}
	if variant.AnimationProfile == nil || (variant.AnimationProfile.ProfileID != "motion_ui" && variant.AnimationProfile.ProfileID != "spatial_3d" && variant.AnimationProfile.ProfileID != "vector_playback") || variant.AnimationProfile.Budgets.NetworkAllowed {
		return pebblestore.SessionArtifactVariant{}, ref, nil, animationError("animation_profile_unsupported", "ready source requires a reviewed non-network animation profile")
	}
	files, entry, err := r.readCaptureSource(ctx, principal, ref, variant)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, nil, normalizeAnimationSourceError(err)
	}
	manifest, err := parseAnimationManifest(files[entry])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, nil, err
	}
	requirements, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, nil, animationError("animation_renderer_failed", "canonical landscape video requirements are unavailable")
	}
	temporalParts := animationTemporalParts(files[entry], int64(manifest.DurationMS))
	result, err := r.htmlAnimationCapture.RenderAnimation(ctx, htmlcapture.AnimationRequest{Entry: entry, Files: files, DurationMS: manifest.DurationMS, FPS: manifest.FPS})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, normalizeAnimationRendererError(err)
	}
	if result.DurationMS != manifest.DurationMS || result.FPS != manifest.FPS || result.FrameCount != (manifest.DurationMS*manifest.FPS+999)/1000 {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, animationError("animation_renderer_failed", "renderer returned inconsistent deterministic timeline metadata")
	}
	if err := validateAnimationMP4(result.MP4); err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, err
	}
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "final_render"})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, animationError("animation_publish_failed", "canonical MP4 playback profile is unavailable")
	}
	collectionID := captureOpaqueID("collection-animation", principal.SessionID, callID, ref, fmt.Sprintf("%d/%d", manifest.DurationMS, manifest.FPS))
	variantID := captureOpaqueID("variant-animation", principal.SessionID, callID, ref, "mp4")
	input := artifact.CreateInput{
		RequestID:    captureOpaqueID("request-export-html-animation", principal.SessionID, callID, ref, "mp4"),
		CollectionID: collectionID, CollectionName: "HTML animation render", VariantID: variantID,
		Filename: "html-animation.mp4", MediaType: "video/mp4",
		Presentation:       pebblestore.SessionArtifactPresentation{Kind: "video", Label: "HTML animation", Previewable: true, Width: htmlcapture.Width, Height: htmlcapture.Height},
		OutputRequirements: requirements, AnimationProfile: profile, Parts: temporalParts,
		SourceSessionID: ref.SessionID, SourceCollectionID: ref.CollectionID, SourceVariantID: ref.VariantID, SourceEventSeq: ref.EventSeq,
		Body: append([]byte(nil), result.MP4...), AutoAccept: true,
	}
	published, err := r.artifactAuthority.Create(ctx, principal, input)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, animationError("animation_publish_failed", "captured MP4 could not be durably published")
	}
	digest := sha256.Sum256(result.MP4)
	if published.Status != pebblestore.SessionArtifactStatusReady || published.MediaType != "video/mp4" || published.EventSeq == 0 || published.DigestSHA256 != hex.EncodeToString(digest[:]) {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, animationError("animation_idempotency_conflict", "published animation metadata conflicts with the trusted capture bytes")
	}
	readyRef := pebblestore.SessionArtifactSelectionReference{SessionID: published.SessionID, CollectionID: published.CollectionID, VariantID: published.ID, EventSeq: published.EventSeq}
	readyBytes, ready, readErr := r.artifactAuthority.ReadReference(ctx, principal, readyRef, htmlcapture.MaxMP4Bytes)
	if readErr != nil || ready.Status != pebblestore.SessionArtifactStatusReady || !bytes.Equal(readyBytes, result.MP4) {
		return pebblestore.SessionArtifactVariant{}, ref, requirements, animationError("animation_publish_failed", "published animation could not be reread as the exact ready MP4")
	}
	return ready, ref, requirements, nil
}

func parseAnimationManifest(html []byte) (animationManifest, error) {
	scripts := captureScriptPattern.FindAllSubmatch(html, -1)
	manifests := make([][]byte, 0, 1)
	for _, script := range scripts {
		if animationManifestID.Match(script[1]) {
			if !animationManifestType.Match(script[1]) {
				return animationManifest{}, animationError("animation_manifest_invalid", "animation manifest has an invalid media type")
			}
			manifests = append(manifests, script[2])
		}
	}
	if len(manifests) == 0 {
		return animationManifest{}, animationError("animation_manifest_missing", "canonical swarm.animation/v1 manifest is missing")
	}
	if len(manifests) != 1 || len(manifests[0]) > captureMaxManifestBytes {
		return animationManifest{}, animationError("animation_manifest_invalid", "animation manifest is duplicated or exceeds fixed bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(manifests[0]))
	decoder.DisallowUnknownFields()
	var manifest animationManifest
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil {
		return animationManifest{}, animationError("animation_manifest_invalid", "animation manifest is malformed")
	}
	if manifest.Version != htmlcapture.AnimationVersion || manifest.DurationMS < 100 || manifest.DurationMS > htmlcapture.MaxAnimationDurationMS || manifest.FPS < 1 || manifest.FPS > htmlcapture.MaxAnimationFPS || (manifest.DurationMS*manifest.FPS+999)/1000 > htmlcapture.MaxAnimationFrames {
		return animationManifest{}, animationError("animation_manifest_invalid", "animation manifest version, duration, FPS, or frame count is outside fixed bounds")
	}
	return manifest, nil
}

func animationTemporalParts(html []byte, durationMS int64) []pebblestore.SessionArtifactPart {
	compatible := false
	for _, script := range artifactHTMLIterationManifest.FindAllSubmatch(html, -1) {
		if !artifactHTMLIterationID.Match(script[1]) || !artifactHTMLManifestType.Match(script[1]) {
			continue
		}
		var manifest artifactHTMLIterationManifestValue
		decoder := json.NewDecoder(bytes.NewReader(script[2]))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&manifest) == nil && ensureJSONEOF(decoder) == nil && manifest.Version == "swarm.iteration/v1" && manifest.DurationMS == durationMS {
			compatible = true
			break
		}
	}
	if !compatible {
		return nil
	}
	derived := deriveArtifactHTMLParts(html, "text/html")
	parts := make([]pebblestore.SessionArtifactPart, 0, len(derived))
	for _, part := range derived {
		if part.Kind == "temporal" && part.StartMs >= 0 && part.EndMs > part.StartMs && part.EndMs <= durationMS {
			parts = append(parts, part)
		}
	}
	return parts
}

func validateAnimationMP4(data []byte) error {
	if len(data) < 12 || len(data) > htmlcapture.MaxMP4Bytes || string(data[4:8]) != "ftyp" {
		return animationError("animation_mp4_invalid", "renderer returned an invalid bounded MP4")
	}
	return nil
}

func normalizeAnimationRendererError(err error) error {
	var animationErr *htmlcapture.Error
	if errors.As(err, &animationErr) {
		return animationError(animationErr.Code, animationErr.SafeMessage)
	}
	return animationError("animation_renderer_failed", "trusted HTML animation capture failed")
}

func normalizeAnimationSourceError(err error) error {
	message := err.Error()
	if start := strings.Index(message, "(code="); start >= 0 {
		if end := strings.Index(message[start:], "): "); end >= 0 {
			code := message[start+6 : start+end]
			return animationError(strings.Replace(code, "capture_", "animation_", 1), message[start+end+3:])
		}
	}
	return animationError("animation_source_invalid", "ready animation source could not be read")
}

func animationError(code, message string) error {
	return fmt.Errorf("manage_artifact export_html_animation failed (code=%s): %s", code, message)
}
