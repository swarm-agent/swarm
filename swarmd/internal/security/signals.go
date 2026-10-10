package security

import (
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
		"token.minted":  "Key created: ",
		"token.revoked": "Key revoked: ",
		"token.deleted": "Key deleted: ",
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

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
