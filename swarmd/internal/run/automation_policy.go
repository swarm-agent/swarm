package run

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	store "swarm/packages/swarmd/internal/store/pebble"
)

var errAutomationPolicy = errors.New("automation execution policy denied")

// Only the immutable creation event owns the execution overlay. Metadata edits,
// checkpoint edits and recovered runs cannot replace or remove it.
func (s *Service) automationPolicy(sessionID string) (*store.AutomationAuthorizationPolicy, error) {
	if !strings.HasPrefix(sessionID, "automation-") {
		return nil, nil
	}
	if s == nil || s.sessions == nil {
		return nil, errAutomationPolicy
	}
	events, err := s.sessions.ListSessionEvents(sessionID, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].EventType != "session.created" {
		return nil, errAutomationPolicy
	}
	var payload struct {
		Session *store.SessionSnapshot `json:"session"`
	}
	if json.Unmarshal(events[0].Payload, &payload) != nil || payload.Session == nil || payload.Session.ID != sessionID {
		return nil, errAutomationPolicy
	}
	pinned := payload.Session
	current, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !found || current.AccountScopeID != pinned.AccountScopeID || current.WorkspacePath != pinned.WorkspacePath || current.WorktreeRootPath != pinned.WorktreeRootPath || !current.WorktreeEnabled {
		return nil, errAutomationPolicy
	}
	if !reflect.DeepEqual(current.WorkspaceGrants, pinned.WorkspaceGrants) {
		return nil, errAutomationPolicy
	}
	for _, key := range []string{"swarm_v3_runtime_kind", "swarm_v3_runtime_swarm_id", "swarm_v3_authority_host_swarm_id", "swarm_v3_runtime_workspace_path"} {
		if !reflect.DeepEqual(current.Metadata[key], pinned.Metadata[key]) {
			return nil, errAutomationPolicy
		}
	}
	var policy store.AutomationAuthorizationPolicy
	encoded, ok := pinned.Metadata["automation_execution_policy"].(string)
	if !ok || json.Unmarshal([]byte(encoded), &policy) != nil {
		return nil, errAutomationPolicy
	}
	if err := automationTargetPolicy(policy, pinned.Metadata); err != nil {
		return nil, err
	}
	if len(policy.AllowedTools) != 0 || len(policy.TargetIDs) != 0 {
		plan, found, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return nil, err
		}
		if found && plan.Document != nil {
			for _, checkpoint := range plan.Document.Checkpoints {
				if checkpoint.TaskProgram != nil {
					return nil, errAutomationPolicy
				}
			}
		}
	}
	return &policy, nil
}

func automationTargetPolicy(policy store.AutomationAuthorizationPolicy, metadata map[string]any) error {
	if len(policy.TargetIDs) == 0 {
		return nil
	}
	// The local canonicalizer, not the approval's strings, supplies identity.
	kind, _ := metadata["swarm_v3_runtime_kind"].(string)
	runtime, _ := metadata["swarm_v3_runtime_swarm_id"].(string)
	host, _ := metadata["swarm_v3_authority_host_swarm_id"].(string)
	if kind != "host" || runtime == "" || runtime != host {
		return errAutomationPolicy
	}
	for _, target := range policy.TargetIDs {
		if target != runtime {
			return errAutomationPolicy
		}
	}
	return nil
}

func automationToolPermitted(policy *store.AutomationAuthorizationPolicy, name string) bool {
	if policy == nil || (len(policy.AllowedTools) == 0 && len(policy.TargetIDs) == 0) {
		return true
	}
	name = canonicalToolName(name)
	// No delegated execution, session deployment or opaque custom capabilities.
	switch name {
	case "read", "write", "edit", "find", "search", "list", "plan_manage", "compact", "bash", "websearch", "webfetch", "webdownload", "media_inspect":
	default:
		return false
	}
	if len(policy.TargetIDs) != 0 {
		// These tools operate within the existing canonical workspace scope. Shell,
		// network, deployment and arbitrary custom tools cannot prove target bounds.
		switch name {
		case "read", "write", "edit", "find", "search", "list", "plan_manage", "compact":
		default:
			return false
		}
	}
	if len(policy.AllowedTools) == 0 {
		return true
	}
	for _, allowed := range policy.AllowedTools {
		if name == allowed {
			return true
		}
	}
	return false
}

func (s *Service) enforceAutomationTool(sessionID, name string) error {
	policy, err := s.automationPolicy(sessionID)
	if err != nil {
		return err
	}
	if !automationToolPermitted(policy, name) {
		return errAutomationPolicy
	}
	return nil
}
