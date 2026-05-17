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
	"swarm/packages/swarmd/internal/flowdiaglog"
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

func (s *Server) SetVideoThreadStore(store *pebblestore.VideoThreadStore) {
	if s == nil {
		return
	}
	s.videoThreads = store
}

func (s *Server) handlePeerFlowApply(w http.ResponseWriter, r *http.Request) {
	peerSwarmID, _ := extractPeerAuth(r)
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.flows == nil {
		flowdiaglog.Printf("peer_apply_no_store", "remote_addr=%q peer_header_swarm_id=%q path=%q", strings.TrimSpace(r.RemoteAddr), peerSwarmID, r.URL.Path)
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	var command flow.AssignmentCommand
	if err := decodeJSON(r, &command); err != nil {
		flowdiaglog.Printf("peer_apply_decode_failed", "remote_addr=%q peer_header_swarm_id=%q path=%q err=%q", strings.TrimSpace(r.RemoteAddr), peerSwarmID, r.URL.Path, err.Error())
		if strings.Contains(err.Error(), "unknown field") || strings.Contains(err.Error(), "runtime-only") {
			writeError(w, http.StatusConflict, fmt.Errorf("peer flow protocol mismatch: %w", err))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.proxyPeerFlowApplyToLocalChild(w, r, command, peerSwarmID) {
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
		flowdiaglog.Printf("peer_apply_store_failed", "flow_id=%q command_id=%q action=%q target_swarm_id=%q db_path=%q accepted_key=%q err=%q", command.FlowID, command.CommandID, command.Action, targetSwarmID, s.flows.StorePath(), pebblestore.KeyFlowTargetAccepted(command.FlowID), err.Error())
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if normalizeAPIFlowAssignmentCommand(command).Action == flow.CommandDelete {
		_, exists, verifyErr := s.flows.GetAcceptedAssignment(command.FlowID)
		flowdiaglog.Printf("peer_apply_delete_verify", "flow_id=%q command_id=%q action=%q target_swarm_id=%q db_path=%q accepted_key=%q accepted_exists_after_delete=%t ack_status=%q ack_revision=%d inserted=%t verify_err=%q", command.FlowID, command.CommandID, command.Action, targetSwarmID, s.flows.StorePath(), pebblestore.KeyFlowTargetAccepted(command.FlowID), exists, ack.Status, ack.AcceptedRevision, inserted, errString(verifyErr))
	} else {
		accepted, exists, verifyErr := s.flows.GetAcceptedAssignment(command.FlowID)
		flowdiaglog.Printf("peer_apply_accept_verify", "flow_id=%q command_id=%q action=%q target_swarm_id=%q db_path=%q accepted_key=%q accepted_exists_after_apply=%t accepted_revision=%d ack_status=%q ack_revision=%d inserted=%t verify_err=%q", command.FlowID, command.CommandID, command.Action, targetSwarmID, s.flows.StorePath(), pebblestore.KeyFlowTargetAccepted(command.FlowID), exists, accepted.Revision, ack.Status, ack.AcceptedRevision, inserted, errString(verifyErr))
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
		flowdiaglog.Printf("controller_enqueue_outbox_failed", "flow_id=%q command_id=%q action=%q controller_db_path=%q target_swarm_id=%q err=%q", command.FlowID, command.CommandID, command.Action, s.flows.StorePath(), resolved.SwarmID, err.Error())
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	flowdiaglog.Printf("controller_enqueue_outbox_saved", "flow_id=%q command_id=%q action=%q revision=%d controller_db_path=%q target_swarm_id=%q outbox_status=%q", stored.FlowID, stored.CommandID, stored.Command.Action, stored.Revision, s.flows.StorePath(), stored.TargetSwarmID, stored.Status)
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
		flowdiaglog.Printf("controller_enqueue_status_failed", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q err=%q", command.FlowID, command.CommandID, s.flows.StorePath(), resolved.SwarmID, err.Error())
		return pebblestore.FlowOutboxCommandRecord{}, err
	}
	flowdiaglog.Printf("controller_enqueue_status_saved", "flow_id=%q command_id=%q revision=%d controller_db_path=%q target_swarm_id=%q status=%q reason=%q", key.FlowID, key.CommandID, key.Revision, s.flows.StorePath(), resolved.SwarmID, flow.AssignmentPendingSync, "assignment command queued for target sync")
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
		ack = normalizeFlowAssignmentAckForAPI(ack)
		if validationErr := validateFlowAssignmentAckForRecord(record, ack); validationErr != nil {
			updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetUnusable, validationErr.Error(), nil)
			if updateErr != nil {
				return flowAssignmentDeliverResult{}, updateErr
			}
			return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, Ack: ack, PendingSync: true}, nil
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
	deliveryTarget := s.flowDeliveryTarget(target)
	if strings.TrimSpace(deliveryTarget.BackendURL) == "" || !deliveryTarget.Online || !deliveryTarget.Selectable {
		reason := firstNonEmpty(strings.TrimSpace(deliveryTarget.LastError), "target is not currently reachable")
		flowdiaglog.Printf("controller_deliver_blocked_target_unreachable", "flow_id=%q command_id=%q action=%q controller_db_path=%q target_swarm_id=%q target_kind=%q target_name=%q backend_url_present=%t online=%t selectable=%t reason=%q", record.FlowID, record.CommandID, record.Command.Action, s.flows.StorePath(), deliveryTarget.SwarmID, deliveryTarget.Kind, deliveryTarget.Name, strings.TrimSpace(deliveryTarget.BackendURL) != "", deliveryTarget.Online, deliveryTarget.Selectable, reason)
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
		"delivery_backend_url_present", strings.TrimSpace(deliveryTarget.BackendURL) != "",
		"delivery_host_swarm_id", deliveryTarget.HostSwarmID,
	)
	var resp flowAssignmentApplyResponse
	flowdiaglog.Printf("controller_http_post_start", "flow_id=%q command_id=%q action=%q controller_db_path=%q target_swarm_id=%q target_kind=%q target_name=%q delivery_host_swarm_id=%q endpoint=%q accepted_key_expected_on_child=%q", record.FlowID, record.CommandID, record.Command.Action, s.flows.StorePath(), target.SwarmID, target.Kind, target.Name, deliveryTarget.HostSwarmID, strings.TrimRight(strings.TrimSpace(deliveryTarget.BackendURL), "/")+flowPeerApplyPath, pebblestore.KeyFlowTargetAccepted(record.FlowID))
	deliverErr := s.postPeerJSONToSwarmTarget(ctx, deliveryTarget, flowPeerApplyPath, record.Command, &resp)
	if deliverErr != nil {
		flowdiaglog.Printf("controller_http_post_failed", "flow_id=%q command_id=%q action=%q controller_db_path=%q target_swarm_id=%q endpoint=%q err=%q", record.FlowID, record.CommandID, record.Command.Action, s.flows.StorePath(), target.SwarmID, strings.TrimRight(strings.TrimSpace(deliveryTarget.BackendURL), "/")+flowPeerApplyPath, deliverErr.Error())
		updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetOffline, deliverErr.Error(), nil)
		if updateErr != nil {
			return flowAssignmentDeliverResult{}, updateErr
		}
		return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, nil
	}
	flowdiaglog.Printf("controller_http_post_response", "flow_id=%q command_id=%q target_swarm_id=%q response_ok=%t response_inserted=%t ack_command_id=%q ack_flow_id=%q ack_target_swarm_id=%q ack_status=%q ack_revision=%d", record.FlowID, record.CommandID, target.SwarmID, resp.OK, resp.Inserted, resp.Ack.CommandID, resp.Ack.FlowID, resp.Ack.TargetSwarmID, resp.Ack.Status, resp.Ack.AcceptedRevision)
	ack, validationErr := validateFlowAssignmentApplyResponse(record, resp)
	if validationErr != nil {
		flowdiaglog.Printf("controller_ack_validation_failed", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q ack_command_id=%q ack_flow_id=%q ack_target_swarm_id=%q ack_status=%q ack_revision=%d err=%q", record.FlowID, record.CommandID, s.flows.StorePath(), target.SwarmID, ack.CommandID, ack.FlowID, ack.TargetSwarmID, ack.Status, ack.AcceptedRevision, validationErr.Error())
		updated, state, updateErr := s.markFlowAssignmentPending(record, flow.AssignmentTargetUnusable, validationErr.Error(), nil)
		if updateErr != nil {
			return flowAssignmentDeliverResult{}, updateErr
		}
		return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, Ack: ack, PendingSync: true}, nil
	}
	updated, state, err := s.applyFlowAssignmentAck(record, ack)
	if err != nil {
		flowdiaglog.Printf("controller_ack_apply_failed", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q ack_status=%q ack_revision=%d err=%q", record.FlowID, record.CommandID, s.flows.StorePath(), target.SwarmID, ack.Status, ack.AcceptedRevision, err.Error())
		return flowAssignmentDeliverResult{}, err
	}
	flowdiaglog.Printf("controller_ack_applied", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q outbox_status=%q assignment_status=%q accepted_revision=%d pending_sync=%t", record.FlowID, record.CommandID, s.flows.StorePath(), target.SwarmID, updated.Status, state.Status, state.AcceptedRevision, state.PendingSync)
	return flowAssignmentDeliverResult{
		Outbox:          updated,
		AssignmentState: state,
		Ack:             ack,
		Delivered:       ack.Status == flow.AssignmentAccepted || ack.Status == flow.AssignmentDuplicate,
		PendingSync:     state.PendingSync,
	}, nil
}

func (s *Server) proxyPeerFlowApplyToLocalChild(w http.ResponseWriter, r *http.Request, command flow.AssignmentCommand, peerSwarmID string) bool {
	if s == nil || r == nil || strings.EqualFold(strings.TrimSpace(peerSwarmID), "") {
		return false
	}
	selection := normalizeFlowTargetSelection(command.Assignment.Target)
	if strings.TrimSpace(selection.SwarmID) == "" || !strings.EqualFold(strings.TrimSpace(selection.Kind), "mirrored") {
		return false
	}
	target, ok := s.localFlowChildTargetForMirroredApply(selection)
	if !ok {
		return false
	}
	flowRouteDiagLog("peer_apply_proxy_to_local_child",
		"peer_header_swarm_id", peerSwarmID,
		"flow_id", command.FlowID,
		"command_id", command.CommandID,
		"action", command.Action,
		"requested_target_swarm_id", selection.SwarmID,
		"local_child_swarm_id", target.SwarmID,
		"local_child_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
	)
	var resp flowAssignmentApplyResponse
	if err := s.postPeerJSONToSwarmTarget(r.Context(), target, flowPeerApplyPath, command, &resp); err != nil {
		flowdiaglog.Printf("peer_apply_proxy_to_local_child_failed", "flow_id=%q command_id=%q peer_header_swarm_id=%q requested_target_swarm_id=%q local_child_swarm_id=%q err=%q", command.FlowID, command.CommandID, peerSwarmID, selection.SwarmID, target.SwarmID, err.Error())
		writeError(w, http.StatusBadGateway, err)
		return true
	}
	writeJSON(w, http.StatusOK, resp)
	return true
}

func (s *Server) localFlowChildTargetForMirroredApply(selection flow.TargetSelection) (swarmTarget, bool) {
	if s == nil || s.deployContainers == nil {
		return swarmTarget{}, false
	}
	items, err := s.deployContainers.List(context.Background())
	if err != nil {
		return swarmTarget{}, false
	}
	deploymentID := strings.TrimSpace(selection.DeploymentID)
	name := strings.TrimSpace(selection.Name)
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.AttachStatus), "attached") {
			continue
		}
		if strings.TrimSpace(item.ChildBackendURL) == "" || strings.TrimSpace(item.ChildSwarmID) == "" {
			continue
		}
		if deploymentID != "" && !strings.EqualFold(strings.TrimSpace(item.ID), deploymentID) && !strings.EqualFold(strings.TrimSpace(item.ContainerName), deploymentID) {
			continue
		}
		if deploymentID == "" && name != "" && !strings.EqualFold(strings.TrimSpace(item.Name), name) && !strings.EqualFold(strings.TrimSpace(item.ContainerName), name) && !strings.EqualFold(strings.TrimSpace(item.ChildDisplayName), name) {
			continue
		}
		if deploymentID == "" && name == "" {
			continue
		}
		return mapDeployContainerTarget(item)
	}
	return swarmTarget{}, false
}

func (s *Server) flowDeliveryTarget(target swarmTarget) swarmTarget {
	if s == nil {
		return target
	}
	if strings.EqualFold(strings.TrimSpace(target.Kind), "mirrored") || strings.TrimSpace(target.HostSwarmID) != "" {
		if backendURL := s.proxyBackendURLForTarget(target); strings.TrimSpace(backendURL) != "" && !sameBackendURL(backendURL, target.BackendURL) {
			deliveryTarget := target
			deliveryTarget.BackendURL = strings.TrimSpace(backendURL)
			if strings.TrimSpace(deliveryTarget.HostSwarmID) == "" {
				deliveryTarget.HostSwarmID = s.ownerHostSwarmIDForTarget(target)
			}
			return deliveryTarget
		}
	}
	return target
}

func validateFlowAssignmentApplyResponse(record pebblestore.FlowOutboxCommandRecord, resp flowAssignmentApplyResponse) (flow.AssignmentAck, error) {
	ack := normalizeFlowAssignmentAckForAPI(resp.Ack)
	if !resp.OK {
		return ack, errors.New("target response did not acknowledge assignment command")
	}
	if err := validateFlowAssignmentAckForRecord(record, ack); err != nil {
		return ack, err
	}
	return ack, nil
}

func validateFlowAssignmentAckForRecord(record pebblestore.FlowOutboxCommandRecord, ack flow.AssignmentAck) error {
	record = normalizeAPIFlowOutboxCommand(record)
	key := record.Command.IdempotencyKey()
	if strings.TrimSpace(ack.CommandID) == "" {
		return errors.New("target response did not include assignment command_id")
	}
	if ack.CommandID != key.CommandID {
		return fmt.Errorf("target response command_id %q does not match command %q", ack.CommandID, key.CommandID)
	}
	if strings.TrimSpace(ack.FlowID) == "" {
		return errors.New("target response did not include assignment flow_id")
	}
	if ack.FlowID != key.FlowID {
		return fmt.Errorf("target response flow_id %q does not match command flow_id %q", ack.FlowID, key.FlowID)
	}
	targetSwarmID := strings.TrimSpace(record.TargetSwarmID)
	if targetSwarmID != "" {
		if strings.TrimSpace(ack.TargetSwarmID) == "" {
			return errors.New("target response did not include target_swarm_id")
		}
		if !strings.EqualFold(ack.TargetSwarmID, targetSwarmID) {
			return fmt.Errorf("target response target_swarm_id %q does not match target %q", ack.TargetSwarmID, targetSwarmID)
		}
	}
	switch ack.Status {
	case flow.AssignmentAccepted, flow.AssignmentDuplicate:
		if ack.AcceptedRevision != key.Revision {
			return fmt.Errorf("target response accepted_revision %d does not match command revision %d", ack.AcceptedRevision, key.Revision)
		}
	case flow.AssignmentRejected, flow.AssignmentOutOfOrder:
		return nil
	case "":
		return errors.New("target response did not include assignment status")
	default:
		return fmt.Errorf("target response included unsupported assignment status %q", ack.Status)
	}
	return nil
}

func normalizeFlowAssignmentAckForAPI(ack flow.AssignmentAck) flow.AssignmentAck {
	ack.CommandID = strings.TrimSpace(ack.CommandID)
	ack.FlowID = strings.TrimSpace(ack.FlowID)
	ack.Status = flow.AssignmentStatus(strings.TrimSpace(strings.ToLower(string(ack.Status))))
	ack.Reason = strings.TrimSpace(ack.Reason)
	ack.TargetSwarmID = strings.TrimSpace(ack.TargetSwarmID)
	ack.TargetClock = ack.TargetClock.UTC()
	return ack
}

func (s *Server) applyFlowAssignmentAck(record pebblestore.FlowOutboxCommandRecord, ack flow.AssignmentAck) (pebblestore.FlowOutboxCommandRecord, pebblestore.FlowAssignmentStatusRecord, error) {
	ack = normalizeFlowAssignmentAckForAPI(ack)
	defer func() {
		// Logging is performed before returns below; this defer documents that this
		// function is the controller-only accepted-state transition point.
	}()
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
		flowdiaglog.Printf("controller_assignment_status_write_failed", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q status=%q err=%q", record.FlowID, record.CommandID, s.flows.StorePath(), state.TargetSwarmID, state.Status, err.Error())
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	flowdiaglog.Printf("controller_assignment_state_saved", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q outbox_status=%q assignment_status=%q accepted_revision=%d pending_sync=%t reason=%q", record.FlowID, record.CommandID, s.flows.StorePath(), storedState.TargetSwarmID, updated.Status, storedState.Status, storedState.AcceptedRevision, storedState.PendingSync, storedState.Reason)
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
		flowdiaglog.Printf("controller_assignment_pending_write_failed", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q status=%q reason=%q err=%q", record.FlowID, record.CommandID, s.flows.StorePath(), state.TargetSwarmID, state.Status, state.Reason, err.Error())
		return pebblestore.FlowOutboxCommandRecord{}, pebblestore.FlowAssignmentStatusRecord{}, err
	}
	flowdiaglog.Printf("controller_assignment_pending_saved", "flow_id=%q command_id=%q controller_db_path=%q target_swarm_id=%q outbox_status=%q assignment_status=%q next_attempt=%q reason=%q", record.FlowID, record.CommandID, s.flows.StorePath(), storedState.TargetSwarmID, updated.Status, storedState.Status, updated.NextAttemptAt.Format(time.RFC3339Nano), storedState.Reason)
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
	if s == nil || s.topology == nil {
		return ""
	}
	hostWorkspacePath = filepath.Clean(strings.TrimSpace(hostWorkspacePath))
	if hostWorkspacePath == "." || hostWorkspacePath == string(filepath.Separator) {
		return ""
	}
	targetSwarmID := firstNonEmpty(strings.TrimSpace(resolved.SwarmID), strings.TrimSpace(target.SwarmID))
	deploymentID := firstNonEmpty(strings.TrimSpace(resolved.DeploymentID), strings.TrimSpace(target.DeploymentID))
	bindings, err := s.topology.ListWorkspaceBindings(100000)
	if err != nil {
		return ""
	}
	bestSource := ""
	bestTarget := ""
	for _, binding := range bindings {
		if !flowWorkspaceBindingMatchesTarget(binding, targetSwarmID, deploymentID) {
			continue
		}
		source := strings.TrimSpace(binding.SourceWorkspacePath)
		targetPath := strings.TrimSpace(binding.DestinationWorkspacePath)
		if source == "" || targetPath == "" || !flowPathWithinRoot(source, hostWorkspacePath) {
			continue
		}
		if len(source) > len(bestSource) {
			bestSource = source
			bestTarget = targetPath
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

func flowWorkspaceBindingMatchesTarget(binding pebblestore.TopologyWorkspaceBindingRecord, targetSwarmID, deploymentID string) bool {
	if targetSwarmID != "" && strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), targetSwarmID) {
		return true
	}
	if deploymentID == "" {
		return false
	}
	bindingID := strings.TrimSpace(binding.BindingID)
	if strings.EqualFold(bindingID, deploymentID) {
		return true
	}
	return strings.Contains(bindingID, ":"+deploymentID+":")
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
