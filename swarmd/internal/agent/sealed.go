package agent

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SealedAgentError reports why name is not a sealed agent in the account, or
// nil when it is one: an enabled, unprotected agent on the custom preset that
// inherits no policy and enables at least one tool, every one of them a client
// tool. A sealed agent runs on its own definition only: no workspace rule
// files, project context or account memory reach its prompt.
func (s *Service) SealedAgentError(accountScopeID, name string) error {
	name = strings.TrimSpace(name)
	if s == nil || name == "" {
		return errors.New("no agent")
	}
	profile, ok, err := s.GetProfileForAccount(accountScopeID, name)
	if err != nil {
		return err
	}
	if !ok || !profile.Enabled {
		return fmt.Errorf("agent %q is not available", name)
	}
	if profile.Protected || profile.ToolContract == nil || strings.TrimSpace(profile.ToolContract.Preset) != "custom" || profile.ToolContract.InheritPolicy {
		return fmt.Errorf("agent %q is not sealed: it must use the custom preset with only client tools", name)
	}
	clientTools := 0
	for tool, config := range profile.ToolContract.Tools {
		if config.Enabled == nil || !*config.Enabled {
			continue
		}
		definition, found, err := s.GetCustomToolForAccount(accountScopeID, tool)
		if err != nil {
			return err
		}
		if !found || definition.Kind != pebblestore.AgentCustomToolKindClient {
			return fmt.Errorf("agent %q is not sealed: tool %q is not a client tool", name, tool)
		}
		clientTools++
	}
	// A custom agent with every tool off is an ordinary chat agent, not a
	// sealed one: it keeps the normal prompt and no sealed limits.
	if clientTools == 0 {
		return fmt.Errorf("agent %q is not sealed: it has no client tools", name)
	}
	return nil
}
