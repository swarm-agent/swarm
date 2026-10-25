package tool

// WorkflowTriggerDescription is shared by the two workflow delegation paths.
const WorkflowTriggerDescription = "Optional on starts a worker job from an outside event: pull polls an HTTPS feed, while webhook accepts signed events. The first poll records what is there without starting jobs. start.worker must not be a reviewer."

// WorkflowTriggerSchema describes the HTTP save shape; validation belongs to the
// workflow service, not either tool adapter.
func WorkflowTriggerSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "required": []string{"event", "start"}, "properties": map[string]any{
		"event": map[string]any{"type": "object", "required": []string{"source", "id"}, "properties": map[string]any{
			"source": map[string]any{"type": "string", "enum": []string{"pull", "webhook"}},
			"url":    text(), "every_seconds": map[string]any{"type": "integer"},
			"secret": map[string]any{"type": "string", "description": "Granted Swarm secret name, not its value."}, "items": text(), "id": text(),
		}},
		"start": map[string]any{"type": "object", "required": []string{"worker", "brief"}, "properties": map[string]any{
			"worker": text(), "brief": text(),
		}},
	}}
}
