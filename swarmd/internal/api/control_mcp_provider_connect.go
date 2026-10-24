package api

import (
	"fmt"
	"slices"
	"strings"
)

// How a person connects a model provider that is not connected yet. The
// methods follow the provider's advertised credential types (its adapter's
// auth_methods), so swarm_list_providers can hand the AI every working option
// as data. Every method keeps the person in charge: they sign in in their own
// browser or terminal, or add a key themselves on the machine. A key is never
// asked for in chat, and Swarm never handles Claude credentials.

const (
	controlMCPConnectSignIn          = "sign_in"
	controlMCPConnectClaudeCodeLogin = "claude_code_login"
	controlMCPConnectAPIKey          = "api_key"
	controlMCPConnectMachineIdentity = "machine_identity"
	controlMCPConnectSettings        = "settings"
)

// controlMCPProviderConnect describes how to connect provider, given its
// advertised credential types and whether this caller may call a tool.
func controlMCPProviderConnect(provider string, credentialTypes []string, runsAgents bool, allows func(string) bool) map[string]any {
	connect := controlMCPProviderConnectMethod(provider, credentialTypes, allows)
	if !runsAgents {
		connect["note"] = "This provider does not run agents (for example web search); connecting it does not give agents a model."
	}
	return connect
}

func controlMCPProviderConnectMethod(provider string, credentialTypes []string, allows func(string) bool) map[string]any {
	has := func(kind string) bool { return slices.Contains(credentialTypes, kind) }
	switch {
	case provider == "codex" && has("oauth"):
		connect := map[string]any{
			"method": controlMCPConnectSignIn,
			"needs":  "a ChatGPT plan; the person signs in in their own browser",
		}
		steps := []string{}
		if allows("swarm_connect_chatgpt") {
			connect["tool"] = "swarm_connect_chatgpt"
			steps = append(steps, "Call swarm_connect_chatgpt with action start; give the person the link and the code at once, then call action status with login_id and wait_seconds until it reports success.")
		}
		steps = append(steps,
			"Or the person runs on the machine: sudo swarmctl setup codex-login --timeout 15m (it prints the link and code and waits until they approve).",
			"Or the person signs in on Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth).")
		connect["steps"] = steps
		return connect
	case provider == "claudecode" && has("cli"):
		connect := map[string]any{
			"method": controlMCPConnectClaudeCodeLogin,
			"needs":  "a Claude plan the person signs in to with Claude Code (2.1.248 or later) on the machine Swarm runs on; Swarm never sees or handles Claude credentials",
		}
		steps := []string{
			"On a box from install.sh, the person runs in their own terminal on the machine: " + controlMCPClaudeCodeSetupCommand + " (it installs or finds Claude Code for Swarm's user, points Swarm at it and starts claude auth login as that user; they finish in their browser).",
		}
		if allows("swarm_connect_claude_code") {
			connect["tool"] = "swarm_connect_claude_code"
			steps = append(steps, "Call swarm_connect_claude_code with action status until it reports signed_in, then action activate to turn it on for this account (no setup page or owner password needed).")
		}
		steps = append(steps,
			"Otherwise the person installs Claude Code 2.1.248 or later where Swarm's daemon finds it: on the daemon's PATH (for example /usr/local/bin), or named by SWARM_CLAUDE_CODE_CLI_PATH in the daemon's environment.",
			"The person signs in as the OS user that runs the Swarm daemon: claude auth login (on a box from install.sh that user is swarm: sudo -u swarm -H claude auth login).",
			"The person turns it on for their Swarm account on Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth): provider claudecode, Claude Code CLI login, make it active. That record holds no secret.")
		connect["steps"] = steps
		return connect
	case has("adc"):
		return map[string]any{
			"method": controlMCPConnectMachineIdentity,
			"needs":  "a Google Cloud identity on the machine (its attached service account, or a key file named by GOOGLE_APPLICATION_CREDENTIALS) with roles/aiplatform.user on a project; no key is stored",
			"steps": []string{
				fmt.Sprintf("As the first provider, the person runs on the machine: sudo swarmctl setup credential --provider %s --project PROJECT [--location global].", provider),
				fmt.Sprintf("Next to another provider: sudo swarmctl setup add-credential --provider %s --project PROJECT.", provider),
			},
		}
	case has("api"):
		connect := map[string]any{
			"method": controlMCPConnectAPIKey,
			"needs":  fmt.Sprintf("a %s API key, which the person adds themselves; never ask for it in chat", provider),
		}
		steps := []string{}
		if allows("swarm_request_provider_key") {
			connect["tool"] = "swarm_request_provider_key"
			steps = append(steps, fmt.Sprintf("First call swarm_request_provider_key with provider %s; the person opens a tailnet link on a device in their tailnet (a loopback link on this machine) and pastes the key on that one-time page. Fleet clients use swarm_fleet_request_provider_key with the named machines.", provider))
		}
		steps = append(steps,
			"The person adds it on Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth).",
			fmt.Sprintf("Or, as the first provider, by script on the machine: sudo swarmctl setup credential --provider %s --api-key-stdin < key-file (on a box from install.sh: install.sh key %s).", provider, provider),
			fmt.Sprintf("Next to another provider: sudo swarmctl setup add-credential --provider %s --api-key-stdin < key-file (on a box from install.sh: install.sh key %s).", provider, provider))
		connect["steps"] = steps
		return connect
	default:
		return map[string]any{
			"method": controlMCPConnectSettings,
			"needs":  "a sign-in the person completes in Swarm's settings",
			"steps":  []string{"The person connects it on Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth)."},
		}
	}
}

// controlMCPProviderOptionsText lists, for the MCP server instructions, every
// provider a fresh install can run agents with and how each is connected, as
// equal options. chatGPTSignIn is how this caller starts the ChatGPT sign-in.
func controlMCPProviderOptionsText(chatGPTSignIn string) string {
	return "a ChatGPT plan (codex: " + chatGPTSignIn + "); " +
		"a Claude plan through Claude Code (claudecode: the person installs Claude Code on the machine, signs in with claude auth login as the OS user that runs Swarm, and turns it on in Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth); Swarm never handles Claude credentials); " +
		"an API key for anthropic, openai, google, openrouter or fireworks, which the person adds on Swarm's setup page, under Settings > Providers (in the desktop app: Settings > Auth), or, as the first provider, through the installed CLI on the machine (swarmctl setup credential --provider PROVIDER --api-key-stdin); " +
		"or Vertex AI with the machine's own Google identity (vertex: swarmctl setup credential --provider vertex --project PROJECT)."
}

// controlMCPConnectSummary is one short option line for the no-provider
// guidance: the provider and what connecting it needs.
func controlMCPConnectSummary(provider string, connect map[string]any) string {
	switch connect["method"] {
	case controlMCPConnectSignIn:
		return provider + " (ChatGPT plan sign-in)"
	case controlMCPConnectClaudeCodeLogin:
		return provider + " (Claude plan through Claude Code, signed in on the machine)"
	case controlMCPConnectMachineIdentity:
		return provider + " (Vertex AI with the machine's Google identity)"
	case controlMCPConnectAPIKey:
		return provider + " (API key)"
	default:
		return provider + " (sign in in Swarm's settings)"
	}
}

// controlMCPNoProviderNext is swarm_list_providers' guidance when no provider
// is ready: every agent provider is an equal option, and the person chooses.
func controlMCPNoProviderNext(options []string) string {
	if len(options) == 0 {
		return "No provider is connected yet and none can be connected from here: the person connects one in Swarm's settings."
	}
	return "No provider is connected yet. Ask the person which they have and present all of these options in one short list with what each needs, then follow their choice (each provider's connect field has the exact steps): " +
		strings.Join(options, "; ") +
		". Never ask for a key in chat: the person signs in or adds keys themselves."
}
