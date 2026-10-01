package run

// Independent design evidence is not an Artifact V3 reference or author grant.
func designPreviewReferenceSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	output := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"request_id", "candidate", "attempt", "child_session_id", "run_id", "sha256"},
		"properties": map[string]any{"request_id": text(), "candidate": map[string]any{"type": "integer", "minimum": 0}, "attempt": map[string]any{"type": "integer", "minimum": 1}, "child_session_id": text(), "run_id": text(), "sha256": text()}}
	return map[string]any{"type": "object", "additionalProperties": false,
		"description": "Exact independent design preview reference from authenticated ready revision history",
		"required": []string{"session_id", "preview"}, "properties": map[string]any{
			"session_id": text(), "preview": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"output", "sha256"}, "properties": map[string]any{"output": output, "sha256": text()}}}}
}
