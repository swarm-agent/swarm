package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type videoStoryPart struct {
	Prompt          string
	Title           string
	DurationSeconds int
}

// parseVideoStoryParts extracts scene parts from args (supporting "scenes", "parts", or nested in "script").
func parseVideoStoryParts(args map[string]any) ([]videoStoryPart, error) {
	var rawParts any
	if scenes, ok := args["scenes"]; ok && scenes != nil {
		rawParts = scenes
	} else if parts, ok := args["parts"]; ok && parts != nil {
		rawParts = parts
	} else if script, ok := args["script"].(map[string]any); ok && script != nil {
		if scenes, ok := script["scenes"]; ok && scenes != nil {
			rawParts = scenes
		} else if parts, ok := script["parts"]; ok && parts != nil {
			rawParts = parts
		}
	}

	if rawParts == nil {
		return nil, errors.New("generate_video_story requires 'scenes' or 'parts' array")
	}

	slice, ok := rawParts.([]any)
	if !ok {
		return nil, errors.New("'scenes' or 'parts' must be an array")
	}

	if len(slice) < minChainedVideoParts {
		return nil, fmt.Errorf("generate_video_story requires at least %d scenes/parts", minChainedVideoParts)
	}
	if len(slice) > maxChainedVideoParts {
		return nil, fmt.Errorf("generate_video_story exceeds maximum of %d scenes/parts", maxChainedVideoParts)
	}

	var parts []videoStoryPart
	for i, item := range slice {
		switch v := item.(type) {
		case string:
			p := strings.TrimSpace(v)
			if p == "" {
				return nil, fmt.Errorf("scene %d prompt is empty", i+1)
			}
			parts = append(parts, videoStoryPart{Prompt: p})
		case map[string]any:
			p := strings.TrimSpace(asString(v["prompt"]))
			if p == "" {
				return nil, fmt.Errorf("scene %d requires non-empty 'prompt'", i+1)
			}
			t := strings.TrimSpace(asString(v["title"]))
			dur := int(asUint64(v["duration_seconds"]))
			parts = append(parts, videoStoryPart{Prompt: p, Title: t, DurationSeconds: dur})
		default:
			return nil, fmt.Errorf("scene %d has unsupported type %T", i+1, item)
		}
	}
	return parts, nil
}

// parseVideoStorySoundtrack extracts soundtrack direction from args or nested script.
func parseVideoStorySoundtrack(args map[string]any) (string, string, float64, any) {
	var rawSoundtrack any
	if st, ok := args["soundtrack"]; ok && st != nil {
		rawSoundtrack = st
	} else if script, ok := args["script"].(map[string]any); ok && script != nil {
		if st, ok := script["soundtrack"]; ok && st != nil {
			rawSoundtrack = st
		}
	} else if sp, ok := args["soundtrack_prompt"]; ok && sp != nil {
		rawSoundtrack = sp
	} else if ap, ok := args["audio_prompt"]; ok && ap != nil {
		rawSoundtrack = ap
	}

	audioMode := strings.ToLower(strings.TrimSpace(asString(args["audio_mode"])))
	foleyVol := asFloat64(args["foley_volume"], defaultFoleyDuckingVol)
	if foleyVol <= 0 {
		foleyVol = defaultFoleyDuckingVol
	}

	existingAudio := args["audio"]

	if rawSoundtrack == nil {
		if audioMode == "" {
			if existingAudio != nil {
				audioMode = "mix_ducked"
			} else {
				audioMode = "native"
			}
		}
		return "", audioMode, foleyVol, existingAudio
	}

	switch v := rawSoundtrack.(type) {
	case string:
		prompt := strings.TrimSpace(v)
		if audioMode == "" {
			audioMode = "mix_ducked"
		}
		return prompt, audioMode, foleyVol, existingAudio
	case map[string]any:
		prompt := strings.TrimSpace(asString(v["prompt"]))
		if m := strings.ToLower(strings.TrimSpace(firstNonEmptyString(asString(v["audio_mode"]), asString(v["mode"])))); m != "" {
			audioMode = m
		}
		if audioMode == "" {
			audioMode = "mix_ducked"
		}
		if fv, ok := v["foley_volume"]; ok {
			foleyVol = asFloat64(fv, defaultFoleyDuckingVol)
		}
		if aud, ok := v["audio"]; ok && aud != nil {
			existingAudio = aud
		}
		return prompt, audioMode, foleyVol, existingAudio
	default:
		return "", audioMode, foleyVol, existingAudio
	}
}

// generateVideoStory orchestrates multi-part video generation with automatic keyframe chaining,
// continuous Lyria soundtrack generation, and FFmpeg concatenation with Foley ducking in one call.
func (r *Runtime) generateVideoStory(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	callID string,
	requestID string,
	args map[string]any,
) (pebblestore.SessionArtifactVariant, map[string]any, error) {
	parts, err := parseVideoStoryParts(args)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}

	soundtrackPrompt, audioMode, foleyVol, existingAudio := parseVideoStorySoundtrack(args)

	storyTitle := strings.TrimSpace(firstNonEmptyString(asString(args["title"]), asString(args["collection_name"])))
	aspectRatio := strings.TrimSpace(asString(args["aspect_ratio"]))
	resolution := strings.TrimSpace(asString(args["resolution"]))

	var generatedVariants []pebblestore.SessionArtifactVariant
	var videoReferences []pebblestore.SessionArtifactSelectionReference
	var lastVariant *pebblestore.SessionArtifactVariant

	totalDurationSeconds := 0

	// Step 1: Sequentially generate each video part with automatic keyframe chaining
	for i, part := range parts {
		partNum := i + 1
		partCallID := fmt.Sprintf("%s-p%02d", callID, partNum)
		partReqID := fmt.Sprintf("%s-p%02d", requestID, partNum)

		partTitle := part.Title
		if partTitle == "" {
			if storyTitle != "" {
				partTitle = fmt.Sprintf("%s - Part %d", storyTitle, partNum)
			} else {
				partTitle = fmt.Sprintf("Scene %d", partNum)
			}
		}

		partArgs := map[string]any{
			"action":       "generate_video",
			"prompt":       part.Prompt,
			"title":        partTitle,
			"aspect_ratio": aspectRatio,
			"resolution":   resolution,
		}
		if part.DurationSeconds > 0 {
			partArgs["duration_seconds"] = part.DurationSeconds
			totalDurationSeconds += part.DurationSeconds
		} else {
			totalDurationSeconds += 8
		}

		if i == 0 {
			// Part 1: Pass initial image or image_path if provided (e.g. Swarm logo)
			if rawImage := args["image"]; rawImage != nil {
				partArgs["image"] = rawImage
			} else if rawImagePath := args["image_path"]; rawImagePath != nil {
				partArgs["image_path"] = rawImagePath
			}
		} else if lastVariant != nil {
			// Parts 2..N: Automatically chain from the preceding video variant
			partArgs["chain_from"] = map[string]any{
				"session_id":    lastVariant.SessionID,
				"collection_id": lastVariant.CollectionID,
				"variant_id":    lastVariant.ID,
				"event_seq":     lastVariant.EventSeq,
			}
		}

		res, genErr := r.generateManagedVideoArtifact(ctx, scope, principal, partCallID, partReqID, partArgs)
		if genErr != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("generate video scene %d: %w", partNum, genErr)
		}

		lastVariant = &res.LastVariant
		generatedVariants = append(generatedVariants, res.LastVariant)
		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID:    res.LastVariant.SessionID,
			CollectionID: res.LastVariant.CollectionID,
			VariantID:    res.LastVariant.ID,
			EventSeq:     res.LastVariant.EventSeq,
		}
		videoReferences = append(videoReferences, ref)
	}

	// Step 2: Generate or resolve continuous soundtrack
	var audioRef *pebblestore.SessionArtifactSelectionReference
	var audioRaw any = existingAudio
	if soundtrackPrompt != "" {
		audioCallID := fmt.Sprintf("%s-snd", callID)
		audioReqID := fmt.Sprintf("%s-snd", requestID)
		audioTitle := "Soundtrack"
		if storyTitle != "" {
			audioTitle = fmt.Sprintf("%s Soundtrack", storyTitle)
		}
		audioArgs := map[string]any{
			"action":           "generate_audio",
			"prompt":           soundtrackPrompt,
			"duration_seconds": totalDurationSeconds,
			"title":            audioTitle,
		}
		audioRes, audioErr := r.generateManagedAudioArtifact(ctx, scope, principal, audioCallID, audioReqID, audioArgs)
		if audioErr != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("generate story soundtrack: %w", audioErr)
		}
		audioRef = &pebblestore.SessionArtifactSelectionReference{
			SessionID:    audioRes.LastVariant.SessionID,
			CollectionID: audioRes.LastVariant.CollectionID,
			VariantID:    audioRes.LastVariant.ID,
			EventSeq:     audioRes.LastVariant.EventSeq,
		}
		audioRaw = map[string]any{
			"session_id":    audioRef.SessionID,
			"collection_id": audioRef.CollectionID,
			"variant_id":    audioRef.VariantID,
			"event_seq":     audioRef.EventSeq,
		}
	}

	// Step 3: Concat all parts and mix soundtrack with FFmpeg into master video
	var videosArg []any
	for _, ref := range videoReferences {
		videosArg = append(videosArg, map[string]any{
			"session_id":    ref.SessionID,
			"collection_id": ref.CollectionID,
			"variant_id":    ref.VariantID,
			"event_seq":     ref.EventSeq,
		})
	}

	masterTitle := storyTitle
	if masterTitle == "" {
		masterTitle = fmt.Sprintf("Master Video (%d Scenes)", len(videoReferences))
	}

	chainArgs := map[string]any{
		"action":       "chain_video",
		"videos":       videosArg,
		"title":        masterTitle,
		"audio_mode":   audioMode,
		"foley_volume": foleyVol,
	}
	if audioRaw != nil {
		chainArgs["audio"] = audioRaw
	}

	masterCallID := fmt.Sprintf("%s-mstr", callID)
	masterReqID := fmt.Sprintf("%s-mstr", requestID)

	masterVariant, details, chainErr := r.chainVideo(ctx, scope, principal, masterCallID, masterReqID, chainArgs)
	if chainErr != nil {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("assemble master video: %w", chainErr)
	}

	// Step 4: Extract final keyframe for immediate inspection/verification
	keyframeVariant, keyframeErr := r.extractVideoFrame(ctx, scope, principal, callID+"-key", requestID+"-key", map[string]any{
		"session_id":    masterVariant.SessionID,
		"collection_id": masterVariant.CollectionID,
		"variant_id":    masterVariant.ID,
		"event_seq":     masterVariant.EventSeq,
		"frame":         "last",
		"title":         fmt.Sprintf("%s Climax Keyframe", masterTitle),
	})
	if keyframeErr == nil {
		details["keyframe_reference"] = managedArtifactReferenceWithSession(keyframeVariant.SessionID, keyframeVariant.CollectionID, keyframeVariant.ID, keyframeVariant.EventSeq)
		details["keyframe_artifact"] = managedArtifactVariant(keyframeVariant)
	}

	var partPayloads []map[string]any
	var partRefs []map[string]any
	for _, v := range generatedVariants {
		partPayloads = append(partPayloads, managedArtifactVariant(v))
		partRefs = append(partRefs, managedArtifactReferenceWithSession(v.SessionID, v.CollectionID, v.ID, v.EventSeq))
	}
	details["scenes"] = partPayloads
	details["scene_references"] = partRefs
	details["scenes_count"] = len(generatedVariants)

	return masterVariant, details, nil
}
