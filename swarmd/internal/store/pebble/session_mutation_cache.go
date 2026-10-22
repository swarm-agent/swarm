package pebblestore

import "encoding/json"

type cachedMutationSession struct {
	session SessionSnapshot
	found   bool
}

func (s *SessionStore) withMutationSessionCache() *SessionStore {
	local := *s
	local.mutationSessions = make(map[string]cachedMutationSession)
	return &local
}

// Cache the serialized value, preserving JSON's metadata number/omitempty
// semantics just as a subsequent storage read would. No storage read is needed.
func (s *SessionStore) cacheMutationSessionWrite(id string, payload []byte) error {
	if s.mutationSessions == nil {
		return nil
	}
	var session SessionSnapshot
	if err := json.Unmarshal(payload, &session); err != nil {
		return err
	}
	s.mutationSessions[id] = cachedMutationSession{session: session, found: true}
	return nil
}

// Metadata and model profile are copied by getSessionFromReader, as before.
// Copy the remaining reference fields too so a caller cannot mutate the cache.
func cloneMutationSession(s SessionSnapshot) SessionSnapshot {
	s.TemporaryWorkspaceRoots = append([]string(nil), s.TemporaryWorkspaceRoots...)
	if s.Lifecycle != nil {
		v := *s.Lifecycle
		s.Lifecycle = &v
	}
	if s.AutomationV2 != nil {
		v := *s.AutomationV2
		s.AutomationV2 = &v
	}
	if s.Automation != nil {
		v := *s.Automation
		v.Policy.AllowedTools = append([]string(nil), v.Policy.AllowedTools...)
		v.Policy.TargetIDs = append([]string(nil), v.Policy.TargetIDs...)
		s.Automation = &v
	}
	if s.WorkspaceGrants != nil {
		s.WorkspaceGrants = append([]WorkspaceGrant{}, s.WorkspaceGrants...)
		for i := range s.WorkspaceGrants {
			if s.WorkspaceGrants[i].Available != nil {
				v := *s.WorkspaceGrants[i].Available
				s.WorkspaceGrants[i].Available = &v
			}
		}
	}
	if s.WorkspaceUsage != nil {
		s.WorkspaceUsage = append([]WorkspaceUsageProjection{}, s.WorkspaceUsage...)
		for i := range s.WorkspaceUsage {
			if s.WorkspaceUsage[i].Available != nil {
				v := *s.WorkspaceUsage[i].Available
				s.WorkspaceUsage[i].Available = &v
			}
		}
	}
	return s
}
