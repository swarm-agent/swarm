package api

import (
	"encoding/json"
	"strings"
	"testing"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a run rebuilt from stored session messages (a task-wake run, or the
// run started by an ordinary follow-up user message) must see every stored
// tool call and its result, in stored order, even when the step that made the
// calls stored no assistant message.
//
// Regression: provider-context compaction (run.CompactMessagesForProviderContext
// -> compactOlderToolMessagesForProviderContext -> compactStoredToolMessageContent)
// re-encoded older V3 tool records (run.v3.provider-tool-result.v1) as legacy
// tool-history records. sessionsV3ProviderInput only decodes the V3 record, so
// the oldest large tool call of the step (here propose_task, the call that
// created the task) vanished from the woken run's input and the Orchestrator
// could not see or quote its own delegation.
//
// Layer: sessionV3ProviderContextMessages ends with
// CompactMessagesForProviderContext(messages, 500) and providerAssistantResponse
// turns the result into provider input with sessionsV3ProviderInput. Composing
// exactly those two production functions over stored-message snapshots is the
// narrowest layer that shows the defect; the input is provider-neutral and is
// what every provider adapter (including the claudecode transcript) consumes.
func TestSessionsV3RebuiltHistoryKeepsEveryToolCallOfYieldedStep(t *testing.T) {
	const taskID = "task_forge_finder_01"
	bulk := strings.Repeat("linked task detail line\n", 60) // > 512 bytes, so compaction applies
	storedTool := func(seq uint64, runID, name, callID, arguments, output, completed string) pebblestore.MessageSnapshot {
		record := map[string]any{
			"path_id":          "run.v3.provider-tool-result.v1",
			"type":             "v3_provider_tool_result",
			"run_id":           runID,
			"step":             1,
			"tool_name":        name,
			"call_id":          callID,
			"arguments":        arguments,
			"output":           output,
			"completed_output": completed,
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal tool record: %v", err)
		}
		return pebblestore.MessageSnapshot{GlobalSeq: seq, Role: "tool", Content: string(raw), Metadata: map[string]any{"run_id": runID, "step": 1}}
	}
	user := pebblestore.MessageSnapshot{GlobalSeq: 1, Role: "user", Content: "send one read-only finder task to forge, then wait_tasks and give me its answer"}
	// Step 1 of the yielding run: three tool calls, no assistant message stored.
	yieldedStep := []pebblestore.MessageSnapshot{
		storedTool(2, "v3run_A", "manage_projects", "call_propose", `{"action":"propose_task","machine":"forge"}`,
			`{"task_id":"`+taskID+`","status":"started","detail":"`+strings.ReplaceAll(bulk, "\n", " ")+`"}`,
			`{"task_id":"`+taskID+`","status":"started"}`),
		storedTool(3, "v3run_A", "manage_projects", "call_list", `{"action":"list_linked_tasks"}`,
			`{"tasks":[{"task_id":"`+taskID+`"}],"detail":"`+strings.ReplaceAll(bulk, "\n", " ")+`"}`,
			`{"tasks":1}`),
		storedTool(4, "v3run_A", "manage_projects", "call_wait", `{"action":"wait_tasks"}`,
			`{"next_action":"yield_until_task_outcome"}`, ""),
	}
	wake := pebblestore.MessageSnapshot{GlobalSeq: 5, Role: "system", Content: "Delegated project task outcomes: " + taskID + " completed", Metadata: map[string]any{"run_id": "task-wake-1"}}
	followUp := pebblestore.MessageSnapshot{GlobalSeq: 5, Role: "user", Content: "which task did you send?"}

	for _, tc := range []struct {
		name string
		next pebblestore.MessageSnapshot
	}{
		{name: "task_wake", next: wake},
		{name: "follow_up_user_message", next: followUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := append([]pebblestore.MessageSnapshot{user}, yieldedStep...)
			stored = append(stored, tc.next)

			input := sessionsV3ProviderInput(runruntime.CompactMessagesForProviderContext(stored, 500))

			var calls, outputs []string
			outputByCall := map[string]string{}
			for i, item := range input {
				switch sessionsV3MapString(item, "type") {
				case "function_call":
					calls = append(calls, sessionsV3MapString(item, "call_id"))
					if i+1 >= len(input) || sessionsV3MapString(input[i+1], "type") != "function_call_output" || sessionsV3MapString(input[i+1], "call_id") != sessionsV3MapString(item, "call_id") {
						t.Fatalf("tool call %q is not followed by its result: %#v", sessionsV3MapString(item, "call_id"), input)
					}
					if sessionsV3MapString(item, "call_id") == "call_propose" && !strings.Contains(sessionsV3MapString(item, "arguments"), `"machine":"forge"`) {
						t.Fatalf("propose_task arguments lost: %q", sessionsV3MapString(item, "arguments"))
					}
				case "function_call_output":
					callID := sessionsV3MapString(item, "call_id")
					outputs = append(outputs, callID)
					outputByCall[callID] = sessionsV3MapString(item, "output")
				}
				if strings.EqualFold(sessionsV3MapString(item, "role"), "assistant") {
					t.Fatalf("rebuilt history invented assistant text: %#v", item)
				}
			}
			want := []string{"call_propose", "call_list", "call_wait"}
			if strings.Join(calls, ",") != strings.Join(want, ",") {
				t.Fatalf("tool calls in rebuilt input = %v, want %v (input %#v)", calls, want, input)
			}
			if strings.Join(outputs, ",") != strings.Join(want, ",") {
				t.Fatalf("tool results in rebuilt input = %v, want %v", outputs, want)
			}
			if !strings.Contains(outputByCall["call_propose"], taskID) {
				t.Fatalf("propose_task result no longer carries the created task id: %q", outputByCall["call_propose"])
			}
			// Older large outputs are still compacted; the fix keeps the record, not the bulk.
			if strings.Contains(outputByCall["call_propose"], "linked task detail line") {
				t.Fatalf("older propose_task output was not compacted: %q", outputByCall["call_propose"])
			}
			last := input[len(input)-1]
			if !strings.Contains(sessionsV3ProviderTestItemText(last), strings.TrimSpace(tc.next.Content)) {
				t.Fatalf("rebuilt input does not end with the waking message: %#v", last)
			}
		})
	}
}

func sessionsV3ProviderTestItemText(item map[string]any) string {
	parts, _ := item["content"].([]map[string]any)
	var texts []string
	for _, part := range parts {
		if text, ok := part["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}
