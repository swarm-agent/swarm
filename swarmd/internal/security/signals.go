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

// NeverExpires reports whether a key never lapses on its own: only Swarm's
// internal keys, which live as long as the setup that made them.
func NeverExpires(record pebblestore.ScopedTokenRecord) bool {
	return KeyKind(record) == KeyKindInternal
}

func keySignalFields(record pebblestore.ScopedTokenRecord) (map[string]string, map[string]string) {
	refs, attrs := tokenSignalFields(record)
	attrs["kind"] = KeyKind(record)
	return refs, attrs
}

// emitKeyNewSource raises key.new_address: a key was used from a place it had
// not been used from before. A stolen key shows up here first.
func (s *Service) emitKeyNewSource(record pebblestore.ScopedTokenRecord, source string) {
	if s == nil || s.signals == nil {
		return
	}
	refs, attrs := keySignalFields(record)
	attrs["from"] = source
	if len(record.SeenFrom) > 0 {
		attrs["seen_from"] = strings.Join(record.SeenFrom, ", ")
	}
	name := firstNonEmptyString(record.Name, record.ID)
	s.signals.Emit(pebblestore.Signal{
		Kind:     "key.new_address",
		Severity: pebblestore.SignalSeverityWarning,
		Account:  record.AccountScopeID,
		Summary:  fmt.Sprintf("Key %s (%s) was used from a new place: %s. If that was not you, revoke it.", name, attrs["kind"], source),
		DedupKey: "key.new_address:" + record.ID + ":" + source,
		Refs:     refs,
		Attrs:    attrs,
	})
}

// WarnExpiringKeys raises key.expiring, once per lapse, for every key that
// stops working on its own within KeyExpiringNotice of now (it is going
// unused, or its fixed end date is near), and returns their ids. It revokes
// nothing.
func (s *Service) WarnExpiringKeys(now time.Time) ([]string, error) {
	if s == nil || s.authStore == nil {
		return nil, errors.New("auth store not configured")
	}
	records, err := s.authStore.ListAllScopedTokens()
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	var warned []string
	for _, record := range records {
		if record.Revoked {
			continue
		}
		at, reason := KeyLapse(record)
		if at <= nowMs || at-nowMs > KeyExpiringNotice.Milliseconds() || record.ExpiringWarnedFor == at {
			continue
		}
		warned = append(warned, record.ID)
		if s.signals != nil {
			refs, attrs := keySignalFields(record)
			day := time.UnixMilli(at).UTC().Format("2006-01-02")
			attrs["lapses_at"] = time.UnixMilli(at).UTC().Format(time.RFC3339)
			attrs["reason"] = reason
			why := "it has not been used for almost 90 days; any use keeps it working"
			if reason == "date" {
				why = "its fixed end date; make a replacement before then"
			}
			s.signals.Emit(pebblestore.Signal{
				Kind:     "key.expiring",
				Severity: pebblestore.SignalSeverityWarning,
				Account:  record.AccountScopeID,
				Summary:  fmt.Sprintf("Key %s (%s) stops working on %s: %s.", firstNonEmptyString(record.Name, record.ID), attrs["kind"], day, why),
				DedupKey: fmt.Sprintf("key.expiring:%s:%d", record.ID, at),
				Refs:     refs,
				Attrs:    attrs,
			})
		}
		_ = s.authStore.MarkScopedTokenExpiringWarned(record.AccountScopeID, record.ID, at)
	}
	sort.Strings(warned)
	return warned, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
