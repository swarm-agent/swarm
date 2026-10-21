package security

import (
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// One rule for every key a person or their AI holds: a key works while it is
// used and lapses after KeyIdleLimit without use. The owner may also give a
// key a fixed end date (at most KeyFixedDateMax away). Swarm warns
// KeyExpiringNotice before either. Internal keys (Swarm's own remote
// transport) live as long as the setup that made them.
const (
	KeyIdleLimit      = 90 * 24 * time.Hour
	KeyFixedDateMax   = 365 * 24 * time.Hour
	KeyExpiringNotice = 7 * 24 * time.Hour
	// maxKeySources bounds the places a key remembers being used from.
	maxKeySources = 16
	// legacyTemporaryLifetime: a key minted before the rule with a shorter
	// lifetime than this was meant to be temporary and keeps its date; a
	// longer one had a date only because one was required, so it moves to
	// the idle rule.
	legacyTemporaryLifetime = 29 * 24 * time.Hour
)

// Key kinds as people and AIs see them.
const (
	KeyKindAI       = "ai"
	KeyKindApp      = "app"
	KeyKindBot      = "bot"
	KeyKindInternal = "internal"
)

// Expiry rules stored on a key (ScopedTokenRecord.ExpiryRule).
const (
	expiryRuleIdle = "idle"
	expiryRuleDate = "date"
)

// KeyKind names what a key is for: an AI key (Swarm Control levels), a bot
// key (one sealed agent), Swarm's own internal key, or an app key (anything
// else: session clients, monitoring, worker triggers).
func KeyKind(record pebblestore.ScopedTokenRecord) string {
	switch {
	case record.Kind == KeyKindInternal || isLegacyRemoteKey(record):
		return KeyKindInternal
	case strings.TrimSpace(record.AgentName) != "":
		return KeyKindBot
	case hasScopePrefix(record, "swarm:"):
		return KeyKindAI
	default:
		return KeyKindApp
	}
}

// KeyOptions are the choices made on an app or bot key, and an AI key's level.
func KeyOptions(record pebblestore.ScopedTokenRecord) []string {
	var options []string
	switch KeyKind(record) {
	case KeyKindAI:
		switch {
		case hasScope(record, "admin"):
			options = append(options, "full")
		case hasScope(record, "swarm:write"):
			options = append(options, "write")
		default:
			options = append(options, "read")
		}
	case KeyKindBot:
		if record.AgentOperator {
			options = append(options, "operator")
		} else {
			options = append(options, "gateway")
		}
	case KeyKindApp:
		if strings.TrimSpace(record.WorkerID) != "" {
			options = append(options, "trigger")
		}
		if isMonitorOnly(record) {
			options = append(options, "monitor")
		}
	}
	return options
}

// KeyLapse is when the key stops working on its own and why: "unused" (KeyIdleLimit
// after its last use, or after it was made if never used) or "date" (its
// fixed end date). 0 means it does not lapse on its own (internal keys).
func KeyLapse(record pebblestore.ScopedTokenRecord) (int64, string) {
	if KeyKind(record) == KeyKindInternal {
		return 0, ""
	}
	var at int64
	var reason string
	// A key with no recorded age (neither made nor used at a known time)
	// cannot have gone unused for a known time; only a fixed date applies.
	if last := max(record.CreatedAt, record.LastUsedAt); last > 0 {
		at, reason = last+KeyIdleLimit.Milliseconds(), "unused"
	}
	if date := fixedDate(record); date > 0 && (at == 0 || date < at) {
		at, reason = date, "date"
	}
	return at, reason
}

// fixedDate is the key's fixed end date, or 0. Keys minted before the rule
// keep a date only when it made them temporary (see legacyTemporaryLifetime).
func fixedDate(record pebblestore.ScopedTokenRecord) int64 {
	if record.ExpiresAt <= 0 {
		return 0
	}
	switch record.ExpiryRule {
	case expiryRuleDate:
		return record.ExpiresAt
	case expiryRuleIdle:
		return 0
	}
	if record.ExpiresAt-record.CreatedAt < legacyTemporaryLifetime.Milliseconds() {
		return record.ExpiresAt
	}
	return 0
}

// HasFixedDate reports whether the key carries a fixed end date.
func HasFixedDate(record pebblestore.ScopedTokenRecord) bool {
	return fixedDate(record) > 0
}

// keyLapsed reports whether the key has stopped working on its own at now.
func keyLapsed(record pebblestore.ScopedTokenRecord, now int64) (bool, string) {
	at, reason := KeyLapse(record)
	return at > 0 && now >= at, reason
}

// isLegacyRemoteKey recognizes a remote transport device key minted before
// internal keys were marked.
func isLegacyRemoteKey(record pebblestore.ScopedTokenRecord) bool {
	return record.Kind == "" && record.ExpiresAt <= 0 && record.WorkerID == "" && record.AgentName == "" &&
		strings.HasPrefix(record.Name, "swarm-remote ")
}

func hasScope(record pebblestore.ScopedTokenRecord, scope string) bool {
	for _, s := range record.Scopes {
		if strings.EqualFold(strings.TrimSpace(s), scope) {
			return true
		}
	}
	return false
}

func hasScopePrefix(record pebblestore.ScopedTokenRecord, prefix string) bool {
	for _, s := range record.Scopes {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), prefix) {
			return true
		}
	}
	return false
}

// MonitorScopes are a monitoring key's: the machine summary and the signal
// feed, nothing else.
var MonitorScopes = []string{"signals:read"}

// isMonitorOnly: the key reads only the machine summary and signal feed.
func isMonitorOnly(record pebblestore.ScopedTokenRecord) bool {
	return len(record.Scopes) == 1 && hasScope(record, MonitorScopes[0])
}
