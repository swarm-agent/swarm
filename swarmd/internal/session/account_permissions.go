package session

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

func (s *Service) GetAccountPermissions(account string) (pebblestore.AccountPermissions, error) {
	return s.Store().GetAccountPermissions(account)
}
