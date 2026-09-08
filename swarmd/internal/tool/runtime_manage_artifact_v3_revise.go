package tool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (r *Runtime) readDirectArtifactV3HTML(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("manage_artifact read_v3 requires the Artifact V3 author service")
	}
	for key := range args {
		if key != "action" && key != "artifact_v3_reference" {
			return nil, fmt.Errorf("manage_artifact read_v3 contains unsupported field %q", key)
		}
	}
	reference, err := parseDirectArtifactV3RevisionInput(args["artifact_v3_reference"])
	if err != nil {
		return nil, err
	}
	if reference.SessionID != strings.TrimSpace(principal.SessionID) || reference.SessionID != strings.TrimSpace(scope.SessionID) {
		return nil, errors.New("manage_artifact read_v3 reference does not belong to the current authenticated session")
	}
	reader, ok := r.artifactV3Author.repository.(ArtifactV3DirectRevisionReader)
	if !ok {
		return nil, errors.New("manage_artifact read_v3 requires native Artifact V3 revision reading")
	}
	project, parts, err := reader.ReadArtifactV3DirectRevision(ctx, principal.AccountScopeID, principal.UserID, reference.SessionID, reference.ArtifactID, reference.RevisionRef)
	if err != nil {
		return nil, err
	}
	manifest, err := directArtifactV3ProjectManifest(project)
	if err != nil { return nil, err }
	html := project[manifest.Entrypoint]
	if len(html) == 0 || len(html) > manageArtifactMaxCreateBytes {
		return nil, errors.New("manage_artifact read_v3 exact HTML is unavailable or exceeds the bounded authoring limit")
	}
	return map[string]any{"status": "ok", "reference": map[string]any{"session_id": reference.SessionID, "artifact_id": reference.ArtifactID, "revision_ref": reference.RevisionRef}, "media_type": "text/html", "content": string(html), "entrypoint": manifest.Entrypoint, "files_base64": project, "manifest": manifest, "parts": parts}, nil
}

type directArtifactV3Alternative struct {
	CandidateIndex int
	Content        string
}

func (r *Runtime) reviseDirectArtifactV3HTML(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any) (map[string]any, error) {
	return r.reviseDirectArtifactV3(ctx, scope, principal, callID, args, false)
}

func (r *Runtime) reviseDirectArtifactV3(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any, preflight bool) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("manage_artifact revise_v3 requires the Artifact V3 author service")
	}
	for key := range args {
		switch key {
		case "action", "artifact_v3_reference", "content", "target_part_ids", "turn_key", "candidate_index", "alternatives", "revision_intent", "native_parts":
		default:
			return nil, fmt.Errorf("manage_artifact revise_v3 contains unsupported field %q", key)
		}
	}
	if asString(args["action"]) == "begin_v3" {
		if err := requireOnlyArtifactV3Fields(args, "action", "artifact_v3_reference", "target_part_ids", "turn_key", "candidate_index", "revision_intent"); err != nil {
			return nil, err
		}
	}
	if raw, supplied := args["alternatives"]; supplied {
		if _, supplied := args["content"]; supplied {
			return nil, errors.New("manage_artifact revise_v3 alternatives cannot be combined with top-level content")
		}
		if _, supplied := args["candidate_index"]; supplied {
			return nil, errors.New("manage_artifact revise_v3 alternatives own their candidate_index values")
		}
		turnKey := strings.TrimSpace(asString(args["turn_key"]))
		if !validManagedArtifactStableID(turnKey) {
			return nil, errors.New("manage_artifact revise_v3 alternatives require a stable turn_key")
		}
		alternatives, err := parseDirectArtifactV3Alternatives(raw)
		if err != nil {
			return nil, err
		}
		for _, alternative := range alternatives {
			candidateArgs := make(map[string]any, len(args))
			for key, value := range args { if key != "alternatives" { candidateArgs[key] = value } }
			candidateArgs["content"], candidateArgs["candidate_index"] = alternative.Content, alternative.CandidateIndex
			if _, err := r.reviseDirectArtifactV3(ctx, scope, principal, callID, candidateArgs, true); err != nil { return nil, err }
		}
		results := make([]map[string]any, 0, len(alternatives))
		for _, alternative := range alternatives {
			candidateArgs := make(map[string]any, len(args)+1)
			for key, value := range args {
				if key != "alternatives" {
					candidateArgs[key] = value
				}
			}
			candidateArgs["content"] = alternative.Content
			candidateArgs["candidate_index"] = alternative.CandidateIndex
			candidate, err := r.reviseDirectArtifactV3HTML(ctx, scope, principal, callID, candidateArgs)
			if err != nil {
				candidate = map[string]any{"status": "failed", "candidate_index": alternative.CandidateIndex, "diagnostic": err.Error()}
			}
			results = append(results, candidate)
		}
		status := "awaiting_selection"
		for _, result := range results {
			if result["status"] != "awaiting_selection" {
				status = "fixing"
			}
		}
		return map[string]any{
			"status": status, "turn_id": results[0]["turn_id"], "turn_key": turnKey,
			"candidate_count": len(results), "candidates": results,
			"message": "Requested alternatives retain individual status and draft handles beside the unchanged selected head. Repair fixing candidates before claiming readiness; selection remains a separate explicit user action.",
		}, nil
	}
	reference, err := parseDirectArtifactV3RevisionInput(args["artifact_v3_reference"])
	if err != nil {
		return nil, err
	}
	if reference.SessionID != strings.TrimSpace(principal.SessionID) || reference.SessionID != strings.TrimSpace(scope.SessionID) {
		return nil, errors.New("manage_artifact revise_v3 reference does not belong to the current authenticated session")
	}
	beginOnly := asString(args["action"]) == "begin_v3"
	body, ok := args["content"].(string)
	if !beginOnly && (!ok || strings.TrimSpace(body) == "") {
		return nil, errors.New("manage_artifact revise_v3 requires the complete corrected UTF-8 HTML content")
	}
	intent := asString(args["revision_intent"])
	var requestedTargets []string
	if intent != pebblestore.ArtifactV3RevisionWholeProject || args["target_part_ids"] != nil {
		requestedTargets, err = parseDirectArtifactV3TargetIDs(args["target_part_ids"])
	}
	if err == nil { err = pebblestore.ValidateArtifactV3RevisionIntent(intent, requestedTargets) }
	if err != nil {
		return nil, err
	}
	reader, ok := r.artifactV3Author.repository.(ArtifactV3DirectRevisionReader)
	if !ok {
		return nil, errors.New("manage_artifact revise_v3 requires native Artifact V3 revision reading")
	}
	baseProject, baseParts, err := reader.ReadArtifactV3DirectRevision(ctx, principal.AccountScopeID, principal.UserID, reference.SessionID, reference.ArtifactID, reference.RevisionRef)
	if err != nil {
		return nil, err
	}
	manifest, err := directArtifactV3ProjectManifest(baseProject)
	if err != nil { return nil, err }
	if !beginOnly && (len(body) > manageArtifactMaxCreateBytes || !utf8.ValidString(body)) { return nil, ErrArtifactV3AuthorQuota }
	if beginOnly {
		if _, supplied := args["content"]; supplied {
			return nil, ErrArtifactV3AuthorInvalid
		}
		body = string(baseProject[manifest.Entrypoint])
	}
	declared := make(map[string]bool, len(baseParts))
	basePartIDs := make([]string, 0, len(baseParts))
	for _, part := range baseParts {
		id := strings.TrimSpace(part.ID)
		declared[id] = true
		basePartIDs = append(basePartIDs, id)
	}
	for _, id := range requestedTargets {
		if !declared[id] {
			return nil, fmt.Errorf("manage_artifact revise_v3 target Part %q is absent from the exact base revision", id)
		}
	}
	parts := deriveArtifactHTMLParts([]byte(body), "text/html")
	manifestParts := make([]pebblestore.ArtifactV3Part, 0, len(basePartIDs))
	baseByID := make(map[string]pebblestore.ArtifactV3Part, len(baseParts))
	for _, part := range baseParts {
		baseByID[strings.TrimSpace(part.ID)] = part
	}
	derived := make(map[string]pebblestore.SessionArtifactPart, len(parts))
	for _, part := range parts {
		derived[strings.TrimSpace(part.ID)] = part
	}
	for _, id := range basePartIDs {
		part, found := derived[id]
		basePart := baseByID[id]
		if beginOnly || intent == pebblestore.ArtifactV3RevisionWholeProject || basePart.Locator.Kind != "selector" || basePart.Locator.Path != manifest.Entrypoint {
			manifestParts = append(manifestParts, basePart)
			continue
		}
		if !found || strings.TrimSpace(part.Selector) == "" {
			return nil, fmt.Errorf("manage_artifact revise_v3 must preserve stable Part %q in the complete corrected HTML", id)
		}
		manifestParts = append(manifestParts, basePart)
	}
	if raw, supplied := args["native_parts"]; supplied {
		if intent != pebblestore.ArtifactV3RevisionWholeProject { return nil, ErrArtifactV3AuthorLocked }
		var err error
		manifestParts, err = parseArtifactV3NativeParts(raw)
		if err != nil { return nil, err }
	}
	manifest.Parts = manifestParts
	proposedManifest, err := json.Marshal(manifest)
	if err != nil { return nil, err }
	proposed := make(map[string][]byte, len(baseProject))
	for name, bytes := range baseProject { proposed[name] = bytes }
	proposed[manifest.Entrypoint] = []byte(body)
	proposed[pebblestore.ArtifactV3ManifestFilename] = proposedManifest
	if _, err := pebblestore.ValidateArtifactV3Project(pebblestore.ArtifactV3Project{Files: proposed}, pebblestore.ArtifactV3Limits{}); err != nil { return nil, err }
	if manifestBody, ok := baseProject[pebblestore.ArtifactV3ManifestFilename]; !ok || len(manifestBody) == 0 {
		return nil, errors.New("manage_artifact revise_v3 exact base has no manifest")
	}
	producerRunID := strings.TrimSpace(principal.RunID)
	if producerRunID == "" {
		return nil, errors.New("manage_artifact revise_v3 requires trusted provider run identity")
	}
	turnKey, candidateIndex, err := directArtifactV3RevisionIdentity(args, callID, producerRunID)
	if err != nil {
		return nil, err
	}
	baseCommit := strings.TrimPrefix(reference.RevisionRef, "revision-")
	var sourceSeq uint64
	if resolver, ok := r.artifactV3Author.repository.(ArtifactV3NativeDiscovery); ok {
		source, err := resolver.ResolveArtifactV3SelectedSource(ctx, principal.AccountScopeID, principal.UserID, reference.SessionID, reference.ArtifactID, baseCommit, 0)
		if err != nil { return nil, err }
		sourceSeq = source.ProjectionSeq
	}
	if preflight { return nil, nil }
	grant, err := r.artifactV3Author.PrepareTurn(ctx, ArtifactV3PrepareTurnRequest{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, OwnerSessionID: principal.SessionID,
		TaskCallID: "direct-revise:" + turnKey, Prompt: "Primary Swarm targeted Artifact V3 revision",
		ArtifactID: reference.ArtifactID, BaseCommitOID: baseCommit, PolicyRevision: "direct-primary-html-v1",
		ProjectionSeq: sourceSeq, RevisionIntent: intent, CandidateIndex: candidateIndex, Initial: false, TargetPartIDs: requestedTargets, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	grant.ProducerSessionID = strings.TrimSpace(scope.SessionID)
	grant.ProducerRunID = producerRunID
	ctx = WithArtifactV3AuthorRunContext(ctx, ArtifactV3AuthorRunContext{Grant: grant})
	author := ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: grant.ProducerSessionID, ProducerRunID: producerRunID}
	fail := func(code string, cause error) (map[string]any, error) {
		// Keep exact candidate identity and failure visible alongside successful siblings.
		return map[string]any{"status": "failed", "draft_handle": directArtifactV3Handle(grant), "artifact_id": grant.ArtifactID, "turn_id": grant.TurnID, "candidate_id": grant.CandidateID, "candidate_index": candidateIndex, "diagnostic_code": code, "diagnostic": cause.Error()}, nil
	}
	current, err := r.artifactV3Author.Read(ctx, author, grant, manifest.Entrypoint, 0, 0)
	if err != nil {
		return fail("html_read_failed", err)
	}
	if beginOnly {
		return map[string]any{"status": "editing", "draft_handle": directArtifactV3Handle(grant), "parts": baseParts, "manifest": manifest, "revision_intent": intent}, nil
	}
	if err := r.artifactV3Author.Edit(ctx, author, grant, manifest.Entrypoint, []byte(current.Content), []byte(body), false); err != nil {
		return fail("html_write_failed", err)
	}
	if _, supplied := args["native_parts"]; supplied {
		if err := r.artifactV3Author.ReconcileParts(ctx, author, grant, manifestParts); err != nil { return fail("parts_reconciliation_failed", err) }
	}
	gate, err := r.artifactV3Author.BuildPreview(ctx, author, grant)
	if err != nil {
		return fail("build_preview_failed", err)
	}
	if !gate.Ready {
		diagnostic := "the complete revised Artifact V3 HTML failed its build or browser preview gate"
		if len(gate.Diagnostics) != 0 && strings.TrimSpace(gate.Diagnostics[0].Message) != "" {
			diagnostic = strings.TrimSpace(gate.Diagnostics[0].Message)
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
	return map[string]any{
		"status": "awaiting_selection", "artifact_id": grant.ArtifactID, "turn_id": grant.TurnID,
		"candidate_id": grant.CandidateID, "base_revision_ref": reference.RevisionRef,
		"target_part_ids": requestedTargets, "turn_key": turnKey, "candidate_index": candidateIndex, "part_count": len(manifestParts), "parts": manifestParts,
		"candidate":               map[string]any{"commit_oid": finished.Revision.CommitOID, "tree_oid": finished.Revision.TreeOID, "revision_ref": "revision-" + finished.Revision.CommitOID},
		"media_inspect_reference": map[string]any{"session_id": principal.SessionID, "artifact_id": grant.ArtifactID, "revision_ref": "revision-" + finished.Revision.CommitOID},
		"message":                 "The exact-base native Artifact V3 candidate is ready beside the unchanged selected head. Inspect this candidate; selection remains a separate explicit user action.",
	}, nil
}

// Omitted single-candidate keys are derived from trusted execution identity, not
// constrained provider-specific call-ID spelling. Explicit sibling keys keep
// their existing grouping semantics; never normalize invalid caller input.
func directArtifactV3RevisionIdentity(args map[string]any, callID, runID string) (string, int, error) {
	turnKey := ""
	if raw, supplied := args["turn_key"]; supplied {
		value, ok := raw.(string)
		turnKey = strings.TrimSpace(value)
		if !ok || !validManagedArtifactStableID(turnKey) {
			return "", 0, errors.New("manage_artifact revise_v3 turn_key must be a stable ID when supplied")
		}
	} else {
		if strings.TrimSpace(callID) == "" || strings.TrimSpace(runID) == "" {
			return "", 0, errors.New("manage_artifact revise_v3 default turn_key requires trusted call and run identity")
		}
		digest := sha256.Sum256([]byte(runID + "\x00" + callID))
		turnKey = fmt.Sprintf("call-%x", digest)
	}
	candidateIndex := 1
	if raw, supplied := args["candidate_index"]; supplied {
		candidateIndex = directArtifactV3CandidateIndex(raw)
	}
	if candidateIndex < 1 || candidateIndex > 50 {
		return "", 0, errors.New("manage_artifact revise_v3 candidate_index must be an integer between 1 and 50")
	}
	return turnKey, candidateIndex, nil
}

func directArtifactV3CandidateIndex(raw any) int {
	switch value := raw.(type) {
	case float64:
		if value >= 1 && value <= 50 && value == float64(int(value)) {
			return int(value)
		}
	case int:
		if value >= 1 && value <= 50 {
			return value
		}
	case json.Number:
		if value, err := value.Int64(); err == nil && value >= 1 && value <= 50 {
			return int(value)
		}
	}
	return 0
}

func parseDirectArtifactV3RevisionInput(raw any) (directArtifactV3RevisionInput, error) {
	value, ok := raw.(map[string]any)
	if !ok {
		return directArtifactV3RevisionInput{}, errors.New("manage_artifact revise_v3 requires artifact_v3_reference")
	}
	for key := range value {
		if key != "session_id" && key != "artifact_id" && key != "revision_ref" {
			return directArtifactV3RevisionInput{}, fmt.Errorf("manage_artifact revise_v3 artifact_v3_reference contains unsupported field %q", key)
		}
	}
	out := directArtifactV3RevisionInput{SessionID: strings.TrimSpace(asString(value["session_id"])), ArtifactID: strings.TrimSpace(asString(value["artifact_id"])), RevisionRef: strings.TrimSpace(asString(value["revision_ref"]))}
	if out.SessionID == "" || out.ArtifactID == "" || !strings.HasPrefix(out.RevisionRef, "revision-") || len(strings.TrimPrefix(out.RevisionRef, "revision-")) != 40 {
		return directArtifactV3RevisionInput{}, errors.New("manage_artifact revise_v3 requires complete session_id, artifact_id, and exact revision_ref")
	}
	return out, nil
}

func parseDirectArtifactV3Alternatives(raw any) ([]directArtifactV3Alternative, error) {
	items, ok := raw.([]any)
	if !ok || len(items) < 2 || len(items) > 16 {
		return nil, errors.New("manage_artifact revise_v3 alternatives must contain 2 to 16 complete candidates")
	}
	out := make([]directArtifactV3Alternative, len(items))
	seen := make(map[int]bool, len(items))
	for position, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("manage_artifact revise_v3 alternatives must contain objects")
		}
		for key := range value {
			if key != "candidate_index" && key != "content" {
				return nil, fmt.Errorf("manage_artifact revise_v3 alternative contains unsupported field %q", key)
			}
		}
		candidateIndex := directArtifactV3CandidateIndex(value["candidate_index"])
		content, contentOK := value["content"].(string)
		if candidateIndex < 1 || candidateIndex > len(items) || seen[candidateIndex] || !contentOK || strings.TrimSpace(content) == "" || len(content) > manageArtifactMaxCreateBytes || !utf8.ValidString(content) {
			return nil, errors.New("manage_artifact revise_v3 alternatives require each candidate_index from 1 through the candidate count exactly once and non-empty complete HTML content")
		}
		seen[candidateIndex] = true
		out[position] = directArtifactV3Alternative{CandidateIndex: candidateIndex, Content: content}
	}
	return out, nil
}

func parseDirectArtifactV3TargetIDs(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 256 {
		return nil, errors.New("manage_artifact revise_v3 requires a bounded non-empty target_part_ids array")
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(asString(item))
		if !validManagedArtifactStableID(id) || seen[id] {
			return nil, errors.New("manage_artifact revise_v3 target_part_ids must contain distinct stable Part IDs")
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}
