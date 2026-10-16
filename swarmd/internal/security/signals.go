package security

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// tokenDeniedWindow limits token.denied to one signal per key in this window,
// so a client retrying a dead key in a loop cannot flood the feed.
const tokenDeniedWindow = 10 * time.Minute

// SetSignalEmitter makes the service raise token.minted, token.revoked,
// token.deleted and token.denied (a revoked or expired key, or one whose
// issuing key is gone, was presented). Signals carry the key's id, name,
// scopes and issuing key, never the key itself or its hash.
func (s *Service) SetSignalEmitter(emitter *signals.Emitter) {
	if s != nil {
		s.signals = emitter
	}
}

func tokenSignalFields(record pebblestore.ScopedTokenRecord) (map[string]string, map[string]string) {
	refs := map[string]string{"token_id": record.ID}
	if record.ParentTokenID != "" {
		refs["parent_token_id"] = record.ParentTokenID
	}
	if record.WorkerID != "" {
		refs["worker_id"] = record.WorkerID
	}
	attrs := map[string]string{"name": record.Name, "scopes": strings.Join(record.Scopes, ",")}
	if record.AgentName != "" {
		attrs["agent"] = record.AgentName
	}
	return refs, attrs
}

func (s *Service) emitTokenLifecycle(kind string, record pebblestore.ScopedTokenRecord) {
	if s == nil || s.signals == nil {
		return
	}
	refs, attrs := tokenSignalFields(record)
	summary := map[string]string{
		"token.minted":         "Key created: ",
		"token.revoked":        "Key revoked: ",
		"token.deleted":        "Key deleted: ",
		"token.limits_updated": "Key limits changed: ",
	}[kind] + firstNonEmptyString(record.Name, record.ID)
	s.signals.Emit(pebblestore.Signal{
		Kind:     kind,
		Severity: pebblestore.SignalSeverityInfo,
		Account:  record.AccountScopeID,
		Summary:  summary,
		DedupKey: kind + ":" + record.ID,
		Refs:     refs,
		Attrs:    attrs,
	})
}

func (s *Service) emitTokenDenied(record pebblestore.ScopedTokenRecord, reason string) {
	if s == nil || s.signals == nil {
		return
	}
	refs, attrs := tokenSignalFields(record)
	attrs["reason"] = reason
	s.signals.EmitThrottled("token.denied:"+record.ID, tokenDeniedWindow, pebblestore.Signal{
		Kind:     "token.denied",
		Severity: pebblestore.SignalSeverityWarning,
		Account:  record.AccountScopeID,
		Summary:  "Refused key " + firstNonEmptyString(record.Name, record.ID) + ": " + reason,
		DedupKey: "token.denied:" + record.ID,
		Refs:     refs,
		Attrs:    attrs,
	})
}

// NeverExpires reports whether a stored token has no expiry. Keys minted
// before every API-minted key had to expire, and resource-bound credentials
// (trigger worker, remote transport and runner tokens), are stored so.
func NeverExpires(record pebblestore.ScopedTokenRecord) bool {
	return record.ExpiresAt <= 0
}

// WarnNeverExpiringTokens raises one token.never_expires warning per account
// that holds unrevoked tokens without an expiry, listing their ids, and
// returns those ids. It revokes nothing: the owner decides.
func (s *Service) WarnNeverExpiringTokens() ([]string, error) {
	if s == nil || s.authStore == nil {
		return nil, errors.New("auth store not configured")
	}
	records, err := s.authStore.ListAllScopedTokens()
	if err != nil {
		return nil, err
	}
	byAccount := map[string][]string{}
	var all []string
	for _, record := range records {
		if record.Revoked || !NeverExpires(record) {
			continue
		}
		byAccount[record.AccountScopeID] = append(byAccount[record.AccountScopeID], record.ID)
		all = append(all, record.ID)
	}
	sort.Strings(all)
	if s.signals == nil {
		return all, nil
	}
	accounts := make([]string, 0, len(byAccount))
	for account := range byAccount {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	for _, account := range accounts {
		ids := byAccount[account]
		sort.Strings(ids)
		listed := fitTokenIDs(ids, pebblestore.MaxSignalValueRunes)
		s.signals.Emit(pebblestore.Signal{
			Kind:     "token.never_expires",
			Severity: pebblestore.SignalSeverityWarning,
			Account:  account,
			Summary:  fmt.Sprintf("%d key(s) never expire (swarmctl setup sdk-tokens marks them); revoke or replace them: %s", len(ids), listed),
			DedupKey: "token.never_expires",
			Attrs:    map[string]string{"token_ids": listed, "count": fmt.Sprint(len(ids))},
		})
	}
	return all, nil
}

// fitTokenIDs joins ids within max runes, ending with "+N more" when they do
// not all fit, so the signal never carries a cut-off id.
func fitTokenIDs(ids []string, max int) string {
	var b strings.Builder
	for i, id := range ids {
		sep := ""
		if i > 0 {
			sep = ","
		}
		// Room for this id, plus the "+N more" a later id might need.
		reserve := 0
		if rest := len(ids) - i - 1; rest > 0 {
			reserve = len(fmt.Sprintf(" +%d more", rest))
		}
		if b.Len()+len(sep)+len(id)+reserve > max {
			fmt.Fprintf(&b, " +%d more", len(ids)-i)
			return b.String()
		}
		b.WriteString(sep + id)
	}
	return b.String()
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
