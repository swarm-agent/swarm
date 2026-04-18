package privacy

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	redactJSONSecretPattern  = regexp.MustCompile(`(?i)("?(?:access_token|refresh_token|id_token|api_key|authorization|token|secret|password|passwd|private_key|client_secret)"?\s*:\s*")([^"]+)(")`)
	redactQuerySecretPattern = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|api_key|token|secret|password|passwd|private_key|client_secret)=([^&\s]+)`)
	redactBearerPattern      = regexp.MustCompile(`(?i)\b(bearer\s+)([a-z0-9._\-]+)`)
	redactJWTPattern         = regexp.MustCompile(`\b[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}\b`)
	redactSKPattern          = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{16,}\b`)
)

// SanitizeText redacts common credential/token material from arbitrary text.
func SanitizeText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	redacted := redactJSONSecretPattern.ReplaceAllString(trimmed, `$1[redacted]$3`)
	redacted = redactQuerySecretPattern.ReplaceAllString(redacted, `$1=[redacted]`)
	redacted = redactBearerPattern.ReplaceAllString(redacted, `$1[redacted]`)
	redacted = redactJWTPattern.ReplaceAllString(redacted, `[redacted.jwt]`)
	redacted = redactSKPattern.ReplaceAllString(redacted, `[redacted.api_key]`)
	return redacted
}

// SanitizeJSONText attempts to preserve JSON structure while redacting sensitive values.
func SanitizeJSONText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return SanitizeText(trimmed)
	}
	encoded, err := json.Marshal(SanitizeValue(parsed))
	if err != nil {
		return SanitizeText(trimmed)
	}
	return string(encoded)
}

func SanitizeMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if isSensitiveKey(trimmedKey) {
			out[trimmedKey] = "[redacted]"
			continue
		}
		out[trimmedKey] = SanitizeValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func SanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return SanitizeMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, SanitizeValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if sanitized := SanitizeMap(item); len(sanitized) > 0 {
				out = append(out, sanitized)
			}
		}
		return out
	case string:
		return SanitizeText(typed)
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "refresh_token", "id_token", "api_key", "authorization", "token", "secret", "password", "passwd", "private_key", "client_secret":
		return true
	default:
		return false
	}
}
