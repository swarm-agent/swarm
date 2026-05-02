package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const flowPeerApplyPath = "/v1/swarm/peer/flows/apply"

type flowAssignmentApplyResponse struct {
	OK       bool               `json:"ok"`
	Ack      flow.AssignmentAck `json:"ack"`
	Inserted bool               `json:"inserted"`
}

type flowAssignmentDeliverResult struct {
	Outbox          pebblestore.FlowOutboxCommandRecord    `json:"outbox"`
	AssignmentState pebblestore.FlowAssignmentStatusRecord `json:"assignment_state"`
	Ack             flow.AssignmentAck                     `json:"ack,omitempty"`
	Delivered       bool                                   `json:"delivered"`
	PendingSync     bool                                   `json:"pending_sync"`
}

func (s *Server) SetFlowStore(flowStore *pebblestore.FlowStore) {
	if s == nil {
		return
	}
	s.flows = flowStore
}

func (s *Server) handlePeerFlowApply(w http.ResponseWriter, r *http.Request) {
	peerSwarmID, _ := extractPeerAuth(r)
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.flows == nil {
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	var command flow.AssignmentCommand
	if err := decodeJSON(r, &command); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			var fallback map[string]any
			if fallbackErr := decodeLenientJSON(r, &fallback); fallbackErr == nil {
				writeError(w, http.StatusConflict, fmt.Errorf("peer flow protocol mismatch: %w", err))
				return
			}
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	targetSwarmID := s.flowPeerApplyTargetSwarmID(command)
	flowRouteDiagLog("peer_apply_received",
		"peer_header_swarm_id", peerSwarmID,
		"flow_id", command.FlowID,
		"assignment_flow_id", command.Assignment.FlowID,
		"command_id", command.CommandID,
		"action", command.Action,
		"apply_target_swarm_id", targetSwarmID,
		"command_assignment_target_swarm_id", command.Assignment.Target.SwarmID,
		"command_assignment_target_kind", command.Assignment.Target.Kind,
		"command_assignment_target_deployment_id", command.Assignment.Target.DeploymentID,
		"command_assignment_target_name", command.Assignment.Target.Name,
	)
	now := time.Now().UTC()
	var (
		ack      flow.AssignmentAck
		inserted bool
		err      error
	)
	if normalizeAPIFlowAssignmentCommand(command).Action == flow.CommandRunNow {
		command.Assignment.Target.SwarmID = targetSwarmID
		ack, inserted, err = s.applyFlowRunNowCommand(r.Context(), command, now)
	} else {
		ack, inserted, err = s.flows.ApplyTargetAssignmentCommand(command, targetSwarmID, now)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, flowAssignmentApplyResponse{OK: true, Ack: ack, Inserted: inserted})
}

func (s *Server) EnqueueFlowAssignmentCommand(ctx context.Context, command flow.AssignmentCommand) (pebblestore.FlowOutboxCommandRecord, error) {
	if s == nil || s.flows == nil {
		return pebblestore.FlowOutboxCommandRecord{}, errors.New("flow store is not configured")
	}
	target, resolved, err := s.resolveFlowAssignmentTarget(ctx, command.Assignment.Target)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	return s.enqueueFlowAssignmentCommandForTarget(command, target, resolved)
}

func (s *Server) EnqueueAndDeliverFlowAssignmentCommand(ctx context.Context, command flow.AssignmentCommand) (flowAssignmentDeliverResult, error) {
	if s == nil || s.flows == nil {
		return flowAssignmentDeliverResult{}, errors.New("flow store is not configured")
	}
	target, resolved, resolveErr := s.resolveFlowAssignmentTarget(ctx, command.Assignment.Target)
	if resolveErr != nil && strings.TrimSpace(resolved.SwarmID) == "" {
		return flowAssignmentDeliverResult{}, resolveErr
	}
	record, err := s.enqueueFlowAssignmentCommandForTarget(command, target, resolved)
	if err != nil {
		return flowAssignmentDeliverResult{}, err
	}
	return s.deliverFlowAssignmentOutboxCommand(ctx, record, target)
}

func (s *Server) DeliverPendingFlowAssignmentCommands(ctx context.Context, limit int) ([]flowAssignmentDeliverResult, error) {
	if s == nil || s.flows == nil {
		return nil, errors.New("flow store is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 100
	}
	pending, err := s.flows.ListOutboxCommands(pebblestore.FlowOutboxStatusPending, limit)
	if err != nil {
		return nil, err
	}
	results := make([]flowAssignmentDeliverResult, 0, len(pending))
	now := time.Now().UTC()
	for _, record := range pending {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if !record.NextAttemptAt.IsZero() && record.NextAttemptAt.After(now) {
			continue
		}
		target, resolved, err := s.resolveFlowAssignmentTarget(ctx, record.Target)
		if err != nil {
			updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetUnusable, err.Error(), nil)
			if updateErr != nil {
				return results, updateErr
			}
			results = append(results, flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true})
			continue
		}
		record.TargetSwarmID = resolved.SwarmID
		result, err := s.deliverFlowAssignmentOutboxCommand(ctx, record, target)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (s *Server) enqueueFlowAssignmentCommandForTarget(command flow.AssignmentCommand, target swarmTarget, resolved flow.ResolvedTarget) (pebblestore.FlowOutboxCommandRecord, error) {
	command = normalizeAPIFlowAssignmentCommand(command)
	command.Assignment.Workspace = s.flowWorkspaceForTarget(command.Assignment.Workspace, target, resolved)
	if err := command.ValidateIdempotencyKey(); err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	key := command.IdempotencyKey()
	now := time.Now().UTC()
	targetSelection := targetSelectionForOutbox(command, target, resolved)
	flowRouteDiagLog("controller_enqueue_assignment",
		"flow_id", command.FlowID,
		"assignment_flow_id", command.Assignment.FlowID,
		"command_id", command.CommandID,
		"action", command.Action,
		"resolved_swarm_id", resolved.SwarmID,
		"resolved_kind", resolved.Kind,
		"resolved_deployment_id", resolved.DeploymentID,
		"resolved_name", resolved.Name,
		"target_swarm_id", target.SwarmID,
		"target_kind", target.Kind,
		"target_deployment_id", target.DeploymentID,
		"target_name", target.Name,
		"target_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
		"target_selection_swarm_id", targetSelection.SwarmID,
		"target_selection_kind", targetSelection.Kind,
		"target_selection_deployment_id", targetSelection.DeploymentID,
		"target_selection_name", targetSelection.Name,
	)
	command.Assignment.Target = targetSelection
	record := pebblestore.FlowOutboxCommandRecord{
		CommandID:     key.CommandID,
		FlowID:        key.FlowID,
		Revision:      key.Revision,
		TargetSwarmID: resolved.SwarmID,
		Target:        targetSelection,
		Command:       command,
		Status:        pebblestore.FlowOutboxStatusPending,
		NextAttemptAt: now,
		CreatedAt:     now,
	}
	stored, err := s.flows.PutOutboxCommand(record, nil)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	_, err = s.flows.PutAssignmentStatus(pebblestore.FlowAssignmentStatusRecord{
		FlowID:          key.FlowID,
		TargetSwarmID:   resolved.SwarmID,
		Target:          targetSelection,
		CommandID:       key.CommandID,
		DesiredRevision: key.Revision,
		Status:          flow.AssignmentPendingSync,
		Reason:          "assignment command queued for target sync",
	})
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	return stored, nil
}

func (s *Server) applyFlowAssignmentCommandLocally(ctx context.Context, command flow.AssignmentCommand, targetSwarmID string) (flow.AssignmentAck, bool, error) {
	targetSwarmID = firstNonEmpty(strings.TrimSpace(targetSwarmID), s.flowPeerApplyTargetSwarmID(command))
	if normalizeAPIFlowAssignmentCommand(command).Action == flow.CommandRunNow {
		command.Assignment.Target.SwarmID = targetSwarmID
		return s.applyFlowRunNowCommand(ctx, command, time.Now().UTC())
	}
	return s.flows.ApplyTargetAssignmentCommand(command, targetSwarmID, time.Now().UTC())
}

func (s *Server) flowPeerApplyTargetSwarmID(command flow.AssignmentCommand) string {
	if localSwarmID := strings.TrimSpace(s.flowLocalSwarmID()); localSwarmID != "" {
		return localSwarmID
	}
	return strings.TrimSpace(command.Assignment.Target.SwarmID)
}

func (s *Server) deliverFlowAssignmentOutboxCommand(ctx context.Context, record pebblestore.FlowOutboxCommandRecord, target swarmTarget) (flowAssignmentDeliverResult, error) {
	if s == nil || s.flows == nil {
		return flowAssignmentDeliverResult{}, errors.New("flow store is not configured")
	}
	record = normalizeAPIFlowOutboxCommand(record)
	if strings.TrimSpace(target.SwarmID) == "" {
		target.SwarmID = strings.TrimSpace(record.TargetSwarmID)
	}
	if strings.EqualFold(strings.TrimSpace(target.Relationship), "self") || strings.EqualFold(strings.TrimSpace(target.Kind), "self") {
		ack, _, err := s.applyFlowAssignmentCommandLocally(ctx, record.Command, strings.TrimSpace(target.SwarmID))
		if err != nil {
			updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetUnusable, err.Error(), nil)
			if updateErr != nil {
				return flowAssignmentDeliverResult{}, updateErr
			}
			return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, nil
		}
		updated, state, err := s.applyFlowAssignmentAck(record, ack)
		if err != nil {
			return flowAssignmentDeliverResult{}, err
		}
		return flowAssignmentDeliverResult{
			Outbox:          updated,
			AssignmentState: state,
			Ack:             ack,
			Delivered:       ack.Status == flow.AssignmentAccepted || ack.Status == flow.AssignmentDuplicate,
			PendingSync:     state.PendingSync,
		}, nil
	}
	if strings.TrimSpace(target.BackendURL) == "" || !target.Online || !target.Selectable {
		reason := firstNonEmpty(strings.TrimSpace(target.LastError), "target is not currently reachable")
		updated, state, err := s.markFlowAssignmentPending(record, flow.AssignmentTargetOffline, reason, nil)
		return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, err
	}
	flowRouteDiagLog("controller_deliver_assignment",
		"flow_id", record.FlowID,
		"command_id", record.CommandID,
		"action", record.Command.Action,
		"record_target_swarm_id", record.TargetSwarmID,
		"record_target_kind", record.Target.Kind,
		"record_target_name", record.Target.Name,
		"target_swarm_id", target.SwarmID,
		"target_kind", target.Kind,
		"target_name", target.Name,
		"target_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
	)
	var resp flowAssignmentApplyResponse
	deliverErr := s.postPeerJSONToSwarmTarget(ctx, target, flowPeerApplyPath, record.Command, &resp)
	if deliverErr != nil {
		updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetOffline, deliverErr.Error(), nil)
		if updateErr != nil {
			return flowAssignmentDeliverResult{}, updateErr
		}
		return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, nil
	}
	ack := resp.Ack
	if ack.Status == "" {
		ack.Status = flow.AssignmentRejected
		ack.Reason = "target response did not include assignment status"
	}
	updated, state, err := s.applyFlowAssignmentAck(record, ack)
	if err != nil {
		return flowAssignmentDeliverResult{}, err
	}
	return flowAssignmentDeliverResult{
		Outbox:          updated,
		AssignmentState: state,
		Ack:             ack,
		Delivered:       ack.Status == flow.AssignmentAccepted || ack.Status == flow.AssignmentDuplicate,
		PendingSync:     state.PendingSync,
	}, nil
}

func (s *Server) applyFlowAssignmentAck(record pebblestore.FlowOutboxCommandRecord, ack flow.AssignmentAck) (pebblestore.FlowOutboxCommandRecord, pebblestore.FlowAssignmentStatusRecord, error) {
	previous := record
	if ack.Status == flow.AssignmentAccepted || ack.Status == flow.AssignmentDuplicate {
		record.Status = pebblestore.FlowOutboxStatusDelivered
		record.LastError = ""
	} else {
		record.Status = pebblestore.FlowOutboxStatusRejected
		record.LastError = strings.TrimSpace(ack.Reason)
	}
	record.LastAttemptAt = time.Now().UTC()
	record.AttemptCount++
	updated, err := s.flows.PutOutboxCommand(record, &previous)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	state := pebblestore.FlowAssignmentStatusRecord{
		FlowID:           record.FlowID,
		TargetSwarmID:    firstNonEmpty(strings.TrimSpace(ack.TargetSwarmID), record.TargetSwarmID),
		Target:           record.Target,
		CommandID:        record.CommandID,
		DesiredRevision:  record.Revision,
		AcceptedRevision: ack.AcceptedRevision,
		Status:           ack.Status,
		Reason:           strings.TrimSpace(ack.Reason),
		TargetClock:      ack.TargetClock,
	}
	storedState, err := s.flows.PutAssignmentStatus(state)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	return updated, storedState, nil
}

func (s *Server) markFlowAssignmentPending(record pebblestore.FlowOutboxCommandRecord, status flow.AssignmentStatus, reason string, previous *pebblestore.FlowOutboxCommandRecord) (pebblestore.FlowOutboxCommandRecord, pebblestore.FlowAssignmentStatusRecord, error) {
	if previous == nil {
		prev := record
		previous = &prev
	}
	record.Status = pebblestore.FlowOutboxStatusPending
	record.LastAttemptAt = time.Now().UTC()
	record.NextAttemptAt = record.LastAttemptAt.Add(flowOutboxRetryDelay(record.AttemptCount + 1))
	record.AttemptCount++
	record.LastError = strings.TrimSpace(reason)
	updated, err := s.flows.PutOutboxCommand(record, previous)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	state := pebblestore.FlowAssignmentStatusRecord{
		FlowID:          record.FlowID,
		TargetSwarmID:   record.TargetSwarmID,
		Target:          record.Target,
		CommandID:       record.CommandID,
		DesiredRevision: record.Revision,
		Status:          status,
		Reason:          strings.TrimSpace(reason),
	}
	storedState, err := s.flows.PutAssignmentStatus(state)
	if err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	return updated, storedState, nil
}

func (s *Server) resolveFlowAssignmentTarget(ctx context.Context, selection flow.TargetSelection) (swarmTarget, flow.ResolvedTarget, error) {
	_ = ctx
	selection = normalizeFlowTargetSelection(selection)
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/v1/swarm/targets", nil)
	if err != nil {
		return swarmTarget{}, flow.ResolvedTarget{}, err
	}
	targets, _, err := s.swarmTargetsForRequest(req)
	if err != nil {
		return swarmTarget{}, flow.ResolvedTarget{}, err
	}
	for _, candidate := range targets {
		if !flowTargetMatchesSelection(candidate, selection) {
			continue
		}
		resolved := resolvedFlowTarget(selection, candidate)
		if !candidate.Online || !candidate.Selectable || strings.TrimSpace(candidate.BackendURL) == "" && !strings.EqualFold(candidate.Relationship, "self") {
			return candidate, resolved, fmt.Errorf("target %q is not currently reachable", resolved.SwarmID)
		}
		return candidate, resolved, nil
	}
	return swarmTarget{}, flow.ResolvedTarget{}, fmt.Errorf("flow target %q was not found", selection.SwarmID)
}

func normalizeAPIFlowAssignmentCommand(command flow.AssignmentCommand) flow.AssignmentCommand {
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.FlowID = strings.TrimSpace(command.FlowID)
	if command.FlowID == "" {
		command.FlowID = strings.TrimSpace(command.Assignment.FlowID)
	}
	if command.Revision == 0 {
		command.Revision = command.Assignment.Revision
	}
	command.Action = flow.CommandAction(strings.TrimSpace(strings.ToLower(string(command.Action))))
	command.Assignment.FlowID = strings.TrimSpace(command.Assignment.FlowID)
	if command.Assignment.FlowID == "" {
		command.Assignment.FlowID = command.FlowID
	}
	if command.Assignment.Revision == 0 {
		command.Assignment.Revision = command.Revision
	}
	command.CreatedAt = command.CreatedAt.UTC()
	if command.CreatedAt.IsZero() {
		command.CreatedAt = time.Now().UTC()
	}
	return command
}

func normalizeAPIFlowOutboxCommand(record pebblestore.FlowOutboxCommandRecord) pebblestore.FlowOutboxCommandRecord {
	record.Command = normalizeAPIFlowAssignmentCommand(record.Command)
	record.CommandID = strings.TrimSpace(firstNonEmpty(record.CommandID, record.Command.CommandID))
	record.FlowID = strings.TrimSpace(firstNonEmpty(record.FlowID, record.Command.FlowID, record.Command.Assignment.FlowID))
	if record.Revision == 0 {
		record.Revision = firstNonZeroInt64(record.Revision, record.Command.Revision, record.Command.Assignment.Revision)
	}
	record.TargetSwarmID = strings.TrimSpace(firstNonEmpty(record.TargetSwarmID, record.Target.SwarmID, record.Command.Assignment.Target.SwarmID))
	record.LastError = strings.TrimSpace(record.LastError)
	record.Status = strings.TrimSpace(strings.ToLower(record.Status))
	if record.Status == "" {
		record.Status = pebblestore.FlowOutboxStatusPending
	}
	return record
}

func targetSelectionForOutbox(command flow.AssignmentCommand, target swarmTarget, resolved flow.ResolvedTarget) flow.TargetSelection {
	selection := normalizeFlowTargetSelection(command.Assignment.Target)
	selection.SwarmID = firstNonEmpty(selection.SwarmID, resolved.SwarmID, target.SwarmID)
	selection.Kind = firstNonEmpty(selection.Kind, resolved.Kind, target.Kind)
	selection.DeploymentID = firstNonEmpty(selection.DeploymentID, resolved.DeploymentID, target.DeploymentID)
	selection.Name = firstNonEmpty(selection.Name, resolved.Name, target.Name)
	return normalizeFlowTargetSelection(selection)
}

func normalizeFlowTargetSelection(selection flow.TargetSelection) flow.TargetSelection {
	selection.SwarmID = strings.TrimSpace(selection.SwarmID)
	selection.Kind = strings.TrimSpace(selection.Kind)
	selection.DeploymentID = strings.TrimSpace(selection.DeploymentID)
	selection.Name = strings.TrimSpace(selection.Name)
	return selection
}

func (s *Server) flowWorkspaceForTarget(workspace flow.WorkspaceContext, target swarmTarget, resolved flow.ResolvedTarget) flow.WorkspaceContext {
	workspace.WorkspacePath = strings.TrimSpace(workspace.WorkspacePath)
	workspace.HostWorkspacePath = strings.TrimSpace(workspace.HostWorkspacePath)
	workspace.RuntimeWorkspacePath = strings.TrimSpace(workspace.RuntimeWorkspacePath)
	workspace.CWD = strings.TrimSpace(workspace.CWD)
	workspace.WorktreeMode = strings.TrimSpace(workspace.WorktreeMode)
	hostWorkspacePath := firstNonEmpty(workspace.HostWorkspacePath, workspace.WorkspacePath)
	runtimeWorkspacePath := firstNonEmpty(workspace.RuntimeWorkspacePath, workspace.WorkspacePath)
	if workspace.HostWorkspacePath == "" {
		workspace.HostWorkspacePath = hostWorkspacePath
	}
	if workspace.RuntimeWorkspacePath == "" {
		workspace.RuntimeWorkspacePath = runtimeWorkspacePath
	}
	if workspace.WorkspacePath == "" || s == nil || s.workspace == nil || isSelfFlowTarget(target, resolved) {
		return workspace
	}
	translated := s.resolveReplicatedFlowWorkspacePath(hostWorkspacePath, target, resolved)
	if translated == "" || translated == workspace.WorkspacePath {
		if translated != "" {
			workspace.RuntimeWorkspacePath = translated
		}
		return workspace
	}
	workspace.CWD = translateFlowSubpath(hostWorkspacePath, translated, workspace.CWD)
	workspace.WorkspacePath = translated
	workspace.RuntimeWorkspacePath = translated
	return workspace
}

func (s *Server) resolveReplicatedFlowWorkspacePath(hostWorkspacePath string, target swarmTarget, resolved flow.ResolvedTarget) string {
	if s == nil || s.workspace == nil {
		return ""
	}
	hostWorkspacePath = filepath.Clean(strings.TrimSpace(hostWorkspacePath))
	if hostWorkspacePath == "." || hostWorkspacePath == string(filepath.Separator) {
		return ""
	}
	targetSwarmID := firstNonEmpty(strings.TrimSpace(resolved.SwarmID), strings.TrimSpace(target.SwarmID))
	targetKind := firstNonEmpty(strings.TrimSpace(resolved.Kind), strings.TrimSpace(target.Kind))
	deploymentID := firstNonEmpty(strings.TrimSpace(resolved.DeploymentID), strings.TrimSpace(target.DeploymentID))
	entries, err := s.workspace.ListKnown(100000)
	if err != nil {
		return ""
	}
	bestSource := ""
	bestTarget := ""
	for _, entry := range entries {
		for _, link := range entry.ReplicationLinks {
			targetPath := strings.TrimSpace(link.TargetWorkspacePath)
			if targetPath == "" || !flowReplicationLinkMatchesTarget(link, targetSwarmID, targetKind, deploymentID) {
				continue
			}
			for _, source := range flowWorkspaceLinkSources(entry) {
				if !flowPathWithinRoot(source, hostWorkspacePath) {
					continue
				}
				if len(source) > len(bestSource) {
					bestSource = source
					bestTarget = targetPath
				}
			}
		}
	}
	if bestSource == "" || bestTarget == "" {
		return ""
	}
	return translateFlowSubpath(bestSource, bestTarget, hostWorkspacePath)
}

func isSelfFlowTarget(target swarmTarget, resolved flow.ResolvedTarget) bool {
	return strings.EqualFold(strings.TrimSpace(target.Relationship), "self") ||
		strings.EqualFold(strings.TrimSpace(target.Kind), "self") ||
		strings.EqualFold(strings.TrimSpace(resolved.Relationship), "self") ||
		strings.EqualFold(strings.TrimSpace(resolved.Kind), "self")
}

func flowReplicationLinkMatchesTarget(link pebblestore.WorkspaceReplicationLink, targetSwarmID, targetKind, deploymentID string) bool {
	if targetSwarmID != "" && strings.EqualFold(strings.TrimSpace(link.TargetSwarmID), targetSwarmID) {
		return true
	}
	if deploymentID != "" && strings.EqualFold(strings.TrimSpace(link.ID), deploymentID) {
		return true
	}
	if targetKind != "" && strings.TrimSpace(link.TargetSwarmID) == "" && strings.EqualFold(strings.TrimSpace(link.TargetKind), targetKind) {
		return true
	}
	return false
}

func flowWorkspaceLinkSources(entry workspaceruntime.Entry) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1+len(entry.Directories))
	for _, raw := range append([]string{entry.Path}, entry.Directories...) {
		source := strings.TrimSpace(raw)
		if source == "" {
			continue
		}
		source = filepath.Clean(source)
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	return out
}

func flowPathWithinRoot(root, candidate string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	candidate = filepath.Clean(strings.TrimSpace(candidate))
	if root == "" || candidate == "" {
		return false
	}
	if root == candidate {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func translateFlowSubpath(sourceRoot, targetRoot, candidate string) string {
	sourceRoot = filepath.Clean(strings.TrimSpace(sourceRoot))
	targetRoot = strings.TrimRight(strings.TrimSpace(targetRoot), "/")
	candidate = strings.TrimSpace(candidate)
	if sourceRoot == "" || targetRoot == "" || candidate == "" {
		return candidate
	}
	cleanCandidate := filepath.Clean(candidate)
	rel, err := filepath.Rel(sourceRoot, cleanCandidate)
	if err != nil || rel == "." {
		return targetRoot
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return candidate
	}
	return path.Join(targetRoot, filepath.ToSlash(rel))
}

func flowTargetMatchesSelection(target swarmTarget, selection flow.TargetSelection) bool {
	if selection.SwarmID != "" && !strings.EqualFold(strings.TrimSpace(target.SwarmID), selection.SwarmID) {
		return false
	}
	if selection.Kind != "" && !strings.EqualFold(strings.TrimSpace(target.Kind), selection.Kind) {
		return false
	}
	if selection.DeploymentID != "" && !strings.EqualFold(strings.TrimSpace(target.DeploymentID), selection.DeploymentID) {
		return false
	}
	if selection.SwarmID == "" && selection.DeploymentID == "" && selection.Name != "" && !strings.EqualFold(strings.TrimSpace(target.Name), selection.Name) {
		return false
	}
	return strings.TrimSpace(target.SwarmID) != ""
}

func resolvedFlowTarget(selection flow.TargetSelection, target swarmTarget) flow.ResolvedTarget {
	return flow.ResolvedTarget{
		Selection:    selection,
		SwarmID:      strings.TrimSpace(target.SwarmID),
		Name:         strings.TrimSpace(target.Name),
		Relationship: strings.TrimSpace(target.Relationship),
		Kind:         strings.TrimSpace(target.Kind),
		DeploymentID: strings.TrimSpace(target.DeploymentID),
		BackendURL:   strings.TrimSpace(target.BackendURL),
		Online:       target.Online,
		Selectable:   target.Selectable,
		LastError:    strings.TrimSpace(target.LastError),
	}
}

func flowOutboxRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(attempt) * time.Minute
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
