package tool

func manageMemoryDefinition() Definition {
	props := map[string]any{
		"action":            map[string]any{"type": "string", "enum": []string{"inspect", "remember", "edit", "forget"}},
		"expected_revision": map[string]any{"type": "integer", "minimum": 1},
		"intent":            map[string]any{"type": "string", "maxLength": 500},
		"entry_id":          map[string]any{"type": "string", "maxLength": 128},
		"content":           map[string]any{"type": "string", "maxLength": 32768},
		"kind":              map[string]any{"type": "string", "enum": []string{"rule", "orientation", "learned"}},
		"workspace_id":      map[string]any{"type": "string"},
		"session_id":        map[string]any{"type": "string"},
		"pinned":            map[string]any{"type": "boolean"},
		"purpose":           map[string]any{"type": "string", "enum": []string{"", "preference", "project_context", "operational_context", "recovery", "orientation"}},
		"subject":           map[string]any{"type": "string", "maxLength": 256},
	}
	return Definition{Type: "function", Name: "manage_memory", Description: "Inspect and manage account memory objects on explicit user request. Store credential locations only, never secret values.", Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}}
}
