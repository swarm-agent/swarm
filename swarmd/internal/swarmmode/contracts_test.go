package swarmmode

import (
	"strings"
	"testing"
)

func TestDecodeToolRequestRejectsInvalidContracts(t *testing.T) {
	valid := `{"prompt":"build variants","agent_type":"designer","count":2,"themes":["compact","spacious"],"output_contract":"write the assigned artifact","owned_scope_template":"web/variant-{index}.tsx"}`
	request, err := DecodeToolRequest(valid)
	if err != nil {
		t.Fatalf("DecodeToolRequest(valid): %v", err)
	}
	if request.Count != 2 || request.AgentType != AgentTypeDesigner {
		t.Fatalf("DecodeToolRequest(valid) = %+v", request)
	}

	for name, raw := range map[string]string{
		"unknown agent":       strings.Replace(valid, `"designer"`, `"finder"`, 1),
		"zero count":          strings.Replace(valid, `"count":2`, `"count":0`, 1),
		"over cap":            strings.Replace(valid, `"count":2`, `"count":101`, 1),
		"theme cardinality":   strings.Replace(valid, `,"spacious"`, ``, 1),
		"missing placeholder": strings.Replace(valid, `variant-{index}`, `variant`, 1),
		"oversized template":  strings.Replace(valid, `web/variant-{index}.tsx`, strings.Repeat("a", MaxOwnedScopeTemplateRunes)+`/{index}`, 1),
		"unknown field":       strings.TrimSuffix(valid, `}`) + `,"extra":true}`,
		"trailing":            valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeToolRequest(raw); err == nil {
				t.Fatalf("DecodeToolRequest(%s) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestNormalizeMaxAgents(t *testing.T) {
	for input, want := range map[int]int{-2: 1, 0: 1, 1: 1, 10: 10, 100: 100, 101: 100} {
		if got := NormalizeMaxAgents(input); got != want {
			t.Fatalf("NormalizeMaxAgents(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestDecodeGroupExpansionResultStrict(t *testing.T) {
	request := GroupExpansionRequest{Prompt: "build", AgentType: AgentTypeCoder, StartIndex: 11, Count: 2}
	valid := `{"themes":[{"index":11,"theme":"security"},{"index":12,"theme":"performance"}]}`
	if _, err := DecodeGroupExpansionResult(valid, request); err != nil {
		t.Fatalf("DecodeGroupExpansionResult(valid): %v", err)
	}
	if err := ValidateExpandedThemes([]IndexedTheme{{Index: 1, Theme: "same"}, {Index: 2, Theme: "Same"}}, 2); err == nil {
		t.Fatal("ValidateExpandedThemes accepted a cross-group duplicate")
	}
	for name, raw := range map[string]string{
		"wrong count":    `{"themes":[{"index":11,"theme":"security"}]}`,
		"unstable index": `{"themes":[{"index":12,"theme":"security"},{"index":11,"theme":"performance"}]}`,
		"duplicate":      `{"themes":[{"index":11,"theme":"security"},{"index":12,"theme":"Security"}]}`,
		"unknown field":  `{"themes":[{"index":11,"theme":"security","extra":true},{"index":12,"theme":"performance"}]}`,
		"trailing":       valid + ` trailing`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeGroupExpansionResult(raw, request); err == nil {
				t.Fatalf("DecodeGroupExpansionResult(%s) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestDecodeRefinementResultStrict(t *testing.T) {
	request := RefinementRequest{Prompt: "build", AgentType: AgentTypeDesigner, OutputContract: "write artifact", Index: 2, Theme: "compact", OwnedScope: "web/variant-2.tsx"}
	valid := `{"index":2,"prompt":"Create the compact variant at web/variant-2.tsx."}`
	result, err := DecodeRefinementResult(valid, request)
	if err != nil {
		t.Fatalf("DecodeRefinementResult(valid): %v", err)
	}
	if err := ValidateRefinementResults([]RefinementResult{{Index: 1, Prompt: "one"}, result}, 2); err != nil {
		t.Fatalf("ValidateRefinementResults(valid): %v", err)
	}
	for name, raw := range map[string]string{
		"wrong index":   `{"index":1,"prompt":"Create it"}`,
		"empty prompt":  `{"index":2,"prompt":""}`,
		"unknown field": `{"index":2,"prompt":"Create it","extra":true}`,
		"trailing":      valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRefinementResult(raw, request); err == nil {
				t.Fatalf("DecodeRefinementResult(%s) unexpectedly succeeded", raw)
			}
		})
	}
	if err := ValidateRefinementResults([]RefinementResult{{Index: 1, Prompt: "same"}, {Index: 2, Prompt: "Same"}}, 2); err == nil {
		t.Fatal("ValidateRefinementResults accepted duplicate prompts")
	}
}

func TestRouterPromptsAreToolFreeOneShotContracts(t *testing.T) {
	for name, prompt := range map[string]string{"group": GroupExpansionSystemPrompt(), "refinement": RefinementSystemPrompt()} {
		lower := strings.ToLower(prompt)
		for _, required := range []string{"tool-free", "one-shot", "do not call tools", "return only one json object"} {
			if !strings.Contains(lower, required) {
				t.Fatalf("%s prompt missing %q: %s", name, required, prompt)
			}
		}
	}
}
