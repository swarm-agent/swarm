package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
)

// ArtifactV3DraftHandle is a locator, never a capability. The repository must
// authenticate every lookup against the durable grant and producer binding.
type ArtifactV3DraftHandle struct {
	SessionID   string `json:"session_id"`
	ArtifactID  string `json:"artifact_id"`
	TurnID      string `json:"turn_id"`
	CandidateID string `json:"candidate_id"`
	GrantID     string `json:"grant_id"`
}

// ResolveArtifactV3DirectDraft must load the canonical durable grant, check
// account/user/owner session and the exact producer run, expiry and terminal
// state, and return the stored grant unchanged. Never construct a grant from
// handle fields, rebind a foreign run, or fall back to an in-memory run cache.
type ArtifactV3DirectDraftResolver interface {
	ResolveArtifactV3DirectDraft(context.Context, ArtifactV3AuthorPrincipal, ArtifactV3DraftHandle) (ArtifactV3AuthorGrant, error)
}

func directArtifactV3Handle(g ArtifactV3AuthorGrant) ArtifactV3DraftHandle {
	return ArtifactV3DraftHandle{SessionID: g.OwnerSessionID, ArtifactID: g.ArtifactID, TurnID: g.TurnID, CandidateID: g.CandidateID, GrantID: g.ID}
}

func directArtifactV3Retained(g ArtifactV3AuthorGrant, gate ArtifactV3AuthorGate) map[string]any {
	return map[string]any{"status": "fixing", "artifact_id": g.ArtifactID, "turn_id": g.TurnID, "candidate_id": g.CandidateID, "draft_handle": directArtifactV3Handle(g), "gate": gate, "message": "Source retained. Use author_v3 with this exact draft_handle: read_file, edit_file, build_preview, then finish_turn. Do not create another artifact or claim this draft is ready."}
}

func (r *Runtime) authorDirectArtifactV3Draft(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID string, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, errors.New("native artifact author service unavailable")
	}
	if err := requireOnlyArtifactV3Fields(args, "action", "draft_handle", "operation"); err != nil {
		return nil, err
	}
	raw, ok := args["draft_handle"].(map[string]any)
	if !ok {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if err := requireOnlyArtifactV3Fields(raw, "session_id", "artifact_id", "turn_id", "candidate_id", "grant_id"); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var handle ArtifactV3DraftHandle
	if err = json.Unmarshal(encoded, &handle); err != nil {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if handle.SessionID != principal.SessionID || handle.SessionID != scope.SessionID || handle.ArtifactID == "" || handle.TurnID == "" || handle.CandidateID == "" || handle.GrantID == "" || strings.TrimSpace(principal.RunID) == "" {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	operation, ok := args["operation"].(map[string]any)
	if !ok {
		return nil, ErrArtifactV3AuthorInvalid
	}
	// Primary incremental repairs preserve the server-derived Part manifest.
	// Layout/code edits may span the project, but cannot replace Part identity.
	switch mapString(operation, "action") {
	case artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete:
		if path.Clean(mapString(operation, "path")) == "swarm-artifact.json" || path.Clean(mapString(operation, "to_path")) == "swarm-artifact.json" {
			return nil, ErrArtifactV3AuthorLocked
		}
	}
	resolver, ok := r.artifactV3Author.repository.(ArtifactV3DirectDraftResolver)
	if !ok {
		return nil, errors.New("native artifact durable draft resolver unavailable")
	}
	p := ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: scope.SessionID, ProducerRunID: principal.RunID}
	grant, err := resolver.ResolveArtifactV3DirectDraft(ctx, p, handle)
	if err != nil {
		return nil, err
	}
	if directArtifactV3Handle(grant) != handle || grant.AccountScopeID != p.AccountScopeID || grant.UserID != p.UserID || grant.ProducerSessionID != p.ProducerSessionID || grant.ProducerRunID != p.ProducerRunID {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	ctx = WithArtifactV3AuthorRunContext(ctx, ArtifactV3AuthorRunContext{Grant: grant})
	output, err := r.executeArtifactV3Author(ctx, scope, callID, operation)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal([]byte(output), &result); err != nil {
		return nil, err
	}
	result["draft_handle"] = handle
	if mapString(operation, "action") == artifactV3ActionFinish {
		result["status"] = "awaiting_selection"
		if grant.Initial {
			result["status"] = "ready"
		}
		var finished ArtifactV3AuthorFinish
		body, err := json.Marshal(result["result"])
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &finished); err != nil {
			return nil, err
		}
		result["media_inspect_reference"] = map[string]any{"session_id": handle.SessionID, "artifact_id": handle.ArtifactID, "revision_ref": "revision-" + finished.Revision.CommitOID}
	}
	return result, nil
}

// ArtifactV3DraftResumeRequest contains only owned locators and CAS evidence.
// Empty ExpectedHead is explicit evidence for a draft with no published revision.
type ArtifactV3DraftResumeRequest struct {
	SessionID             string `json:"session_id"`
	ArtifactID            string `json:"artifact_id"`
	ExpectedSequence      uint64 `json:"expected_sequence"`
	ExpectedProjectionSeq uint64 `json:"expected_projection_seq"`
	ExpectedHead          string `json:"expected_head"`
}

type ArtifactV3DirectDraftResumer interface {
	ResumeArtifactV3DirectDraft(context.Context, ArtifactV3AuthorPrincipal, ArtifactV3DraftResumeRequest) (ArtifactV3AuthorGrant, error)
}

func (r *Runtime) resumeDirectArtifactV3Draft(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if err := requireOnlyArtifactV3Fields(args, "action", "resume_draft"); err != nil {
		return nil, err
	}
	raw, ok := args["resume_draft"].(map[string]any)
	if !ok {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if err := requireOnlyArtifactV3Fields(raw, "session_id", "artifact_id", "expected_sequence", "expected_projection_seq", "expected_head"); err != nil {
		return nil, err
	}
	if _, ok := raw["expected_head"].(string); !ok {
		return nil, ErrArtifactV3AuthorInvalid
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var request ArtifactV3DraftResumeRequest
	if json.Unmarshal(body, &request) != nil || request.ExpectedSequence == 0 || request.ExpectedProjectionSeq == 0 {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if request.SessionID != scope.SessionID || request.SessionID != principal.SessionID || principal.RunID == "" {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	resumer, ok := r.artifactV3Author.repository.(ArtifactV3DirectDraftResumer)
	if !ok {
		return nil, errors.New("native artifact durable draft resumer unavailable")
	}
	p := ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: scope.SessionID, ProducerRunID: principal.RunID}
	grant, err := resumer.ResumeArtifactV3DirectDraft(ctx, p, request)
	if err != nil {
		return nil, err
	}
	if grant.AccountScopeID != p.AccountScopeID || grant.UserID != p.UserID || grant.OwnerSessionID != request.SessionID || grant.ArtifactID != request.ArtifactID || grant.ProducerSessionID != p.ProducerSessionID || grant.ProducerRunID != p.ProducerRunID {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	return map[string]any{"status": "fixing", "draft_handle": directArtifactV3Handle(grant), "message": "Draft resumed in this run. Inspect the retained state first. If publication is already reserved, retry finish_turn without edits; otherwise read and repair the source, rebuild, then finish_turn. The old handle is invalid."}, nil
}

// Draft status exposes CAS locators, never the private grant or source envelope.
type ArtifactV3DirectDraftLocator interface {
	LocateArtifactV3DirectDraft(context.Context, ArtifactV3AuthorPrincipal, string) (ArtifactV3DraftResumeRequest, error)
}

func (r *Runtime) locateDirectArtifactV3Draft(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, args map[string]any) (map[string]any, error) {
	if r == nil || r.artifactV3Author == nil {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if err := requireOnlyArtifactV3Fields(args, "action", "artifact_id"); err != nil {
		return nil, err
	}
	locator, ok := r.artifactV3Author.repository.(ArtifactV3DirectDraftLocator)
	if !ok {
		return nil, ErrArtifactV3AuthorInvalid
	}
	request, err := locator.LocateArtifactV3DirectDraft(ctx, ArtifactV3AuthorPrincipal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ProducerSessionID: scope.SessionID, ProducerRunID: principal.RunID}, mapString(args, "artifact_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"resume_draft": request}, nil
}
