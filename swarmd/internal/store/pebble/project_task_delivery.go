package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const V3SessionMutationDeliverTasks = "project_task.delivered"
const projectTaskDeliveryLimit = 16

// Receipts acknowledge successful provider consumption, never enqueue or semantic
// acceptance. The immutable source event remains retained after the pending index
// is removed in the same V3 mutation batch as the receipt.
type V3ProjectTaskDeliveryMutation struct {
	RunID   string
	Updates []ProjectTaskUpdate
}

func taskUpdateRoot(owner V3SessionRunIntent) string {
	if owner.TaskUpdateRootRunID != "" {
		return owner.TaskUpdateRootRunID
	}
	return owner.RunID
}
func taskUpdatePrefix(parent, root string) string {
	sum := sha256.Sum256([]byte(parent + "\x00" + root))
	return "v3:task-update:pending:" + hex.EncodeToString(sum[:]) + ":"
}
func taskUpdateKey(u ProjectTaskUpdate) string {
	return taskUpdatePrefix(u.ParentSessionID, u.ParentRunID) + fmt.Sprintf("%020d:%s", u.QueueSeq, u.EventID)
}
func (s *SessionStore) taskUpdateRaw(key string) ([]byte, error) {
	raw, closer, err := s.store.db.Get([]byte(key))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), raw...), nil
}
func taskUpdateFenceKey(parent string) string { return "v3:task-update:fence:" + parent }
func taskUpdateOwnerFenceKey(owner V3SessionRunIntent) string {
	return "v3:task-update:owner-fence:" + taskUpdatePrefix(owner.SessionID, taskUpdateRoot(owner))
}
func (s *SessionStore) validateTaskUpdateOwnerFence(owner V3SessionRunIntent) error {
	var captured string
	found, err := s.store.GetJSON(taskUpdateOwnerFenceKey(owner), &captured)
	if err != nil {
		return err
	}
	current, err := s.taskUpdateFence(owner.SessionID)
	if err != nil {
		return err
	}
	if !found || current != captured {
		return ErrProjectTaskWaitStale
	}
	return nil
}
func (s *SessionStore) taskUpdateFence(parent string) (string, error) {
	raw, err := s.taskUpdateRaw(taskUpdateFenceKey(parent))
	if errors.Is(err, pebble.ErrNotFound) {
		return "", nil
	}
	return string(raw), err
}

// Called under project/account/session locks; the account lock also serializes
// parent lifecycle mutations against child report publication.
func (s *SessionStore) bindProjectTaskUpdate(u *ProjectTaskUpdate) error {
	var binding projectTaskUpdateBinding
	found, err := s.store.GetJSON(taskUpdateBindingKey(u.SessionID, u.ParentSessionID), &binding)
	if err != nil {
		return err
	}
	if found {
		u.ParentRunID, u.ParentEpochID, u.ParentFence = binding.Root, binding.Epoch, binding.Fence
	}
	return nil
}

func (s *SessionStore) projectTaskDeliveryOwner(account, user, parent, run string) (V3SessionRunIntent, error) {
	session, found, err := s.GetSession(parent)
	if err != nil {
		return V3SessionRunIntent{}, err
	}
	if !found {
		return V3SessionRunIntent{}, ErrProjectTaskWaitStale
	}
	if err := s.ValidateProjectConversation(session, account, user); err != nil {
		return V3SessionRunIntent{}, err
	}
	tomb, found, err := s.GetV3SessionTombstone(parent)
	if err != nil {
		return V3SessionRunIntent{}, err
	}
	if found && (tomb.Archived || tomb.Deleted) {
		return V3SessionRunIntent{}, ErrProjectTaskWaitStale
	}
	state, found, err := s.GetV3SessionRunState(parent)
	if err != nil {
		return V3SessionRunIntent{}, err
	}
	if !found || state.RunID != run {
		return V3SessionRunIntent{}, ErrProjectTaskWaitStale
	}
	owner, found, err := s.GetV3SessionRunIntent(parent, run)
	if err != nil {
		return owner, err
	}
	if !found || owner.AccountScopeID != account || owner.UserID != user || (owner.Status != V3RunIntentRunning && owner.Status != V3RunIntentWaitingTasks) {
		return owner, ErrProjectTaskWaitStale
	}
	epoch, ok, err := s.GetActiveExecutionEpoch(parent)
	if err != nil {
		return owner, err
	}
	if owner.EpochID != "" && (!ok || epoch.EpochID != owner.EpochID) {
		return owner, ErrProjectTaskWaitStale
	}
	return owner, s.validateTaskUpdateOwnerFence(owner)
}

func (s *SessionStore) taskUpdateEligible(u ProjectTaskUpdate, owner V3SessionRunIntent, fence string) (bool, error) {
	if u.ParentSessionID != owner.SessionID || u.AccountScopeID != owner.AccountScopeID || u.UserID != owner.UserID || u.ParentRunID != taskUpdateRoot(owner) || u.ParentEpochID != owner.EpochID || u.ParentFence != fence {
		return false, nil
	}
	task, ok, err := s.GetProjectTask(u.AccountScopeID, u.ProjectID, u.TaskID)
	if err != nil || !ok || task.Archived {
		return false, err
	}
	task.EnsureTaskAttempts()
	a := task.ActiveAttempt()
	if a == nil || a.ID != u.AttemptID || a.SessionID != u.SessionID {
		return false, nil
	}
	if owner.TaskWait != nil {
		if owner.TaskWait.ProjectID != u.ProjectID {
			return false, nil
		}
		for _, target := range owner.TaskWait.Tasks {
			if target.TaskID == u.TaskID && target.AttemptID == u.AttemptID && target.SessionID == u.SessionID {
				return true, nil
			}
		}
		return false, nil
	}
	return true, nil
}

// Reads one bounded page of unconsumed events. A cursor is only an optimization
// for this provider loop; it never marks delivery. Restart begins at the retained
// pending index again. Waiting eligibility scans pages without provider work.
func (s *SessionStore) PendingProjectTaskUpdates(account, user, parent, run, after string) ([]ProjectTaskUpdate, string, error) {
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	unlock := s.store.sessionMutations.lockSessions(parent, "account:"+account)
	defer unlock()
	owner, err := s.projectTaskDeliveryOwner(account, user, parent, run)
	if err != nil {
		return nil, "", err
	}
	return s.pendingProjectTaskUpdates(owner, after)
}
func (s *SessionStore) pendingProjectTaskUpdates(owner V3SessionRunIntent, after string) ([]ProjectTaskUpdate, string, error) {
	fence, err := s.taskUpdateFence(owner.SessionID)
	if err != nil {
		return nil, "", err
	}
	var updates []ProjectTaskUpdate
	next, scanned := "", 0
	start := ""
	if after != "" {
		start = after + "\x00"
	}
	err = scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: taskUpdatePrefix(owner.SessionID, taskUpdateRoot(owner)), StartKey: start, Limit: 64}, func(key string, raw []byte) (bool, error) {
		scanned++
		next = key
		var u ProjectTaskUpdate
		if err := json.Unmarshal(raw, &u); err != nil {
			return false, err
		}
		eligible, err := s.taskUpdateEligible(u, owner, fence)
		if err != nil {
			return false, err
		}
		if eligible {
			updates = append(updates, u)
		}
		return len(updates) < projectTaskDeliveryLimit, nil
	})
	if scanned < 64 && len(updates) < projectTaskDeliveryLimit {
		next = ""
	}
	return updates, next, err
}
func (s *SessionStore) projectTaskUpdateWakeEligible(owner V3SessionRunIntent) (bool, error) {
	after := ""
	for {
		updates, next, err := s.pendingProjectTaskUpdates(owner, after)
		if err != nil {
			return false, err
		}
		for _, u := range updates {
			if u.Kind == ProjectTaskUpdateAttention || u.Kind == ProjectTaskUpdateWakeRequest {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		after = next
	}
}

// A provider-managed wait tool can commit its wake before that same provider
// response returns. Acknowledge its exact input only while the deterministic
// successor is still pending, never after stop, a new user message or admission.
func (s *SessionStore) projectTaskReceiptOwner(account, user, parent, run string) (V3SessionRunIntent, error) {
	owner, err := s.projectTaskDeliveryOwner(account, user, parent, run)
	if !errors.Is(err, ErrProjectTaskWaitStale) {
		return owner, err
	}
	old, found, readErr := s.GetV3SessionRunIntent(parent, run)
	if readErr != nil {
		return old, readErr
	}
	if !found || old.Status != V3RunIntentCompleted || old.TaskWait == nil || old.AccountScopeID != account || old.UserID != user {
		return old, err
	}
	state, found, readErr := s.GetV3SessionRunState(parent)
	if readErr != nil {
		return old, readErr
	}
	if !found || state.RunID != ProjectTaskWaitResumeID(run) || state.Status != V3RunIntentPendingExecutor {
		return old, err
	}
	next, found, readErr := s.GetV3SessionRunIntent(parent, state.RunID)
	if readErr != nil {
		return old, readErr
	}
	if !found || next.TaskWaitOwnerRunID != run || taskUpdateRoot(next) != taskUpdateRoot(old) || next.EpochID != old.EpochID {
		return old, err
	}
	session, found, readErr := s.GetSession(parent)
	if readErr != nil {
		return old, readErr
	}
	if !found {
		return old, err
	}
	if readErr := s.ValidateProjectConversation(session, account, user); readErr != nil {
		return old, readErr
	}
	tomb, found, readErr := s.GetV3SessionTombstone(parent)
	if readErr != nil {
		return old, readErr
	}
	if found && (tomb.Archived || tomb.Deleted) {
		return old, err
	}
	epoch, found, readErr := s.GetActiveExecutionEpoch(parent)
	if readErr != nil {
		return old, readErr
	}
	if old.EpochID != "" && (!found || epoch.EpochID != old.EpochID) {
		return old, err
	}
	return old, s.validateTaskUpdateOwnerFence(old)
}

func (s *SessionStore) prepareProjectTaskDelivery(input *V3SessionMutationInput) error {
	op := input.TaskDelivery
	if op == nil {
		return nil
	}
	if input.Kind != V3SessionMutationDeliverTasks || input.RunIntent != nil || input.Message != nil || input.Session != nil || input.TaskReport != nil || input.TaskWait != nil || len(op.Updates) < 1 || len(op.Updates) > projectTaskDeliveryLimit {
		return errors.New("invalid task delivery receipt")
	}
	owner, err := s.projectTaskReceiptOwner(input.AccountScopeID, input.UserID, input.SessionID, op.RunID)
	if err != nil {
		return err
	}
	fence, err := s.taskUpdateFence(input.SessionID)
	if err != nil {
		return err
	}
	// Input was selected before this provider step; a wait tool may select a
	// smaller task set during the step without invalidating consumed input.
	owner.TaskWait = nil
	for _, u := range op.Updates {
		eligible, err := s.taskUpdateEligible(u, owner, fence)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrProjectTaskWaitStale
		}
		// Immutable identity and bytes, not caller-provided event IDs alone.
		raw, err := s.taskUpdateRaw(taskUpdateKey(u))
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(u)
		if string(raw) != string(expected) {
			return errors.New("task update receipt does not match retained event")
		}
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	input.PayloadHash, input.RequestHash = hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])
	input.EventType, input.EventPayload = "session.task.delivered", raw
	return nil
}

type projectTaskUpdateBinding struct{ Root, Epoch, Fence string }

func taskUpdateBindingKey(child, parent string) string {
	sum := sha256.Sum256([]byte(child + "\x00" + parent))
	return "v3:task-update:binding:" + hex.EncodeToString(sum[:])
}

// Capture a generation at deployment, not when a delayed report arrives. An
// explicit wait may select a retained attempt for the new goal; incidental
// reports and checkpoint self-parent runs can never retarget it.
func (s *SessionStore) setTaskUpdateBinding(batch *pebble.Batch, child string, owner V3SessionRunIntent) error {
	fence, err := s.taskUpdateFence(owner.SessionID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(projectTaskUpdateBinding{taskUpdateRoot(owner), owner.EpochID, fence})
	if err != nil {
		return err
	}
	return batch.Set([]byte(taskUpdateBindingKey(child, owner.SessionID)), raw, nil)
}
func (s *SessionStore) setProjectTaskDeliveryInBatch(batch *pebble.Batch, input V3SessionMutationInput, seq, endpointSeq uint64) error {
	if input.RunIntent != nil {
		owner := *input.RunIntent
		owner.SessionID = input.SessionID
		var captured string
		exists, err := s.store.GetJSON(taskUpdateOwnerFenceKey(owner), &captured)
		if err != nil {
			return err
		}
		_, existingRun, err := s.GetV3SessionRunIntent(input.SessionID, owner.RunID)
		if err != nil {
			return err
		}
		if !exists && !existingRun {
			fence, err := s.taskUpdateFence(input.SessionID)
			if err != nil {
				return err
			}
			if input.Message != nil && strings.EqualFold(strings.TrimSpace(input.Message.Role), "user") && input.Message.Metadata["feedback_note"] != true {
				fence = fmt.Sprint(seq)
			}
			raw, _ := json.Marshal(fence)
			if err := batch.Set([]byte(taskUpdateOwnerFenceKey(owner)), raw, nil); err != nil {
				return err
			}
		}
	}
	if input.RunIntent != nil && input.RunIntent.ParentSessionID != "" && input.RunIntent.ParentSessionID != input.SessionID {
		parent := input.RunIntent.ParentSessionID
		var binding projectTaskUpdateBinding
		exists, err := s.store.GetJSON(taskUpdateBindingKey(input.SessionID, parent), &binding)
		if err != nil {
			return err
		}
		if !exists {
			state, found, err := s.GetV3SessionRunState(parent)
			if err != nil {
				return err
			}
			if found && (state.Status == V3RunIntentRunning || state.Status == V3RunIntentWaitingTasks) {
				owner, found, err := s.GetV3SessionRunIntent(parent, state.RunID)
				if err != nil {
					return err
				}
				if found && owner.AccountScopeID == input.AccountScopeID && owner.UserID == input.UserID {
					if err := s.setTaskUpdateBinding(batch, input.SessionID, owner); err != nil {
						return err
					}
				}
			}
		}
	}
	if input.TaskWait != nil && !input.TaskWait.Wake && input.RunIntent != nil && input.RunIntent.TaskWait != nil {
		for _, target := range input.RunIntent.TaskWait.Tasks {
			if err := s.setTaskUpdateBinding(batch, target.SessionID, *input.RunIntent); err != nil {
				return err
			}
		}
	}
	if input.Message != nil && strings.EqualFold(strings.TrimSpace(input.Message.Role), "user") && input.Message.Metadata["feedback_note"] != true {
		if err := batch.Set([]byte(taskUpdateFenceKey(input.SessionID)), []byte(fmt.Sprint(seq)), nil); err != nil {
			return err
		}
	}
	if input.taskUpdate != nil {
		u := *input.taskUpdate
		u.EventSeq, u.QueueSeq = seq, endpointSeq
		raw, err := json.Marshal(u)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(taskUpdateKey(u)), raw, nil); err != nil {
			return err
		}
	}
	if input.TaskDelivery != nil {
		for _, u := range input.TaskDelivery.Updates {
			if err := batch.Delete([]byte(taskUpdateKey(u)), nil); err != nil {
				return err
			}
		}
	}
	return nil
}
