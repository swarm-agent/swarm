package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type directArtifactV3Publication struct {
	Result        map[string]any
	ProjectDigest string
}

type directArtifactV3RevisionInput struct {
	SessionID, ArtifactID, RevisionRef string
}

// createDirectArtifactV3HTML is the ordinary primary-Swarm creation boundary.
// It converts one complete authored HTML document into a conventional V3 project,
// then uses the same context-bound build, browser-preview, Git, and projection
// path as managed whole-project authoring. It never writes a V1/V2 artifact.
func (r *Runtime) createDirectArtifactV3HTML(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("manage_artifact create requires the Artifact V3 author service")
	}
	if strings.TrimSpace(scope.SessionID) == "" || scope.SessionID != principal.SessionID || scope.Principal.AccountScopeID != principal.AccountScopeID || scope.Principal.UserID != principal.UserID {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	for key := range args {
		switch key {
		case "action", "collection_name", "collection_description", "filename", "media_type", "content", "presentation", "parts", "narration_plan", "animation_profile", "scene_contract", "native_parts":
		default:
			return nil, fmt.Errorf("manage_artifact create for Artifact V3 HTML contains unsupported field %q", key)
		}
	}
	profile, err := artifact.ParseAnimationProfile(args["animation_profile"])
	if err != nil {
		return nil, err
	}
	if profile != nil && profile.ProfileID != "motion_ui" && profile.ProfileID != "spatial_3d" {
		return nil, errors.New("direct Artifact V3 HTML supports reviewed animation_profile motion_ui or spatial_3d only")
	}
	_, narrationPlan := args["narration_plan"]
	if narrationPlan {
		for _, key := range []string{"content", "parts"} {
			if _, supplied := args[key]; supplied {
				return nil, fmt.Errorf("manage_artifact narration_plan cannot be combined with %s", key)
			}
		}
	}
	mediaType := canonicalArtifactMediaType(asString(args["media_type"]))
	filename := strings.TrimSpace(asString(args["filename"]))
	if mediaType == "" && (narrationPlan || strings.HasSuffix(strings.ToLower(filename), ".html")) {
		mediaType = "text/html"
	}
	if mediaType != "text/html" {
		return nil, errors.New("manage_artifact create currently accepts only one complete text/html Artifact V3 document")
	}
	var body string
	var parts []pebblestore.SessionArtifactPart
	if narrationPlan {
		var err error
		body, parts, err = renderArtifactNarrationPlan(args["narration_plan"])
		if err != nil {
			return nil, err
		}
	} else {
		var ok bool
		body, ok = args["content"].(string)
		if !ok || strings.TrimSpace(body) == "" {
			return nil, errors.New("manage_artifact create requires non-empty UTF-8 HTML content or narration_plan")
		}
		parts = deriveArtifactHTMLParts([]byte(body), mediaType)
	}
	var durationMS int64
	if profile != nil {
		durationMS, err = ArtifactHTMLAnimationDurationMS([]byte(body))
		if err != nil {
			return nil, err
		}
	}
	requestedParts, err := parseArtifactParts(args["parts"])
	if err != nil {
		return nil, err
	}
	if len(requestedParts) != 0 {
		derivedByID := make(map[string]pebblestore.SessionArtifactPart, len(parts))
		for _, part := range parts {
			derivedByID[strings.TrimSpace(part.ID)] = part
		}
		captureOnly := artifactHTMLCaptureOnlyRegions([]byte(body))
		for _, requested := range requestedParts {
			if captureOnly[strings.TrimSpace(requested.ID)] {
				return nil, fmt.Errorf("manage_artifact create requested Part %q is a capture-only HTML region", requested.ID)
			}
			derived, ok := derivedByID[strings.TrimSpace(requested.ID)]
			if !ok || derived.Kind != "selector" {
				return nil, fmt.Errorf("manage_artifact create requested Part %q does not resolve to a stable HTML region id", requested.ID)
			}
			derived.Label = firstNonEmptyString(strings.TrimSpace(requested.Label), derived.Label)
		}
		parts = parts[:0]
		for _, requested := range requestedParts {
			derived := derivedByID[strings.TrimSpace(requested.ID)]
			derived.Label = firstNonEmptyString(strings.TrimSpace(requested.Label), derived.Label)
			if requested.Kind == "temporal" {
				if profile == nil || requested.StartMs < 0 || requested.EndMs <= requested.StartMs || requested.EndMs > durationMS {
					return nil, errors.New("native temporal Parts require motion_ui or spatial_3d and 0 <= start_ms < end_ms <= canonical animation duration within the 36000-frame budget")
				}
				derived.StartMs, derived.EndMs = requested.StartMs, requested.EndMs
			}
			parts = append(parts, derived)
		}
	}
	manifestParts := make([]pebblestore.ArtifactV3Part, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Kind) != "selector" || strings.TrimSpace(part.Selector) == "" {
			continue
		}
		var captureTime *int64
		var temporal *pebblestore.ArtifactV3TemporalScene
		if part.EndMs > part.StartMs {
			value := part.StartMs + (part.EndMs-part.StartMs)/2
			captureTime = &value
			temporal = &pebblestore.ArtifactV3TemporalScene{SceneID: part.ID, StartMS: part.StartMs, EndMS: part.EndMs}
		}
		manifestParts = append(manifestParts, pebblestore.ArtifactV3Part{
			CaptureTimeMS: captureTime,
			Temporal:      temporal,
			ID:            strings.TrimSpace(part.ID),
			Label:         strings.TrimSpace(part.Label),
			Locator: pebblestore.ArtifactV3Locator{
				Kind: "selector", Path: "index.html", Value: strings.TrimSpace(part.Selector),
			},
		})
	}
	if len(manifestParts) == 0 {
		return nil, errors.New("manage_artifact create requires at least one stable HTML region id on header, main, section, article, nav, aside, or footer")
	}
	if profile != nil && len(requestedParts) == 0 {
		// One whole-animation sample; all other meaningful regions remain global
		// requirements. Do not turn capture controls into output metadata.
		midpoint := durationMS / 2
		manifestParts[0].CaptureTimeMS = &midpoint
	}
	if raw, ok := args["native_parts"]; ok {
		if len(requestedParts) != 0 {
			return nil, errors.New("native_parts and parts are mutually exclusive")
		}
		manifestParts, err = parseArtifactV3NativeParts(raw)
		if err != nil {
			return nil, err
		}
	}
	var sceneContract *pebblestore.ArtifactV3SceneContract
	if raw, ok := args["scene_contract"]; ok {
		sceneContract, err = ParseArtifactV3SceneContract(raw)
		if err != nil {
			return nil, err
		}
	}
	nativeManifest := pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: manifestParts, AnimationProfile: profile, SceneContract: sceneContract}
	if err := pebblestore.ValidateArtifactV3Scenes(nativeManifest, durationMS); err != nil {
		return nil, err
	}
	manifest, err := json.Marshal(nativeManifest)
	if err != nil {
		return nil, err
	}
	producerRunID := strings.TrimSpace(principal.RunID)
	if producerRunID == "" {
		return nil, errors.New("manage_artifact create requires trusted provider run identity")
	}
	project := map[string][]byte{pebblestore.ArtifactV3ManifestFilename: manifest, "index.html": []byte(body)}
	projectDigest := artifactV3Digest(project)
	r.directArtifactV3Mu.Lock()
	existing, hasExisting := r.directArtifactV3ByRun[producerRunID]
	r.directArtifactV3Mu.Unlock()
	if hasExisting && existing.ProjectDigest == projectDigest {
		result := cloneDirectArtifactV3Result(existing.Result)
		result["idempotent_replay"] = true
		result["message"] = "This exact ready Artifact V3 revision was already published in the current run; do not recreate it."
		return result, nil
	}
	prompt := strings.TrimSpace(firstNonEmptyString(asString(args["collection_description"]), asString(args["collection_name"]), filename))
	prepare := ArtifactV3PrepareTurnRequest{
		AccountScopeID:   principal.AccountScopeID,
		UserID:           principal.UserID,
		OwnerSessionID:   principal.SessionID,
		TaskCallID:       "direct-create:" + producerRunID,
		Prompt:           prompt,
		PolicyRevision:   "direct-primary-html-v1",
		SceneContract:    sceneContract,
		AnimationProfile: profile,
		CandidateIndex:   1,
		Initial:          true,
		ExpiresAt:        time.Now().Add(15 * time.Minute).UnixMilli(),
	}
	if hasExisting {
		priorPartIDs, partErr := directArtifactV3PartIDs(existing.Result["parts"])
		if partErr != nil {
			return nil, partErr
		}
		if nextPartIDs := artifactV3ManifestPartIDs(manifestParts); strings.Join(priorPartIDs, "\x00") != strings.Join(nextPartIDs, "\x00") {
			return nil, errors.New("manage_artifact create repair must preserve the prior Artifact V3 stable Part IDs and order")
		}
		reference, _ := existing.Result["reference"].(map[string]any)
		baseCommit := strings.TrimSpace(asString(reference["commit_oid"]))
		artifactID := strings.TrimSpace(asString(existing.Result["artifact_id"]))
		if artifactID == "" || baseCommit == "" {
			return nil, errors.New("manage_artifact create cannot repair an incomplete prior Artifact V3 publication")
		}
		prepare.ArtifactID = artifactID
		prepare.BaseCommitOID = baseCommit
		prepare.Initial = false
		prepare.TaskCallID = "direct-repair:" + strings.TrimSpace(callID)
	}
	grant, err := r.artifactV3Author.PrepareTurn(ctx, prepare)
	if err != nil {
		return nil, err
	}
	grant.ProducerSessionID = strings.TrimSpace(scope.SessionID)
	grant.ProducerRunID = producerRunID
	ctx = WithArtifactV3AuthorRunContext(ctx, ArtifactV3AuthorRunContext{Grant: grant})
	author := ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: grant.ProducerSessionID, ProducerRunID: producerRunID}
	fail := func(code string, cause error) (map[string]any, error) {
		// Terminal errors remain errors; retain durable source for diagnosis.
		return nil, cause
	}
	// A replayed allocation may already have retained source. Never overwrite it
	// with a new complete create payload, even after a process restart.
	inspection, err := r.artifactV3Author.Inspect(ctx, author, grant)
	if err != nil {
		return nil, err
	}
	if grant.Initial && len(inspection.Files) != 0 {
		gate := ArtifactV3AuthorGate{}
		if inspection.LatestGate != nil {
			gate = *inspection.LatestGate
		}
		return directArtifactV3Retained(grant, gate), nil
	}
	writeProject := func(path string, content []byte) error {
		if grant.Initial {
			return r.artifactV3Author.Create(ctx, author, grant, path, content)
		}
		current, readErr := r.artifactV3Author.Read(ctx, author, grant, path, 0, 0)
		if readErr != nil {
			return readErr
		}
		if current.Content == string(content) {
			return nil
		}
		return r.artifactV3Author.Edit(ctx, author, grant, path, []byte(current.Content), content, false)
	}
	if err := writeProject(pebblestore.ArtifactV3ManifestFilename, manifest); err != nil {
		return fail("manifest_write_failed", err)
	}
	if err := writeProject("index.html", []byte(body)); err != nil {
		return fail("html_write_failed", err)
	}
	gate, err := r.artifactV3Author.BuildPreview(ctx, author, grant)
	if err != nil {
		return fail("build_preview_failed", err)
	}
	if !gate.Ready {
		diagnostic := "the complete Artifact V3 HTML failed its build or browser preview gate"
		if len(gate.Diagnostics) != 0 && strings.TrimSpace(gate.Diagnostics[0].Message) != "" {
			diagnostic = strings.TrimSpace(gate.Diagnostics[0].Message)
		}
		if profile != nil && strings.Contains(diagnostic, "Part is missing or not visible") {
			diagnostic += "; sequential scenes need explicit parts kind=temporal with start_ms/end_ms and the same stable HTML id, plus __SWARM_ANIMATION_V1__ {version:'swarm.animation/v1', ready:async()=>{}, seek:async ms=>({time_ms:ms})}; seek must pause and render the requested playhead deterministically"
		}
		result := directArtifactV3Retained(grant, gate)
		result["message"] = diagnostic + "; source retained: use author_v3 with draft_handle to repair"
		return result, nil
	}
	finished, err := r.artifactV3Author.Finish(ctx, author, grant)
	if err != nil {
		return fail("finish_failed", err)
	}
	_ = r.artifactV3Author.Discard(grant)
	selectedRevision := finished.Revision
	if !grant.Initial {
		selector, ok := r.artifactV3Author.repository.(ArtifactV3DirectHeadSelector)
		if !ok {
			return nil, errors.New("manage_artifact create repair requires native Artifact V3 head selection")
		}
		selectedRevision, err = selector.SelectArtifactV3DirectHead(ctx, principal.AccountScopeID, principal.UserID, principal.SessionID, grant.ArtifactID, grant.TurnID, grant.CandidateID)
		if err != nil {
			return nil, err
		}
	}
	reference := map[string]any{
		"session_id":   principal.SessionID,
		"artifact_id":  grant.ArtifactID,
		"commit_oid":   selectedRevision.CommitOID,
		"revision_ref": "revision-" + selectedRevision.CommitOID,
	}
	inspectionReference := map[string]any{
		"session_id": principal.SessionID, "artifact_id": grant.ArtifactID,
		"revision_ref": "revision-" + selectedRevision.CommitOID,
	}
	result := map[string]any{
		"status":                  "ready",
		"artifact_id":             grant.ArtifactID,
		"turn_id":                 grant.TurnID,
		"candidate_id":            grant.CandidateID,
		"commit_oid":              selectedRevision.CommitOID,
		"tree_oid":                selectedRevision.TreeOID,
		"part_count":              len(manifestParts),
		"parts":                   manifestParts,
		"reference":               reference,
		"media_inspect_reference": inspectionReference,
	}
	if grant.Initial {
		result["revision_kind"] = "initial"
		result["message"] = "The initial Artifact V3 revision is ready. Inspect its exact media reference before completion."
	} else {
		result["revision_kind"] = "visual_repair"
		result["message"] = "The corrected complete HTML is now a selected exact child revision. Inspect this new revision, then complete or make one further corrected create call."
	}
	r.directArtifactV3Mu.Lock()
	if r.directArtifactV3ByRun == nil {
		r.directArtifactV3ByRun = make(map[string]directArtifactV3Publication)
	}
	r.directArtifactV3ByRun[producerRunID] = directArtifactV3Publication{Result: cloneDirectArtifactV3Result(result), ProjectDigest: projectDigest}
	r.directArtifactV3Mu.Unlock()
	return result, nil
}

func artifactV3ManifestPartIDs(parts []pebblestore.ArtifactV3Part) []string {
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		ids = append(ids, strings.TrimSpace(part.ID))
	}
	return ids
}

func directArtifactV3PartIDs(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("manage_artifact create cannot repair an incomplete prior Artifact V3 Part projection")
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(asString(part["id"])) == "" {
			return nil, errors.New("manage_artifact create cannot repair an incomplete prior Artifact V3 Part projection")
		}
		ids = append(ids, strings.TrimSpace(asString(part["id"])))
	}
	return ids, nil
}

func cloneDirectArtifactV3Result(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	encoded, _ := json.Marshal(input)
	var output map[string]any
	_ = json.Unmarshal(encoded, &output)
	return output
}
