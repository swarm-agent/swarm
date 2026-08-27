package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/storyboard"
)

func (r *Runtime) importStoryboardPlan(ctx context.Context, principal identity.Principal, projectSessionID, projectID, baseRevisionID string, args map[string]any) (*pebblestore.VideoPlanProposal, error) {
	for key := range args {
		switch key {
		case "action", "project_id", "base_revision_id", "proposal_id", "title", "rationale", "storyboard_source", "exports":
		default:
			return nil, storyboardImportError("storyboard_input_invalid", fmt.Sprintf("import_storyboard contains unsupported field %q", key))
		}
	}
	if r.artifactAuthority == nil {
		return nil, storyboardImportError("storyboard_authority_unavailable", "managed artifact authority is not configured")
	}
	if r.videoProjects == nil {
		return nil, storyboardImportError("storyboard_project_authority_unavailable", "video project authority is not configured")
	}
	project, found, err := r.videoProjects.GetProject(principal, projectSessionID, projectID)
	if err != nil || !found {
		return nil, storyboardImportError("storyboard_project_missing", "video project was not found")
	}
	if project.CurrentRevisionID != baseRevisionID {
		return nil, storyboardImportError("storyboard_revision_stale", "base_revision_id is not the current video project revision")
	}
	sourceRef, err := parseManageVideoArtifactReference(args["storyboard_source"], "storyboard_source")
	if err != nil {
		return nil, storyboardImportError("storyboard_source_reference_invalid", "complete exact ready storyboard HTML reference is required")
	}
	artifactPrincipal := artifact.Principal{SessionID: sourceRef.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	source, err := r.artifactAuthority.GetReference(artifactPrincipal, *sourceRef)
	if err != nil || source.Status != pebblestore.SessionArtifactStatusReady || source.EventSeq != sourceRef.EventSeq {
		return nil, storyboardImportError("storyboard_source_stale", "storyboard source is stale, missing, or not ready")
	}
	if mediaType := canonicalArtifactMediaType(source.MediaType); mediaType != "text/html" && mediaType != "application/zip" {
		return nil, storyboardImportError("storyboard_source_type_unsupported", "storyboard source must be ready text/html or an HTML package")
	}
	files, entry, err := r.readCaptureSource(ctx, artifactPrincipal, *sourceRef, source)
	if err != nil {
		return nil, storyboardImportError("storyboard_source_invalid", "storyboard source could not be read as bounded HTML")
	}
	capture, err := parseCaptureManifest(files[entry])
	if err != nil {
		return nil, storyboardImportError("storyboard_capture_manifest_invalid", "storyboard source must declare a valid swarm.capture/v1 manifest")
	}
	manifest, err := storyboard.ParseHTML(files[entry], captureManifestStateIDs(capture.States))
	if err != nil {
		return nil, storyboardImportError(storyboard.ErrorCode(err), storyboard.SafeMessage(err))
	}

	rawExports, ok := args["exports"]
	if !ok {
		return nil, storyboardImportError("storyboard_exports_missing", "storyboard import requires one exact exported PNG for every section")
	}
	exports, err := parseStoryboardExports(rawExports)
	if err != nil {
		return nil, err
	}
	if len(exports) != len(manifest.Sections) {
		return nil, storyboardImportError("storyboard_exports_incomplete", "storyboard export count does not match section count")
	}

	parts := make([]pebblestore.VideoPlanPart, 0, len(manifest.Sections))
	seenExports := make(map[string]struct{}, len(exports))
	for _, section := range manifest.Sections {
		visual, found := exports[section.CaptureStateID]
		if !found {
			return nil, storyboardImportError("storyboard_exports_incomplete", "storyboard export is missing capture state "+section.CaptureStateID)
		}
		if _, duplicate := seenExports[visual.VariantID]; duplicate {
			return nil, storyboardImportError("storyboard_export_duplicate", "storyboard exports must use one distinct PNG per capture state")
		}
		seenExports[visual.VariantID] = struct{}{}
		visualPrincipal := artifact.Principal{SessionID: visual.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
		variant, readErr := r.artifactAuthority.GetReference(visualPrincipal, visual)
		if readErr != nil || variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq != visual.EventSeq {
			return nil, storyboardImportError("storyboard_export_stale", "storyboard export for "+section.CaptureStateID+" is stale, missing, or not ready")
		}
		if canonicalArtifactMediaType(variant.MediaType) != "image/png" {
			return nil, storyboardImportError("storyboard_export_type_invalid", "storyboard exports must be exact ready image/png artifacts")
		}
		lineage := variant.Lineage
		if lineage.SourceSessionID != sourceRef.SessionID || lineage.SourceCollectionID != sourceRef.CollectionID || lineage.SourceVariantID != sourceRef.VariantID || lineage.SourceEventSeq != sourceRef.EventSeq {
			return nil, storyboardImportError("storyboard_export_lineage_mismatch", "storyboard export for "+section.CaptureStateID+" does not descend from the exact storyboard source")
		}
		if source.OutputRequirements != nil && variant.OutputRequirements != nil && (source.OutputRequirements.Width != variant.OutputRequirements.Width || source.OutputRequirements.Height != variant.OutputRequirements.Height) {
			return nil, storyboardImportError("storyboard_export_requirements_mismatch", "storyboard export output requirements do not match the storyboard source")
		}
		sourceCopy := *sourceRef
		visualCopy := visual
		parts = append(parts, pebblestore.VideoPlanPart{ID: section.ID, Title: section.Title, DurationMs: section.DurationMs, Narration: section.Narration, OnScreenText: section.OnScreenText, VisualDirection: section.CreativeDirection, CaptureStateID: section.CaptureStateID, FilmingRequirements: append([]string(nil), section.FilmingRequirements...), ProductionState: section.ProductionState, StoryboardSource: &sourceCopy, StoryboardStill: &visualCopy, Visual: &visualCopy, VisualMediaType: "image/png"})
	}
	if len(seenExports) != len(exports) {
		return nil, storyboardImportError("storyboard_export_unknown", "storyboard exports contain an undeclared capture state")
	}
	return &pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Summary: strings.TrimSpace(asString(args["rationale"])), Parts: parts}, nil
}

func parseStoryboardExports(raw any) (map[string]pebblestore.SessionArtifactSelectionReference, error) {
	if text, ok := raw.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decoded); err != nil {
			return nil, storyboardImportError("storyboard_exports_invalid", "exports must be a valid JSON array")
		}
		raw = decoded
	}
	values, ok := raw.([]any)
	if !ok || len(values) < 1 || len(values) > storyboard.MaxSections {
		return nil, storyboardImportError("storyboard_exports_invalid", "exports must contain 1 to 16 exact state/reference objects")
	}
	exports := make(map[string]pebblestore.SessionArtifactSelectionReference, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, storyboardImportError("storyboard_exports_invalid", "each export must be an object")
		}
		for key := range item {
			if key != "state_id" && key != "reference" {
				return nil, storyboardImportError("storyboard_exports_invalid", fmt.Sprintf("storyboard export contains unsupported field %q", key))
			}
		}
		stateID := strings.TrimSpace(asString(item["state_id"]))
		if stateID == "" {
			return nil, storyboardImportError("storyboard_exports_invalid", "each export requires state_id")
		}
		if _, duplicate := exports[stateID]; duplicate {
			return nil, storyboardImportError("storyboard_export_duplicate", "storyboard exports contain duplicate state ids")
		}
		ref, err := parseManageVideoArtifactReference(item["reference"], "exports.reference")
		if err != nil {
			return nil, storyboardImportError("storyboard_export_reference_invalid", "each storyboard export requires a complete exact ready reference")
		}
		exports[stateID] = *ref
	}
	return exports, nil
}

func storyboardImportError(code, message string) error {
	return fmt.Errorf("manage_video import_storyboard failed (code=%s): %s", code, message)
}
