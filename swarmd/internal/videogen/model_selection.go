package videogen

import (
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ResolveOperationModel selects only an explicit or account-configured model.
// Source identity determines the settings slot, never an implicit model fallback.
// Catalog capability, source ownership, and credential checks remain mandatory.
func ResolveOperationModel(operation, explicitModel, defaultModel, iterationModel string, omniSource bool) (string, error) {
	if model := strings.TrimSpace(explicitModel); model != "" {
		return model, nil
	}
	if operation == pebblestore.VideoOperationEdit || (operation == pebblestore.VideoOperationExtend && omniSource) {
		if model := strings.TrimSpace(iterationModel); model != "" {
			return model, nil
		}
		return "", errors.New("no default video iteration model configured for account; pass an explicit supported model with user authorization or configure Tools.Video.IterationModel in Settings before creating a video intended for editing or Omni extension; the source model is not selected automatically")
	}
	if model := strings.TrimSpace(defaultModel); model != "" {
		return model, nil
	}
	return "", errors.New("no default video model configured for account; select a model or configure one in Settings")
}
