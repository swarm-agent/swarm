package gitenv

import "strings"

var identityOverrideKeys = map[string]struct{}{
	"GIT_AUTHOR_NAME":     {},
	"GIT_AUTHOR_EMAIL":    {},
	"GIT_AUTHOR_DATE":     {},
	"GIT_COMMITTER_NAME":  {},
	"GIT_COMMITTER_EMAIL": {},
	"GIT_COMMITTER_DATE":  {},
}

// FilterIdentityOverrides removes inherited Git author and committer overrides
// so commit-creating commands use repository or global Git configuration.
func FilterIdentityOverrides(base []string) []string {
	if len(base) == 0 {
		return nil
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.Index(entry, "="); idx >= 0 {
			key = entry[:idx]
		}
		if _, blocked := identityOverrideKeys[key]; blocked {
			continue
		}
		out = append(out, entry)
	}
	return out
}
