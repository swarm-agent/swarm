package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/flow"
	"swarm/packages/swarmd/internal/flowdiaglog"
)

func (s *FlowStore) acceptTargetAssignmentCommand(command flow.AssignmentCommand, baseAck flow.AssignmentAck, assignment flow.Assignment, now time.Time) (flow.AssignmentAck, bool, error) {
	flowdiaglog.Printf("store_accept_command_start", "flow_id=%q command_id=%q action=%q revision=%d target_swarm_id=%q db_path=%q accepted_key=%q", command.FlowID, command.CommandID, command.Action, command.Revision, baseAck.TargetSwarmID, s.StorePath(), KeyFlowTargetAccepted(command.FlowID))
	accepted := flow.AcceptedAssignment{AccountScopeID: command.AccountScopeID, UserID: command.UserID, Assignment: assignment, AcceptedAt: now}
	accepted = normalizeAcceptedAssignment(accepted)
	if err := validateFlowOwner(accepted.AccountScopeID, accepted.UserID); err != nil {
		return flow.AssignmentAck{}, false, err
	}
	ack := baseAck
	ack.AcceptedRevision = accepted.Revision
	ack.Status = flow.AssignmentAccepted
	if err := s.putTargetCommandAcceptance(command, ack, accepted, now); err != nil {
		flowdiaglog.Printf("store_accept_command_failed", "flow_id=%q command_id=%q action=%q revision=%d target_swarm_id=%q db_path=%q accepted_key=%q err=%q", command.FlowID, command.CommandID, command.Action, command.Revision, baseAck.TargetSwarmID, s.StorePath(), KeyFlowTargetAccepted(command.FlowID), err.Error())
		return flow.AssignmentAck{}, false, err
	}
	return ack, true, nil
}

func (s *FlowStore) acceptTargetDeleteCommand(command flow.AssignmentCommand, baseAck flow.AssignmentAck, now time.Time) (flow.AssignmentAck, bool, error) {
	flowdiaglog.Printf("store_delete_command_start", "flow_id=%q command_id=%q action=%q revision=%d target_swarm_id=%q db_path=%q accepted_key=%q", command.FlowID, command.CommandID, command.Action, command.Revision, baseAck.TargetSwarmID, s.StorePath(), KeyFlowTargetAccepted(command.FlowID))
	key := command.IdempotencyKey()
	ack := baseAck
	ack.AcceptedRevision = key.Revision
	ack.Status = flow.AssignmentAccepted
	if err := s.putTargetCommandDelete(command, ack, now); err != nil {
		flowdiaglog.Printf("store_delete_command_failed", "flow_id=%q command_id=%q action=%q revision=%d target_swarm_id=%q db_path=%q accepted_key=%q err=%q", command.FlowID, command.CommandID, command.Action, command.Revision, baseAck.TargetSwarmID, s.StorePath(), KeyFlowTargetAccepted(command.FlowID), err.Error())
		return flow.AssignmentAck{}, false, err
	}
	return ack, true, nil
}

func (s *FlowStore) putTargetCommandAcceptance(command flow.AssignmentCommand, ack flow.AssignmentAck, accepted flow.AcceptedAssignment, now time.Time) error {
	if err := validateFlowOwner(accepted.AccountScopeID, accepted.UserID); err != nil {
		return err
	}
	key := command.IdempotencyKey()
	dueKeys, err := s.dueKeysForFlow(accepted.FlowID)
	if err != nil {
		return err
	}
	ledger := normalizeFlowCommandLedgerRecord(FlowCommandLedgerRecord{
		AccountScopeID: command.AccountScopeID,
		UserID:         command.UserID,
		CommandID:      key.CommandID,
		FlowID:         key.FlowID,
		Revision:       key.Revision,
		Action:         command.Action,
		Status:         ack.Status,
		Ack:            ack,
		AppliedAt:      now,
	})
	acceptedKey := KeyFlowTargetAccepted(accepted.FlowID)
	ledgerKey := KeyFlowTargetCommandLedger(key.FlowID, key.Revision, key.CommandID)
	flowdiaglog.Printf("store_accept_prepare_keys", "flow_id=%q command_id=%q revision=%d db_path=%q accepted_key=%q ledger_key=%q stale_due_keys=%d", accepted.FlowID, key.CommandID, key.Revision, s.StorePath(), acceptedKey, ledgerKey, len(dueKeys))
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
	if err := batch.Set([]byte(acceptedKey), acceptedPayload, nil); err != nil {
		flowdiaglog.Printf("store_accept_set_failed", "flow_id=%q command_id=%q db_path=%q accepted_key=%q err=%q", accepted.FlowID, key.CommandID, s.StorePath(), acceptedKey, err.Error())
		return fmt.Errorf("set accepted flow assignment: %w", err)
	}
	for _, dueKey := range dueKeys {
		if err := batch.Delete([]byte(dueKey), nil); err != nil {
			return fmt.Errorf("delete stale flow due record: %w", err)
		}
	}
	if err := batch.Set([]byte(ledgerKey), ledgerPayload, nil); err != nil {
		flowdiaglog.Printf("store_accept_ledger_set_failed", "flow_id=%q command_id=%q db_path=%q ledger_key=%q err=%q", key.FlowID, key.CommandID, s.StorePath(), ledgerKey, err.Error())
		return fmt.Errorf("set flow command ledger: %w", err)
	}
	if next, ok, err := flow.NextFire(accepted.Assignment, now); err != nil {
		return err
	} else if ok {
		due := normalizeFlowDueRecord(FlowDueRecord{AccountScopeID: accepted.AccountScopeID, UserID: accepted.UserID, FlowID: accepted.FlowID, Revision: accepted.Revision, DueAt: next, ScheduledAt: next})
		duePayload, err := json.Marshal(due)
		if err != nil {
			return fmt.Errorf("marshal flow due record: %w", err)
		}
		dueKey := KeyFlowTargetDue(due.DueAt.UTC().UnixMilli(), due.FlowID, due.Revision)
		flowdiaglog.Printf("store_accept_due_prepare_key", "flow_id=%q command_id=%q db_path=%q due_key=%q", accepted.FlowID, key.CommandID, s.StorePath(), dueKey)
		if err := batch.Set([]byte(dueKey), duePayload, nil); err != nil {
			return fmt.Errorf("set flow due record: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		flowdiaglog.Printf("store_accept_commit_failed", "flow_id=%q command_id=%q db_path=%q accepted_key=%q ledger_key=%q err=%q", accepted.FlowID, key.CommandID, s.StorePath(), acceptedKey, ledgerKey, err.Error())
		return err
	}
	_, exists, verifyErr := s.GetAcceptedAssignment(accepted.FlowID)
	flowdiaglog.Printf("store_accept_commit_verified", "flow_id=%q command_id=%q revision=%d db_path=%q accepted_key=%q ledger_key=%q accepted_exists_after_commit=%t verify_err=%q", accepted.FlowID, key.CommandID, accepted.Revision, s.StorePath(), acceptedKey, ledgerKey, exists, flowStoreErrString(verifyErr))
	return nil
}

func (s *FlowStore) putTargetCommandDelete(command flow.AssignmentCommand, ack flow.AssignmentAck, now time.Time) error {
	if err := validateFlowOwner(command.AccountScopeID, command.UserID); err != nil {
		return err
	}
	key := command.IdempotencyKey()
	dueKeys, err := s.dueKeysForFlow(key.FlowID)
	if err != nil {
		return err
	}
	ledger := normalizeFlowCommandLedgerRecord(FlowCommandLedgerRecord{
		AccountScopeID: command.AccountScopeID,
		UserID:         command.UserID,
		CommandID:      key.CommandID,
		FlowID:         key.FlowID,
		Revision:       key.Revision,
		Action:         command.Action,
		Status:         ack.Status,
		Ack:            ack,
		AppliedAt:      now,
	})
	acceptedKey := KeyFlowTargetAccepted(key.FlowID)
	ledgerKey := KeyFlowTargetCommandLedger(key.FlowID, key.Revision, key.CommandID)
	flowdiaglog.Printf("store_delete_prepare_keys", "flow_id=%q command_id=%q revision=%d db_path=%q accepted_key=%q ledger_key=%q stale_due_keys=%d", key.FlowID, key.CommandID, key.Revision, s.StorePath(), acceptedKey, ledgerKey, len(dueKeys))
	ledgerPayload, err := json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("marshal flow command ledger: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(acceptedKey), nil); err != nil {
		flowdiaglog.Printf("store_delete_accepted_failed", "flow_id=%q command_id=%q db_path=%q accepted_key=%q err=%q", key.FlowID, key.CommandID, s.StorePath(), acceptedKey, err.Error())
		return fmt.Errorf("delete accepted flow assignment: %w", err)
	}
	for _, dueKey := range dueKeys {
		if err := batch.Delete([]byte(dueKey), nil); err != nil {
			return fmt.Errorf("delete flow due record: %w", err)
		}
	}
	if err := batch.Set([]byte(ledgerKey), ledgerPayload, nil); err != nil {
		flowdiaglog.Printf("store_delete_ledger_set_failed", "flow_id=%q command_id=%q db_path=%q ledger_key=%q err=%q", key.FlowID, key.CommandID, s.StorePath(), ledgerKey, err.Error())
		return fmt.Errorf("set flow command ledger: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		flowdiaglog.Printf("store_delete_commit_failed", "flow_id=%q command_id=%q db_path=%q accepted_key=%q ledger_key=%q err=%q", key.FlowID, key.CommandID, s.StorePath(), acceptedKey, ledgerKey, err.Error())
		return err
	}
	_, exists, verifyErr := s.GetAcceptedAssignment(key.FlowID)
	flowdiaglog.Printf("store_delete_commit_verified", "flow_id=%q command_id=%q revision=%d db_path=%q accepted_key=%q ledger_key=%q accepted_exists_after_commit=%t verify_err=%q", key.FlowID, key.CommandID, key.Revision, s.StorePath(), acceptedKey, ledgerKey, exists, flowStoreErrString(verifyErr))
	return nil
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
	command.AccountScopeID = strings.TrimSpace(command.AccountScopeID)
	command.UserID = strings.TrimSpace(command.UserID)
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

func flowStoreErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
