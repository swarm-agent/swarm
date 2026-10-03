package tool

import (
	"strings"
	"testing"
)

// Purpose: manageProjectsDefinition must expose project-source inspection alongside
// the independently landed requirement-edit action. This schema-level regression
// catches either side being dropped during integration; it does not establish
// authorization or execution safety, which require the runtime boundary tests.
func TestProjectSourceDefinitionPreservesIntegratedActions(t *testing.T) {
	definition := manageProjectsDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	actions := strings.Split(strings.TrimPrefix(action["description"].(string), "Action: "), "|")
	for _, want := range []string{"list_sources", "inspect_source", "edit_requirements"} {
		count := 0
		for _, got := range actions {
			if got == want {
				count++
			}
		}
		if count != 1 {
			t.Errorf("action %q appears %d times, want exactly once", want, count)
		}
		if !strings.Contains(definition.Description, want) {
			t.Errorf("top-level description omits %q", want)
		}
	}
	for _, name := range []string{"workspace_path", "workspace_id", "workspace_generation", "document_patch"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("integrated action parameter %q missing", name)
		}
	}
}
