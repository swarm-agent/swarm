package client

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// WorkerReview is an exact, server-issued revision. A proposal title or a
// session's plan is never an authorization to accept a worker.
type WorkerReview struct {
	ProposalID string `json:"proposal_id"`
	Revision   uint64 `json:"revision"`
	Digest     string `json:"digest"`
}

type WorkerProposal struct {
	WorkerReview
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

func (c *API) GetWorkerReview(ctx context.Context, workspaceID, sessionID string) (WorkerProposal, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(sessionID) == "" {
		return WorkerProposal{}, errors.New("worker review scope required")
	}
	path := "/v3/automations/v2/review?workspace_id=" + url.QueryEscape(workspaceID) + "&session_id=" + url.QueryEscape(sessionID)
	var response struct {
		Proposal WorkerProposal `json:"proposal"`
	}
	if err := c.getJSON(ctx, path, &response, true); err != nil {
		return WorkerProposal{}, err
	}
	return response.Proposal, nil
}

func (c *API) DecideWorkerReview(ctx context.Context, workspaceID, sessionID string, review WorkerReview, accept bool) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(sessionID) == "" || review.ProposalID == "" || review.Revision == 0 || review.Digest == "" {
		return errors.New("exact worker review and scope required")
	}
	action, path := "decline_automation", "/v3/automations/v2/decline"
	if accept {
		action, path = "accept_automation", "/v3/automations/v2/accept"
	}
	var response struct {
		OK       bool            `json:"ok"`
		Declined bool            `json:"declined"`
		Record   *WorkerProposal `json:"record"`
	}
	if err := c.postJSON(ctx, path, map[string]any{"action": action, "workspace_id": workspaceID, "session_id": sessionID, "review": review}, &response, true); err != nil {
		return err
	}
	if accept && (response.Record == nil || response.Record.WorkerReview != review || response.Record.WorkspaceID != workspaceID || response.Record.SessionID != sessionID) {
		return errors.New("worker acceptance result unavailable; refresh review")
	}
	if !accept && (!response.OK || !response.Declined) {
		return errors.New("worker decline result unavailable; refresh review")
	}
	return nil
}
