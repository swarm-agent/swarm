package pebblestore

import "errors"

var ErrAgentModelSettingsStale = errors.New("agent model settings changed; reload before restoring defaults")

// RestoreForAccount replaces assignments only if the exact read record still holds.
func (s *AgentModelSettingsStore) RestoreForAccount(expected, replacement AgentModelSettingsRecord) (AgentModelSettingsRecord, error) {
	return s.updateCheckedForAccount(expected.AccountScopeID, func(current *AgentModelSettingsRecord) error {
		if *current != expected {
			return ErrAgentModelSettingsStale
		}
		current.Swarm = replacement.Swarm
		current.SystemAgents = replacement.SystemAgents
		current.UpdatedAt = replacement.UpdatedAt
		return nil
	})
}
