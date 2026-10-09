package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Client tools are typed tools the session's client answers. A call is
// validated against the tool's input schema, recorded as a pending
// permission (an input boundary, like ask_user, so bypass never skips it),
// and answered by resolving that record: allow_once with approved_arguments
// {"result": <JSON>} returns the result to the model; deny returns the reason
// as the tool's error. Swarm executes nothing for a client tool.

const clientToolTimeoutReason = "client tool timed out"

func (s *Service) clientToolForAccount(accountScopeID, name string) (pebblestore.AgentCustomToolDefinition, bool) {
	if s == nil || s.agents == nil || strings.TrimSpace(accountScopeID) == "" || IsReservedToolName(name) {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	definition, ok, err := s.agents.GetCustomToolForAccount(accountScopeID, canonicalToolName(name))
	if err != nil || !ok || definition.Kind != pebblestore.AgentCustomToolKindClient {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	return definition, true
}

func (s *Service) clientToolForSession(sessionID, name string) (pebblestore.AgentCustomToolDefinition, bool) {
	if s == nil || s.sessions == nil || IsReservedToolName(name) {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	return s.clientToolForAccount(session.AccountScopeID, name)
}

func clientToolTimeout(definition pebblestore.AgentCustomToolDefinition) time.Duration {
	ms := definition.TimeoutMS
	if ms <= 0 {
		ms = pebblestore.AgentClientToolDefaultTimeoutMS
	}
	if ms > pebblestore.AgentClientToolMaxTimeoutMS {
		ms = pebblestore.AgentClientToolMaxTimeoutMS
	}
	return time.Duration(ms) * time.Millisecond
}

// clientToolOutput turns the client's resolution into the tool output the
// model sees. The result is labelled untrusted: it came from outside Swarm.
func clientToolOutput(definition pebblestore.AgentCustomToolDefinition, approvedArguments string) (string, error) {
	approvedArguments = strings.TrimSpace(approvedArguments)
	if len(approvedArguments) > agentruntime.ClientToolResultMaxBytes {
		return "", fmt.Errorf("client tool result exceeds %d bytes", agentruntime.ClientToolResultMaxBytes)
	}
	var payload map[string]any
	if approvedArguments == "" || json.Unmarshal([]byte(approvedArguments), &payload) != nil {
		return "", errors.New("client tool was approved without a result")
	}
	result, ok := payload["result"]
	if !ok {
		return "", errors.New(`client tool result must be sent as approved_arguments {"result": ...}`)
	}
	rendered, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(map[string]any{
		"tool":    canonicalToolName(definition.Name),
		"status":  "ok",
		"effect":  definition.Effect,
		"result":  result,
		"safety":  tool.UntrustedSafety(string(rendered)),
		"path_id": "tool.client.v1",
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
