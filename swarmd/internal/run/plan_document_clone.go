package run

import (
	"encoding/json"
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func clonePlanDocumentForExecutionAction(doc *pebblestore.SessionPlanDocument) (*pebblestore.SessionPlanDocument, error) {
	if doc == nil {
		return nil, errors.New("plan document is required")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var clone pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}
