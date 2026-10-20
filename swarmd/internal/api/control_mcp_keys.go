package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Keys, for the AI that runs this box: every key works while it is used and
// lapses after 90 days unused (or at a fixed end date the owner chose); the AI
// can see every key, where it was last used from, make app and bot keys and
// revoke any of them. Swarm's own internal keys are never listed.

const controlMCPKeysRule = "Every key works while it is used and stops working after 90 days unused, or at its fixed end date if it has one. Nothing needs renewing. Swarm raises key.expiring a week before a key stops working and key.new_address when a key is used from a place it has not been used from before; if that was not the person, revoke the key."

func controlMCPKeyTools() []controlMCPTool {
	return []controlMCPTool{
		{
			Name: "swarm_list_keys", Title: "List this box's keys",
			Description: "Every key that reaches this box: AI keys (Claude Code, Codex, ChatGPT), app keys (programs, monitoring, worker triggers) and bot keys (a public chat bot's gateway or operator key). For each: kind, options, state (active, expired, revoked), when it was made and last used, where it was last used from and every place it has been used from, and when it stops working on its own and why. this_key marks the key making this call. Never key material. " + controlMCPKeysRule,
			InputSchema: controlMCPObject(nil, map[string]any{
				"include_revoked": map[string]any{"type": "boolean", "description": "Also list revoked keys. Default false."},
			}),
			Annotations: controlMCPReadOnly, call: controlMCPListKeys,
		},
		{
			Name: "swarm_create_key", Title: "Make an app or bot key",
			Description: "Make a key and return it once. kind app: a program's key for this box's HTTP API; access sessions (start, read and drive sessions; the default) or monitor (read the machine summary and signal feed only). kind bot: a public chat bot's key, limited to one sealed agent (define it with swarm_define_agent) and its limits; operator true makes the staff key that reads and takes over that agent's chats instead. AI keys come from the installer on the machine (sudo bash /var/lib/swarm-headless/install.sh connect-ai). " + controlMCPKeysRule + " Give days only if the person wants a fixed end date. Keep the key in a server process, never in browser code or chat.",
			InputSchema: controlMCPObject([]string{"kind", "name"}, map[string]any{
				"kind":                map[string]any{"type": "string", "enum": []string{security.KeyKindApp, security.KeyKindBot}},
				"name":                controlMCPStringProp("What the key is for, e.g. my-chat-ui or site-frontdesk.", 1, 64),
				"access":              map[string]any{"type": "string", "enum": []string{"sessions", "monitor"}, "description": "App keys: sessions (default) or monitor."},
				"agent":               controlMCPStringProp("Bot keys: the sealed agent it may talk to.", 0, 63),
				"operator":            map[string]any{"type": "boolean", "description": "Bot keys: the staff key that reads and takes over the agent's chats, instead of the gateway key. Default false."},
				"messages_per_minute": map[string]any{"type": "integer", "minimum": 0, "description": "Bot keys: messages per minute across all chats (0: Swarm's default)."},
				"sessions_per_hour":   map[string]any{"type": "integer", "minimum": 0, "description": "Bot keys: new chats per hour (0: Swarm's default)."},
				"max_concurrent_runs": map[string]any{"type": "integer", "minimum": 0, "description": "Bot keys: replies being written at once (0: Swarm's default)."},
				"end_user_max_tokens_per_day": map[string]any{"type": "integer", "minimum": 0,
					"description": "Bot keys: model tokens per visitor per UTC day (0: no cap)."},
				"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "description": "Optional fixed end date, in days. Leave it out: the key then works until 90 days unused."},
			}),
			Annotations: controlMCPWrites, call: controlMCPCreateKey,
		},
		{
			Name: "swarm_revoke_key", Title: "Revoke a key",
			Description: "Stop a key working now, and every key made with it. Use it for a key the person no longer needs, one used from a place they do not recognize (key.new_address), or one that leaked. Revoking the key making this call ends this connection. Swarm's own internal keys cannot be revoked here.",
			InputSchema: controlMCPObject([]string{"key_id"}, map[string]any{
				"key_id": controlMCPStringProp("The key's id from swarm_list_keys (tok_...).", 1, 100),
			}),
			Annotations: controlMCPDestructive, call: controlMCPRevokeKey,
		},
	}
}

func controlMCPKeyAccount(c *controlMCPCall) (string, error) {
	if c.server == nil || c.server.security == nil {
		return "", controlMCPToolFailure("keys are unavailable on this box")
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || strings.TrimSpace(principal.AccountScopeID) == "" {
		return "", controlMCPToolFailure("this connection has no account")
	}
	return principal.AccountScopeID, nil
}

func controlMCPKeyTime(ms int64) any {
	if ms <= 0 {
		return nil
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func controlMCPKeyView(record pebblestore.ScopedTokenRecord, caller string, now int64) map[string]any {
	item := newScopedTokenListItem(record, now)
	view := map[string]any{
		"id":         record.ID,
		"name":       record.Name,
		"kind":       item.KeyKind,
		"state":      item.State,
		"created_at": controlMCPKeyTime(record.CreatedAt),
	}
	if len(item.Options) > 0 {
		view["options"] = item.Options
	}
	if record.LastUsedAt > 0 {
		view["last_used_at"] = controlMCPKeyTime(record.LastUsedAt)
	}
	if record.LastUsedFrom != "" {
		view["last_used_from"] = record.LastUsedFrom
	}
	if len(record.SeenFrom) > 0 {
		view["used_from"] = record.SeenFrom
	}
	if item.State == "active" && item.LapsesAt > 0 {
		view["stops_working_at"] = controlMCPKeyTime(item.LapsesAt)
		view["stops_because"] = map[string]string{"unused": "90 days unused (any use keeps it working)", "date": "its fixed end date"}[item.LapseReason]
	}
	if record.AgentName != "" {
		view["agent"] = record.AgentName
	}
	if record.WorkerID != "" {
		view["worker"] = firstNonEmpty(record.WorkerName, record.WorkerID)
	}
	if record.ID == caller {
		view["this_key"] = true
	}
	return view
}

func controlMCPCallerKeyID(c *controlMCPCall) string {
	if record, ok := ScopedTokenFromRequest(c.request); ok && record != nil {
		return record.ID
	}
	return ""
}

func controlMCPListKeys(c *controlMCPCall, args map[string]any) (any, error) {
	account, err := controlMCPKeyAccount(c)
	if err != nil {
		return nil, err
	}
	records, err := c.server.security.ListScopedTokens(account)
	if err != nil {
		return nil, controlMCPToolFailure("keys could not be read")
	}
	includeRevoked, _ := args["include_revoked"].(bool)
	caller := controlMCPCallerKeyID(c)
	now := time.Now().UnixMilli()
	sort.SliceStable(records, func(i, j int) bool { return records[i].CreatedAt < records[j].CreatedAt })
	keys := []map[string]any{}
	counts := map[string]int{}
	for _, record := range records {
		if security.KeyKind(record) == security.KeyKindInternal || (record.Revoked && !includeRevoked) {
			continue
		}
		view := controlMCPKeyView(record, caller, now)
		keys = append(keys, view)
		if view["state"] == "active" {
			counts[view["kind"].(string)]++
		}
	}
	return map[string]any{"keys": keys, "active": counts, "rule": controlMCPKeysRule}, nil
}

func controlMCPCreateKey(c *controlMCPCall, args map[string]any) (any, error) {
	name := controlMCPString(args, "name")
	if name == "" || len(name) > 64 {
		return nil, controlMCPToolFailure("name is required (at most 64 characters)")
	}
	body := map[string]any{"name": name}
	if days := controlMCPInt(args, "days", 0); days != 0 {
		if days < 1 || days > 365 {
			return nil, controlMCPToolFailure("days is 1 to 365, or left out (the key then works until 90 days unused)")
		}
		body["expires_in_seconds"] = days * 24 * 60 * 60
	}
	use := "Shown once. Send it as 'Authorization: Bearer <key>' to this box's HTTP API from a server process (for example the Node SDK: new SwarmClient({ baseUrl, token })). Never put it in browser code."
	switch kind := controlMCPString(args, "kind"); kind {
	case security.KeyKindApp:
		body["kind"] = security.KeyKindApp
		switch access := controlMCPString(args, "access"); access {
		case "", "sessions":
			body["scopes"] = controlMCPClientKeyScopes
		case "monitor":
			body["monitor"] = true
			use = "Shown once. A monitor reads GET /v3/box/summary and the signal feed (SDK client.box.summary(), client.signals). Keep it in a server process."
		default:
			return nil, controlMCPToolFailure(`access is "sessions" or "monitor"`)
		}
	case security.KeyKindBot:
		agent := controlMCPString(args, "agent")
		if agent == "" {
			return nil, controlMCPToolFailure("agent is required for a bot key: the sealed agent it may talk to")
		}
		operator, _ := args["operator"].(bool)
		body["kind"] = security.KeyKindBot
		body["agent_name"] = agent
		body["operator"] = operator
		for _, field := range []string{"messages_per_minute", "sessions_per_hour", "max_concurrent_runs", "end_user_max_tokens_per_day"} {
			if value := controlMCPInt(args, field, 0); value > 0 {
				body[field] = value
			}
		}
		use = "Shown once. The bot's server sends it as 'Authorization: Bearer <key>'; it reaches only this agent's chats. Never put it in browser code."
	default:
		return nil, controlMCPToolFailure(`kind is "app" or "bot"; AI keys come from the installer (install.sh connect-ai)`)
	}
	response, err := c.dispatch(http.MethodPost, "/v3/auth/tokens", nil, body)
	if err != nil {
		return nil, err
	}
	token, _ := response["token"].(string)
	if token == "" {
		return nil, controlMCPToolFailure("Swarm did not return a key")
	}
	record := controlMCPPick(controlMCPMap(response["record"]), "id", "name", "key_kind", "options", "lapses_at", "lapse_reason", "fixed_date")
	return map[string]any{"key": token, "record": record, "use": use, "rule": controlMCPKeysRule}, nil
}

func controlMCPRevokeKey(c *controlMCPCall, args map[string]any) (any, error) {
	account, err := controlMCPKeyAccount(c)
	if err != nil {
		return nil, err
	}
	id := controlMCPString(args, "key_id")
	if id == "" {
		return nil, controlMCPToolFailure("key_id is required (from swarm_list_keys)")
	}
	records, err := c.server.security.ListScopedTokens(account)
	if err != nil {
		return nil, controlMCPToolFailure("keys could not be read")
	}
	var target *pebblestore.ScopedTokenRecord
	for i := range records {
		if records[i].ID == id {
			target = &records[i]
			break
		}
	}
	if target == nil {
		return nil, controlMCPToolFailure("no key %s on this box; swarm_list_keys lists them", id)
	}
	if security.KeyKind(*target) == security.KeyKindInternal {
		return nil, controlMCPToolFailure("%s is one of Swarm's own keys; it goes away with the setup that made it", id)
	}
	if target.Revoked {
		return map[string]any{"revoked": true, "key": controlMCPKeyView(*target, controlMCPCallerKeyID(c), time.Now().UnixMilli()), "note": "it was already revoked"}, nil
	}
	record, err := c.server.security.RevokeScopedToken(account, id)
	if errors.Is(err, security.ErrScopedTokenNotFound) {
		return nil, controlMCPToolFailure("no key %s on this box", id)
	}
	if err != nil {
		return nil, controlMCPToolFailure("the key could not be revoked")
	}
	return map[string]any{"revoked": true, "key": controlMCPKeyView(record, controlMCPCallerKeyID(c), time.Now().UnixMilli())}, nil
}
