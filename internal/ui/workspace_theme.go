package ui

import (
	"fmt"
	"strings"
)

func workspaceThemeLabel(themeID string) string {
	themeID = NormalizeThemeID(themeID)
	if themeID == "" {
		return "global"
	}
	if option, ok := ResolveTheme(themeID); ok {
		return option.Name
	}
	return themeID
}

func (p *HomePage) workspaceDisplayLabel(name string, themeID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "workspace"
	}
	label := name
	if strings.TrimSpace(themeID) != "" {
		label = fmt.Sprintf("%s{%s}", name, workspaceThemeLabel(themeID))
	}
	return label
}
