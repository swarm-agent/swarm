package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"regexp"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	animationManifestID   = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-animation-manifest["'](?:\s|$)`)
	animationManifestType = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
)

const (
	animationSynchronousFrameLimit = 300
	animationProgressHeartbeat     = 5 * time.Second
)

type animationManifest struct {
	Version    string `json:"version"`
	DurationMS int    `json:"duration_ms"`
	FPS        int    `json:"fps"`
}

type animationExportPrepared struct {
	SourceRef    pebblestore.SessionArtifactSelectionReference
	Files        map[string][]byte
	Entry        string
	Manifest     animationManifest
	Requirements *pebblestore.SessionArtifactOutputRequirements
	Input        artifact.CreateInput
}

// exportHTMLAnimation validates and durably reserves long exports before
// returning. Small exports retain the synchronous behavior for fast feedback.
func (r *Runtime) exportHTMLAnimation(ctx context.Context, principal artifact.Principal, callID string, args map[string]any) (pebblestore.SessionArtifactVariant, pebblestore.SessionArtifactSelectionReference, *pebblestore.SessionArtifactOutputRequirements, error) {
	prepared, err := r.prepareHTMLAnimationExport(ctx, principal, callID, "export_html_animation", args)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, err
	}
	frameCount := (prepared.Manifest.DurationMS*prepared.Manifest.FPS + 999) / 1000
	if frameCount <= animationSynchronousFrameLimit {
		ready, err := r.renderAndPublishHTMLAnimation(ctx, principal, prepared, nil)
		return ready, prepared.SourceRef, prepared.Requirements, err
	}
	renderRequest := htmlcapture.AnimationRequest{Entry: prepared.Entry, Files: prepared.Files, DurationMS: prepared.Manifest.DurationMS, FPS: prepared.Manifest.FPS}
	if _, err := r.htmlAnimationCapture.PreflightAnimation(ctx, renderRequest); err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, normalizeAnimationRendererError(err)
	}

	staging, err := r.artifactAuthority.Reserve(principal, prepared.Input)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, animationError("animation_publish_failed", "animation export could not be durably queued")
	}
	if staging.Status != pebblestore.SessionArtifactStatusStaging {
		return staging, prepared.SourceRef, prepared.Requirements, nil
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	r.animationJobsMu.Lock()
	if previous := r.animationJobs[staging.ID]; previous != nil {
		previous()
	}
	r.animationJobs[staging.ID] = cancel
	r.animationJobsMu.Unlock()
	staging, err = r.artifactAuthority.UpdateProgress(principal, prepared.Input.RequestID+":progress:queued", staging.CollectionID, staging.ID, pebblestore.SessionArtifactProgress{Stage: "queued", Total: frameCount, HeartbeatAt: time.Now().UnixMilli()})
	if err != nil {
		cancel()
		_, _ = r.artifactAuthority.MarkFailed(principal, prepared.Input.RequestID+":terminal:animation_progress_failed", prepared.Input.CollectionID, prepared.Input.VariantID, "animation_progress_failed")
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, animationError("animation_progress_failed", "animation export progress could not be durably initialized")
	}
	go r.runHTMLAnimationExport(jobCtx, principal, prepared, staging.ID)
	return staging, prepared.SourceRef, prepared.Requirements, nil
}

func (r *Runtime) exportHTMLAnimationFallback(ctx context.Context, principal artifact.Principal, callID string, args map[string]any) (pebblestore.SessionArtifactVariant, pebblestore.SessionArtifactSelectionReference, *pebblestore.SessionArtifactOutputRequirements, error) {
	prepared, err := r.prepareHTMLAnimationExport(ctx, principal, callID, "export_html_animation_fallback", args)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, err
	}
	preflight, err := r.htmlAnimationCapture.PreflightAnimation(ctx, htmlcapture.AnimationRequest{Entry: prepared.Entry, Files: prepared.Files, DurationMS: prepared.Manifest.DurationMS, FPS: prepared.Manifest.FPS})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, normalizeAnimationRendererError(err)
	}
	if err := validateCapturePNG(preflight.PreviewPNG); err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, animationError("animation_png_invalid", "animation preflight did not produce a valid render-ready fallback frame")
	}
	input := artifact.CreateInput{
		RequestID:      captureOpaqueID("request-export-html-animation-fallback", principal.SessionID, callID, prepared.SourceRef, "png"),
		CollectionID:   captureOpaqueID("collection-animation-fallback", principal.SessionID, callID, prepared.SourceRef, "png"),
		CollectionName: "HTML animation fallback", VariantID: captureOpaqueID("variant-animation-fallback", principal.SessionID, callID, prepared.SourceRef, "png"),
		Filename: "html-animation-fallback.png", MediaType: "image/png", Role: pebblestore.SessionArtifactRoleRenderOnly,
		Presentation:       pebblestore.SessionArtifactPresentation{Kind: "image", Label: "HTML animation fallback", Previewable: true, Width: htmlcapture.Width, Height: htmlcapture.Height},
		OutputRequirements: prepared.Requirements,
		SourceSessionID:    prepared.SourceRef.SessionID, SourceCollectionID: prepared.SourceRef.CollectionID, SourceVariantID: prepared.SourceRef.VariantID, SourceEventSeq: prepared.SourceRef.EventSeq,
		Body: append([]byte(nil), preflight.PreviewPNG...), AutoAccept: true,
	}
	published, err := r.artifactAuthority.Create(ctx, principal, input)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, animationError("animation_publish_failed", "animation fallback could not be durably published")
	}
	if published.Status != pebblestore.SessionArtifactStatusReady || published.MediaType != "image/png" || published.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, prepared.SourceRef, prepared.Requirements, animationError("animation_publish_failed", "animation fallback did not publish as a ready PNG")
	}
	return published, prepared.SourceRef, prepared.Requirements, nil
}

func (r *Runtime) prepareHTMLAnimationExport(ctx context.Context, principal artifact.Principal, callID, actionName string, args map[string]any) (animationExportPrepared, error) {
	prepared := animationExportPrepared{}
	if err := validateArtifactRetrievalIdentity(args, actionName, true); err != nil {
		return prepared, animationError("animation_source_reference_invalid", fmt.Sprintf("%s requires a complete exact ready HTML reference", actionName))
	}
	for key := range args {
		switch key {
		case "action", "session_id", "collection_id", "variant_id", "event_seq":
		default:
			return prepared, animationError("animation_source_reference_invalid", fmt.Sprintf("HTML animation export contains unsupported field %q", key))
		}
	}
	if r.htmlAnimationCapture == nil {
		return prepared, animationError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	ref, explicit, err := parseArtifactReadReference(args, strings.TrimSpace(asString(args["variant_id"])))
	prepared.SourceRef = ref
	if err != nil || !explicit {
		return prepared, animationError("animation_source_reference_invalid", "complete exact ready HTML reference is required")
	}
	variant, err := r.artifactAuthority.GetReference(principal, ref)
	if err != nil || variant.Status != pebblestore.SessionArtifactStatusReady {
		return prepared, animationError("animation_source_reference_invalid", "exact ready HTML reference could not be authenticated")
	}
	if variant.AnimationProfile == nil || (variant.AnimationProfile.ProfileID != "motion_ui" && variant.AnimationProfile.ProfileID != "spatial_3d" && variant.AnimationProfile.ProfileID != "vector_playback") || variant.AnimationProfile.Budgets.NetworkAllowed {
		return prepared, animationError("animation_profile_unsupported", "ready source requires a reviewed non-network animation profile")
	}
	files, entry, err := r.readCaptureSource(ctx, principal, ref, variant)
	if err != nil {
		return prepared, normalizeAnimationSourceError(err)
	}
	manifest, err := parseAnimationManifest(files[entry])
	if err != nil {
		return prepared, err
	}
	requirements, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	prepared.Requirements = requirements
	if err != nil {
		return prepared, animationError("animation_renderer_failed", "canonical landscape video requirements are unavailable")
	}
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "final_render"})
	if err != nil {
		return prepared, animationError("animation_publish_failed", "canonical MP4 playback profile is unavailable")
	}
	collectionID := captureOpaqueID("collection-animation", principal.SessionID, callID, ref, fmt.Sprintf("%d/%d", manifest.DurationMS, manifest.FPS))
	variantID := captureOpaqueID("variant-animation", principal.SessionID, callID, ref, "mp4")
	prepared.Files, prepared.Entry, prepared.Manifest = files, entry, manifest
	prepared.Input = artifact.CreateInput{
		RequestID:    captureOpaqueID("request-export-html-animation", principal.SessionID, callID, ref, "mp4"),
		CollectionID: collectionID, CollectionName: "HTML animation render", VariantID: variantID,
		Filename: "html-animation.mp4", MediaType: "video/mp4",
		Presentation:       pebblestore.SessionArtifactPresentation{Kind: "video", Label: "HTML animation", Previewable: true, Width: htmlcapture.Width, Height: htmlcapture.Height},
		OutputRequirements: requirements, AnimationProfile: profile, Parts: animationTemporalParts(files[entry], int64(manifest.DurationMS)),
		SourceSessionID: ref.SessionID, SourceCollectionID: ref.CollectionID, SourceVariantID: ref.VariantID, SourceEventSeq: ref.EventSeq,
		AutoAccept: true,
	}
	return prepared, nil
}

func (r *Runtime) renderAndPublishHTMLAnimation(ctx context.Context, principal artifact.Principal, prepared animationExportPrepared, progress func(htmlcapture.AnimationProgress)) (pebblestore.SessionArtifactVariant, error) {
	manifest := prepared.Manifest
	result, err := r.htmlAnimationCapture.RenderAnimation(ctx, htmlcapture.AnimationRequest{Entry: prepared.Entry, Files: prepared.Files, DurationMS: manifest.DurationMS, FPS: manifest.FPS, Progress: progress})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, normalizeAnimationRendererError(err)
	}
	if result.DurationMS != manifest.DurationMS || result.FPS != manifest.FPS || result.FrameCount != (manifest.DurationMS*manifest.FPS+999)/1000 {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_renderer_failed", "renderer returned inconsistent deterministic timeline metadata")
	}
	if err := validateAnimationMP4(result.MP4); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input := prepared.Input
	input.Body = append([]byte(nil), result.MP4...)
	published, err := r.artifactAuthority.Create(ctx, principal, input)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_publish_failed", "captured MP4 could not be durably published")
	}
	digest := sha256.Sum256(result.MP4)
	if published.Status != pebblestore.SessionArtifactStatusReady || published.MediaType != "video/mp4" || published.EventSeq == 0 || published.DigestSHA256 != hex.EncodeToString(digest[:]) {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_idempotency_conflict", "published animation metadata conflicts with the trusted capture bytes")
	}
	readyRef := pebblestore.SessionArtifactSelectionReference{SessionID: published.SessionID, CollectionID: published.CollectionID, VariantID: published.ID, EventSeq: published.EventSeq}
	readyBytes, ready, readErr := r.artifactAuthority.ReadReference(ctx, principal, readyRef, htmlcapture.MaxMP4Bytes)
	if readErr != nil || ready.Status != pebblestore.SessionArtifactStatusReady || !bytes.Equal(readyBytes, result.MP4) {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_publish_failed", "published animation could not be reread as the exact ready MP4")
	}
	return ready, nil
}

func (r *Runtime) runHTMLAnimationExport(ctx context.Context, principal artifact.Principal, prepared animationExportPrepared, variantID string) {
	defer func() {
		r.animationJobsMu.Lock()
		delete(r.animationJobs, variantID)
		r.animationJobsMu.Unlock()
	}()
	progress := r.animationProgressReporter(principal, prepared)
	if _, err := r.renderAndPublishHTMLAnimation(ctx, principal, prepared, progress); err != nil {
		code := animationFailureCode(err)
		if errors.Is(ctx.Err(), context.Canceled) {
			code = "animation_cancelled"
		}
		_, _ = r.artifactAuthority.MarkFailed(principal, prepared.Input.RequestID+":terminal:"+code, prepared.Input.CollectionID, prepared.Input.VariantID, code)
	}
}

func (r *Runtime) animationProgressReporter(principal artifact.Principal, prepared animationExportPrepared) func(htmlcapture.AnimationProgress) {
	var mu sync.Mutex
	lastPersisted, lastStage, lastPercent := time.Time{}, "", -1.0
	return func(update htmlcapture.AnimationProgress) {
		mu.Lock()
		defer mu.Unlock()
		percent := max(lastPercent, animationOverallProgress(update))
		now := time.Now()
		if update.Stage == lastStage && percent < lastPercent+1 && now.Sub(lastPersisted) < animationProgressHeartbeat {
			return
		}
		remaining := int64(0)
		if percent > 0 && percent < 100 {
			remaining = int64(float64(update.Elapsed.Milliseconds()) * (100 - percent) / percent)
		}
		progress := pebblestore.SessionArtifactProgress{Stage: update.Stage, Completed: update.Completed, Total: update.Total, Percent: percent, ElapsedMS: update.Elapsed.Milliseconds(), EstimatedRemainingMS: remaining, HeartbeatAt: now.UnixMilli()}
		requestID := fmt.Sprintf("%s:progress:%s:%d:%d", prepared.Input.RequestID, update.Stage, update.Completed, now.Unix()/5)
		if _, err := r.artifactAuthority.UpdateProgress(principal, requestID, prepared.Input.CollectionID, prepared.Input.VariantID, progress); err == nil {
			lastPersisted, lastStage, lastPercent = now, update.Stage, max(lastPercent, percent)
		}
	}
}

func animationOverallProgress(update htmlcapture.AnimationProgress) float64 {
	ratio := 0.0
	if update.Total > 0 {
		ratio = float64(update.Completed) / float64(update.Total)
	}
	switch update.Stage {
	case "queue_wait":
		return ratio
	case "readiness_preflight":
		return 1 + ratio
	case "deterministic_preflight":
		return 2 + 3*ratio
	case "frame_capture", "segment_encode":
		return 5 + 90*ratio
	case "segment_concatenation":
		return 95 + 4*ratio
	default:
		return 0
	}
}

func (r *Runtime) cancelHTMLAnimationExport(principal artifact.Principal, callID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		switch key {
		case "action", "session_id", "collection_id", "variant_id", "event_seq":
		default:
			return pebblestore.SessionArtifactVariant{}, animationError("animation_source_reference_invalid", fmt.Sprintf("cancel_html_animation_export contains unsupported field %q", key))
		}
	}
	collectionID, variantID := strings.TrimSpace(asString(args["collection_id"])), strings.TrimSpace(asString(args["variant_id"]))
	if strings.TrimSpace(asString(args["session_id"])) != principal.SessionID || collectionID == "" || variantID == "" || asUint64(args["event_seq"]) == 0 {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_source_reference_invalid", "complete exact staging animation reference is required")
	}
	variants, err := r.artifactAuthority.ListVariants(principal, collectionID, manageArtifactMaxListLimit)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	var current pebblestore.SessionArtifactVariant
	for _, candidate := range variants {
		if candidate.ID == variantID && candidate.EventSeq == asUint64(args["event_seq"]) {
			current = candidate
			break
		}
	}
	if current.Status != pebblestore.SessionArtifactStatusStaging {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_cancel_conflict", "animation export is not an exact cancellable staging job")
	}
	r.animationJobsMu.Lock()
	cancel := r.animationJobs[variantID]
	r.animationJobsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	failed, err := r.artifactAuthority.MarkFailed(principal, managedArtifactRequestID(principal.SessionID, callID, "cancel_html_animation_export"), collectionID, variantID, "animation_cancelled")
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, animationError("animation_cancel_failed", "animation export cancellation could not be persisted")
	}
	return failed, nil
}

// reserveAndPreflightManagedAnimation prevents a profiled HTML animation from
// becoming ready until the trusted renderer has verified its bootstrap binding,
// exact timeline acknowledgements, stable representative pixels, and viewport
// containment. The durable reservation is terminally failed with the renderer's
// bounded code so a retry must use a fresh tool-call identity rather than replay
// the failed reference.
func (r *Runtime) reserveAndPreflightManagedAnimation(ctx context.Context, principal artifact.Principal, input artifact.CreateInput, entries []artifact.PackageEntry, packageArtifact bool) (bool, error) {
	files, entry, manifest, gated, err := managedAnimationPublicationSource(input, entries, packageArtifact)
	if !gated {
		return false, nil
	}
	if strings.TrimSpace(input.RequestID) == "" || strings.TrimSpace(input.CollectionID) == "" || strings.TrimSpace(input.VariantID) == "" {
		return true, animationError("animation_publish_failed", "profiled HTML animation requires trusted request, collection, and variant identities before preflight")
	}
	if r.artifactAuthority == nil {
		return true, animationError("animation_publish_failed", "managed artifact authority is unavailable for profiled HTML animation preflight")
	}
	input.Body = nil
	reserved, reserveErr := r.artifactAuthority.Reserve(principal, input)
	if reserveErr != nil {
		return true, animationError("animation_publish_failed", "profiled HTML animation could not be durably reserved for trusted preflight")
	}
	if reserved.Status != pebblestore.SessionArtifactStatusStaging || !reserved.ProjectionReservation {
		return true, animationError("animation_publication_conflict", "profiled HTML animation publication already reached a terminal state; retry with a fresh tool call")
	}
	if err == nil && r.htmlAnimationCapture == nil {
		err = htmlcapture.NewError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	if err == nil {
		result, preflightErr := r.htmlAnimationCapture.PreflightAnimation(ctx, htmlcapture.AnimationRequest{Entry: entry, Files: files, DurationMS: manifest.DurationMS, FPS: manifest.FPS})
		if preflightErr != nil {
			err = normalizeAnimationRendererError(preflightErr)
		} else if result.DurationMS != manifest.DurationMS || result.FPS != manifest.FPS || result.FrameCount != (manifest.DurationMS*manifest.FPS+999)/1000 {
			err = animationError("animation_renderer_failed", "trusted animation preflight returned inconsistent deterministic timeline metadata")
		} else if previewErr := validateAnimationPreviewPNG(result.PreviewPNG); previewErr != nil {
			err = previewErr
		}
	}
	if err != nil {
		code := animationFailureCode(err)
		if _, markErr := r.artifactAuthority.MarkFailed(principal, input.RequestID+":terminal:"+code, input.CollectionID, input.VariantID, code); markErr != nil {
			return true, animationError("animation_publish_failed", "profiled HTML animation failed trusted preflight and its terminal failure could not be persisted")
		}
		return true, err
	}
	return true, nil
}

func managedHTMLAnimationProfile(profile *pebblestore.SessionArtifactAnimationProfile) bool {
	if profile == nil {
		return false
	}
	switch profile.ProfileID {
	case "motion_ui", "spatial_3d", "vector_playback":
		return true
	default:
		return false
	}
}

func managedAnimationPublicationSource(input artifact.CreateInput, entries []artifact.PackageEntry, packageArtifact bool) (map[string][]byte, string, animationManifest, bool, error) {
	if !managedHTMLAnimationProfile(input.AnimationProfile) {
		return nil, "", animationManifest{}, false, nil
	}
	files := make(map[string][]byte)
	entry := "index.html"
	if packageArtifact {
		for _, candidate := range entries {
			name := pathClean(candidate.Name)
			if name == "" || name != candidate.Name {
				return nil, entry, animationManifest{}, true, animationError("animation_source_invalid", "profiled HTML animation package contains a non-canonical entry")
			}
			files[name] = append([]byte(nil), candidate.Data...)
		}
		if _, ok := files[entry]; !ok {
			return files, entry, animationManifest{}, true, animationError("animation_manifest_missing", "profiled HTML animation package requires index.html with the canonical animation manifest")
		}
	} else {
		if canonicalArtifactMediaType(input.MediaType) != "text/html" {
			return nil, "", animationManifest{}, false, nil
		}
		files[entry] = append([]byte(nil), input.Body...)
	}
	manifest, err := parseAnimationManifest(files[entry])
	return files, entry, manifest, true, err
}

func validateAnimationPreviewPNG(data []byte) error {
	if len(data) == 0 || len(data) > manageArtifactMaxImageReadBytes {
		return animationError("animation_png_invalid", "trusted animation preflight did not produce a bounded preview frame")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != htmlcapture.Width || config.Height != htmlcapture.Height {
		return animationError("animation_png_invalid", "trusted animation preflight did not produce the canonical 1920x1080 preview frame")
	}
	return nil
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
		code := animationErr.Code
		if !strings.HasPrefix(code, "animation_") {
			code = "animation_renderer_failed"
		}
		return animationError(code, animationRendererSafeMessage(code))
	}
	return animationError("animation_renderer_failed", "trusted HTML animation capture failed")
}

func animationRendererSafeMessage(code string) string {
	switch code {
	case "animation_renderer_unavailable", "animation_encoder_unavailable":
		return "trusted HTML animation renderer is unavailable"
	case "animation_renderer_failed":
		return "trusted HTML animation capture failed"
	case "animation_source_limit_exceeded":
		return "animation request exceeds fixed renderer bounds"
	case "animation_bootstrap_missing":
		return "trusted animation bootstrap is missing or invalid"
	case "animation_runtime_missing_before_dom_content_loaded":
		return "animation runtime did not bind before DOMContentLoaded"
	case "animation_bind_timeout":
		return "animation runtime binding exceeded the fixed deadline"
	case "animation_not_ready":
		return "animation runtime did not become ready"
	case "animation_manifest_mismatch":
		return "animation runtime acknowledgement does not match the manifest"
	case "animation_seek_failed":
		return "animation runtime did not acknowledge the exact renderer-controlled timestamp"
	case "animation_frame_unstable":
		return "animation pixels changed after the renderer selected a deterministic timestamp"
	case "animation_viewport_overflow":
		return "animation content escapes the canonical viewport"
	case "animation_network_blocked":
		return "animation document attempted a prohibited network request"
	case "animation_blocked":
		return "animation document contains visible blocking UI"
	case "animation_timeout":
		return "animation runtime exceeded a fixed renderer deadline"
	case "animation_png_invalid":
		return "animation renderer returned an invalid PNG frame"
	default:
		return "trusted HTML animation capture failed"
	}
}

func animationFailureCode(err error) string {
	if err == nil {
		return "animation_renderer_failed"
	}
	message := err.Error()
	const marker = "(code="
	start := strings.Index(message, marker)
	if start < 0 {
		return "animation_renderer_failed"
	}
	start += len(marker)
	end := strings.Index(message[start:], ")")
	if end <= 0 {
		return "animation_renderer_failed"
	}
	code := strings.TrimSpace(message[start : start+end])
	if !strings.HasPrefix(code, "animation_") {
		return "animation_renderer_failed"
	}
	return code
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
	return fmt.Errorf("manage_artifact HTML animation failed (code=%s): %s", code, message)
}
