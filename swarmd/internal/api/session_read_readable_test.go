package api

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: get_task_session (controlMCPSessionStateView, here and on a
// linked machine) and read_task_session over a link (boundLinkedTaskMessages)
// spend their text budgets on readable text: a message whose stored JSON
// carries a provider thought signature reads without it, with the one-line
// provider_internal marker, and its readable text inside the budget. Threat:
// a 3,000-character read of a running task going to a thought_signature blob.
// Authority: controlMCPSessionStateView and boundLinkedTaskMessages, which
// both page through tool.ReadableMessageContent; pure view builders are the
// narrowest layer that owns the budget.
func TestSessionReadsLeaveOutProviderInternals(t *testing.T) {
	signature := strings.Repeat("U2lnbmF0dXJl", 1000)
	stored, _ := json.Marshal(map[string]any{"metadata": map[string]any{"google": map[string]any{"thought_signature": signature}}, "text": "Found the limit in api/upload.go:40."})

	view := controlMCPSessionStateView(map[string]any{"messages": []any{
		map[string]any{"id": "m1", "global_seq": float64(3), "role": "assistant", "content": string(stored)},
	}}, 3000, 3000, map[string]any{"action": "read_task_session", "project_id": "p", "task_id": "t"})
	messages, _ := view["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("messages: %v", view["messages"])
	}
	content, _ := messages[0]["content"].(string)
	if strings.Contains(content, "U2lnbmF0dXJl") || !strings.HasPrefix(content, `{"text":"Found the limit in api/upload.go:40."`) || !strings.Contains(content, tool.ProviderInternalMarkerKey) || messages[0]["truncated"] != nil {
		t.Fatalf("get_task_session content: %.300s", content)
	}

	picked, more := boundLinkedTaskMessages([]pebblestore.MessageSnapshot{{ID: "m1", GlobalSeq: 3, Role: "assistant", Content: string(stored)}},
		tool.FleetTaskSessionRead{Limit: 5, MaxChars: 3000, Role: "assistant,tool"})
	if len(picked) != 1 || more || picked[0]["truncated"] != nil || picked[0]["content"] != content {
		t.Fatalf("read_task_session: %v more=%v", picked, more)
	}
}
