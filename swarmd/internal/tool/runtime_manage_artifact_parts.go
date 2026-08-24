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
	if !ok || run.SourceArtifact == nil || run.SourceComposition == nil || run.SourcePartDefinition == nil || run.SourcePartRevision == nil {
		return ArtifactRunContext{}, errors.New("manage_artifact part action requires trusted focused part context")
	}
	if strings.TrimSpace(run.PartID) == "" || run.PartID != run.SourcePartDefinition.ID || run.PartID != run.SourcePartRevision.PartID || run.SourceComposition.ArtifactChainID != run.SourcePartRevision.ArtifactChainID {
		return ArtifactRunContext{}, errors.New("manage_artifact focused part authority is inconsistent")
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

func (r *Runtime) readManagedArtifactPart(ctx context.Context, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if err := rejectFocusedPartCallerAuthority(args, "max_bytes"); err != nil {
		return nil, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return nil, err
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
	result := map[string]any{
		"part_id": revision.PartID, "part_revision_id": revision.ID, "media_type": revision.MediaType,
		"digest_sha256": revision.DigestSHA256, "size": revision.Size,
	}
	if strings.HasPrefix(revision.MediaType, "text/") || revision.MediaType == "application/json" || revision.MediaType == "application/javascript" || revision.MediaType == "image/svg+xml" {
		result["content"] = string(body)
	} else {
		result["content_base64"] = base64.StdEncoding.EncodeToString(body)
	}
	return result, nil
}

func (r *Runtime) publishManagedArtifactPart(ctx context.Context, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	if err := rejectFocusedPartCallerAuthority(args, "content", "content_base64", "media_type", "filename", "presentation"); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	run, err := managedArtifactFocusedPartRun(ctx)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
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
