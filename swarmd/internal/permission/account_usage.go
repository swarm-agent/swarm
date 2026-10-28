package permission

import (
	store "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) recordResolvedRuleUse(record store.PermissionRecord, at int64) error {
	kind, target := "", record.TargetDevice
	if target != "" {
		kind = "files"
		if record.ToolName == "manage_browser" {
			kind = "browser"
		}
	} else if record.ToolName == "manage_projects" {
		target = mapStringAny(parsePermissionJSONMap(record.ToolArguments)["machine"])
		if target != "" {
			kind = "linked"
		}
	}
	if kind == "" {
		return nil
	}
	session, _, err := s.store.DeviceCallContext(record.SessionID, record.RunID)
	if err != nil {
		return err
	}
	return s.store.RecordPermissionUse(session.AccountScopeID, kind, target, at)
}
