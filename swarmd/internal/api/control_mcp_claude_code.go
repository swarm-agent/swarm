package api

import (
	"context"
	"net/http"
	"os/user"
	"strings"

	"swarm/packages/swarmd/internal/provider/claudecode"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Claude Code over Swarm Control (swarm_connect_claude_code). Swarm runs the
// unmodified `claude` CLI as the daemon's own OS user, so that user needs the
// CLI and its own Claude sign-in; Swarm never reads, copies or stores Claude
// credentials. The machine side (install, path, sign-in) is the installer's
// `claude-code` command, which the person runs; this tool reports that state
// and, once the CLI is signed in, makes the provider active for the caller's
// account through the same credential route and handler as the setup page's
// "Save and make active" (POST /v1/auth/credentials, type cli, active). The
// record it stores holds no secret.

// controlMCPClaudeCodeSetupCommand is the command the person runs on the
// machine: Swarm's headless installer keeps its checkout at this location
// (INSTALLER in containers/headless/app/install.sh).
const controlMCPClaudeCodeSetupCommand = "sudo bash /var/lib/swarm-headless/install.sh claude-code"

// claudeCodeStatusRefresher is the CLI's uncached probe, so a sign-in the
// person just finished shows at once.
type claudeCodeStatusRefresher interface {
	Refresh(ctx context.Context) claudecode.CLIStatus
}

func (c *controlMCPCall) claudeCodeCLIStatus() claudecode.CLIStatus {
	if refresher, ok := c.server.claudeCodeUsage.(claudeCodeStatusRefresher); ok {
		return refresher.Refresh(c.request.Context())
	}
	return c.server.claudeCodeUsage.Status(c.request.Context())
}

// claudeCodeCredential is the account's Claude Code CLI login record, the
// active one first, and whether that one is active.
func (c *controlMCPCall) claudeCodeCredential(accountScopeID string) (id string, active bool, err error) {
	list, err := c.server.auth.ListCredentialsForAccount(accountScopeID, claudecode.ProviderID, "", 200)
	if err != nil {
		return "", false, err
	}
	for _, record := range list.Records {
		if !strings.EqualFold(strings.TrimSpace(record.AuthType), pebblestore.AuthTypeCLI) {
			continue
		}
		if record.Active {
			return record.ID, true, nil
		}
		if id == "" {
			id = record.ID
		}
	}
	return id, false, nil
}

func controlMCPDaemonUser() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	if name := strings.TrimSpace(current.Username); name != "" {
		return name
	}
	return current.Uid
}

// controlMCPClaudeCodeState describes the CLI as the person's AI needs it.
func controlMCPClaudeCodeState(status claudecode.CLIStatus) map[string]any {
	out := map[string]any{
		"cli_installed": status.Path != "",
		"signed_in":     status.Ready,
	}
	if name := controlMCPDaemonUser(); name != "" {
		out["service_user"] = name
	}
	if status.Version != "" {
		out["cli_version"] = status.Version
	}
	switch {
	case status.Path == "":
		out["state"] = "not_installed"
	case status.Version == "":
		out["state"] = "not_working"
	case status.Ready:
		out["state"] = "signed_in"
	case status.Auth.LoggedIn:
		// Signed in but refused (an unusable auth method): sign in again.
		out["state"] = "signed_out"
	case strings.Contains(status.Reason, "too old"):
		out["state"] = "needs_update"
	default:
		out["state"] = "signed_out"
	}
	if status.Ready {
		if method := strings.TrimSpace(status.Auth.AuthMethod); method != "" {
			out["sign_in_method"] = method
		}
		if plan := strings.TrimSpace(status.Auth.SubscriptionType); plan != "" {
			out["plan"] = plan
		}
	} else if reason := strings.TrimSpace(status.Reason); reason != "" {
		out["reason"] = controlMCPTruncate(reason, 300)
	}
	return out
}

const controlMCPClaudeCodeMachineStep = "The person runs this on the machine, in their own terminal (it needs sudo, and the sign-in needs them at the keyboard): it installs or finds the Claude Code CLI for Swarm's user, points Swarm at it and starts `claude auth login` as that user; they open the link it shows and finish in their browser. You may run it yourself as the person with sudo for the install part; it then stops and says the sign-in needs a terminal. Never copy the person's own Claude login. Then call this tool with action status."

func controlMCPConnectClaudeCode(c *controlMCPCall, args map[string]any) (any, error) {
	// The same authority as swarm_connect_chatgpt: a key with Swarm Control
	// levels is held to its level (swarm:write, checked before any tool
	// runs); any other scoped token needs admin, as the credential routes do.
	if record, ok := ScopedTokenFromRequest(c.request); ok && len(c.levels()) == 0 && !record.HasScope("admin") {
		return nil, controlMCPToolFailure("this connection lacks the admin permission")
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.auth == nil {
		return nil, controlMCPToolFailure("provider credentials are unavailable")
	}
	action := controlMCPString(args, "action")
	if action != "status" && action != "activate" {
		return nil, controlMCPToolFailure("action must be status or activate")
	}
	if c.server.claudeCodeUsage == nil {
		return nil, controlMCPToolFailure("this Swarm has no Claude Code provider")
	}
	status := c.claudeCodeCLIStatus()
	out := controlMCPClaudeCodeState(status)
	credentialID, active, err := c.claudeCodeCredential(principal.AccountScopeID)
	if err != nil {
		return nil, controlMCPToolFailure("provider credentials could not be read")
	}
	if !status.Ready {
		out["provider_active"] = false
		out["run_on_the_machine"] = controlMCPClaudeCodeSetupCommand
		out["next"] = controlMCPClaudeCodeMachineStep
		if action == "activate" {
			return nil, controlMCPToolFailure("Claude Code is not ready for Swarm's user (%v): the person runs `%s` on the machine first. %s",
				out["state"], controlMCPClaudeCodeSetupCommand, controlMCPClaudeCodeMachineStep)
		}
		return out, nil
	}
	if action == "status" {
		out["provider_active"] = active
		if active {
			out["next"] = "Claude Code is this account's active provider. swarm_list_models shows its models; swarm_set_agent_model changes a role's model when the person asks."
		} else {
			out["next"] = "Claude Code is signed in; call this tool with action activate to make it the active provider."
		}
		return out, nil
	}
	body := map[string]any{"provider": claudecode.ProviderID, "type": pebblestore.AuthTypeCLI, "label": "", "active": true}
	if credentialID != "" {
		// Reactivating updates the account's existing record instead of
		// adding another.
		body["id"] = credentialID
	}
	if _, err := c.dispatchTo(http.HandlerFunc(c.server.handleAuthCredentials), http.MethodPost, "/v1/auth/credentials", nil, body); err != nil {
		return nil, err
	}
	if _, active, err = c.claudeCodeCredential(principal.AccountScopeID); err != nil || !active {
		return nil, controlMCPToolFailure("Claude Code was saved but is not the active provider")
	}
	out["provider_active"] = true
	out["next"] = "Claude Code is now this account's active provider. swarm_list_models shows its models; swarm_set_agent_model changes a role's model when the person asks."
	return out, nil
}

// controlMCPClaudeCodeDescription says, on its own, everything an AI needs to
// set the provider up.
var controlMCPClaudeCodeDescription = "Set up the Claude Code CLI on this machine as Swarm's model provider (claudecode), using the person's Claude plan. Swarm runs the `claude` CLI as its own service user, which needs its own install and its own Claude sign-in; Swarm never reads, copies or stores Claude credentials, and the person's own Claude login is never copied. " +
	"action status reports state (not_installed, not_working, needs_update, signed_out or signed_in), cli_version, signed_in and provider_active; when the CLI is not ready, run_on_the_machine is the exact command to run: `" + controlMCPClaudeCodeSetupCommand + "` (Swarm's installer). It installs or finds the CLI for Swarm's user, sets SWARM_CLAUDE_CODE_CLI_PATH and, in the person's terminal, starts `claude auth login` as that user; they finish in their browser. Re-running it is safe and reports the current state. " +
	"Once status shows signed_in, action activate makes Claude Code the active provider for this account, the same as the setup page's Save and make active; no owner password or web form is needed. Steps: status, the person runs the command, status until signed_in, activate."

func controlMCPClaudeCodeTool() controlMCPTool {
	return controlMCPTool{
		Name: "swarm_connect_claude_code", Title: "Connect Claude Code",
		Description: controlMCPClaudeCodeDescription,
		InputSchema: controlMCPObject([]string{"action"}, map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"status", "activate"},
				"description": "status: what is set up and the command to run when something is missing. activate: make Claude Code the active provider (needs signed_in)."},
		}),
		Annotations: controlMCPWrites, call: controlMCPConnectClaudeCode,
	}
}
