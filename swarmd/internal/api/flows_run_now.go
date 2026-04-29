package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) applyFlowRunNowCommand(ctx context.Context, command flow.AssignmentCommand, now time.Time) (flow.AssignmentAck, bool, error) {
	if s == nil || s.flows == nil {
		return flow.AssignmentAck{}, false, fmt.Errorf("flow store is not configured")
	}
	command = normalizeAPIFlowAssignmentCommand(command)
	if err := command.ValidateIdempotencyKey(); err != nil {
		return flow.AssignmentAck{}, false, err
	}
	key := command.IdempotencyKey()
	if existing, ok, err := s.flows.GetCommandLedger(key.FlowID, key.Revision, key.CommandID); err != nil || ok {
		if err != nil {
			return flow.AssignmentAck{}, false, err
		}
		return existing.Ack, false, nil
	}
	accepted, ok, err := s.flows.GetAcceptedAssignment(key.FlowID)
	if err != nil {
		return flow.AssignmentAck{}, false, err
	}
	baseAck := flow.AssignmentAck{
		CommandID:   key.CommandID,
		FlowID:      key.FlowID,
		TargetClock: now.UTC(),
	}
	if now.IsZero() {
		now = time.Now().UTC()
		baseAck.TargetClock = now
	}
	peerSwarmID := strings.TrimSpace(command.Assignment.Target.SwarmID)
	if peerSwarmID != "" {
		baseAck.TargetSwarmID = peerSwarmID
	}
	reject := func(status flow.AssignmentStatus, reason string) (flow.AssignmentAck, bool, error) {
		ack := baseAck
		ack.Status = status
		ack.Reason = strings.TrimSpace(reason)
		if ok {
			ack.AcceptedRevision = accepted.Revision
		}
		_, inserted, err := s.flows.PutCommandLedger(pebblestore.FlowCommandLedgerRecord{
			CommandID: key.CommandID,
			FlowID:    key.FlowID,
			Revision:  key.Revision,
			Action:    command.Action,
			Status:    status,
			Ack:       ack,
			AppliedAt: now,
		})
		return ack, inserted, err
	}
	if !ok {
		return reject(flow.AssignmentRejected, "accepted assignment is not installed on target")
	}
	if accepted.Revision > key.Revision {
		return reject(flow.AssignmentOutOfOrder, fmt.Sprintf("target already accepted revision %d", accepted.Revision))
	}
	if accepted.Revision != key.Revision {
		return reject(flow.AssignmentRejected, fmt.Sprintf("accepted revision %d does not match run_now revision %d", accepted.Revision, key.Revision))
	}
	start, err := s.RunAcceptedFlowNowAt(ctx, accepted, now, key.CommandID)
	if err != nil {
		return reject(flow.AssignmentRejected, err.Error())
	}
	ack := baseAck
	ack.AcceptedRevision = accepted.Revision
	ack.Status = flow.AssignmentAccepted
	ack.Reason = strings.TrimSpace(fmt.Sprintf("run_now started %s", strings.TrimSpace(start.RunID)))
	_, inserted, err := s.flows.PutCommandLedger(pebblestore.FlowCommandLedgerRecord{
		CommandID: key.CommandID,
		FlowID:    key.FlowID,
		Revision:  key.Revision,
		Action:    command.Action,
		Status:    ack.Status,
		Ack:       ack,
		AppliedAt: now,
	})
	return ack, inserted, err
}
