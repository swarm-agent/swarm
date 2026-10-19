package tool

import (
	"errors"

	"swarm/packages/swarmd/internal/design"
)

// SwarmDesignToolName is the in-session design tool. The run service executes
// it inside the calling run (run/design_tool.go); the tool runtime only
// declares it.
const SwarmDesignToolName = "swarm_design"

// errSwarmDesignRunServiceOnly is returned when swarm_design reaches the tool
// runtime instead of the run service.
var errSwarmDesignRunServiceOnly = errors.New("swarm_design runs only inside a Swarm session run")

func swarmDesignDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        SwarmDesignToolName,
		Description: "Make standalone HTML designs or edit one existing HTML file. Each design is one model call, then a browser check, then one repair call only if the check found errors. Designs are written to <folder>/<batch>-<id>/01.html… with results.json; returns each design's path, status (passed, repaired, failed, check_unavailable, …), check findings and cost. Never write HTML by hand instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"brief":      map[string]any{"type": "string", "maxLength": design.MaxBriefRunes, "description": "What to design: content, audience, layout and any constraints."},
				"count":      map[string]any{"type": "integer", "minimum": 1, "maximum": design.MaxCount, "description": "How many designs to make (default 1). Must be 1 with edit_path."},
				"style":      map[string]any{"type": "string", "maxLength": design.MaxStyleRunes, "description": "Optional visual style shared by every design."},
				"variations": map[string]any{"type": "array", "maxItems": design.MaxVariations, "items": map[string]any{"type": "string", "maxLength": design.MaxVariationRunes}, "description": "Optional direction per design, used in order."},
				"batch_name": map[string]any{"type": "string", "maxLength": 200, "description": "Optional name for the batch folder (default: from the brief)."},
				"folder":     map[string]any{"type": "string", "description": "Workspace-relative parent folder for new designs (default \"designs\")."},
				"edit_path":  map[string]any{"type": "string", "description": "Workspace-relative existing .html file to edit in place instead of making new designs."},
			},
			"required":             []string{"brief"},
			"additionalProperties": false,
		},
	}
}
