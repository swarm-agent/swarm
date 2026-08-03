package api

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionsV3CheckpointResumeRoutingKey           = "swarm_v3_checkpoint_resume_routing"
	sessionsV3CheckpointResumeRoutingRunIDKey      = "swarm_v3_checkpoint_resume_routing_run_id"
	sessionsV3CheckpointResumeRoutingCheckpointKey = "swarm_v3_checkpoint_resume_routing_checkpoint_id"
	sessionsV3CheckpointResumeRoutingReasonKey     = "swarm_v3_checkpoint_resume_routing_reason"
)

func markSessionsV3CheckpointResumeRouting(message *pebblestore.MessageSnapshot, runID, checkpointID, reason string) {
	if message == nil || !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
		return
	}
	runID = strings.TrimSpace(runID)
	checkpointID = strings.TrimSpace(checkpointID)
	if runID == "" || checkpointID == "" {
		return
	}
	if message.Metadata == nil {
		message.Metadata = map[string]any{}
	}
	message.Metadata[sessionsV3CheckpointResumeRoutingKey] = true
	message.Metadata[sessionsV3CheckpointResumeRoutingRunIDKey] = runID
	message.Metadata[sessionsV3CheckpointResumeRoutingCheckpointKey] = checkpointID
	message.Metadata[sessionsV3CheckpointResumeRoutingReasonKey] = strings.TrimSpace(reason)
}

func sessionsV3InjectCheckpointResumeRoutingMessage(messages []pebblestore.MessageSnapshot, runID string) []pebblestore.MessageSnapshot {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return messages
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || !sessionV3MetadataBool(message.Metadata, sessionsV3CheckpointResumeRoutingKey) {
			continue
		}
		if strings.TrimSpace(sessionV3MetadataString(message.Metadata, sessionsV3CheckpointResumeRoutingRunIDKey)) != runID {
			continue
		}
		checkpointID := strings.TrimSpace(sessionV3MetadataString(message.Metadata, sessionsV3CheckpointResumeRoutingCheckpointKey))
		if checkpointID == "" {
			return messages
		}
		routing := pebblestore.MessageSnapshot{
			SessionID: message.SessionID,
			Role:      "system",
			Content:   sessionsV3CheckpointResumeRoutingMessage(checkpointID),
			Metadata: map[string]any{
				"source":        "checkpoint_resume_routing",
				"synthetic":     true,
				"checkpoint_id": checkpointID,
				"run_id":        runID,
			},
			CreatedAt: message.CreatedAt,
		}
		out := make([]pebblestore.MessageSnapshot, 0, len(messages)+1)
		out = append(out, messages[:index]...)
		out = append(out, routing, message)
		out = append(out, messages[index+1:]...)
		return out
	}
	return messages
}

func sessionsV3CheckpointResumeRoutingMessage(checkpointID string) string {
	return fmt.Sprintf(`Active checkpoint resumed by this user message

This message continues checkpoint %s; it is not a new-session request. Do not call start_session_checkpoint or request_followup_checkpoint.

Interpret the message against the current objective and checklist:
- If it answers a question or selects a recommendation, continue without plan mutation.
- If it adds a bounded requirement, use add_subtask.
- If it replaces the current checklist, use replace_subtasks.
- If it invalidates the objective or acceptance criteria, use restart_checkpoint.

Preserve unrelated later checkpoints. Only replace/amend the larger plan if this direction invalidates or reorders them.`, strings.TrimSpace(checkpointID))
}
