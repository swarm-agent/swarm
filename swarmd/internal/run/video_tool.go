package run

import (
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

// MaterializeSessionVideoTool narrows the provider-visible manage_video action
// enum to the same capability set enforced by runtime dispatch for Video Studio.
func MaterializeSessionVideoTool(definitions []provideriface.ToolDefinition, metadata map[string]any) []provideriface.ToolDefinition {
	studio := strings.EqualFold(strings.TrimSpace(mapString(metadata, "lineage_kind")), "video_project") ||
		strings.EqualFold(strings.TrimSpace(mapString(metadata, "experience")), "video_studio")
	if !studio {
		return definitions
	}
	out := append([]provideriface.ToolDefinition(nil), definitions...)
	for i := range out {
		if out[i].Name != "manage_video" {
			continue
		}
		parameters := cloneToolSchemaMap(out[i].Parameters)
		properties, _ := parameters["properties"].(map[string]any)
		action, _ := properties["action"].(map[string]any)
		if action == nil {
			continue
		}
		action["enum"] = tool.ManageVideoActionNames(true)
		out[i].Parameters = parameters
	}
	return out
}
