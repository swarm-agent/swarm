package app

import (
	"sort"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
)

func chatMentionSubagentNames(state client.AgentState) []string {
	if len(state.Profiles) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(state.Profiles))
	out := make([]string, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.Mode), "subagent") {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
