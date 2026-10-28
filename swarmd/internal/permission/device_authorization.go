package permission

import (
	"fmt"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// deviceTarget is the connected device a device tool call goes to.
type deviceTarget struct {
	id, name string
}

// resolveDeviceTarget finds the connected device a device tool call goes to:
// the one named in the call's "device" argument, else the device that sent
// the run's message. A run without a sending device (a delegated or started
// run, or a message sent through the API) goes to the account's only
// connected device. With several connected it is refused: the device tool
// schemas declare no "device" property (and reject unknown ones), so the
// agent cannot pick one and the person must send the request from the
// device to use. It returns a refusal reason when no single connected device
// matches.
func (s *Service) resolveDeviceTarget(input AuthorizationInput) (deviceTarget, string, error) {
	session, run, clients, err := s.store.DeviceCallContext(input.SessionID, input.RunID)
	if err != nil {
		return deviceTarget{}, "", err
	}
	if session.AccountScopeID == "" || session.AccountScopeID != input.AccountScopeID {
		return deviceTarget{}, "device call account does not match the session", nil
	}
	target := strings.TrimSpace(run.SendingClientSessionID)
	args := parsePermissionJSONMap(input.ToolArguments)
	named := strings.TrimSpace(mapStringAny(args["device"]))
	if named != "" {
		target = named
	}
	now := time.Now().UnixMilli()
	connected := make([]pebblestore.ClientSessionRecord, 0, len(clients))
	for _, c := range clients {
		if c.AccountScopeID != input.AccountScopeID || c.Device == "" || c.RevokedAt != 0 || c.ExpiresAt <= now || c.LastSeenAt < now-120000 {
			continue
		}
		connected = append(connected, c)
	}
	if target == "" {
		switch len(connected) {
		case 0:
			return deviceTarget{}, "no device is connected; the person opens their app on the device to use", nil
		case 1:
			return deviceTarget{id: connected[0].SessionID, name: connected[0].Device}, "", nil
		default:
			return deviceTarget{}, fmt.Sprintf("several devices are connected (%s) and this run has no sending device to choose between them; the person sends the request from the device to use", connectedDeviceNames(connected)), nil
		}
	}
	var found deviceTarget
	for _, c := range connected {
		if c.SessionID == target || (named != "" && c.Device == named) {
			if found.id != "" {
				return deviceTarget{}, "device name is ambiguous; name its client session id", nil
			}
			found = deviceTarget{id: c.SessionID, name: c.Device}
		}
	}
	if found.id == "" {
		return deviceTarget{}, fmt.Sprintf("target device %q is not connected", target), nil
	}
	return found, "", nil
}

func connectedDeviceNames(clients []pebblestore.ClientSessionRecord) string {
	names := make([]string, 0, len(clients))
	for _, c := range clients {
		names = append(names, fmt.Sprintf("%q", c.Device))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// deviceRule is the account rule for the device tool's kind on a device.
func deviceRule(p pebblestore.AccountPermissions, id, toolName string) (rule, kind string) {
	switch normalizePolicyToolName(toolName) {
	case "manage_browser", pebblestore.BrowserToolRequestAccess:
		return p.Device(id).Browser, "browser"
	}
	return p.Device(id).Files, "files"
}

func (s *Service) authorizeDeviceCall(input AuthorizationInput) (AuthorizationResult, error) {
	deny := func(reason string) (AuthorizationResult, error) {
		return AuthorizationResult{Decision: AuthorizationDeny, Requirement: RequirementClientTool, Source: "account_permissions", Reason: reason}, nil
	}
	target, refusal, err := s.resolveDeviceTarget(input)
	if err != nil {
		return AuthorizationResult{}, err
	}
	if refusal != "" {
		return deny(refusal)
	}
	id, name := target.id, target.name
	p, err := s.store.GetAccountPermissions(input.AccountScopeID)
	if err != nil {
		return AuthorizationResult{}, err
	}
	rule, kind := deviceRule(p, id, input.ToolName)
	if rule != pebblestore.RuleAllowed {
		return deny(fmt.Sprintf("%s rule for device %q is off; change it on that device's Permissions page", kind, name))
	}
	// Device calls never wait on a person: an allowed call is recorded as
	// ready, which only the target device's client completes with its result.
	if err := s.store.RecordPermissionUse(input.AccountScopeID, kind, id, time.Now().UnixMilli()); err != nil {
		return AuthorizationResult{}, err
	}
	record, err := s.CreatePending(CreateInput{SessionID: input.SessionID, RunID: input.RunID, Step: input.Step, CallID: input.CallID, ToolName: input.ToolName, ToolArguments: input.ToolArguments, ToolCallArguments: input.ToolCallArguments, Requirement: RequirementClientTool, Mode: input.Mode, TargetDevice: id, Execution: "ready"})
	if err != nil {
		return AuthorizationResult{}, err
	}
	return AuthorizationResult{Decision: AuthorizationPending, Requirement: RequirementClientTool, Source: "account_permissions", Reason: fmt.Sprintf("%s rule for device %q: allowed", kind, name), Record: &record}, nil
}

// deviceAccessRequestAnswer answers request_browser_access and
// request_machine_access from the target device's account rule. Access is a
// rule of the device, never a grant in a conversation, so the request never
// reaches the person's app: it says whether the device's rule already lets
// the agent act there (and which tool to use) or how the person changes it.
func (s *Service) deviceAccessRequestAnswer(input AuthorizationInput) (string, error) {
	tools := "machine_run, machine_read and machine_write"
	if normalizePolicyToolName(input.ToolName) == pebblestore.BrowserToolRequestAccess {
		tools = "manage_browser"
	}
	target, refusal, err := s.resolveDeviceTarget(input)
	if err != nil {
		return "", err
	}
	if refusal != "" {
		return refusal, nil
	}
	p, err := s.store.GetAccountPermissions(input.AccountScopeID)
	if err != nil {
		return "", err
	}
	rule, kind := deviceRule(p, target.id, input.ToolName)
	if rule == pebblestore.RuleAllowed {
		return fmt.Sprintf("no request is needed: the %s rule for device %q is allowed; call %s directly", kind, target.name, tools), nil
	}
	return fmt.Sprintf("the %s rule for device %q is off; the person changes it on that device's Permissions page (/v3/permissions); a request cannot grant access", kind, target.name), nil
}
