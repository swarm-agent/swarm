package pebblestore

import (
	"encoding/json"

	"swarm/packages/swarmd/internal/privacy"
)

// MarshalJSON protects task error presentation and new task snapshots without
// rewriting historical events. Alias conversion avoids recursive marshaling;
// the copied deliverable slice leaves the caller's canonical record untouched.
func (task ProjectTaskRecord) MarshalJSON() ([]byte, error) {
	type plain ProjectTaskRecord
	out := plain(task)
	out.LastError = privacy.SanitizeDiagnostic(out.LastError)
	out.Deliverables = append([]ProjectTaskDeliverable(nil), task.Deliverables...)
	for i := range out.Deliverables {
		if out.Deliverables[i].Status == "failed" {
			out.Deliverables[i].Description = privacy.SanitizeDiagnostic(out.Deliverables[i].Description)
		}
	}
	return json.Marshal(out)
}
