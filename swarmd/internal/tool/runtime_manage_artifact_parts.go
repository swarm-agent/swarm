package tool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type focusedPartProtocolState struct {
	Read      bool
	Published bool
}

func focusedPartProtocolKey(run ArtifactRunContext) string {
	return strings.Join([]string{strings.TrimSpace(run.TaskCallID), strings.TrimSpace(run.ChildSessionID), strings.TrimSpace(run.CollectionID), strings.TrimSpace(run.VariantID)}, "\x00")
}

func (r *Runtime) markFocusedPartRead(run ArtifactRunContext) error {
	key := focusedPartProtocolKey(run)
	if key == "\x00\x00\x00" {
		return errors.New("focused part protocol identity is incomplete")
	}
	r.focusedPartMu.Lock()
	defer r.focusedPartMu.Unlock()
	if r.focusedPartProtocols == nil {
		r.focusedPartProtocols = map[string]focusedPartProtocolState{}
	}
	state := r.focusedPartProtocols[key]
	if state.Published {
		return errors.New("manage_artifact read_part cannot run after publish_part")
	}
	state.Read = true
	r.focusedPartProtocols[key] = state
	return nil
}

func (r *Runtime) beginFocusedPartPublish(run ArtifactRunContext) (func(bool), error) {
	key := focusedPartProtocolKey(run)
	r.focusedPartMu.Lock()
	if r.focusedPartProtocols == nil {
		r.focusedPartProtocols = map[string]focusedPartProtocolState{}
	}
	state := r.focusedPartProtocols[key]
	if !state.Read {
		r.focusedPartMu.Unlock()
		return nil, errors.New("manage_artifact publish_part requires a successful read_part first")
	}
	if state.Published {
		r.focusedPartMu.Unlock()
		return nil, errors.New("manage_artifact publish_part permits exactly one publication")
	}
	state.Published = true
	r.focusedPartProtocols[key] = state
	r.focusedPartMu.Unlock()
	return func(success bool) {
		if success {
			return
		}
		r.focusedPartMu.Lock()
		state := r.focusedPartProtocols[key]
		state.Published = false
		r.focusedPartProtocols[key] = state
		r.focusedPartMu.Unlock()
	}, nil
}

func managedArtifactFocusedPartRun(ctx context.Context) (ArtifactRunContext, error) {
	run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext)
	if !ok || run.SourceArtifact == nil || run.SourceComposition == nil {
		return ArtifactRunContext{}, errors.New("manage_artifact part action requires trusted focused part context")
	}
	if len(run.SourcePartDefinitions) == 0 && run.SourcePartDefinition != nil {
		run.SourcePartDefinitions = []pebblestore.SessionArtifactPartDefinition{*run.SourcePartDefinition}
	}
	if len(run.SourcePartRevisions) == 0 && run.SourcePartRevision != nil {
		run.SourcePartRevisions = []pebblestore.SessionArtifactPartRevisionReference{*run.SourcePartRevision}
	}
	if len(run.SourcePartDefinitions) == 0 || len(run.SourcePartDefinitions) != len(run.SourcePartRevisions) || len(run.SourcePartDefinitions) > pebblestore.SessionArtifactMaxParts {
		return ArtifactRunContext{}, errors.New("manage_artifact focused part authority is incomplete")
	}
	seen := map[string]struct{}{}
	for index, definition := range run.SourcePartDefinitions {
		revision := run.SourcePartRevisions[index]
		if definition.ID == "" || definition.ID != revision.PartID || revision.ArtifactChainID != run.SourceComposition.ArtifactChainID {
			return ArtifactRunContext{}, errors.New("manage_artifact focused part authority is inconsistent")
		}
		if _, duplicate := seen[definition.ID]; duplicate {
			return ArtifactRunContext{}, errors.New("manage_artifact focused part selection contains a duplicate")
		}
		seen[definition.ID] = struct{}{}
	}
	if len(run.SourcePartDefinitions) == 1 {
		run.SourcePartDefinition, run.SourcePartRevision = &run.SourcePartDefinitions[0], &run.SourcePartRevisions[0]
		run.PartID = run.SourcePartDefinitions[0].ID
	}
	if strings.TrimSpace(run.CollectionID) == "" || strings.TrimSpace(run.VariantID) == "" || strings.TrimSpace(run.ArtifactStepID) == "" || run.CandidateIndex < 1 {
		return ArtifactRunContext{}, errors.New("manage_artifact focused part destination is incomplete")
	}
	return run, nil
}

func rejectFocusedPartCallerAuthority(args map[string]any, allowed ...string) error {
	allow := map[string]bool{"action": true}
	for _, key := range allowed {
		allow[key] = true
	}
	for key := range args {
		if !allow[key] {
			return fmt.Errorf("manage_artifact focused part action rejects caller-authored field %q", key)
		}
	}
	return nil
}

func encodeManagedArtifactPart(body []byte, revision pebblestore.SessionArtifactPartRevision) map[string]any {
	result := map[string]any{"part_id": revision.PartID, "part_revision_id": revision.ID, "media_type": revision.MediaType, "digest_sha256": revision.DigestSHA256, "size": revision.Size}
	if strings.HasPrefix(revision.MediaType, "text/") || revision.MediaType == "application/json" || revision.MediaType == "application/javascript" || revision.MediaType == "image/svg+xml" {
		result["content"] = string(body)
	} else {
		result["content_base64"] = base64.StdEncoding.EncodeToString(body)
	}
	return result
}

func (r *Runtime) readManagedArtifactParts(ctx context.Context, principal artifact.Principal, args map[string]any) ([]map[string]any, error) {
	if err := rejectFocusedPartCallerAuthority(args, "max_bytes"); err != nil {
		return nil, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return nil, err
	}
	maxBytes := int64(asInt(args["max_bytes"], manageArtifactDefaultReadBytes))
	if maxBytes < 1 || maxBytes > manageArtifactMaxReadBytes {
		return nil, fmt.Errorf("manage_artifact read_parts max_bytes must be between 1 and %d", manageArtifactMaxReadBytes)
	}
	authority, ok := r.artifactAuthority.(*artifact.Authority)
	if !ok || authority == nil {
		return nil, errors.New("authoritative artifact part operations are unavailable")
	}
	parts := make([]map[string]any, 0, len(run.SourcePartRevisions))
	var total int64
	for _, selected := range run.SourcePartRevisions {
		body, revision, readErr := authority.ReadPartRevision(ctx, principal, selected, maxBytes-total)
		if readErr != nil {
			return nil, readErr
		}
		total += revision.Size
		if total > maxBytes {
			return nil, errors.New("selected artifact parts exceed the bounded read limit")
		}
		parts = append(parts, encodeManagedArtifactPart(body, revision))
	}
	if err := r.markFocusedPartRead(run); err != nil {
		return nil, err
	}
	return parts, nil
}

func (r *Runtime) readManagedArtifactPart(ctx context.Context, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if err := rejectFocusedPartCallerAuthority(args, "max_bytes"); err != nil {
		return nil, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return nil, err
	}
	if len(run.SourcePartRevisions) != 1 {
		return nil, errors.New("manage_artifact read_part requires exactly one selected part; use read_parts")
	}
	maxBytes := int64(asInt(args["max_bytes"], manageArtifactDefaultReadBytes))
	if maxBytes < 1 || maxBytes > manageArtifactMaxReadBytes {
		return nil, fmt.Errorf("manage_artifact read_part max_bytes must be between 1 and %d", manageArtifactMaxReadBytes)
	}
	authority, ok := r.artifactAuthority.(*artifact.Authority)
	if !ok || authority == nil {
		return nil, errors.New("authoritative artifact part operations are unavailable")
	}
	body, revision, err := authority.ReadPartRevision(ctx, principal, *run.SourcePartRevision, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := r.markFocusedPartRead(run); err != nil {
		return nil, err
	}
	return encodeManagedArtifactPart(body, revision), nil
}

func parseManagedPartReplacement(raw map[string]any) ([]byte, string, string, bool, error) {
	text, hasText := raw["content"]
	encoded, hasBase64 := raw["content_base64"]
	if hasText == hasBase64 {
		return nil, "", "", false, errors.New("each replacement requires exactly one of content or content_base64")
	}
	var body []byte
	if hasText {
		value, ok := text.(string)
		if !ok {
			return nil, "", "", false, errors.New("replacement content must be a string")
		}
		body = []byte(value)
	} else {
		value, ok := encoded.(string)
		if !ok {
			return nil, "", "", false, errors.New("replacement content_base64 must be a string")
		}
		var err error
		body, err = base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			return nil, "", "", false, errors.New("replacement content_base64 is invalid")
		}
	}
	if len(body) == 0 || len(body) > manageArtifactMaxCreateBytes {
		return nil, "", "", false, fmt.Errorf("replacement content must be between 1 and %d bytes", manageArtifactMaxCreateBytes)
	}
	return body, strings.TrimSpace(asString(raw["media_type"])), strings.TrimSpace(asString(raw["filename"])), raw["locked"] == true, nil
}

func parseExactPartRevision(raw any) (pebblestore.SessionArtifactPartRevisionReference, error) {
	row, ok := raw.(map[string]any)
	if !ok {
		return pebblestore.SessionArtifactPartRevisionReference{}, errors.New("part choice revision must be an object")
	}
	ref := pebblestore.SessionArtifactPartRevisionReference{ArtifactChainID: strings.TrimSpace(asString(row["artifact_chain_id"])), PartID: strings.TrimSpace(asString(row["part_id"])), PartRevisionID: strings.TrimSpace(asString(row["part_revision_id"])), OwnerSessionID: strings.TrimSpace(asString(row["owner_session_id"])), DigestSHA256: strings.ToLower(strings.TrimSpace(asString(row["digest_sha256"]))), Size: int64(asInt(row["size"], 0)), MediaType: strings.TrimSpace(asString(row["media_type"]))}
	if ref.ArtifactChainID == "" || ref.PartID == "" || ref.PartRevisionID == "" || ref.OwnerSessionID == "" || ref.DigestSHA256 == "" || ref.Size < 1 || ref.MediaType == "" {
		return pebblestore.SessionArtifactPartRevisionReference{}, errors.New("part choice revision is incomplete")
	}
	return ref, nil
}

func (r *Runtime) selectManagedArtifactParts(ctx context.Context, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		if key != "action" && key != "session_id" && key != "collection_id" && key != "variant_id" && key != "event_seq" && key != "part_choices" {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact select_parts contains unsupported field %q", key)
		}
	}
	source := pebblestore.SessionArtifactSelectionReference{SessionID: strings.TrimSpace(asString(args["session_id"])), CollectionID: strings.TrimSpace(asString(args["collection_id"])), VariantID: strings.TrimSpace(asString(args["variant_id"])), EventSeq: asUint64(args["event_seq"])}
	if source.SessionID == "" || source.CollectionID == "" || source.VariantID == "" || source.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact select_parts requires one complete exact source reference")
	}
	authority, ok := r.artifactAuthority.(*artifact.Authority)
	if !ok || authority == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("authoritative artifact part operations are unavailable")
	}
	variant, err := authority.GetReference(principal, source)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if variant.Composition == nil || variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact select_parts source has no authoritative composition")
	}
	raw, ok := args["part_choices"].([]any)
	if !ok || len(raw) == 0 || len(raw) > pebblestore.SessionArtifactMaxParts {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact select_parts requires one or more bounded part_choices")
	}
	choices := make([]artifact.PartRevisionChoiceInput, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact part_choices must contain objects")
		}
		ref, parseErr := parseExactPartRevision(row["revision"])
		if parseErr != nil {
			return pebblestore.SessionArtifactVariant{}, parseErr
		}
		choices = append(choices, artifact.PartRevisionChoiceInput{PartID: strings.TrimSpace(asString(row["part_id"])), Revision: ref, RevisionEventSeq: asUint64(row["revision_event_seq"]), Locked: row["locked"] == true})
	}
	return authority.SelectPartRevisions(ctx, principal, artifact.SelectPartRevisionsInput{RequestID: requestID, CollectionID: managedArtifactOpaqueID("collection", principal.SessionID, callID), VariantID: managedArtifactOpaqueID("variant", principal.SessionID, callID), ArtifactStepID: managedArtifactOpaqueID("step", principal.SessionID, callID), SourceArtifact: source, SourceComposition: *variant.Composition, Choices: choices})
}

func (r *Runtime) publishManagedArtifactParts(ctx context.Context, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	if err := rejectFocusedPartCallerAuthority(args, "replacements"); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	raw, ok := args["replacements"].([]any)
	if !ok || len(raw) != len(run.SourcePartDefinitions) {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_parts requires exactly one replacement for every selected part")
	}
	selected := make(map[string]int, len(run.SourcePartDefinitions))
	for index, definition := range run.SourcePartDefinitions {
		selected[definition.ID] = index
	}
	replacements := make([]artifact.PartReplacementInput, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_parts replacements must be objects")
		}
		for key := range row {
			if key != "part_id" && key != "content" && key != "content_base64" && key != "media_type" && key != "filename" && key != "locked" {
				return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact publish_parts replacement contains unsupported field %q", key)
			}
		}
		partID := strings.TrimSpace(asString(row["part_id"]))
		index, exists := selected[partID]
		if !exists {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("replacement part %q is not in the authenticated selection", partID)
		}
		if _, duplicate := seen[partID]; duplicate {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("replacement part %q is duplicated", partID)
		}
		seen[partID] = struct{}{}
		body, mediaType, filename, locked, parseErr := parseManagedPartReplacement(row)
		if parseErr != nil {
			return pebblestore.SessionArtifactVariant{}, parseErr
		}
		if mediaType == "" {
			mediaType = run.SourcePartRevisions[index].MediaType
		}
		if filename == "" {
			filename = "part-" + partID
		}
		replacements = append(replacements, artifact.PartReplacementInput{PartDefinition: run.SourcePartDefinitions[index], SourcePartRevision: run.SourcePartRevisions[index], Filename: filename, MediaType: mediaType, Body: body, Locked: locked})
	}
	finish, err := r.beginFocusedPartPublish(run)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	success := false
	defer func() { finish(success) }()
	authority, ok := r.artifactAuthority.(*artifact.Authority)
	if !ok || authority == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("authoritative artifact part operations are unavailable")
	}
	variant, err := authority.PublishPartReplacements(ctx, principal, artifact.PublishPartReplacementsInput{RequestID: requestID, CallID: strings.TrimSpace(callID), CollectionID: run.CollectionID, VariantID: run.VariantID, ArtifactStepID: run.ArtifactStepID, IterationTurnID: firstNonEmptyManagedPartID(run.IterationID, run.ArtifactStepID), IterationGroupID: firstNonEmptyManagedPartID(run.IterationGroupID, callID), CandidateIndex: run.CandidateIndex, AutoAccept: run.AutoAccept, SourceArtifact: *run.SourceArtifact, SourceComposition: *run.SourceComposition, Replacements: replacements})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	success = true
	return variant, nil
}

func firstNonEmptyManagedPartID(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "part-turn"
}

func (r *Runtime) publishManagedArtifactPart(ctx context.Context, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	if err := rejectFocusedPartCallerAuthority(args, "content", "content_base64", "media_type", "filename", "presentation"); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if len(run.SourcePartRevisions) != 1 {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_part requires exactly one selected part; use publish_parts")
	}
	text, hasText := args["content"]
	encoded, hasBase64 := args["content_base64"]
	if hasText == hasBase64 {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_part requires exactly one of content or content_base64")
	}
	var body []byte
	if hasText {
		value, ok := text.(string)
		if !ok {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_part content must be a string")
		}
		body = []byte(value)
	} else {
		value, ok := encoded.(string)
		if !ok {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_part content_base64 must be a string")
		}
		body, err = base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact publish_part content_base64 is invalid")
		}
	}
	if len(body) == 0 || len(body) > manageArtifactMaxCreateBytes {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact publish_part content must be between 1 and %d bytes", manageArtifactMaxCreateBytes)
	}
	finish, err := r.beginFocusedPartPublish(run)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	success := false
	defer func() { finish(success) }()
	mediaType := strings.TrimSpace(asString(args["media_type"]))
	if mediaType == "" {
		mediaType = run.SourcePartRevision.MediaType
	}
	presentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	filename := strings.TrimSpace(asString(args["filename"]))
	if filename == "" {
		filename = "part-" + run.PartID
	}
	authority, ok := r.artifactAuthority.(*artifact.Authority)
	if !ok || authority == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("authoritative artifact part operations are unavailable")
	}
	variant, err := authority.PublishPartReplacement(ctx, principal, artifact.PublishPartReplacementInput{
		RequestID: requestID, CallID: strings.TrimSpace(callID), CollectionID: run.CollectionID, VariantID: run.VariantID,
		ArtifactStepID: run.ArtifactStepID, CandidateIndex: run.CandidateIndex, AutoAccept: run.AutoAccept,
		SourceArtifact: *run.SourceArtifact, SourceComposition: *run.SourceComposition, PartDefinition: *run.SourcePartDefinition,
		SourcePartRevision: *run.SourcePartRevision, Filename: filename, MediaType: mediaType, Presentation: presentation,
		OutputRequirements: run.OutputRequirements, AnimationProfile: run.AnimationProfile, Body: body,
	})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	success = true
	return variant, nil
}
