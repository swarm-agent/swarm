package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
		writeError(w, http.StatusBadRequest, err)
		return
	}
	peerSwarmID, _ := extractPeerAuth(r)
	now := time.Now().UTC()
	var (
		ack      flow.AssignmentAck
		inserted bool
		err      error
	)
	if normalizeAPIFlowAssignmentCommand(command).Action == flow.CommandRunNow {
		command.Assignment.Target.SwarmID = firstNonEmpty(strings.TrimSpace(command.Assignment.Target.SwarmID), strings.TrimSpace(peerSwarmID))
		ack, inserted, err = s.applyFlowRunNowCommand(r.Context(), command, now)
	} else {
		ack, inserted, err = s.flows.ApplyTargetAssignmentCommand(command, peerSwarmID, now)
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
	if err := command.ValidateIdempotencyKey(); err != nil {
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	key := command.IdempotencyKey()
	now := time.Now().UTC()
	targetSelection := targetSelectionForOutbox(command, target, resolved)
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

func (s *Server) deliverFlowAssignmentOutboxCommand(ctx context.Context, record pebblestore.FlowOutboxCommandRecord, target swarmTarget) (flowAssignmentDeliverResult, error) {
	if s == nil || s.flows == nil {
		return flowAssignmentDeliverResult{}, errors.New("flow store is not configured")
	}
	record = normalizeAPIFlowOutboxCommand(record)
	if strings.TrimSpace(target.SwarmID) == "" {
		target.SwarmID = strings.TrimSpace(record.TargetSwarmID)
	}
	if strings.TrimSpace(target.BackendURL) == "" || !target.Online || !target.Selectable {
		reason := firstNonEmpty(strings.TrimSpace(target.LastError), "target is not currently reachable")
		updated, state, err := s.markFlowAssignmentPending(record, flow.AssignmentTargetOffline, reason, nil)
		return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, err
	}
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
