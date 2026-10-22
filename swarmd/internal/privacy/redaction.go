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
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	ascii := true
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			ascii = false
			break
		}
	}
	// Non-ASCII case folding includes long s and the Kelvin sign. Keep the
	// original regex path for that text rather than approximating Unicode folding.
	lower := ""
	if ascii {
		lower = strings.ToLower(raw)
	}
	secret := !ascii || strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "passw") || strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "private_key") || strings.Contains(lower, "authorization")
	redacted := raw
	if !ascii || (secret && strings.Contains(raw, ":")) {
		redacted = redactJSONSecretPattern.ReplaceAllString(redacted, `$1[redacted]$3`)
	}
	if !ascii || (secret && strings.Contains(raw, "=")) {
		redacted = redactQuerySecretPattern.ReplaceAllString(redacted, `$1=[redacted]`)
	}
	if !ascii || strings.Contains(lower, "bearer") {
		redacted = redactBearerPattern.ReplaceAllString(redacted, `$1[redacted]`)
	}
	if !ascii || strings.Count(raw, ".") >= 2 {
		redacted = redactJWTPattern.ReplaceAllString(redacted, `[redacted.jwt]`)
	}
	if !ascii || strings.Contains(raw, "sk-") {
		redacted = redactSKPattern.ReplaceAllString(redacted, `[redacted.api_key]`)
	}
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
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "accesstoken":
		normalized = "access_token"
	case "refreshtoken":
		normalized = "refresh_token"
	case "idtoken":
		normalized = "id_token"
	case "apikey":
		normalized = "api_key"
	case "privatekey":
		normalized = "private_key"
	case "clientsecret":
		normalized = "client_secret"
	}
	switch normalized {
	case "access_token", "refresh_token", "id_token", "api_key", "authorization", "token", "secret", "password", "passwd", "private_key", "client_secret":
		return true
	default:
		return false
	}
}
