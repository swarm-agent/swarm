package api

import (
	"net/http"
	"strings"
)

// Fleet keys over Swarm Control: thin calls to /v3/fleet/keys
// (fleet_keys.go). The AI asks for a link and reads outcomes; the person
// types the key on the home's own key page, so the AI never handles it.

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
