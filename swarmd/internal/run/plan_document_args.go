package run

import (
	"encoding/json"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func planPatchArgsPresent(args map[string]any, action string) bool {
	keys := []string{"operation", "patch_operation", "op", "patch_action", "old_text", "new_text", "text", "checklist_item", "item", "replace_all", "checked", "patch"}
	if action == "update_section" {
		keys = append(keys, "section", "update_scope", "scope")
	} else {
		keys = append(keys, "section")
	}
	for _, key := range keys {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func planDocumentFromArgs(args map[string]any) (*pebblestore.SessionPlanDocument, error) {
	value, ok := args["document"]
	if !ok || value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("plan_manage document invalid: %w", err)
	}
	var document pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("plan_manage document invalid: %w", err)
	}
	return &document, nil
}
