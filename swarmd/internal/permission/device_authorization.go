package permission

import (
	"fmt"
	"strings"
	"time"
)

func (s *Service) authorizeDeviceCall(input AuthorizationInput) (AuthorizationResult, error) {
	deny := func(reason string) (AuthorizationResult, error) {
		return AuthorizationResult{Decision: AuthorizationDeny, Requirement: RequirementClientTool, Source: "account_permissions", Reason: reason}, nil
	}
	session, run, clients, err := s.store.DeviceCallContext(input.SessionID, input.RunID)
	if err != nil {
		return AuthorizationResult{}, err
	}
	if session.AccountScopeID == "" || session.AccountScopeID != input.AccountScopeID {
		return deny("device call account does not match the session")
	}
	target := run.SendingClientSessionID
	args := parsePermissionJSONMap(input.ToolArguments)
	named := strings.TrimSpace(mapStringAny(args["device"]))
	if named != "" {
		target = named
	}
	now := time.Now().UnixMilli()
	id, name := "", ""
	for _, c := range clients {
		if c.AccountScopeID != input.AccountScopeID || c.Device == "" || c.RevokedAt != 0 || c.ExpiresAt <= now || c.LastSeenAt < now-120000 {
			continue
		}
		if c.SessionID == target || (named != "" && c.Device == named) {
			if id != "" {
				return deny("device name is ambiguous; name its client session id")
			}
			id, name = c.SessionID, c.Device
		}
	}
	if id == "" {
		return deny(fmt.Sprintf("target device %q is not connected", target))
	}
	p, err := s.store.GetAccountPermissions(input.AccountScopeID)
	if err != nil {
		return AuthorizationResult{}, err
	}
	rule := p.Device(id).Files
	kind := "files"
	if normalizePolicyToolName(input.ToolName) == "manage_browser" {
		rule = p.Device(id).Browser
		kind = "browser"
	}
	if rule != "ask" && rule != "allowed" {
		return deny(fmt.Sprintf("%s rule for device %q is off; change it on that device's Permissions page", kind, name))
	}
	execution := "awaiting_approval"
	if rule == "allowed" {
		if err := s.store.RecordPermissionUse(input.AccountScopeID, kind, id, now); err != nil {
			return AuthorizationResult{}, err
		}
		execution = "ready"
	}
	record, err := s.CreatePending(CreateInput{SessionID: input.SessionID, RunID: input.RunID, Step: input.Step, CallID: input.CallID, ToolName: input.ToolName, ToolArguments: input.ToolArguments, ToolCallArguments: input.ToolCallArguments, Requirement: RequirementClientTool, Mode: input.Mode, TargetDevice: id, Execution: execution})
	if err != nil {
		return AuthorizationResult{}, err
	}
	return AuthorizationResult{Decision: AuthorizationPending, Requirement: RequirementClientTool, Source: "account_permissions", Reason: fmt.Sprintf("%s rule for device %q: %s", kind, name, rule), Record: &record}, nil
}
