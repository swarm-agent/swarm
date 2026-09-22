package api

import (
	"testing"
)

func TestAnthropicTokenOverflowDiagnosticMatching(t *testing.T) {
	// Purpose:
	// - Requirement: Anthropic 400 invalid_request_error token count overflow errors must be recognized by context overflow classifiers.
	// - Threat/regression: Anthropic error formatting variations prevent context overflow compaction from triggering, causing premature session failure.
	// - Boundary/authority: sessionV3IsAnthropicTokenOverflowDiagnostic, sessionV3IsContextOverflowDiagnostic, parseSessionV3AnthropicMaxAllowedTokens in api/sessions_v3_executor.go.
	// - Narrowest test layer: Unit test verifying pattern matching across exact Anthropic error signatures and negative cases.
	const sdkSample = `anthropic: 400 invalid_request_error: prompt is too long: 215432 tokens > 200000 maximum`
	const jsonSample = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 204800 tokens > 200000 maximum"}}`
	const genericSample = `prompt is too long`
	const shortSample = `prompt too long`
	const maxTokensSample = `prompt is too long: 250000 tokens > 128000 max`

	positives := []string{
		sdkSample,
		jsonSample,
		genericSample,
		shortSample,
		maxTokensSample,
		"prompt is too long: 215432 tokens > 200000 maximum",
		"Prompt is too long: 1000 tokens > 500 maximum",
	}
	for _, raw := range positives {
		if !sessionV3IsAnthropicTokenOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsAnthropicTokenOverflowDiagnostic(%q) = false, want true", raw)
		}
		if !sessionV3IsContextOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsContextOverflowDiagnostic(%q) = false, want true", raw)
		}
	}

	negatives := []string{
		"",
		"anthropic: 529 overloaded_error: Overloaded",
		"status=500 body=internal server error",
		"status=401 body=invalid x-api-key",
		"rate limit exceeded",
		"credit balance is too low",
	}
	for _, raw := range negatives {
		if sessionV3IsAnthropicTokenOverflowDiagnostic(raw) {
			t.Errorf("sessionV3IsAnthropicTokenOverflowDiagnostic(%q) = true, want false", raw)
		}
	}

	// Test max allowed tokens parser.
	if limit := parseSessionV3AnthropicMaxAllowedTokens(sdkSample); limit != 200000 {
		t.Fatalf("parseSessionV3AnthropicMaxAllowedTokens(sdkSample) = %d, want 200000", limit)
	}
	if limit := parseSessionV3AnthropicMaxAllowedTokens(jsonSample); limit != 200000 {
		t.Fatalf("parseSessionV3AnthropicMaxAllowedTokens(jsonSample) = %d, want 200000", limit)
	}
	if limit := parseSessionV3AnthropicMaxAllowedTokens(maxTokensSample); limit != 128000 {
		t.Fatalf("parseSessionV3AnthropicMaxAllowedTokens(maxTokensSample) = %d, want 128000", limit)
	}
	if limit := parseSessionV3AnthropicMaxAllowedTokens("unrelated error"); limit != 0 {
		t.Fatalf("parseSessionV3AnthropicMaxAllowedTokens(unrelated) = %d, want 0", limit)
	}
}

func TestGenericAndFireworksTokenOverflowDiagnosticMatching(t *testing.T) {
	// Purpose:
	// - Requirement: OpenRouter/Fireworks/OpenAI context overflow error strings must be parsed to extract token window.
	// - Threat/regression: Failure to parse context limit leaves contextWindow=0, disabling compaction.
	// - Boundary/authority: parseSessionV3GenericMaxAllowedTokens in api/sessions_v3_executor.go.
	// - Narrowest test layer: Unit test verifying limit extraction across provider error signatures.
	const fwSample = "Input length (135000) exceeds the maximum length (131072)"
	const orSample = "This endpoint's maximum context length is 131072 tokens. However, you requested 135000 tokens"
	const oaiSample = "This model's maximum context length is 128000 tokens. However, you requested 130000 tokens"
	const genericSample = "context length of 65536 tokens was exceeded"

	if limit := parseSessionV3GenericMaxAllowedTokens(fwSample); limit != 131072 {
		t.Fatalf("parseSessionV3GenericMaxAllowedTokens(fwSample) = %d, want 131072", limit)
	}
	if limit := parseSessionV3GenericMaxAllowedTokens(orSample); limit != 131072 {
		t.Fatalf("parseSessionV3GenericMaxAllowedTokens(orSample) = %d, want 131072", limit)
	}
	if limit := parseSessionV3GenericMaxAllowedTokens(oaiSample); limit != 128000 {
		t.Fatalf("parseSessionV3GenericMaxAllowedTokens(oaiSample) = %d, want 128000", limit)
	}
	if limit := parseSessionV3GenericMaxAllowedTokens(genericSample); limit != 65536 {
		t.Fatalf("parseSessionV3GenericMaxAllowedTokens(genericSample) = %d, want 65536", limit)
	}
}
