package swarmmode

import (
	"encoding/json"
	"testing"
)

func testRequest(count int) ToolRequest {
	return ToolRequest{
		Prompt:             "Build independent variants",
		AgentType:          AgentTypeDesigner,
		Count:              count,
		OutputContract:     "Create one reusable artifact",
		OwnedScopeTemplate: "web/variants/variant-{index}.tsx",
	}
}

func TestToolRequestStrictAndScoped(t *testing.T) {
	raw := `{"prompt":"build","agent_type":"designer","count":2,"output_contract":"artifacts","owned_scope_template":"web/v-{index}.tsx"}`
	request, err := DecodeToolRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Count != 2 {
		t.Fatalf("count = %d", request.Count)
	}
	for _, invalid := range []string{
		`{"prompt":"build","agent_type":"designer","count":2,"output_contract":"artifacts"}`,
		`{"prompt":"build","agent_type":"coder","count":0,"output_contract":"artifacts"}`,
		`{"prompt":"build","agent_type":"coder","count":2,"output_contract":"artifacts","extra":true}`,
	} {
		if _, err := DecodeToolRequest(invalid); err == nil {
			t.Fatalf("DecodeToolRequest(%s) unexpectedly succeeded", invalid)
		}
	}
}

func TestTwoRoundContractsRequireCompleteOrderedBatches(t *testing.T) {
	request := testRequest(3)
	roundOneRequest := RoundOneRequest{Prompt: request.Prompt, AgentType: request.AgentType, Count: request.Count}
	roundOneRaw := `{"themes":[{"index":1,"theme":"compact"},{"index":2,"theme":"spacious"},{"index":3,"theme":"editorial"}]}`
	roundOne, err := DecodeRoundOneResult(roundOneRaw, roundOneRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundOne.Themes) != 3 {
		t.Fatalf("themes = %d", len(roundOne.Themes))
	}
	roundTwoRequest := RoundTwoRequest{Prompt: request.Prompt, AgentType: request.AgentType, OutputContract: request.OutputContract, OwnedScopeTemplate: request.OwnedScopeTemplate, Themes: roundOne.Themes}
	roundTwoRaw := `{"prompts":[{"index":1,"prompt":"build compact"},{"index":2,"prompt":"build spacious"},{"index":3,"prompt":"build editorial"}]}`
	roundTwo, err := DecodeRoundTwoResult(roundTwoRaw, roundTwoRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTwo.Prompts) != 3 {
		t.Fatalf("prompts = %d", len(roundTwo.Prompts))
	}
	for _, invalid := range []string{
		`{"themes":[{"index":1,"theme":"compact"},{"index":3,"theme":"editorial"}]}`,
		`{"themes":[{"index":1,"theme":"same"},{"index":2,"theme":"same"},{"index":3,"theme":"other"}]}`,
	} {
		if _, err := DecodeRoundOneResult(invalid, roundOneRequest); err == nil {
			t.Fatalf("round one accepted %s", invalid)
		}
	}
	if _, err := DecodeRoundTwoResult(`{"prompts":[{"index":1,"prompt":"one"}]}`, roundTwoRequest); err == nil {
		t.Fatal("round two accepted incomplete batch")
	}
}

func TestTwoRoundSchemasAndPromptsAreToolFree(t *testing.T) {
	for _, schema := range []map[string]any{RoundOneResultSchema(2), RoundTwoResultSchema(2)} {
		raw, err := json.Marshal(schema)
		if err != nil || len(raw) == 0 {
			t.Fatalf("schema marshal: %v", err)
		}
	}
	for name, prompt := range map[string]string{"round one": RoundOneSystemPrompt(), "round two": RoundTwoSystemPrompt()} {
		if prompt == "" || !containsAll(prompt, "tool-free", "single response", "Do not call tools") {
			t.Fatalf("%s prompt missing contract: %q", name, prompt)
		}
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !contains(value, needle) {
			return false
		}
	}
	return true
}

func contains(value, needle string) bool {
	return len(needle) == 0 || len(value) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(value); i++ {
			if value[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
