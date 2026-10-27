package permission

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

// ResolveFromClient binds device completion to authenticated transport identity,
// never an id supplied in the result arguments.
func (s *Service) ResolveFromClient(sessionID, permissionID, action, reason, arguments, clientSessionID string) (pebblestore.PermissionRecord, *PolicyRule, error) {
	record, err := s.lookupPermission(sessionID, permissionID)
	if err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	}
	if record.TargetDevice == "" {
		return s.ResolveWithPolicyAndArguments(sessionID, permissionID, action, reason, arguments)
	}
	action, err = normalizeResolveAction(action)
	if err != nil {
		return pebblestore.PermissionRecord{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated, _, err := s.resolveForDeviceLocked(sessionID, permissionID, action, reason, arguments, clientSessionID, time.Now().UnixMilli())
	return updated, nil, err
}
