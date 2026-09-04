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
	Result map[string]any
}

// createDirectArtifactV3HTML is the ordinary primary-Swarm creation boundary.
// It converts one complete authored HTML document into a conventional V3 project,
// then uses the same context-bound build, browser-preview, Git, and projection
// path as managed whole-project authoring. It never writes a V1/V2 artifact.
func (r *Runtime) createDirectArtifactV3HTML(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("manage_artifact create requires the Artifact V3 author service")
	}
	for key := range args {
		switch key {
		case "action", "collection_name", "collection_description", "filename", "media_type", "content", "presentation", "parts":
		default:
			return nil, fmt.Errorf("manage_artifact create for Artifact V3 HTML contains unsupported field %q", key)
		}
	}
	mediaType := canonicalArtifactMediaType(asString(args["media_type"]))
	filename := strings.TrimSpace(asString(args["filename"]))
	if mediaType == "" && strings.HasSuffix(strings.ToLower(filename), ".html") {
		mediaType = "text/html"
	}
	if mediaType != "text/html" {
		return nil, errors.New("manage_artifact create currently accepts only one complete text/html Artifact V3 document")
	}
	body, ok := args["content"].(string)
	if !ok || strings.TrimSpace(body) == "" {
		return nil, errors.New("manage_artifact create requires non-empty UTF-8 HTML content")
	}
	parts := deriveArtifactHTMLParts([]byte(body), mediaType)
	requestedParts, err := parseArtifactParts(args["parts"])
	if err != nil {
		return nil, err
	}
	if len(requestedParts) != 0 {
		derivedByID := make(map[string]pebblestore.SessionArtifactPart, len(parts))
		for _, part := range parts {
			derivedByID[strings.TrimSpace(part.ID)] = part
		}
		for _, requested := range requestedParts {
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
			parts = append(parts, derived)
		}
	}
	manifestParts := make([]pebblestore.ArtifactV3Part, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Kind) != "selector" || strings.TrimSpace(part.Selector) == "" {
			continue
		}
		manifestParts = append(manifestParts, pebblestore.ArtifactV3Part{
			ID:    strings.TrimSpace(part.ID),
			Label: strings.TrimSpace(part.Label),
			Locator: pebblestore.ArtifactV3Locator{
				Kind: "selector", Path: "index.html", Value: strings.TrimSpace(part.Selector),
			},
		})
	}
	if len(manifestParts) == 0 {
		return nil, errors.New("manage_artifact create requires at least one stable HTML region id on header, main, section, article, nav, aside, or footer")
	}
	manifest, err := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: manifestParts})
	if err != nil {
		return nil, err
	}
	producerRunID := strings.TrimSpace(principal.RunID)
	if producerRunID == "" {
		return nil, errors.New("manage_artifact create requires trusted provider run identity")
	}
	r.directArtifactV3Mu.Lock()
	if existing, ok := r.directArtifactV3ByRun[producerRunID]; ok {
		result := cloneDirectArtifactV3Result(existing.Result)
		r.directArtifactV3Mu.Unlock()
		return result, nil
	}
	r.directArtifactV3Mu.Unlock()
	prompt := strings.TrimSpace(firstNonEmptyString(asString(args["collection_description"]), asString(args["collection_name"]), filename))
	grant, err := r.artifactV3Author.PrepareTurn(ctx, ArtifactV3PrepareTurnRequest{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		OwnerSessionID: principal.SessionID,
		TaskCallID:     "direct-create:" + strings.TrimSpace(callID),
		Prompt:         prompt,
		PolicyRevision: "direct-primary-html-v1",
		CandidateIndex: 1,
		Initial:        true,
		ExpiresAt:      time.Now().Add(15 * time.Minute).UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	grant.ProducerSessionID = strings.TrimSpace(scope.SessionID)
	grant.ProducerRunID = producerRunID
	author := ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: grant.ProducerSessionID, ProducerRunID: producerRunID}
	fail := func(code string, cause error) (map[string]any, error) {
		_ = r.artifactV3Author.MarkFailed(ctx, grant, code, cause.Error())
		_ = r.artifactV3Author.Discard(grant)
		return nil, cause
	}
	if err := r.artifactV3Author.Create(ctx, author, grant, pebblestore.ArtifactV3ManifestFilename, manifest); err != nil {
		return fail("manifest_create_failed", err)
	}
	if err := r.artifactV3Author.Create(ctx, author, grant, "index.html", []byte(body)); err != nil {
		return fail("html_create_failed", err)
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
		return fail("build_preview_not_ready", errors.New(diagnostic))
	}
	finished, err := r.artifactV3Author.Finish(ctx, author, grant)
	if err != nil {
		return fail("finish_failed", err)
	}
	_ = r.artifactV3Author.Discard(grant)
	reference := map[string]any{
		"session_id":   principal.SessionID,
		"artifact_id":  grant.ArtifactID,
		"commit_oid":   finished.Revision.CommitOID,
		"revision_ref": "revision-" + finished.Revision.CommitOID,
	}
	inspectionReference := map[string]any{
		"session_id": principal.SessionID, "artifact_id": grant.ArtifactID,
		"revision_ref": "revision-" + finished.Revision.CommitOID,
	}
	result := map[string]any{
		"status":                  "ready",
		"artifact_id":             grant.ArtifactID,
		"turn_id":                 grant.TurnID,
		"candidate_id":            grant.CandidateID,
		"commit_oid":              finished.Revision.CommitOID,
		"tree_oid":                finished.Revision.TreeOID,
		"part_count":              len(manifestParts),
		"parts":                   manifestParts,
		"reference":               reference,
		"media_inspect_reference": inspectionReference,
	}
	r.directArtifactV3Mu.Lock()
	if r.directArtifactV3ByRun == nil {
		r.directArtifactV3ByRun = make(map[string]directArtifactV3Publication)
	}
	if existing, ok := r.directArtifactV3ByRun[producerRunID]; ok {
		result = cloneDirectArtifactV3Result(existing.Result)
	} else {
		r.directArtifactV3ByRun[producerRunID] = directArtifactV3Publication{Result: cloneDirectArtifactV3Result(result)}
	}
	r.directArtifactV3Mu.Unlock()
	return result, nil
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
