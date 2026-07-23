package tool

import (
	"strings"
	"testing"
)

func TestBashDefinitionRequiresIntentMetadata(t *testing.T) {
	var bash Definition
	for _, definition := range NewRuntime(1).Definitions() {
		if definition.Name == "bash" {
			bash = definition
			break
		}
	}
	if bash.Name == "" {
		t.Fatal("bash definition not found")
	}

	required, ok := bash.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("bash required fields = %#v", bash.Parameters["required"])
	}
	for _, want := range []string{"command", "explanation", "category", "critical"} {
		if !containsString(required, want) {
			t.Fatalf("bash required fields missing %q: %#v", want, required)
		}
	}

	properties := bash.Parameters["properties"].(map[string]any)
	category := properties["category"].(map[string]any)
	if got := category["enum"].([]string); len(got) != 4 || got[0] != "read" || got[1] != "write" || got[2] != "update" || got[3] != "delete" {
		t.Fatalf("bash category enum = %#v", category["enum"])
	}
	description := strings.ToLower(bash.Description)
	if !strings.Contains(description, "routine intent in one direct line") || !strings.Contains(description, "consequential environmental changes") {
		t.Fatalf("bash description does not require concise, risk-focused intent: %s", bash.Description)
	}
	explanation := strings.ToLower(properties["explanation"].(map[string]any)["description"].(string))
	for _, want := range []string{"one concise", "multiple concise items", "listeners", "ports", "network exposure", "privileges", "destructive"} {
		if !strings.Contains(explanation, want) {
			t.Fatalf("bash explanation guidance missing %q: %s", want, explanation)
		}
	}
	for _, discouraged := range []string{"output capture", "exit status", "working directory", "lack of source edits", "generic artifacts"} {
		if !strings.Contains(explanation, discouraged) {
			t.Fatalf("bash explanation guidance does not discourage %q narration: %s", discouraged, explanation)
		}
	}
}

func TestBashDefinitionDocumentsEffectCategoriesAndCriticalReads(t *testing.T) {
	var bash Definition
	for _, definition := range NewRuntime(1).Definitions() {
		if definition.Name == "bash" {
			bash = definition
			break
		}
	}
	properties := bash.Parameters["properties"].(map[string]any)
	category := strings.ToLower(properties["category"].(map[string]any)["description"].(string))
	for _, want := range []string{"read only observes", "write creates", "update is a non-removal", "delete removes", "critical=true", "highest-impact"} {
		if !strings.Contains(category, want) {
			t.Fatalf("bash category guidance missing %q: %s", want, category)
		}
	}
}

func TestBashDefinitionShowsRoutineBuildAsOneDirectLine(t *testing.T) {
	arguments := `{"command":"npm run build","explanation":["Build the workspace."],"category":"write","critical":false}`
	if err := ValidateBashCallArguments(arguments); err != nil {
		t.Fatalf("concise routine Bash intent rejected: %v", err)
	}
}

func TestValidateBashCallArgumentsAcceptsCriticalDelete(t *testing.T) {
	arguments := `{"command":"rm old.log","explanation":["Remove old.log."],"category":"delete","critical":true}`
	if err := ValidateBashCallArguments(arguments); err != nil {
		t.Fatalf("critical delete rejected: %v", err)
	}
}

func TestValidateBashCallArgumentsRejectsMissingOrMalformedMetadata(t *testing.T) {
	valid := `{"command":"git status --short","explanation":["Show the working-tree status."],"category":"read","critical":false}`
	if err := ValidateBashCallArguments(valid); err != nil {
		t.Fatalf("valid Bash arguments rejected: %v", err)
	}

	for name, arguments := range map[string]string{
		"missing explanation": `{"command":"pwd","category":"read","critical":false}`,
		"invalid category":    `{"command":"pwd","explanation":["Print the current working directory."],"category":"inspect","critical":false}`,
		"missing critical":    `{"command":"pwd","explanation":["Print the current working directory."],"category":"read"}`,
		"empty item":          `{"command":"pwd","explanation":[""],"category":"read","critical":false}`,
		"delete not critical": `{"command":"rm old.log","explanation":["Remove old.log."],"category":"delete","critical":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBashCallArguments(arguments); err == nil {
				t.Fatal("expected malformed Bash arguments to be rejected")
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
