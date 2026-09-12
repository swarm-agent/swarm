package tool

func manageMemoryDefinition() Definition {
	props := map[string]any{
		"action":            map[string]any{"type": "string", "enum": []string{"inspect", "remember", "forget"}},
		"expected_revision": map[string]any{"type": "integer", "minimum": 1},
		"intent":            map[string]any{"type": "string", "maxLength": 500},
		"entry_id":          map[string]any{"type": "string", "maxLength": 128},
		"content":           map[string]any{"type": "string", "maxLength": 32768},
		"kind":              map[string]any{"type": "string", "enum": []string{"rule", "orientation", "learned"}},
		"workspace_id":      map[string]any{"type": "string"},
		"session_id":        map[string]any{"type": "string"},
		"pinned":            map[string]any{"type": "boolean"},
	}
	return Definition{Type: "function", Name: "manage_memory", Description: "Inspect account memory and current-session next-request selection. Remember or permanently forget only on explicit user request, with intent and exact expected_revision from inspect. Mutations require separate permission. Forget redacts history and cannot be restored. Scope IDs label contextual guidance and never grant access; do not infer rules from learned context.", Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}}
}
