package tool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (r *Runtime) readDirectArtifactV3HTML(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("manage_artifact read_v3 requires the Artifact V3 author service")
	}
	for key := range args {
		if key != "action" && key != "artifact_v3_reference" {
			if key == "max_bytes" {
				return nil, errors.New("manage_artifact read_v3 does not support max_bytes; complete project HTML is returned up to the authoring limit")
			}
			return nil, fmt.Errorf("manage_artifact read_v3 contains unsupported field %q", key)
		}
	}
	reference, err := parseDirectArtifactV3RevisionInput(args["artifact_v3_reference"])
	if err != nil {
		return nil, err
	}
	callerSessionID := strings.TrimSpace(principal.SessionID)
	scopeSessionID := strings.TrimSpace(scope.SessionID)
	if callerSessionID == "" || callerSessionID != scopeSessionID {
		return nil, errors.New("manage_artifact read_v3 caller is not authenticated for the current session")
	}
	var project map[string][]byte
	var parts []pebblestore.ArtifactV3Part
	if reference.SessionID == callerSessionID {
		reader, ok := r.artifactV3Author.repository.(ArtifactV3DirectRevisionReader)
		if !ok {
			return nil, errors.New("manage_artifact read_v3 requires native Artifact V3 revision reading")
		}
		project, parts, err = reader.ReadArtifactV3DirectRevision(ctx, principal.AccountScopeID, principal.UserID, reference.SessionID, reference.ArtifactID, reference.RevisionRef)
	} else {
		if retainedReader, ok := r.artifactV3Author.repository.(ArtifactV3RetainedSourceReader); ok {
			project, parts, err = retainedReader.ReadArtifactV3RetainedRevision(ctx, principal.AccountScopeID, principal.UserID, reference.SessionID, reference.ArtifactID, reference.RevisionRef)

		} else {
			return nil, errors.New("manage_artifact read_v3 requires native Artifact V3 revision reading")
		}
	}
	if err != nil {
		return nil, err
	}
	manifest, err := directArtifactV3ProjectManifest(project)
	if err != nil {
		return nil, err
	}
	html := project[manifest.Entrypoint]
	if len(html) == 0 || len(html) > manageArtifactMaxCreateBytes {
		return nil, errors.New("manage_artifact read_v3 exact HTML is unavailable or exceeds the bounded authoring limit")
	}
	refMap := map[string]any{"session_id": reference.SessionID, "artifact_id": reference.ArtifactID, "revision_ref": reference.RevisionRef}
	resp := map[string]any{
		"status":                "ok",
		"reference":             refMap,
		"artifact_v3_reference": refMap,
		"media_type":            "text/html",
		"content":               string(html),
		"entrypoint":            manifest.Entrypoint,
		"files_base64":          project,
		"manifest":              manifest,
		"parts":                 parts,
	}
	if reference.SessionID != callerSessionID {
		resp["copyable_next_calls"] = []map[string]any{
			{"action": "import", "artifact_v3_reference": refMap},
		}
	}
	return resp, nil
}

type directArtifactV3RevisionInput struct {
	SessionID, ArtifactID, RevisionRef string
}

func parseDirectArtifactV3RevisionInput(raw any) (directArtifactV3RevisionInput, error) {
	value, ok := raw.(map[string]any)
	if !ok {
		return directArtifactV3RevisionInput{}, errors.New("manage_artifact requires artifact_v3_reference")
	}
	for key := range value {
		if key != "session_id" && key != "artifact_id" && key != "revision_ref" {
			return directArtifactV3RevisionInput{}, fmt.Errorf("manage_artifact artifact_v3_reference contains unsupported field %q", key)
		}
	}
	out := directArtifactV3RevisionInput{SessionID: strings.TrimSpace(asString(value["session_id"])), ArtifactID: strings.TrimSpace(asString(value["artifact_id"])), RevisionRef: strings.TrimSpace(asString(value["revision_ref"]))}
	if out.SessionID == "" || out.ArtifactID == "" || !strings.HasPrefix(out.RevisionRef, "revision-") || len(strings.TrimPrefix(out.RevisionRef, "revision-")) != 40 {
		return directArtifactV3RevisionInput{}, errors.New("manage_artifact artifact_v3_reference requires complete session_id, artifact_id, and exact revision_ref")
	}
	commit := strings.TrimPrefix(out.RevisionRef, "revision-")
	if decoded, err := hex.DecodeString(commit); err != nil || len(decoded) != 20 || commit != strings.ToLower(commit) {
		return directArtifactV3RevisionInput{}, errors.New("manage_artifact requires an exact lowercase hexadecimal revision_ref")
	}
	return out, nil
}
