package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ArtifactV3NativeDiscovery is read-only and returns exact selected Git identity.
type ArtifactV3NativeDiscovery interface {
	ResolveArtifactV3SelectedSource(context.Context, string, string, string, string, string, uint64) (pebblestore.ArtifactV3SelectedSource, error)
	ListArtifactV3SelectedSources(context.Context, string, string, string, int) ([]pebblestore.ArtifactV3SelectedSource, error)
}

func directArtifactV3ProjectManifest(project map[string][]byte) (pebblestore.ArtifactV3Manifest, error) {
	var manifest pebblestore.ArtifactV3Manifest
	total := 0
	for _, body := range project {
		total += len(body)
		if total > manageArtifactMaxCreateBytes {
			return manifest, ErrArtifactV3AuthorQuota
		}
	}
	if json.Unmarshal(project[pebblestore.ArtifactV3ManifestFilename], &manifest) != nil || manifest.Entrypoint == "" || len(project[manifest.Entrypoint]) == 0 {
		return manifest, ErrArtifactV3AuthorInvalid
	}
	return manifest, nil
}

func (r *Runtime) discoverDirectArtifactV3(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if r.artifactV3Author == nil || scope.SessionID != principal.SessionID {
		return nil, ErrArtifactV3AuthorInvalid
	}
	discovery, ok := r.artifactV3Author.repository.(ArtifactV3NativeDiscovery)
	if !ok {
		return nil, errors.New("native Artifact V3 discovery unavailable")
	}
	if err := requireOnlyArtifactV3Fields(args, "action", "artifact_id"); err != nil {
		return nil, fmt.Errorf("%w: native discovery is session-bound; source_v3 accepts only action and artifact_id, list_v3 needs only action; omit session_id and artifact_v3_reference", err)
	}
	if asString(args["action"]) == "list_v3" {
		sources, err := discovery.ListArtifactV3SelectedSources(ctx, principal.AccountScopeID, principal.UserID, principal.SessionID, 50)
		return map[string]any{"sources": sources, "limit": 50}, err
	}
	id := asString(args["artifact_id"])
	if id == "" {
		return nil, ErrArtifactV3AuthorInvalid
	}
	source, err := discovery.ResolveArtifactV3SelectedSource(ctx, principal.AccountScopeID, principal.UserID, principal.SessionID, id, "", 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact_v3_source": map[string]any{"session_id": source.SessionID, "artifact_id": source.ArtifactID, "commit_oid": source.CommitOID, "projection_seq": source.ProjectionSeq}, "reference": map[string]any{"session_id": source.SessionID, "artifact_id": source.ArtifactID, "revision_ref": source.RevisionRef}, "parts": source.Revision.Parts, "turns": source.Turns, "candidates": source.Candidates, "selection_calls": artifactV3SelectionCalls(source)}, nil
}

// Selection deliberately requires caller-supplied CAS; discovery never selects.
type ArtifactV3NativeSelector interface {
	SelectArtifactV3Exact(context.Context, string, string, string, string, string, string, string, string, uint64) (ArtifactV3Revision, error)
}

func (r *Runtime) selectDirectArtifactV3(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any) (map[string]any, error) {
	if r.artifactV3Author == nil || scope.SessionID != principal.SessionID {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	if err := requireOnlyArtifactV3Fields(args, "action", "artifact_id", "turn_id", "candidate_id", "expected_head", "expected_turn_revision"); err != nil {
		return nil, err
	}
	id, turn, candidate, head := asString(args["artifact_id"]), asString(args["turn_id"]), asString(args["candidate_id"]), asString(args["expected_head"])
	seq, supplied, err := optionalArtifactInt64(args, "expected_turn_revision")
	if err != nil || !supplied || seq <= 0 || id == "" || turn == "" || candidate == "" || head == "" {
		return nil, fmt.Errorf("%w: select_v3 requires artifact_id, turn_id, candidate_id, expected_head and positive expected_turn_revision from a fresh source_v3 response", ErrArtifactV3AuthorInvalid)
	}
	selector, ok := r.artifactV3Author.repository.(ArtifactV3NativeSelector)
	if !ok {
		return nil, errors.New("native Artifact V3 selection unavailable")
	}
	revision, err := selector.SelectArtifactV3Exact(ctx, principal.AccountScopeID, principal.UserID, principal.SessionID, id, turn, candidate, head, callID, uint64(seq))
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "selected", "reference": map[string]any{"session_id": principal.SessionID, "artifact_id": id, "revision_ref": "revision-" + revision.CommitOID}}, nil
}

// Exact copyable arguments are read-only evidence, not consent or selection.
func artifactV3SelectionCalls(source pebblestore.ArtifactV3SelectedSource) []map[string]any {
	calls := []map[string]any{}
	for _, candidate := range source.Candidates {
		if candidate.Status != "ready" {
			continue
		}
		for _, turn := range source.Turns {
			if turn.TurnID != candidate.TurnID || turn.Status != "awaiting_selection" || turn.EventSeq == 0 {
				continue
			}
			calls = append(calls, map[string]any{"action": "select_v3", "artifact_id": source.ArtifactID, "turn_id": turn.TurnID, "candidate_id": candidate.CandidateID, "expected_head": source.CommitOID, "expected_turn_revision": turn.EventSeq})
			break
		}
	}
	return calls
}
