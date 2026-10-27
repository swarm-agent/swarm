package google

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestGoogleModelVersionCapabilities(t *testing.T) {
	// Purpose: supportsGoogle3OrLater and supportsGoogleThinking must recognize
	// future Gemini majors without enabling unknown models or changing legacy
	// thinking support. Pure capability checks are the narrowest boundary.
	for _, tc := range []struct {
		model            string
		modern, thinking bool
	}{
		{"gemini-3-pro", true, true},
		{"gemini-4-argon", true, true},
		{"gemini-10-pro", true, true},
		{" models/GEMINI-4-argon ", true, true},
		{"gemini-2.5-pro", false, true},
		{"gemini-2.0-flash-thinking", false, true},
		{"gemini-2.0-flash", false, false},
		{"gemini-unknown", false, false},
		{"gemini-", false, false},
		{"other-4-thinking", false, false},
		{"", false, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := supportsGoogle3OrLater(tc.model); got != tc.modern {
				t.Errorf("modern = %v, want %v", got, tc.modern)
			}
			if got := supportsGoogleThinking(tc.model); got != tc.thinking {
				t.Errorf("thinking = %v, want %v", got, tc.thinking)
			}
		})
	}
}

func TestGoogleModernHistorySignatures(t *testing.T) {
	// Purpose: buildGoogleRequest/buildGoogleContentsWithMedia must repair an
	// unsigned first call in replayed history, without overwriting signatures or
	// signing later parallel calls. Request construction directly proves the
	// outgoing contract without a provider/network dependency.
	for _, model := range []string{"gemini-3-pro", "gemini-4-argon", "gemini-10-pro", "gemini-2.5-pro", "unknown"} {
		for _, signature := range []string{"", "stored-signature"} {
			t.Run(model+"/"+signature, func(t *testing.T) {
				req, err := buildGoogleRequest(provideriface.Request{Model: model, Input: []map[string]any{
					{"type": "function_call", "call_id": "first", "name": "read", "arguments": "{}", "thought_signature": signature},
					{"type": "function_call", "call_id": "parallel", "name": "read", "arguments": "{}"},
					{"type": "function_call", "call_id": "signed-parallel", "name": "read", "arguments": "{}", "thought_signature": "parallel-signature"},
					{"type": "function_call_output", "call_id": "first", "output": "done"},
					{"type": "function_call_output", "call_id": "parallel", "output": "done"},
					{"type": "function_call_output", "call_id": "signed-parallel", "output": "done"},
					{"type": "function_call", "call_id": "next", "name": "read", "arguments": "{}"},
				}})
				if err != nil {
					t.Fatal(err)
				}
				if len(req.Contents) != 5 || len(req.Contents[1].Parts) != 3 || len(req.Contents[3].Parts) != 1 {
					t.Fatalf("unexpected history: %+v", req.Contents)
				}
				unsigned := ""
				if model != "gemini-2.5-pro" && model != "unknown" {
					unsigned = googleSkipSignature
				}
				want := signature
				if want == "" {
					want = unsigned
				}
				for i, expected := range []string{want, "", "parallel-signature"} {
					if got := partThoughtSignatureValue(req.Contents[1].Parts[i]); got != expected {
						t.Errorf("call %d signature = %q, want %q", i, got, expected)
					}
				}
				if got := partThoughtSignatureValue(req.Contents[3].Parts[0]); got != unsigned {
					t.Errorf("next step signature = %q, want %q", got, unsigned)
				}
			})
		}
	}
}

func TestGoogleModernThinkingRequest(t *testing.T) {
	// Purpose: buildGoogleRequest/googleThinkingConfigForRequest must emit the
	// same supported thinking settings for Gemini 4 and Gemini 3 when no catalog
	// mapping is supplied. Request-level assertions catch omitted configs, while
	// unsupported levels must leave generation config absent.
	for _, model := range []string{"gemini-3-pro", "gemini-4-argon", "gemini-10-pro"} {
		for _, tc := range []struct {
			level  string
			budget int
		}{
			{"off", 0}, {"low", 1024}, {"medium", 4096}, {"high", 8192}, {"xhigh", 16384}, {"", -1}, {"unsupported", -1},
		} {
			t.Run(model+"/"+tc.level, func(t *testing.T) {
				req, err := buildGoogleRequest(provideriface.Request{Model: model, Thinking: tc.level, Input: []map[string]any{{"role": "user", "content": "hello"}}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.budget < 0 {
					if req.GenerationConfig != nil {
						t.Fatalf("unexpected config: %+v", req.GenerationConfig)
					}
					return
				}
				if req.GenerationConfig == nil || req.GenerationConfig.ThinkingConfig == nil {
					t.Fatal("missing thinking config")
				}
				config := req.GenerationConfig.ThinkingConfig
				if config.ThinkingBudget == nil || *config.ThinkingBudget != tc.budget || config.IncludeThoughts != (tc.level != "off") || config.ThinkingLevel != "" {
					t.Fatalf("unexpected thinking config: %+v; want budget %d", config, tc.budget)
				}
			})
		}
	}
}
