package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Repair only missing exact grants after authenticated historical recovery and
// fresh catalog validation. An existing conflicting identity is never replaced.
func (s *Server) reconcileTaskFollowupSourceGrants(p identity.Principal, task *pebblestore.ProjectTaskRecord, owned *pebblestore.SessionSnapshot) error {
	if !isProjectTaskFollowup(task) {
		return nil
	}
	grants := append([]pebblestore.WorkspaceGrant(nil), owned.WorkspaceGrants...)
	changed := false
	for _, source := range task.ProgramSources {
		matched := false
		for _, grant := range grants {
			if grant.Path != source.Path {
				continue
			}
			if grant.WorkspaceID != source.WorkspaceID || grant.WorkspaceGeneration != source.WorkspaceGeneration {
				return errors.New("follow-up session source grant conflicts with authenticated admission")
			}
			matched = true
		}
		if !matched {
			grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantAdditional, Path: source.Path, WorkspaceID: source.WorkspaceID, WorkspaceGeneration: source.WorkspaceGeneration})
			changed = true
		}
	}
	if !changed {
		return nil
	}
	next := *owned
	next.WorkspaceGrants = grants
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(grants)
	raw, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	key := fmt.Sprintf("project-task:followup-sources:%s:%s", task.ActiveAttemptID, hash)
	if _, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &next, ClientRequestID: key, IdempotencyKey: key, PayloadHash: hash, RequestHash: hash}); err != nil {
		return err
	}
	retained, found, err := s.sessions.Store().GetSession(owned.ID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("repaired follow-up session unavailable")
	}
	for _, source := range task.ProgramSources {
		matched := false
		for _, grant := range retained.WorkspaceGrants {
			matched = matched || (grant.Path == source.Path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration)
		}
		if !matched {
			return errors.New("follow-up source grant repair did not persist; explicit source reapproval required")
		}
	}
	*owned = retained
	return nil
}
