package api

import (
	"net/http"
	"strings"
)

// Fleet keys over Swarm Control: thin calls to /v3/fleet/keys
// (fleet_keys.go). The AI asks for a link and reads outcomes; the person
// types the key on the home's own key page, so the AI never handles it.

func controlMCPFleetKeyTools() []controlMCPTool {
	machines := map[string]any{"type": "array", "maxItems": fleetKeyMaxMachines, "items": controlMCPStringProp("A linked machine's name (swarm_list_machines or GET /v3/fleet).", 1, 63)}
	return []controlMCPTool{
		{
			Name: "swarm_list_fleet_keys", Title: "List the fleet's provider keys",
			Description: "The provider API keys this home manages for its linked machines: each key's id, provider, label, a short fingerprint (SHA-256 prefix and last 4 characters) and, per machine and for the home itself, its state (pending, installed, failed with a reason, removed) and when. Never a key value: the AI never sees or handles a key. With id and wait_seconds, waits while that key's link from swarm_request_fleet_key is still waiting for the person.",
			InputSchema: controlMCPObject(nil, map[string]any{
				"id": controlMCPStringProp("Only this key (fk_...).", 0, 40),
				"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": controlMCPMaxWaitSeconds,
					"description": "With id: wait up to this many seconds (at most 110) while its key link is waiting, returning once the person saved the key and the home installed it, or the link failed or expired."},
			}),
			Annotations: controlMCPReadOnly, call: controlMCPListFleetKeys,
		},
		{
			Name: "swarm_request_fleet_key", Title: "Ask the person for a provider key for the fleet",
			Description: "Install one provider API key on several linked machines without seeing it: returns a one-time link to Swarm's own key page on this home (expires in 10 minutes). Give the person tell_the_person. They paste the key on that page, never in the chat; the AI never sees or handles the key. The home verifies it with the provider, keeps it sealed, and installs it on the machines named (and on the home with include_home) over each machine's link; each machine verifies it again and reports. Then call swarm_list_fleet_keys with id and wait_seconds for the outcome. Needs the approve level.",
			InputSchema: controlMCPObject([]string{"provider"}, map[string]any{
				"provider":     controlMCPStringProp("A provider that takes an API key (see list_providers).", 1, 100),
				"label":        controlMCPStringProp("Optional name for the key.", 0, providerKeyMaxLabel),
				"machines":     machines,
				"include_home": map[string]any{"type": "boolean", "description": "Also install it on this home. Default false."},
			}),
			Annotations: controlMCPWrites, call: controlMCPRequestFleetKey,
		},
		{
			Name: "swarm_revoke_fleet_key", Title: "Remove a fleet key from machines",
			Description: "Remove a fleet key from the machines named (and the home with include_home), or from everywhere it was installed when neither is given. Each machine deletes only the credential this key installed. Once the key is removed everywhere, the home deletes the value it kept and the key cannot be installed again. The AI never sees or handles the key. Needs the approve level.",
			InputSchema: controlMCPObject([]string{"id"}, map[string]any{
				"id":           controlMCPStringProp("The key's id from swarm_list_fleet_keys (fk_...).", 1, 40),
				"machines":     machines,
				"include_home": map[string]any{"type": "boolean", "description": "Also remove it from this home."},
			}),
			Annotations: controlMCPDestructive, call: controlMCPRevokeFleetKey,
		},
	}
}

func controlMCPFleetKeyTargets(args map[string]any) ([]string, bool) {
	raw, present := args["machines"]
	names := []string{}
	for _, item := range controlMCPList(raw) {
		if name, ok := item.(string); ok {
			names = append(names, name)
		}
	}
	return names, present
}

func controlMCPListFleetKeys(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil {
		return nil, controlMCPToolFailure("this connection has no account")
	}
	id := controlMCPString(args, "id")
	if id != "" && !fleetKeyIDPattern.MatchString(id) {
		return nil, controlMCPToolFailure("id must be a fleet key id (fk_...)")
	}
	if seconds := controlMCPInt(args, "wait_seconds", 0); seconds > 0 {
		if id == "" {
			return nil, controlMCPToolFailure("wait_seconds needs id")
		}
		c.waitForKeyRequest(principal.AccountScopeID, fleetKeySlot(id), seconds)
	}
	response, err := c.dispatch(http.MethodGet, fleetKeysPath, nil, nil)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return response, nil
	}
	out := map[string]any{"revision": response["revision"], "home": response["home"]}
	for _, raw := range controlMCPList(response["keys"]) {
		if key := controlMCPMap(raw); key["id"] == id {
			out["key"] = key
		}
	}
	if outcome := c.server.providerKeys.outcome(principal.AccountScopeID, fleetKeySlot(id)); outcome != nil {
		out["key_request"] = outcome
	}
	if out["key"] == nil && out["key_request"] == nil {
		return nil, controlMCPToolFailure("no fleet key with that id")
	}
	return out, nil
}

func controlMCPRequestFleetKey(c *controlMCPCall, args map[string]any) (any, error) {
	body := map[string]any{"provider": strings.ToLower(controlMCPString(args, "provider"))}
	if label := controlMCPString(args, "label"); label != "" {
		body["label"] = label
	}
	if names, present := controlMCPFleetKeyTargets(args); present {
		body["machines"] = names
	}
	if include, ok := args["include_home"].(bool); ok {
		body["include_home"] = include
	}
	response, err := c.dispatch(http.MethodPost, fleetKeysPath+"/requests", nil, body)
	if err != nil {
		return nil, err
	}
	response["next"] = "Call swarm_list_fleet_keys with id and wait_seconds to wait until the person saves the key and the home installs it (or the link fails or expires)."
	return response, nil
}

func controlMCPRevokeFleetKey(c *controlMCPCall, args map[string]any) (any, error) {
	id := controlMCPString(args, "id")
	if !fleetKeyIDPattern.MatchString(id) {
		return nil, controlMCPToolFailure("id must be a fleet key id (fk_...)")
	}
	body := map[string]any{}
	if names, present := controlMCPFleetKeyTargets(args); present {
		body["machines"] = names
	}
	if include, ok := args["include_home"].(bool); ok {
		body["include_home"] = include
	}
	return c.dispatch(http.MethodPost, fleetKeysPath+"/"+id+"/revoke", nil, body)
}
