package app

import (
	"encoding/json"

	"swarm-refactor/swarmtui/internal/client"
)

func clientSessionPlanDocumentFromAny(value any) *client.SessionPlanDocument {
	if value == nil {
		return nil
	}
	if document, ok := value.(*client.SessionPlanDocument); ok {
		return document
	}
	if document, ok := value.(client.SessionPlanDocument); ok {
		return &document
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var document client.SessionPlanDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil
	}
	return &document
}
