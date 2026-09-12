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
	}
	return Definition{Type: "function", Name: "manage_memory", Description: "Inspect lists all account memory objects and current-session next-request selection. Mutations require entry_id, explicit user intent, and exact expected_revision from inspect. Remember creates or fully replaces one object and requires content and kind (rule, orientation, learned); choose a stable entry_id for a new object. Edit requires an existing entry_id and at least one of content, kind, workspace_id, session_id, pinned; omitted fields and provenance are preserved. Forget permanently removes an existing object. Remember, edit, or forget only on explicit user request. Mutations require separate permission. Forget redacts history and cannot be restored. Scope IDs label contextual guidance and never grant access; do not infer rules from learned context.", Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}}
}
