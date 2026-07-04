package openrouter

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestBuildChatCompletionRequestUsesLineageScopedSessionID(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{
		SessionID:          "durable-session",
		ProviderLineageID:  "provider-lineage",
		ProviderCacheKey:   "cache-lineage",
		SessionAffinityKey: "affinity-lineage",
		Model:              "openai/gpt-test",
		Input: []map[string]any{{
			"role":    "user",
			"content": "hello",
		}},
	})
	if payload.SessionID != "swarm-lineage-affinity-lineage" {
		t.Fatalf("session_id = %q, want lineage-scoped affinity key", payload.SessionID)
	}
	if payload.SessionID == "durable-session" {
		t.Fatalf("session_id used raw durable session id")
	}
}

func TestBuildChatCompletionRequestOmitsSessionIDWithoutLineage(t *testing.T) {
	payload := buildChatCompletionRequest(provideriface.Request{
		SessionID: "durable-session",
		Model:     "openai/gpt-test",
		Input: []map[string]any{{
			"role":    "user",
			"content": "hello",
		}},
	})
	if payload.SessionID != "" {
		t.Fatalf("session_id = %q, want omitted without provider lineage", payload.SessionID)
	}
}
