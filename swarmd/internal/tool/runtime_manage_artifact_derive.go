package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const maxManagedTextDeriveEdits = 32

type managedTextEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (r *Runtime) deriveManagedTextArtifact(ctx context.Context, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		switch key {
		case "action", "session_id", "collection_id", "variant_id", "event_seq", "text_edits", "collection_name", "collection_description", "filename", "presentation", "animation_profile":
		default:
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact derive_text contains unsupported field %q", key)
		}
	}
	if err := validateArtifactRetrievalIdentity(args, "derive_text", true); err != nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text requires one complete exact ready UTF-8 text reference")
	}
	ref, explicit, err := parseArtifactReadReference(args, strings.TrimSpace(asString(args["variant_id"])))
	if err != nil || !explicit {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text requires one complete exact ready UTF-8 text reference")
	}
	source, err := r.artifactAuthority.GetReference(principal, ref)
	if err != nil || source.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text exact source could not be authenticated as ready")
	}
	mediaType := canonicalArtifactMediaType(source.MediaType)
	if !managedArtifactTextMediaType(mediaType) {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text source must be a UTF-8 text artifact")
	}
	body, ready, err := r.artifactAuthority.ReadReference(ctx, principal, ref, int64(manageArtifactMaxReadBytes))
	if err != nil || ready.Status != pebblestore.SessionArtifactStatusReady || !utf8.Valid(body) {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text source is unavailable, exceeds the bounded text limit, or is not valid UTF-8")
	}
	edits, err := parseManagedTextEdits(args["text_edits"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	derived := string(body)
	for index, edit := range edits {
		occurrences := strings.Count(derived, edit.OldString)
		if occurrences == 0 {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact derive_text edit %d old_string was not found", index)
		}
		if !edit.ReplaceAll && occurrences != 1 {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact derive_text edit %d old_string matched %d times; use a unique string or replace_all", index, occurrences)
		}
		if edit.ReplaceAll {
			derived = strings.ReplaceAll(derived, edit.OldString, edit.NewString)
		} else {
			derived = strings.Replace(derived, edit.OldString, edit.NewString, 1)
		}
	}

	filename := strings.TrimSpace(asString(args["filename"]))
	if filename == "" {
		filename = source.Filename
	}
	presentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if presentation.Kind == "" {
		presentation = source.Presentation
	}
	profile := cloneArtifactAnimationProfile(source.AnimationProfile)
	if raw, supplied := args["animation_profile"]; supplied {
		requested, profileErr := artifact.ParseAnimationProfile(raw)
		if profileErr != nil {
			return pebblestore.SessionArtifactVariant{}, profileErr
		}
		if profile != nil && *requested != *profile {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text animation_profile conflicts with the exact source snapshot")
		}
		profile = requested
	}
	if err := validateArtifactAnimationMedia(profile, false, filename, mediaType); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collectionID := managedArtifactOpaqueID("collection-derived-text", principal.SessionID, callID)
	variantID := managedArtifactOpaqueID("variant-derived-text", principal.SessionID, callID)
	input := artifact.CreateInput{
		RequestID: requestID, CollectionID: collectionID, VariantID: variantID,
		CollectionName: strings.TrimSpace(asString(args["collection_name"])), CollectionDescription: strings.TrimSpace(asString(args["collection_description"])),
		Filename: filename, MediaType: mediaType, Presentation: presentation,
		OutputRequirements: cloneArtifactOutputRequirements(source.OutputRequirements), AnimationProfile: profile,
		SourceSessionID: ref.SessionID, SourceCollectionID: ref.CollectionID, SourceVariantID: ref.VariantID, SourceEventSeq: ref.EventSeq,
		Body: []byte(derived), AutoAccept: true,
	}
	if input.CollectionName == "" {
		input.CollectionName = "Exact text derivation"
	}
	if mediaType == "text/html" {
		input.Parts = deriveArtifactHTMLParts(input.Body, input.MediaType)
	}
	variant, err := r.artifactAuthority.Create(ctx, principal, input)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact derive_text did not publish a ready artifact")
	}
	return variant, nil
}

func parseManagedTextEdits(raw any) ([]managedTextEdit, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > maxManagedTextDeriveEdits {
		return nil, fmt.Errorf("manage_artifact derive_text text_edits must contain 1 to %d edits", maxManagedTextDeriveEdits)
	}
	result := make([]managedTextEdit, 0, len(values))
	for index, rawEdit := range values {
		value, ok := rawEdit.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("manage_artifact derive_text edit %d must be an object", index)
		}
		for key := range value {
			switch key {
			case "old_string", "new_string", "replace_all":
			default:
				return nil, fmt.Errorf("manage_artifact derive_text edit %d contains unsupported field %q", index, key)
			}
		}
		oldString, oldOK := value["old_string"].(string)
		newString, newOK := value["new_string"].(string)
		if !oldOK || !newOK || oldString == "" || !utf8.ValidString(oldString) || !utf8.ValidString(newString) {
			return nil, fmt.Errorf("manage_artifact derive_text edit %d requires non-empty valid UTF-8 old_string and valid UTF-8 new_string", index)
		}
		replaceAll := false
		if rawReplaceAll, supplied := value["replace_all"]; supplied {
			var valid bool
			replaceAll, valid = rawReplaceAll.(bool)
			if !valid {
				return nil, fmt.Errorf("manage_artifact derive_text edit %d replace_all must be boolean", index)
			}
		}
		result = append(result, managedTextEdit{OldString: oldString, NewString: newString, ReplaceAll: replaceAll})
	}
	return result, nil
}
