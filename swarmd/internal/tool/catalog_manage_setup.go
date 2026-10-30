package tool

import (
	"context"
	"encoding/json"
	"strings"
)

// manage_setup in the catalog: the machine setup that Swarm Control had as
// separate swarm_* tools (models, providers, usage, limits, permissions,
// keys, fleet keys, provider sign-in, media models, secrets and update
// drains). Each action runs the same function as the swarm_* tool with the
// same effect (api.Server.ManageSetup), for the Orchestrator and Swarm
// Control alike; one handler (Runtime.executeManageSetup) serves both.
//
// Safety is the same on both surfaces:
//   - every change action needs the person's decision each time: in a
//     conversation the run's person-decision gate (WithPersonDecision; an
//     allow rule or permission bypass never stands in for it), over Swarm
//     Control the person's own approval in their AI's chat plus the key's
//     level;
//   - no caller widens its own access: set_permissions, set_limits and
//     set_usage_limits only narrow, and create_key and create_client_key
//     mint nothing (no path yet hands a new key's secret to the person alone);
//   - no answer holds a secret or key value: names, ids, scopes and expiry
//     only.

const manageSetupToolName = "manage_setup"

// ManageSetupToolName is manage_setup's name for the services behind it.
const ManageSetupToolName = manageSetupToolName

const manageSetupDescription = "This machine's setup: models and the agent role defaults, model providers and their sign-in, usage and limits, account permissions, keys, fleet keys, secrets (names only), media models and update drains. Every change waits for the person's decision each time; make one only when the person asks for it in this chat, and never change a model or media model on your own. No change widens your own access: permissions and limits can only be narrowed here (the person raises them in Swarm's settings). Answers never hold a secret or key value. Machine setup on a linked machine (machine=NAME) covers models, providers, usage, limits, media models and update drains; keys, secrets and permissions are never managed remotely."

func manageSetupFields() []CatalogField {
	described := func(kind, description string) map[string]any {
		return map[string]any{"type": kind, "description": description}
	}
	text := func(description string, maxLength int) map[string]any {
		return map[string]any{"type": "string", "description": description, "maxLength": maxLength}
	}
	machines := map[string]any{"type": "array", "maxItems": 64, "description": "Linked machine names (manage_projects list_machines).", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 63}}
	return []CatalogField{
		{"machine", text("A linked machine's name (manage_projects list_machines): run this setup action there. Keys, secrets, permissions and provider sign-in are never managed remotely.", 63)},
		{"provider", text("A provider id (providers lists them); for models and providers, only this provider.", 100)},
		{"wait_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": 110, "description": "providers with provider, list_fleet_keys with id, connect_chatgpt status: wait up to this many seconds for the person to finish."}},
		{"period", map[string]any{"type": "string", "enum": []string{"today", "7d", "30d"}, "description": "usage: the period. Default today."}},
		{"detail", described("boolean", "usage: also every provider and model, each day, media, the busiest sessions and workers.")},
		{"role", map[string]any{"type": "string", "description": "set_agent_model: the agent role (models lists them), or all."}},
		{"model", text("set_agent_model: the model id (models lists them).", 200)},
		{"thinking", text("set_agent_model: the thinking level the model takes.", 50)},
		{"fast", described("boolean", "set_agent_model: priority serving (faster, costs more), only where models shows fast: true.")},
		{"image", text("set_media_models: image model id, or empty to clear.", 200)},
		{"video", text("set_media_models: video model id, or empty to clear.", 200)},
		{"video_iteration", text("set_media_models: the cheaper model video drafts iterate on, or empty.", 200)},
		{"audio", text("set_media_models: music or sound model id, or empty.", 200)},
		{"transcription", text("set_media_models: transcription model id, or empty.", 200)},
		{"kind", map[string]any{"type": "string", "enum": []string{"image", "video", "sound"}, "description": "media_models: only this kind. Default: all."}},
		{"include_unready", described("boolean", "media_models: also models whose provider has no key yet.")},
		{"expected_revision", map[string]any{"type": "integer", "minimum": 0, "description": "set_limits and set_permissions: the revision you read (limits, permissions)."}},
		{"limits", described("object", "set_limits: the whole box limits record as limits returns it, with lower values only.")},
		{"client_request_id", text("set_permissions: a stable id for this change.", 200)},
		{"tasks", map[string]any{"type": "string", "enum": []string{"approve"}, "description": "set_permissions: Start tasks. Only approve (asking first) can be set here; auto is the person's to set in Swarm."}},
		{"linked", map[string]any{"type": "object", "description": "set_permissions: linked work rules by machine name, each {\"work\": \"off\" or \"ask\"}, lower than the current rule only.", "additionalProperties": map[string]any{"type": "object", "properties": map[string]any{"work": map[string]any{"type": "string", "enum": []string{"off", "ask"}}}}}},
		{"daily_cost_limit_usd", map[string]any{"type": "number", "minimum": 0, "description": "set_usage_limits: the daily spend limit (UTC day), lower than the current one."}},
		{"daily_tokens_limit", map[string]any{"type": "integer", "minimum": 0, "description": "set_usage_limits: the daily token limit (UTC day), lower than the current one."}},
		{"enabled", described("boolean", "set_usage_limits: true turns the limits on; turning them off is the person's to do in Swarm.")},
		{"include_revoked", described("boolean", "list_keys: also revoked keys.")},
		{"name", text("create_key and create_client_key: what the key is for.", 64)},
		{"key_kind", map[string]any{"type": "string", "enum": []string{"app", "bot"}, "description": "create_key: app or bot."}},
		{"key_id", text("revoke_key: the key's id from list_keys (tok_...).", 100)},
		{"days", map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "description": "create_key and create_client_key: a fixed end date in days."}},
		{"id", text("list_fleet_keys and revoke_fleet_key: a fleet key's id (fk_...).", 40)},
		{"machines", machines},
		{"include_home", described("boolean", "request_fleet_key and revoke_fleet_key: this home as well.")},
		{"label", text("request_provider_key and request_fleet_key: a name for the stored key.", 64)},
		{"step", map[string]any{"type": "string", "enum": []string{"start", "status", "activate"}, "description": "connect_chatgpt: start or status; connect_claude_code: status or activate."}},
		{"login_id", text("connect_chatgpt status: the login_id from start.", 200)},
		{"max_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": 86400, "description": "request_update: how long to hold new work at most."}},
	}
}

func manageSetupCatalog() CatalogTool {
	const (
		read    = CatalogLevelRead
		write   = CatalogLevelWrite
		approve = CatalogLevelApprove
		manage  = CatalogLevelManage
	)
	// Each level is remote.ToolScope of the swarm_* tool with the same
	// effect (TestManageSetupLevelsAreTheSwarmToolsLevels); each API scope
	// set is the one that tool's handler and route check.
	sessionsRead := []string{"sessions:read"}
	actions := []CatalogAction{
		{Action: "models", Inputs: []string{"machine", "provider"}, Level: read, APIScopes: sessionsRead,
			Description: "Connected providers with their models, thinking options and fast serving, and each agent role's default model (swarm_list_models)."},
		{Action: "providers", Inputs: []string{"machine", "provider", "wait_seconds"}, Level: read, APIScopes: sessionsRead,
			Description: "Each model provider: ready, whether it takes an API key, how many credentials and the active one's status, and how to connect one that is not ready. Never key material (swarm_list_providers)."},
		{Action: "usage", Inputs: []string{"machine", "period", "detail"}, Level: read, APIScopes: sessionsRead,
			Description: "Model spend and tokens for a period, top models, runs now and the account's daily limits (swarm_get_usage)."},
		{Action: "plan_usage", Inputs: []string{"machine"}, Level: read, APIScopes: sessionsRead,
			Description: "How much of each sign-in plan's rate limits is used: ChatGPT and Claude Code (swarm_get_plan_usage)."},
		{Action: "limits", Inputs: []string{"machine"}, Level: read, APIScopes: []string{"usage:read", "usage:write"},
			Description: "The box's revisioned admission and per-job limits (swarm_get_limits)."},
		{Action: "permissions", Level: read, APIScopes: sessionsRead,
			Description: "The account's Start tasks, linked work and device rules with their revision (swarm_get_permissions)."},
		{Action: "list_keys", Inputs: []string{"include_revoked"}, Level: read, APIScopes: sessionsRead,
			Description: "Every key that reaches this machine: id, name, kind, options, state, when it was made and last used, from where, and when it stops working. Never key material (swarm_list_keys)."},
		{Action: "list_fleet_keys", Inputs: []string{"id", "wait_seconds"}, Level: read, APIScopes: sessionsRead,
			Description: "The provider keys this home manages for its linked machines: id, provider, label, a short fingerprint and each machine's state. Never a key value (swarm_list_fleet_keys)."},
		{Action: "list_secrets", Level: read, APIScopes: []string{"secrets:read"},
			Description: "Each secret's name, the websites it may go to, whether a value is set and the folders it is granted to. Values never come back (swarm_list_secrets)."},
		{Action: "media_models", Inputs: []string{"machine", "kind", "include_unready"}, Level: read, APIScopes: sessionsRead,
			Description: "The image, video and sound models this machine can use, the default per kind and each model's options (swarm_media_models)."},
		{Action: "set_agent_model", Inputs: []string{"machine", "role", "provider", "model", "thinking", "fast"}, Level: write, APIScopes: []string{"settings:write"},
			Description: "Only when the person asks: set the account default model for one agent role, or every role with role all (swarm_set_agent_model). Waits for the person's decision."},
		{Action: "set_media_models", Inputs: []string{"machine", "image", "video", "video_iteration", "audio", "transcription"}, Level: write, APIScopes: []string{"settings:write"},
			Description: "Only when the person asks: set the default model of each medium; an empty string clears one (swarm_set_media_models). Waits for the person's decision."},
		{Action: "set_limits", Inputs: []string{"machine", "expected_revision", "limits"}, Level: write, APIScopes: []string{"usage:write"},
			Description: "Lower the box limits at expected_revision; a value above the current one (or a job budget removed) is refused here (swarm_set_limits). Waits for the person's decision."},
		{Action: "set_usage_limits", Inputs: []string{"machine", "daily_cost_limit_usd", "daily_tokens_limit", "enabled"}, Level: manage, APIScopes: []string{"usage:write"},
			Description: "Lower the account's daily spend or token limit, or turn the limits on; raising, removing or turning them off is refused here (swarm_set_usage_limits). Waits for the person's decision."},
		{Action: "set_permissions", Inputs: []string{"expected_revision", "client_request_id", "tasks", "linked"}, Level: approve, APIScopes: []string{"swarm:approve"},
			Description: "Narrow the account rules at expected_revision: Start tasks to approve, a linked machine's work to ask or off. Anything wider is refused here (swarm_set_permissions). Waits for the person's decision."},
		{Action: "create_key", Inputs: []string{"key_kind", "name", "days"}, Level: manage, APIScopes: []string{"admin"},
			Description: "Refused for now: a new key's secret may reach only the person, and no one-time path hands it to them yet. The person makes app and bot keys in Swarm."},
		{Action: "create_client_key", Inputs: []string{"name", "days"}, Level: manage, APIScopes: []string{"admin"},
			Description: "Refused for now, as create_key: the person makes client app keys in Swarm."},
		{Action: "revoke_key", Inputs: []string{"key_id"}, Level: write, APIScopes: []string{"sessions:write"},
			Description: "Stop a key working now, and every key made with it (swarm_revoke_key). Waits for the person's decision."},
		{Action: "request_fleet_key", Inputs: []string{"provider", "label", "machines", "include_home"}, Level: approve, APIScopes: []string{"swarm:approve"},
			Description: "A one-time link where the person pastes one provider key for linked machines; the AI never sees it (swarm_request_fleet_key). Waits for the person's decision."},
		{Action: "revoke_fleet_key", Inputs: []string{"id", "machines", "include_home"}, Level: approve, APIScopes: []string{"swarm:approve"},
			Description: "Remove a fleet key from the machines named, or from everywhere (swarm_revoke_fleet_key). Waits for the person's decision."},
		{Action: "request_provider_key", Inputs: []string{"provider", "label"}, Level: write, APIScopes: []string{"sessions:write"},
			Description: "A one-time link where the person pastes a provider's API key on Swarm's own page, never in the chat (swarm_request_provider_key). Waits for the person's decision."},
		{Action: "connect_chatgpt", Inputs: []string{"step", "login_id", "wait_seconds"}, Level: write, APIScopes: []string{"admin", "swarm:write"},
			Description: "Connect the person's ChatGPT plan: step start returns a link and a code for the person; step status with login_id follows it (swarm_connect_chatgpt). Waits for the person's decision."},
		{Action: "connect_claude_code", Inputs: []string{"step"}, Level: write, APIScopes: []string{"admin", "swarm:write"},
			Description: "Set up Claude Code as the provider: step status says what is missing and the command the person runs; step activate turns it on once signed in (swarm_connect_claude_code). Waits for the person's decision."},
		{Action: "request_update", Inputs: []string{"machine", "max_seconds"}, Level: manage, APIScopes: []string{"admin"},
			Description: "Hold new work while running work finishes, for an installer update; never changes the host (swarm_request_update). Waits for the person's decision."},
		{Action: "cancel_update", Inputs: []string{"machine"}, Level: manage, APIScopes: []string{"admin"},
			Description: "Reopen held admissions without interrupting running jobs (swarm_cancel_update). Waits for the person's decision."},
	}
	// Machine setup a relay key may not do (remote's localOnlyTools): this
	// machine only, and refused for relayed callers.
	localOnly := map[string]bool{"create_key": true, "create_client_key": true, "connect_chatgpt": true, "connect_claude_code": true, "request_provider_key": true, "request_fleet_key": true}
	// Keys, secrets and permissions are never managed remotely: this
	// machine only.
	home := map[string]bool{"permissions": true, "set_permissions": true, "list_keys": true, "revoke_key": true, "list_fleet_keys": true, "revoke_fleet_key": true, "list_secrets": true}
	// The change actions whose swarm_* tool is in Swarm Control's default
	// listing today; the others stay in tools=all.
	essentialChange := map[string]bool{"set_agent_model": true, "set_media_models": true, "set_limits": true, "create_key": true, "revoke_key": true, "request_fleet_key": true, "revoke_fleet_key": true, "request_provider_key": true, "connect_chatgpt": true, "connect_claude_code": true}
	nextActions := map[string]string{
		"request_provider_key": "Give the person tell_the_person; providers with provider and wait_seconds follows the outcome.",
		"request_fleet_key":    "Give the person tell_the_person; list_fleet_keys with id and wait_seconds follows the outcome.",
		"connect_chatgpt":      "Give the person the link and code; connect_chatgpt step status with login_id and wait_seconds follows it.",
	}
	for i := range actions {
		actions[i].Tool = manageSetupToolName
		actions[i].Reach = CatalogReachAny
		if localOnly[actions[i].Action] || home[actions[i].Action] {
			actions[i].Reach = CatalogReachHome
		}
		actions[i].LocalOnly = localOnly[actions[i].Action]
		if actions[i].Level == read || essentialChange[actions[i].Action] {
			actions[i].Profiles = []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
		} else {
			actions[i].Profiles = []CatalogProfile{CatalogProfileFull, CatalogProfileOrchestrator}
		}
		actions[i].NextAction = nextActions[actions[i].Action]
	}
	return CatalogTool{Name: manageSetupToolName, Description: manageSetupDescription, Fields: manageSetupFields(), Actions: actions}
}

func manageSetupDefinition() Definition {
	return manageSetupCatalog().OrchestratorDefinition()
}

// ManageSetupChange reports whether a manage_setup call is a change: every
// action above read level. A change needs the person's decision each time.
func ManageSetupChange(arguments string) bool {
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(arguments)), &args) != nil {
		// Unreadable arguments are treated as a change: the handler refuses
		// them, and nothing runs without the decision.
		return true
	}
	action, ok := manageSetupCatalog().Action(args.Action)
	if !ok {
		return true
	}
	return action.Level != CatalogLevelRead
}

type personDecisionKey struct{}

// WithPersonDecision marks a conversation's manage_setup call as decided by
// the person: the run service sets it only after the person approved this
// exact call in their app (never from tool arguments, rules or bypass).
func WithPersonDecision(ctx context.Context) context.Context {
	return context.WithValue(ctx, personDecisionKey{}, true)
}

// PersonDecisionFromContext reports whether the person decided this call.
func PersonDecisionFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	decided, _ := ctx.Value(personDecisionKey{}).(bool)
	return decided
}
