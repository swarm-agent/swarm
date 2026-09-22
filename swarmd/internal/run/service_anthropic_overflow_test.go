package run

import (
	"testing"
)

func TestRunIsAnthropicTokenOverflowDiagnostic(t *testing.T) {
	// Purpose:
	// - Requirement: Anthropic 400 invalid_request_error token overflow errors must be classified as context overflow in run.Service.
	// - Threat/regression: Failure to recognize Anthropic's exact error format skips compaction recovery in RunTurn.
	// - Boundary/authority: isAnthropicTokenOverflowDiagnostic, isContextOverflowDiagnostic, parseAnthropicMaxAllowedTokens in run/service.go.
	// - Narrowest test layer: Unit test verifying pattern matching and limit parsing.
	const sdkSample = `anthropic: 400 invalid_request_error: prompt is too long: 215432 tokens > 200000 maximum`
	const jsonSample = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 204800 tokens > 200000 maximum"}}`

	if !isAnthropicTokenOverflowDiagnostic(sdkSample) {
		t.Fatal("isAnthropicTokenOverflowDiagnostic(sdkSample) = false, want true")
	}
	if !isContextOverflowDiagnostic(sdkSample) {
		t.Fatal("isContextOverflowDiagnostic(sdkSample) = false, want true")
	}
	if limit := parseAnthropicMaxAllowedTokens(sdkSample); limit != 200000 {
		t.Fatalf("parseAnthropicMaxAllowedTokens(sdkSample) = %d, want 200000", limit)
	}

	if !isAnthropicTokenOverflowDiagnostic(jsonSample) {
		t.Fatal("isAnthropicTokenOverflowDiagnostic(jsonSample) = false, want true")
	}
	if !isContextOverflowDiagnostic(jsonSample) {
		t.Fatal("isContextOverflowDiagnostic(jsonSample) = false, want true")
	}
	if limit := parseAnthropicMaxAllowedTokens(jsonSample); limit != 200000 {
		t.Fatalf("parseAnthropicMaxAllowedTokens(jsonSample) = %d, want 200000", limit)
	}

	negative := "anthropic: 529 overloaded_error: Overloaded"
	if isAnthropicTokenOverflowDiagnostic(negative) {
		t.Fatal("isAnthropicTokenOverflowDiagnostic(negative) = true, want false")
	}
}
