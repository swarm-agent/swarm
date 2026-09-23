package pebblestore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cockroachdb/pebble"
)

// AutomationV2PermissionID is stable across pending revisions. The permission is
// a review resource, not a suspended executable tool call or a bearer grant.
func AutomationV2PermissionID(proposalID string) string { return "permission_" + proposalID }

func (s *SessionStore) setAutomationV2PermissionInBatch(batch *pebble.Batch, m *automationV2Mutation) error {
	p := m.proposal
	ps := NewPermissionStore(s.store)
	previous, found, err := ps.GetPermission(p.SessionID, AutomationV2PermissionID(p.ProposalID))
	if err != nil {
		return err
	}
	consequence := "Accept worker creates and activates this worker. First execution follows the schedule; no immediate one-shot run."
	var accepted AutomationV2Record
	if exists, readErr := s.store.GetJSON(automationV2Key("accepted", p.AccountID, p.SessionID), &accepted); readErr != nil {
		return readErr
	} else if exists {
		consequence = "Accept worker replaces future instructions and schedule on the existing worker and activates the accepted revision. Already admitted work keeps its original snapshot; no immediate run."
	}
	payload, err := json.Marshal(map[string]any{
		"path_id": "permission.automation-v2-plan.v2", "review_kind": "worker_v2", "action": "propose",
		"title": "Worker review", "document": p.Document, "proposal_revision": p.Revision,
		"worker_review": p.AutomationV2Review, "scope": map[string]string{"account_id": p.AccountID, "workspace_id": p.WorkspaceID},
		"acceptance_consequence": consequence,
		"acceptance":             map[string]any{"method": "POST", "path": "/v3/automations/v2/accept", "body": map[string]any{"action": "accept_automation", "workspace_id": p.WorkspaceID, "session_id": p.SessionID, "review": p.AutomationV2Review}},
	})
	if err != nil {
		return err
	}
	record := PermissionRecord{ID: AutomationV2PermissionID(p.ProposalID), SessionID: p.SessionID, ToolName: "manage_workers", ToolArguments: string(payload), ProposalRevision: int64(p.Revision), Requirement: "automation_v2_acceptance", Mode: "plan", Status: PermissionStatusPending, ExecutionStatus: PermissionExecWaitingApproval, CreatedAt: p.CreatedAt, UpdatedAt: p.CreatedAt, PermissionRequested: p.CreatedAt}
	var prior *PermissionRecord
	if found {
		prior = &previous
		if previous.Requirement != "automation_v2_acceptance" || (previous.Status != PermissionStatusPending && (m.accept || m.decline || previous.Status != PermissionStatusApproved || int64(p.Revision) <= previous.ProposalRevision)) {
			return ErrAutomationV2Conflict
		}
		record.CreatedAt = previous.CreatedAt
		record.PermissionRequested = previous.PermissionRequested
	}
	if m.accept {
		if !found || previous.ProposalRevision != int64(p.Revision) {
			return ErrAutomationV2Conflict
		}
		record.Status, record.Decision, record.ExecutionStatus = PermissionStatusApproved, "accept_automation", PermissionExecCompleted
		record.ResolvedAt, record.CompletedAt, record.UpdatedAt = m.record.AcceptedAt, m.record.AcceptedAt, m.record.AcceptedAt
	}
	if m.decline {
		if !found || previous.ProposalRevision != int64(p.Revision) {
			return ErrAutomationV2Conflict
		}
		now := time.Now().UnixMilli()
		record.Status, record.Decision, record.ExecutionStatus = PermissionStatusDenied, "decline_automation", PermissionExecCompleted
		record.ResolvedAt, record.CompletedAt, record.UpdatedAt = now, now, now
	}
	pending, err := ps.ListPendingPermissions(p.SessionID, 1001)
	if err != nil {
		return err
	}
	if len(pending) > 1000 {
		return errors.New("too many pending permissions for automation review")
	}
	summary := PermissionSummary{AccountScopeID: p.AccountID, PrincipalID: p.UserID, SessionID: p.SessionID, UpdatedAt: record.UpdatedAt}
	add := func(r PermissionRecord) {
		if r.Status != PermissionStatusPending {
			return
		}
		summary.PendingCount++
		if summary.OldestPendingAt == 0 || r.CreatedAt < summary.OldestPendingAt {
			summary.OldestPendingAt = r.CreatedAt
		}
		if r.CreatedAt > summary.NewestPendingAt {
			summary.NewestPendingAt = r.CreatedAt
		}
	}
	for _, r := range pending {
		if r.ID != record.ID {
			add(r)
		}
	}
	add(record)
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := putPermissionRecordInBatch(batch, record, prior, raw); err != nil {
		return err
	}
	previousSummary, ok, err := ps.GetSummary(p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	rawSummary, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return putPermissionSummaryInBatch(batch, summary, rawSummary, previousSummary, ok)
}
