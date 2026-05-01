package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/flow"
)

func (s *FlowStore) acceptTargetAssignmentCommand(command flow.AssignmentCommand, baseAck flow.AssignmentAck, assignment flow.Assignment, now time.Time) (flow.AssignmentAck, bool, error) {
	accepted := flow.AcceptedAssignment{Assignment: assignment, AcceptedAt: now}
	accepted = normalizeAcceptedAssignment(accepted)
	ack := baseAck
	ack.AcceptedRevision = accepted.Revision
	ack.Status = flow.AssignmentAccepted
	if err := s.putTargetCommandAcceptance(command, ack, accepted, now); err != nil {
		return flow.AssignmentAck{}, false, err
	}
	return ack, true, nil
}

func (s *FlowStore) acceptTargetDeleteCommand(command flow.AssignmentCommand, baseAck flow.AssignmentAck, now time.Time) (flow.AssignmentAck, bool, error) {
	key := command.IdempotencyKey()
	ack := baseAck
	ack.AcceptedRevision = key.Revision
	ack.Status = flow.AssignmentAccepted
	if err := s.putTargetCommandDelete(command, ack, now); err != nil {
		return flow.AssignmentAck{}, false, err
	}
	return ack, true, nil
}

func (s *FlowStore) putTargetCommandAcceptance(command flow.AssignmentCommand, ack flow.AssignmentAck, accepted flow.AcceptedAssignment, now time.Time) error {
	key := command.IdempotencyKey()
	dueKeys, err := s.dueKeysForFlow(accepted.FlowID)
	if err != nil {
		return err
	}
	ledger := normalizeFlowCommandLedgerRecord(FlowCommandLedgerRecord{
		CommandID: key.CommandID,
		FlowID:    key.FlowID,
		Revision:  key.Revision,
		Action:    command.Action,
		Status:    ack.Status,
		Ack:       ack,
		AppliedAt: now,
	})
	acceptedPayload, err := json.Marshal(accepted)
	if err != nil {
		return fmt.Errorf("marshal accepted flow assignment: %w", err)
	}
	ledgerPayload, err := json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("marshal flow command ledger: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyFlowTargetAccepted(accepted.FlowID)), acceptedPayload, nil); err != nil {
		return fmt.Errorf("set accepted flow assignment: %w", err)
	}
	for _, dueKey := range dueKeys {
		if err := batch.Delete([]byte(dueKey), nil); err != nil {
			return fmt.Errorf("delete stale flow due record: %w", err)
		}
	}
	if err := batch.Set([]byte(KeyFlowTargetCommandLedger(key.FlowID, key.Revision, key.CommandID)), ledgerPayload, nil); err != nil {
		return fmt.Errorf("set flow command ledger: %w", err)
	}
	if next, ok, err := flow.NextFire(accepted.Assignment, now); err != nil {
		return err
	} else if ok {
		due := normalizeFlowDueRecord(FlowDueRecord{FlowID: accepted.FlowID, Revision: accepted.Revision, DueAt: next, ScheduledAt: next})
		duePayload, err := json.Marshal(due)
		if err != nil {
			return fmt.Errorf("marshal flow due record: %w", err)
		}
		if err := batch.Set([]byte(KeyFlowTargetDue(due.DueAt.UTC().UnixMilli(), due.FlowID, due.Revision)), duePayload, nil); err != nil {
			return fmt.Errorf("set flow due record: %w", err)
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *FlowStore) putTargetCommandDelete(command flow.AssignmentCommand, ack flow.AssignmentAck, now time.Time) error {
	key := command.IdempotencyKey()
	dueKeys, err := s.dueKeysForFlow(key.FlowID)
	if err != nil {
		return err
	}
	ledger := normalizeFlowCommandLedgerRecord(FlowCommandLedgerRecord{
		CommandID: key.CommandID,
		FlowID:    key.FlowID,
		Revision:  key.Revision,
		Action:    command.Action,
		Status:    ack.Status,
		Ack:       ack,
		AppliedAt: now,
	})
	ledgerPayload, err := json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("marshal flow command ledger: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeyFlowTargetAccepted(key.FlowID)), nil); err != nil {
		return fmt.Errorf("delete accepted flow assignment: %w", err)
	}
	for _, dueKey := range dueKeys {
		if err := batch.Delete([]byte(dueKey), nil); err != nil {
			return fmt.Errorf("delete flow due record: %w", err)
		}
	}
	if err := batch.Set([]byte(KeyFlowTargetCommandLedger(key.FlowID, key.Revision, key.CommandID)), ledgerPayload, nil); err != nil {
		return fmt.Errorf("set flow command ledger: %w", err)
	}
	return batch.Commit(pebble.Sync)
}

func (s *FlowStore) dueKeysForFlow(flowID string) ([]string, error) {
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return nil, nil
	}
	keys := make([]string, 0, 4)
	err := s.store.IteratePrefix(FlowTargetDuePrefix(), 100000, func(key string, value []byte) error {
		var record FlowDueRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow due record: %w", err)
		}
		record = normalizeFlowDueRecord(record)
		if record.FlowID == flowID {
			keys = append(keys, key)
		}
		return nil
	})
	return keys, err
}

func (s *FlowStore) maxAppliedTargetAssignmentRevision(flowID string) (int64, error) {
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return 0, nil
	}
	var maxRevision int64
	err := s.store.IteratePrefix(FlowTargetCommandLedgerPrefix(flowID), 100000, func(_ string, value []byte) error {
		var record FlowCommandLedgerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow command ledger: %w", err)
		}
		record = normalizeFlowCommandLedgerRecord(record)
		switch record.Status {
		case flow.AssignmentAccepted, flow.AssignmentOutOfOrder:
			if record.Revision > maxRevision {
				maxRevision = record.Revision
			}
		}
		if record.Ack.AcceptedRevision > maxRevision {
			maxRevision = record.Ack.AcceptedRevision
		}
		return nil
	})
	return maxRevision, err
}

func normalizeFlowAssignmentCommand(command flow.AssignmentCommand) flow.AssignmentCommand {
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.FlowID = strings.TrimSpace(command.FlowID)
	if command.FlowID == "" {
		command.FlowID = strings.TrimSpace(command.Assignment.FlowID)
	}
	if command.Revision == 0 {
		command.Revision = command.Assignment.Revision
	}
	command.Action = flow.CommandAction(strings.TrimSpace(strings.ToLower(string(command.Action))))
	command.Assignment = normalizeFlowAssignment(command.Assignment)
	command.CreatedAt = command.CreatedAt.UTC()
	return command
}

func normalizeFlowAssignmentAck(ack flow.AssignmentAck) flow.AssignmentAck {
	ack.CommandID = strings.TrimSpace(ack.CommandID)
	ack.FlowID = strings.TrimSpace(ack.FlowID)
	ack.Reason = strings.TrimSpace(ack.Reason)
	ack.TargetSwarmID = strings.TrimSpace(ack.TargetSwarmID)
	ack.TargetClock = ack.TargetClock.UTC()
	return ack
}
