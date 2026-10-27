package tool

import (
	"encoding/json"
	"testing"
)

// Purpose: manageMediaDefinition is the public action/argument contract. This
// unit test prevents list/retry and speech fields from becoming unreachable through strict schemas;
// authorization and mutation postconditions belong to the API integration tests.
func TestManageMediaDefinition(t *testing.T) {
	def := manageMediaDefinition()
	data, err := json.Marshal(def.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"list", "retry"} {
		found := false
		for _, action := range schema.Properties["action"].Enum {
			if action == wanted {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing action %s", wanted)
		}
	}
	for _, key := range []string{"status", "limit", "job_id", "model", "brief", "operation", "text", "voice", "style", "language", "format"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("missing property %s", key)
		}
	}
}
