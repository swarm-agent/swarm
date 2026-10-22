package pebblestore

import (
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
)

func (input V3SessionMutationInput) requireAccountLock() error {
	if !input.accountLockHeld {
		return errors.New("account-shared mutation requires account lock")
	}
	return nil
}

// Called only while holding the session's keyed lock. Incoming snapshots are
// conservative: they may introduce shared identity during this very mutation.
func (s *SessionStore) isPlainSessionMutation(input V3SessionMutationInput) (bool, error) {
	if input.TurnUsage != nil || input.MediaUsage != nil || input.UsageReset != nil || input.usageScopeRepair != nil ||
		input.TaskReport != nil || input.TaskDelivery != nil || input.TaskWait != nil || input.taskUpdate != nil ||
		input.PlanSave != nil && input.PlanSave.TaskPublication != nil ||
		input.DesignAllocation != nil || input.DesignAcceptance != nil || input.designChange != nil ||
		input.automationV2 != nil || input.AutomationBinding != nil || input.AutomationPermission != nil ||
		input.automationRealtime != nil || input.environmentRealtime != nil || input.projectRealtime != nil || input.workspaceCatalog != nil ||
		input.Session != nil || input.WorktreeRecovery != nil || len(input.MediaStagingBindings) != 0 ||
		input.Artifact != nil || input.ArtifactV2 != nil || input.ArtifactV3 != nil || input.Transcription != nil || input.VideoProject != nil ||
		input.RunIntent != nil && isV3RunIntentTerminal(input.RunIntent.Status) {
		return false, nil
	}
	if input.Message != nil && (len(input.Message.ArtifactSelections) != 0 || len(input.Message.VideoAttachments) != 0) {
		return false, nil // Selection validation reads another session's artifact authority.
	}
	current, found, err := s.GetSession(input.SessionID)
	if err != nil {
		return false, err
	}
	if !found {
		tombstone, ok, err := s.GetV3SessionTombstone(input.SessionID)
		if err != nil {
			return false, err
		}
		if ok {
			current, found = tombstone.Session, true
		}
	}
	if found {
		if ProjectConversationID(current) != "" {
			return false, nil
		}
		for _, key := range []string{"project_id", "task_id", "task_attempt_id", "project_task_id"} {
			if value, ok := current.Metadata[key]; ok && value != nil {
				text, isString := value.(string)
				if !isString || strings.TrimSpace(text) != "" {
					return false, nil
				}
			}
		}
	}
	// Synthetic account streams carry private shared participants; fail closed
	// for future participants rather than admitting them as ordinary sessions.
	return !strings.HasPrefix(input.SessionID, "__"), nil
}

// Daily accumulator writes share the same fail-closed evidence as scope totals.
func (s *SessionStore) setDailyUsageAccumulatorPayload(batch *pebble.Batch, acc DailyUsageAccumulator, payload []byte) error {
	if err := s.accountLockInput.requireAccountLock(); err != nil {
		return err
	}
	return batch.Set([]byte(KeyDailyUsageAccumulator(acc.AccountScopeID, acc.Date)), payload, nil)
}
