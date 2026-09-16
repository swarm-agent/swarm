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
	return Definition{Type: "function", Name: "manage_memory", Description: "Inspect lists all account memory objects and current-session next-request selection. Mutations require entry_id, explicit user intent, and exact expected_revision from inspect. Remember creates or fully replaces one object and requires content and kind (rule, orientation, learned); choose a stable entry_id for a new object. Edit requires an existing entry_id and at least one of content, kind, purpose, subject, workspace_id, session_id, pinned; omitted fields and provenance are preserved. Purpose describes use independently of kind; subject is a descriptive label. Origin is server-owned creation provenance; legacy unknown origins remain unknown. Forget permanently removes an existing object. Remember, edit, or forget only on explicit user request. Mutations require separate permission. Forget redacts history and cannot be restored. Scope IDs label contextual guidance and never grant access; do not infer rules from learned context. Keep portable repository rules in AGENTS.md and private operational facts in explicitly approved memory. Store credential locations only, never secret values. Recovery guidance is optional and must not gate resume. Update the canonical Workspace Map through manage_workspace, not as an ordinary memory replacement.", Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}}
}
