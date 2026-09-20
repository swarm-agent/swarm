package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ArtifactV3NativeDiscovery is read-only and returns exact selected Git identity.
type ArtifactV3NativeDiscovery interface {
	ResolveArtifactV3SelectedSource(context.Context, string, string, string, string, string, uint64) (pebblestore.ArtifactV3SelectedSource, error)
	ListArtifactV3SelectedSources(context.Context, string, string, string, int) ([]pebblestore.ArtifactV3SelectedSource, error)
}

// ArtifactV3NativeCatalogSearcher searches the retained native Artifact V3 catalog across sessions.
type ArtifactV3NativeCatalogSearcher interface {
	SearchArtifactV3Catalog(ctx context.Context, accountScopeID, userID string, options pebblestore.ArtifactV3CatalogOptions) (pebblestore.ArtifactV3CatalogPage, error)
}

// ArtifactV3RetainedSourceResolver verifies exact retained sources without touching selection state.
type ArtifactV3RetainedSourceResolver interface {
	ResolveArtifactV3RetainedSource(context.Context, string, string, string, string, string, uint64) (pebblestore.ArtifactV3SelectedSource, error)
}

// ArtifactV3RetainedSourceReader reads project files and parts from a retained source artifact.
type ArtifactV3RetainedSourceReader interface {
	ReadArtifactV3RetainedRevision(ctx context.Context, accountScopeID, userID, sourceSessionID, artifactID, revisionRef string) (map[string][]byte, []pebblestore.ArtifactV3Part, error)
}

// ArtifactV3NativeImporter imports an exact retained Artifact V3 version into a destination session as an editable head.
type ArtifactV3NativeImporter interface {
	ImportArtifactV3(ctx context.Context, input pebblestore.ArtifactV3ImportInput) (pebblestore.ArtifactV3Projection, error)
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
	action := asString(args["action"])
	if action == "list_v3" {
		if err := requireOnlyArtifactV3Fields(args, "action", "limit", "cursor", "query", "status", "source_kind", "media_type", "session_id", "artifact_id", "created_after", "created_before"); err != nil {
			return nil, fmt.Errorf("manage_artifact list_v3 contains unsupported field: %w", err)
		}
		limit := clampInt(asInt(args["limit"], pebblestore.ArtifactV3CatalogDefaultLimit), 1, pebblestore.ArtifactV3CatalogMaxLimit)
		cursor := strings.TrimSpace(asString(args["cursor"]))
		query := strings.TrimSpace(asString(args["query"]))
		status := strings.TrimSpace(asString(args["status"]))
		sourceKind := strings.TrimSpace(asString(args["source_kind"]))
		mediaType := strings.TrimSpace(asString(args["media_type"]))
		sessionFilter := strings.TrimSpace(asString(args["session_id"]))
		artifactFilter := strings.TrimSpace(asString(args["artifact_id"]))
		createdAfter, _, err := optionalArtifactInt64(args, "created_after")
		if err != nil {
			return nil, err
		}
		createdBefore, _, err := optionalArtifactInt64(args, "created_before")
		if err != nil {
			return nil, err
		}
		options := pebblestore.ArtifactV3CatalogOptions{
			Query:         query,
			Status:        status,
			SourceKind:    sourceKind,
			MediaType:     mediaType,
			SessionID:     sessionFilter,
			ArtifactID:    artifactFilter,
			CreatedAfter:  createdAfter,
			CreatedBefore: createdBefore,
			Limit:         limit,
			Cursor:        cursor,
		}
		if searcher, ok := r.artifactV3Author.repository.(ArtifactV3NativeCatalogSearcher); ok {
			page, err := searcher.SearchArtifactV3Catalog(ctx, principal.AccountScopeID, principal.UserID, options)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(page.Items))
			for _, item := range page.Items {
				refMap := map[string]any{
					"session_id":   item.SessionID,
					"artifact_id":  item.ArtifactID,
					"revision_ref": item.RevisionRef,
				}
				itemMap := map[string]any{
					"artifact_id":           item.ArtifactID,
					"session_id":            item.SessionID,
					"commit_oid":            item.CommitOID,
					"revision_ref":          item.RevisionRef,
					"source_kind":           item.SourceKind,
					"turn_id":               item.TurnID,
					"candidate_id":          item.CandidateID,
					"intent":                item.Intent,
					"file_count":            item.FileCount,
					"tree_bytes":            item.TreeBytes,
					"status":                item.Status,
					"media_type":            item.MediaType,
					"entrypoint":            item.Entrypoint,
					"parts":                 item.Parts,
					"created_at":            item.CreatedAt,
					"reference":             refMap,
					"artifact_v3_reference": refMap,
				}
				if item.SessionID == principal.SessionID {
					itemMap["copyable_next_calls"] = []map[string]any{
						{"action": "read_v3", "artifact_v3_reference": refMap},
						{"action": "source_v3", "artifact_v3_reference": refMap},
					}
				} else {
					itemMap["copyable_next_calls"] = []map[string]any{
						{"action": "read_v3", "artifact_v3_reference": refMap},
						{"action": "import", "artifact_v3_reference": refMap},
					}
				}
				items = append(items, itemMap)
			}
			resp := map[string]any{
				"artifacts": items,
				"count":     len(items),
				"limit":     limit,
				"has_more":  page.HasMore || page.NextCursor != "",
			}
			if page.NextCursor != "" {
				resp["next_cursor"] = page.NextCursor
			}
			return resp, nil
		}
		return nil, errors.New("native Artifact V3 retained catalog unavailable")
	}

	// source_v3
	if err := requireOnlyArtifactV3Fields(args, "action", "artifact_id", "session_id", "artifact_v3_reference"); err != nil {
		return nil, fmt.Errorf("manage_artifact source_v3 contains unsupported field: %w", err)
	}
	var id, targetSessionID, commitOID string
	var err error
	if refRaw, supplied := args["artifact_v3_reference"]; supplied {
		if err := requireOnlyArtifactV3Fields(args, "action", "artifact_v3_reference"); err != nil {
			return nil, err
		}
		ref, err := parseDirectArtifactV3RevisionInput(refRaw)
		if err != nil {
			return nil, err
		}
		id, targetSessionID = ref.ArtifactID, ref.SessionID
		commitOID = strings.TrimPrefix(ref.RevisionRef, "revision-")
	}
	if id == "" {
		id = strings.TrimSpace(asString(args["artifact_id"]))
	}
	if targetSessionID == "" {
		targetSessionID = strings.TrimSpace(asString(args["session_id"]))
	}
	if targetSessionID == "" {
		targetSessionID = principal.SessionID
	}
	if id == "" {
		return nil, ErrArtifactV3AuthorInvalid
	}
	var source pebblestore.ArtifactV3SelectedSource
	if targetSessionID != principal.SessionID {
		resolver, ok := r.artifactV3Author.repository.(ArtifactV3RetainedSourceResolver)
		if !ok {
			return nil, errors.New("native Artifact V3 retained resolution unavailable")
		}
		source, err = resolver.ResolveArtifactV3RetainedSource(ctx, principal.AccountScopeID, principal.UserID, targetSessionID, id, commitOID, 0)
	} else {
		discovery, ok := r.artifactV3Author.repository.(ArtifactV3NativeDiscovery)
		if !ok {
			return nil, errors.New("native Artifact V3 discovery unavailable")
		}
		source, err = discovery.ResolveArtifactV3SelectedSource(ctx, principal.AccountScopeID, principal.UserID, targetSessionID, id, commitOID, 0)
	}
	if err != nil {
		return nil, err
	}
	refMap := map[string]any{"session_id": source.SessionID, "artifact_id": source.ArtifactID, "revision_ref": source.RevisionRef}
	result := map[string]any{
		"artifact_v3_source": map[string]any{
			"session_id":     source.SessionID,
			"artifact_id":    source.ArtifactID,
			"commit_oid":     source.CommitOID,
			"projection_seq": source.ProjectionSeq,
		},
		"reference":             refMap,
		"artifact_v3_reference": refMap,
		"parts":                 source.Revision.Parts,
		"turns":                 source.Turns,
		"candidates":            source.Candidates,
	}
	if source.SessionID == principal.SessionID {
		result["selection_calls"] = artifactV3SelectionCalls(source)
	} else {
		result["copyable_next_calls"] = []map[string]any{
			{"action": "read_v3", "artifact_v3_reference": refMap},
			{"action": "import", "artifact_v3_reference": refMap},
		}
	}
	return result, nil
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
