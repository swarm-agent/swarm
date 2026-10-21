package pebblestore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// SessionEndUserMetadataKey holds the end user (the gateway's customer or
// visitor) a session belongs to: {"agent_name", "end_user", "labels"}. Only a
// gateway (agent-bound) token sets it, once, when it creates the session; the
// metadata routes refuse it and nothing rewrites it, so it is the authority for
// per-end-user limits and usage. It is never sent to the model.
const SessionEndUserMetadataKey = "swarm_v3_end_user"

// UsageScopeKindEndUser attributes a session's receipts to its end user. The
// scope's ProjectID is the agent name; its ID is EndUserScopeID(end user).
const UsageScopeKindEndUser = "end_user"

const (
	EndUserMaxLength     = 128
	EndUserMaxLabels     = 16
	EndUserLabelValueMax = 256
)

var (
	endUserPattern  = regexp.MustCompile(`^[A-Za-z0-9._:@+=-]{1,128}$`)
	endUserLabelKey = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

// SessionEndUser is the stored end-user binding of a session.
type SessionEndUser struct {
	AgentName string
	EndUser   string
	Labels    map[string]string
}

// ValidateEndUser checks an opaque end-user id: 1-128 characters from letters,
// digits and . _ : @ + = -.
func ValidateEndUser(endUser string) error {
	if !endUserPattern.MatchString(endUser) {
		return fmt.Errorf("end_user must be 1-%d characters from letters, digits and . _ : @ + = -", EndUserMaxLength)
	}
	return nil
}

// ValidateEndUserLabels checks a session's labels: at most 16, keys lowercase
// identifiers of up to 64 characters, values up to 256 printable characters.
func ValidateEndUserLabels(labels map[string]string) error {
	if len(labels) > EndUserMaxLabels {
		return fmt.Errorf("labels holds at most %d entries", EndUserMaxLabels)
	}
	for key, value := range labels {
		if !endUserLabelKey.MatchString(key) {
			return fmt.Errorf("label key %q must be a lowercase identifier of at most 64 characters", key)
		}
		if len(value) > EndUserLabelValueMax {
			return fmt.Errorf("label %q is longer than %d bytes", key, EndUserLabelValueMax)
		}
		for _, r := range value {
			if !unicode.IsPrint(r) {
				return fmt.Errorf("label %q has a non-printable character", key)
			}
		}
	}
	return nil
}

// EndUserMetadata is the metadata value stored under SessionEndUserMetadataKey.
func EndUserMetadata(agentName, endUser string, labels map[string]string) map[string]any {
	value := map[string]any{"agent_name": agentName, "end_user": endUser}
	if len(labels) > 0 {
		stored := make(map[string]any, len(labels))
		for key, label := range labels {
			stored[key] = label
		}
		value["labels"] = stored
	}
	return value
}

// SessionEndUserFromMetadata reads a session's end-user binding. A malformed
// binding is an error, never an absent one.
func SessionEndUserFromMetadata(metadata map[string]any) (SessionEndUser, bool, error) {
	raw, ok := metadata[SessionEndUserMetadataKey]
	if !ok || raw == nil {
		return SessionEndUser{}, false, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return SessionEndUser{}, false, errors.New("session end-user binding is malformed")
	}
	agent, _ := value["agent_name"].(string)
	endUser, _ := value["end_user"].(string)
	out := SessionEndUser{AgentName: strings.TrimSpace(agent), EndUser: endUser}
	switch labels := value["labels"].(type) {
	case nil:
	case map[string]any:
		out.Labels = make(map[string]string, len(labels))
		for key, label := range labels {
			text, ok := label.(string)
			if !ok {
				return SessionEndUser{}, false, errors.New("session end-user labels are malformed")
			}
			out.Labels[key] = text
		}
	case map[string]string:
		out.Labels = make(map[string]string, len(labels))
		for key, label := range labels {
			out.Labels[key] = label
		}
	default:
		return SessionEndUser{}, false, errors.New("session end-user labels are malformed")
	}
	if out.AgentName == "" || (out.EndUser != "" && ValidateEndUser(out.EndUser) != nil) {
		return SessionEndUser{}, false, errors.New("session end-user binding is malformed")
	}
	return out, true, nil
}

// EndUserScopeID encodes an end user for keys: store key parts are lowercased,
// and end-user ids are case-sensitive, so the id is hex-encoded (reversible).
func EndUserScopeID(endUser string) string {
	return hex.EncodeToString([]byte(endUser))
}

// EndUserFromScopeID reverses EndUserScopeID.
func EndUserFromScopeID(id string) (string, error) {
	raw, err := hex.DecodeString(id)
	if err != nil {
		return "", errors.New("invalid end-user scope id")
	}
	return string(raw), nil
}

// ListUsageScopeDays returns one UTC day's projections of every scope of kind
// under project (for end users: an agent), sorted by total tokens, at most
// limit of them; truncated reports whether more exist.
func (s *SessionStore) ListUsageScopeDays(account, kind, project, date string, limit int) ([]UsageScopeTotal, bool, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(kind) == "" || limit <= 0 {
		return nil, false, errors.New("usage scope account, kind and limit are required")
	}
	if parsed, err := time.Parse("2006-01-02", date); err != nil || parsed.Format("2006-01-02") != date {
		return nil, false, errors.New("invalid usage day")
	}
	prefix := fmt.Sprintf("usage_scope/%s/%s/%s/", keyPart(account), keyPart(kind), keyPart(project))
	suffix := "/day/" + date
	var out []UsageScopeTotal
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(prefix, iterateAll, func(key string, value []byte) error {
		if !strings.HasSuffix(key, suffix) {
			return nil
		}
		var total UsageScopeTotal
		if err := json.Unmarshal(value, &total); err != nil {
			return err
		}
		out = append(out, total)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].ID < out[j].ID
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}
