package pebblestore

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// TaskEnvironmentMutation requires both revisions; zero attachment revision means
// create-only. Attachment is resolved by the backend, never trusted from a client.
// These storage guards are not principal authorization or deployment validation.
type TaskEnvironmentMutation struct {
	ExpectedTaskRevision       int
	ExpectedAttachmentRevision int
	AttachmentID               string
	Attachment                 *environments.TaskEnvironmentAttachment // nil detaches
}

// MutateProjectTaskEnvironment changes only attachment evidence and task revision.
// It neither enqueues execution nor releases leases, stops or deletes deployments.
func (s *SessionStore) MutateProjectTaskEnvironment(account, project, taskID string, req TaskEnvironmentMutation) (*ProjectTaskRecord, error) {
	if req.ExpectedTaskRevision <= 0 || req.ExpectedAttachmentRevision < 0 || req.AttachmentID == "" {
		return nil, errors.New("exact task and attachment revisions and identity required")
	}
	return s.UpdateProjectTask(account, project, taskID, func(t *ProjectTaskRecord) error {
		if t.AccountID != account || t.ProjectID != project || t.ID != taskID {
			return errors.New("task identity mismatch")
		}
		if t.Revision != req.ExpectedTaskRevision {
			return errors.New("stale task revision")
		}
		for _, retired := range t.RetiredEnvironmentAttachmentIDs {
			if retired == req.AttachmentID {
				return errors.New("attachment identity retired; use a fresh attachment ID")
			}
		}
		index := -1
		for i, a := range t.EnvironmentAttachments {
			if a.ID == req.AttachmentID {
				index = i
				if a.Revision != req.ExpectedAttachmentRevision {
					return errors.New("stale attachment revision")
				}
			}
		}
		if index < 0 && (req.ExpectedAttachmentRevision != 0 || req.Attachment == nil) {
			return errors.New("attachment not found")
		}
		if req.Attachment == nil {
			if len(t.RetiredEnvironmentAttachmentIDs) >= 4096 {
				return errors.New("task attachment history limit reached")
			}
			t.RetiredEnvironmentAttachmentIDs = append(t.RetiredEnvironmentAttachmentIDs, req.AttachmentID)
			t.EnvironmentAttachments = append(t.EnvironmentAttachments[:index], t.EnvironmentAttachments[index+1:]...)
		} else {
			a := *req.Attachment
			if a.ID != req.AttachmentID || a.Revision != req.ExpectedAttachmentRevision+1 || a.AccountScopeID != account || a.ProjectID != project || a.TaskID != taskID {
				return errors.New("attachment identity or revision mismatch")
			}
			if a.AttemptID != "" && a.AttemptID != t.ActiveAttemptID {
				return errors.New("attachment must explicitly target the current attempt")
			}
			if err := a.Validate(); err != nil {
				return err
			}
			if a.ExpiresAt <= time.Now().UnixMilli() {
				return errors.New("attachment expiry must be in the future")
			}
			if index < 0 {
				if len(t.EnvironmentAttachments) >= environments.MaxTaskEnvironmentAttachments {
					return errors.New("task attachment limit reached")
				}
				t.EnvironmentAttachments = append(t.EnvironmentAttachments, a)
			} else {
				t.EnvironmentAttachments[index] = a
			}
		}
		t.Revision++
		return nil
	})
}

func (t *ProjectTaskRecord) validateEnvironmentAttachments() error {
	if len(t.EnvironmentAttachments) > environments.MaxTaskEnvironmentAttachments {
		return errors.New("task attachment limit reached")
	}
	if len(t.RetiredEnvironmentAttachmentIDs) > 4096 {
		return errors.New("task attachment history limit reached")
	}
	seen := make(map[string]bool, len(t.EnvironmentAttachments))
	for _, id := range t.RetiredEnvironmentAttachmentIDs {
		if id == "" || len(id) > 256 || seen[id] {
			return errors.New("invalid retired attachment identity")
		}
		seen[id] = true
	}
	for _, a := range t.EnvironmentAttachments {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("task environment attachment: %w", err)
		}
		if seen[a.ID] || a.AccountScopeID != t.AccountID || a.ProjectID != t.ProjectID || a.TaskID != t.ID {
			return errors.New("duplicate or mismatched task attachment identity")
		}
		seen[a.ID] = true
	}
	return nil
}

// Legacy full-record writes must not drop a newer attachment snapshot. Reopen
// preserves evidence; an attempt mismatch is projected stale, never rebound.
func validateTaskEnvironmentWrite(prior, next *ProjectTaskRecord) error {
	if len(next.RetiredEnvironmentAttachmentIDs) < len(prior.RetiredEnvironmentAttachmentIDs) {
		return errors.New("attachment revocation history cannot be removed")
	}
	for i, id := range prior.RetiredEnvironmentAttachmentIDs {
		if next.RetiredEnvironmentAttachmentIDs[i] != id {
			return errors.New("attachment revocation history cannot be rewritten")
		}
	}
	for _, old := range prior.EnvironmentAttachments {
		found := false
		for _, current := range next.EnvironmentAttachments {
			if current.ID != old.ID {
				continue
			}
			found = true
			if !reflect.DeepEqual(old, current) && current.Revision != old.Revision+1 {
				return errors.New("attachment revision must advance exactly once")
			}
		}
		if !found {
			retired := false
			for _, id := range next.RetiredEnvironmentAttachmentIDs {
				if id == old.ID {
					retired = true
				}
			}
			if !retired {
				return errors.New("removed attachment must retain revocation tombstone")
			}
		}
	}
	if (!reflect.DeepEqual(prior.EnvironmentAttachments, next.EnvironmentAttachments) || !reflect.DeepEqual(prior.RetiredEnvironmentAttachmentIDs, next.RetiredEnvironmentAttachmentIDs)) && next.Revision <= prior.Revision {
		return errors.New("stale task attachment snapshot")
	}
	return nil
}

func taskEnvironmentProjection(t ProjectTaskRecord) []environments.TaskEnvironmentAttachment {
	out := append([]environments.TaskEnvironmentAttachment(nil), t.EnvironmentAttachments...)
	for i := range out {
		out[i].State = out[i].EffectiveState(t.ActiveAttemptID, time.Now().UnixMilli())
	}
	return out
}
