package tool

func taskProgressDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "task_progress",
		Description: "Track subagent task progress, maintain the execution checklist, and signal lifecycle state changes (completed or blocked). Subagent sessions start in progress automatically. Call this tool to set or update checklist items, mark the current focus, or signal that work is finished (transitioning to needs_review) or blocked.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action: set_todos | add_todo | update_todo | complete_todo | in_progress | done | complete | blocked | status | list",
				},
				"todos": map[string]any{
					"type":        "array",
					"description": "Array of todo items (strings or objects with id, title, status) for set_todos or batch updates",
					"items": map[string]any{
						"type": "string",
					},
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Title or text of a todo item (for add_todo, update_todo, in_progress, complete_todo)",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "ID of a specific todo item (optional; can identify by title instead)",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "Status for update_todo: pending | in_progress | completed",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Explanation of blocker for action='blocked'",
				},
				"blocker_code": map[string]any{
					"type":        "string",
					"description": "Optional blocker code for action='blocked'",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "Summary of work done for action='done' / action='complete'",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}
