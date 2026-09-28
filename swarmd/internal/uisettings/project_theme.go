package uisettings

import (
	"fmt"
	"strings"

	sharedtheme "swarm-refactor/swarmtui/theme"
)

// ResolveProjectThemeID validates a project's optional theme reference against
// the authenticated account's canonical builtin and custom theme catalog.
// An empty ID clears the selection and restores the normal Swarm default.
func ResolveProjectThemeID(settings UISettings, themeID string) (string, error) {
	id := strings.TrimSpace(themeID)
	if id == "" {
		return "", nil
	}
	if builtin, ok := sharedtheme.ResolveBuiltinTheme(id); ok {
		return builtin.ID, nil
	}
	for _, theme := range settings.Theme.CustomThemes {
		if theme.ID == id {
			return theme.ID, nil
		}
	}
	return "", fmt.Errorf("project theme %q not found in account theme catalog", id)
}
