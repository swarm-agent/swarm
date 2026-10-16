package security

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func (s *Service) CreateRunnerJoinToken(account, user, name string) (string, error) {
	return s.authStore.CreateRunnerJoinToken(account, user, name, time.Now())
}
func (s *Service) JoinRunner(token string) (string, pebblestore.ScopedTokenRecord, error) {
	return s.authStore.ConsumeRunnerJoinToken(token, time.Now())
}
