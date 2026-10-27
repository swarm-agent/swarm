package session

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

func (s *Service) GetAccountPermissions(account string) (pebblestore.AccountPermissions, error) {
	return s.Store().GetAccountPermissions(account)
}

func (s *Service) RecordPermissionUse(account, kind, target string, at int64) error {
	return s.Store().RecordPermissionUse(account, kind, target, at)
}
