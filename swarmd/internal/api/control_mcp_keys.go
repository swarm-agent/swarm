package api

import (
	"errors"
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

func controlMCPRevokeKey(c *controlMCPCall, args map[string]any) (any, error) {
	account, err := controlMCPKeyAccount(c)
	if err != nil {
		return nil, err
	}
	id := controlMCPString(args, "key_id")
	if id == "" {
		return nil, controlMCPToolFailure("key_id is required (from manage_setup list_keys)")
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
		return nil, controlMCPToolFailure("no key %s on this box; manage_setup list_keys lists them", id)
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
