package api

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/claudecode"
)

// Provider keys over Swarm Control. list_providers reports credential
// metadata only (never key material, last four characters or labels);
// request_provider_key hands the AI a link to Swarm's own key page on this
// machine (provider_key_entry.go), so the person pastes the key there and it
// never passes through the AI.

// controlMCPKeyProviders reads /v1/providers and returns every provider id,
// with whether it takes an API key.
func (c *controlMCPCall) controlMCPKeyProviders() (map[string]map[string]any, error) {
	out, _, err := c.controlMCPProviderAuth()
	return out, err
}

// controlMCPProviderAuth is controlMCPKeyProviders plus each provider's
// advertised credential types, from which its connect method is derived.
func (c *controlMCPCall) controlMCPProviderAuth() (map[string]map[string]any, map[string][]string, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/providers", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]map[string]any{}
	authTypes := map[string][]string{}
	for _, raw := range controlMCPList(response["providers"]) {
		provider := controlMCPMap(raw)
		id, _ := provider["id"].(string)
		if id == "" {
			continue
		}
		acceptsKey := false
		types := []string{}
		for _, method := range controlMCPList(provider["auth_methods"]) {
			kind, _ := controlMCPMap(method)["credential_type"].(string)
			if kind == "api" {
				acceptsKey = true
			}
			if kind != "" {
				types = append(types, kind)
			}
		}
		ready, _ := provider["ready"].(bool)
		out[id] = map[string]any{"provider": id, "ready": ready, "accepts_api_key": acceptsKey}
		authTypes[id] = types
	}
	return out, authTypes, nil
}

// controlMCPRunsAgents reports whether provider has a registered runner, so
// it can serve agent turns (web search providers, for example, cannot).
func (c *controlMCPCall) controlMCPRunsAgents(provider string) bool {
	if c.server == nil || c.server.providers == nil {
		return false
	}
	runner, ok := c.server.providers.GetRunner(provider)
	return ok && runner != nil
}

func controlMCPVerification(connection *auth.ConnectionStatus) (string, string) {
	switch {
	case connection == nil:
		return "not verified", ""
	case connection.Connected:
		return "verified", ""
	default:
		return "failed", providerKeyRedact(connection.Message, "")
	}
}

// controlMCPCredentialStatus is the active credential's status as a person
// would describe it. A sign-in (any auth type but an API key, e.g. ChatGPT)
// is not key-verified; it reads signed in, and needs attention only when the
// provider cannot run. An API key reads verified, failed or saved but not
// verified yet.
func controlMCPCredentialStatus(authType string, connection *auth.ConnectionStatus, ready bool) map[string]any {
	active := map[string]any{"auth_type": authType}
	if strings.TrimSpace(authType) != "api" {
		active["auth_type"] = "sign_in"
		if ready {
			active["status"] = "signed in"
		} else {
			active["status"] = "signed in, but the provider is not ready; sign in again"
		}
		if connection != nil && !connection.Connected && strings.TrimSpace(connection.Message) != "" {
			active["status_message"] = providerKeyRedact(connection.Message, "")
		}
		return active
	}
	status, message := controlMCPVerification(connection)
	if status == "not verified" {
		status = "saved, not verified yet"
	}
	active["status"] = status
	if message != "" {
		active["status_message"] = message
	}
	return active
}

func controlMCPListProviders(c *controlMCPCall, args map[string]any) (any, error) {
	// The providers handler checks no scope and credential routes are
	// owner-only; this summary carries no key material, as list_models.
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.auth == nil {
		return nil, controlMCPToolFailure("provider credentials are unavailable")
	}
	only := controlMCPString(args, "provider")
	if seconds := controlMCPInt(args, "wait_seconds", 0); seconds > 0 {
		if only == "" {
			return nil, controlMCPToolFailure("wait_seconds needs provider")
		}
		c.waitForKeyRequest(principal.AccountScopeID, only, seconds)
	}
	providers, authTypes, err := c.controlMCPProviderAuth()
	if err != nil {
		return nil, err
	}
	list, err := c.server.auth.ListCredentialsForAccount(principal.AccountScopeID, "", "", 200)
	if err != nil {
		return nil, controlMCPToolFailure("provider credentials could not be read")
	}
	for _, record := range list.Records {
		entry := providers[record.Provider]
		if entry == nil {
			entry = map[string]any{"provider": record.Provider}
			providers[record.Provider] = entry
		}
		count, _ := entry["stored_credentials"].(int)
		entry["stored_credentials"] = count + 1
		if record.Active {
			ready, _ := entry["ready"].(bool)
			entry["active"] = controlMCPCredentialStatus(record.AuthType, record.Connection, ready)
		}
	}
	ids := make([]string, 0, len(providers))
	for id := range providers {
		if only == "" || id == only {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	anyReady := false
	options := []string{}
	for _, id := range ids {
		entry := providers[id]
		if _, ok := entry["stored_credentials"]; !ok {
			entry["stored_credentials"] = 0
		}
		if outcome := c.server.providerKeys.outcome(principal.AccountScopeID, id); outcome != nil {
			entry["key_request"] = outcome
		}
		ready, _ := entry["ready"].(bool)
		if ready {
			anyReady = true
		}
		// Only a provider the registry advertises can be connected; a stored
		// credential for an unregistered one gets no connect method.
		if types, advertised := authTypes[id]; advertised {
			runsAgents := c.controlMCPRunsAgents(id)
			entry["runs_agents"] = runsAgents
			if !ready {
				connect := controlMCPProviderConnect(id, types, runsAgents, c.levelAllows)
				entry["connect"] = connect
				if runsAgents {
					options = append(options, controlMCPConnectSummary(id, connect))
				}
			}
		}
		if id == claudecode.ProviderID && ready {
			c.controlMCPAddClaudeCodeAccount(entry)
		}
		out = append(out, entry)
	}
	result := map[string]any{"providers": out}
	if only == "" && !anyReady {
		result["next"] = controlMCPNoProviderNext(options)
	}
	return result, nil
}

// controlMCPAddClaudeCodeAccount adds the signed-in Claude account (email,
// organization and their ids) to the claudecode entry, read from the same
// route plan_usage reads (GET /v1/claudecode/usage). Nothing is added when
// this Swarm has no Claude Code, the route fails or it names no account.
func (c *controlMCPCall) controlMCPAddClaudeCodeAccount(entry map[string]any) {
	if c.server == nil || c.server.claudeCodeUsage == nil {
		return
	}
	usage, err := c.dispatch(http.MethodGet, "/v1/claudecode/usage", nil, nil)
	if err != nil {
		return
	}
	cli := controlMCPMap(usage["cli"])
	if loggedIn, _ := cli["logged_in"].(bool); !loggedIn {
		return
	}
	for key, value := range controlMCPClaudeCodeAccount(cli) {
		entry[key] = value
	}
}

// waitForKeyRequest blocks up to seconds while the latest key link for this
// account and provider is waiting for (or processing) the person's key. It
// wakes on registry changes and at the link's expiry; there is no polling.
func (c *controlMCPCall) waitForKeyRequest(accountScopeID, provider string, seconds int) {
	seconds = min(seconds, controlMCPMaxWaitSeconds)
	deadline := time.NewTimer(time.Duration(seconds) * time.Second)
	defer deadline.Stop()
	registry := &c.server.providerKeys
	for {
		changed := registry.changes()
		outcome := registry.outcome(accountScopeID, provider)
		if outcome == nil || (outcome.Status != "waiting" && outcome.Status != "submitting") {
			return
		}
		// Expiry is passive in the registry; wake just after it to report it.
		expiry := time.NewTimer(time.Duration(outcome.ExpiresInSeconds+1) * time.Second)
		select {
		case <-changed:
		case <-expiry.C:
		case <-deadline.C:
			expiry.Stop()
			return
		case <-c.request.Context().Done():
			expiry.Stop()
			return
		}
		expiry.Stop()
	}
}

func controlMCPKeyLabel(label string) (string, error) {
	if len([]rune(label)) > providerKeyMaxLabel {
		return "", controlMCPToolFailure("label exceeds %d characters", providerKeyMaxLabel)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", controlMCPToolFailure("label must be plain text")
		}
	}
	return label, nil
}

func controlMCPRequestProviderKey(c *controlMCPCall, args map[string]any) (any, error) {
	// Write clients may ask; only the person supplies the secret on our page.
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	record, ok := ScopedTokenFromRequest(c.request)
	principal, principalOK := PrincipalFromRequest(c.request)
	if !ok || !principalOK || c.server == nil || record.AccountScopeID != principal.AccountScopeID {
		return nil, controlMCPToolFailure("this connection's identity is unavailable")
	}
	provider := strings.ToLower(controlMCPString(args, "provider"))
	providers, err := c.controlMCPKeyProviders()
	if err != nil {
		return nil, err
	}
	accepted := []string{}
	for id, entry := range providers {
		if takes, _ := entry["accepts_api_key"].(bool); takes {
			accepted = append(accepted, id)
		}
	}
	sort.Strings(accepted)
	if !slices.Contains(accepted, provider) {
		return nil, controlMCPToolFailure("provider must be one that takes an API key: %s", strings.Join(accepted, ", "))
	}
	label, err := controlMCPKeyLabel(controlMCPString(args, "label"))
	if err != nil {
		return nil, err
	}
	_, link, err := c.server.providerKeys.issue(providerKeyRequest{
		tailnet:        providerKeyTailnetRequest(c.request),
		provider:       provider,
		label:          label,
		accountScopeID: principal.AccountScopeID,
		userID:         principal.UserID,
		token:          *record,
	})
	if err != nil {
		return nil, controlMCPToolFailure("%s", err.Error())
	}
	minutes := int(providerKeyCodeTTL.Minutes())
	device := "this machine"
	if strings.HasPrefix(link, "https://") {
		device = "a device in your tailnet"
	}
	return map[string]any{
		"provider":           provider,
		"url":                link,
		"expires_in_seconds": int(providerKeyCodeTTL.Seconds()),
		"tell_the_person": fmt.Sprintf("Open %s in a browser on %s and paste your %s API key there. Do not paste the key into this chat. The link works once and expires in %d minutes.",
			link, device, provider, minutes),
		"next": "Call swarm_list_providers with provider and wait_seconds to wait until the person saves the key (or it fails or the link expires).",
	}, nil
}
