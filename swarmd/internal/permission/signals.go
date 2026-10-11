package permission

import (
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SetSignalEmitter makes the service raise agent.blocked when an agent starts
// waiting on a person (a tool approval, a question, a plan) and agent.unblocked
// when that wait ends.
func (s *Service) SetSignalEmitter(emitter *signals.Emitter) {
	if s == nil {
		return
	}
	s.signals = emitter
}

// emitPermissionSignal raises the blocked/unblocked signal for one permission
// transition. It names the tool and ids only: the command, arguments and the
// agent's question never go into the feed.
func (s *Service) emitPermissionSignal(record pebblestore.PermissionRecord, sourceEventType string) {
	if s == nil || s.signals == nil {
		return
	}
	tool := fallbackToolName(record.ToolName)
	refs := map[string]string{
		"session_id":    record.SessionID,
		"run_id":        record.RunID,
		"permission_id": record.ID,
		"tool":          tool,
	}
	account := accountScopeIDForPermissionRecord(s.sessions, record)
	dedup := "agent.blocked:" + record.ID
	status := strings.ToLower(strings.TrimSpace(record.Status))
	if status == pebblestore.PermissionStatusPending {
		if sourceEventType != "permission.requested" {
			return
		}
		s.signals.Emit(pebblestore.Signal{
			Kind:     "agent.blocked",
			Severity: pebblestore.SignalSeverityWarning,
			Account:  account,
			Summary:  blockedSummary(tool),
			DedupKey: dedup,
			Refs:     refs,
		})
		return
	}
	switch status {
	case pebblestore.PermissionStatusApproved, pebblestore.PermissionStatusDenied, pebblestore.PermissionStatusCancelled:
	default:
		return
	}
	s.signals.Emit(pebblestore.Signal{
		Kind:     "agent.unblocked",
		Severity: pebblestore.SignalSeverityInfo,
		Account:  account,
		Summary:  fmt.Sprintf("Agent resumed: %s %s", tool, status),
		DedupKey: dedup,
		Refs:     refs,
		Attrs:    map[string]string{"outcome": status},
	})
}

func blockedSummary(tool string) string {
	switch tool {
	case "ask_user":
		return "Agent asked a question and is waiting for an answer"
	case "plan_manage", "exit_plan_mode":
		return "A plan is waiting for approval"
	default:
		return "Agent is waiting for approval to use " + tool
	}
}
