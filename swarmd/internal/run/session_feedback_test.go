package run

import (
	"context"
	"strings"
	"sync"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type queuedNoteStubInvoker struct {
	result provideriface.ToolExecutionResult
}

func (s queuedNoteStubInvoker) ExecuteTool(context.Context, provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	return s.result, nil
}

// queuedNoteLog is an append-only message log read the way the executor reads
// session messages: everything after a cursor, advancing it.
type queuedNoteLog struct {
	mu       sync.Mutex
	messages []pebblestore.MessageSnapshot
}

func (l *queuedNoteLog) add(id, content string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := uint64(len(l.messages) + 1)
	l.messages = append(l.messages, pebblestore.MessageSnapshot{ID: id, Role: "user", Content: content, GlobalSeq: seq, Metadata: map[string]any{"feedback_note": true, QueuedNoteMetadataKey: true}})
}

func (l *queuedNoteLog) read(cursor *uint64) ([]pebblestore.MessageSnapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []pebblestore.MessageSnapshot
	for _, message := range l.messages {
		if message.GlobalSeq > *cursor {
			*cursor = message.GlobalSeq
			out = append(out, message)
		}
	}
	return out, nil
}

// Purpose: the invoker that delivers queued notes with tool results hands
// each note to the model exactly once, even when a provider runs tool calls in
// parallel, appends it after the tool's own output without touching the
// tool's payload, and leaves notes for the next boundary when the tool result
// restarts the turn (its input is rebuilt, so an appended note could be lost).
// Threat: a note delivered twice, lost on restart, or corrupting a tool
// payload that the run loop parses.
// Boundary: QueuedNoteTracker.Claim, QueuedNoteToolInvoker.ExecuteTool,
// AppendQueuedNotesToToolResult.
// Narrowest layer: the invoker over a stub inner invoker and an in-memory log;
// the end-to-end route is covered in the api package.
func TestQueuedNoteToolInvokerDeliversEachNoteOnce(t *testing.T) {
	log := &queuedNoteLog{}
	tracker := NewQueuedNoteTracker(0)
	var mu sync.Mutex
	var delivered []string
	invoker := &QueuedNoteToolInvoker{
		Inner:   queuedNoteStubInvoker{result: provideriface.ToolExecutionResult{Output: `{"status":"ok"}`}},
		Tracker: tracker,
		Read:    log.read,
		Delivered: func(notes []pebblestore.MessageSnapshot) error {
			mu.Lock()
			defer mu.Unlock()
			for _, note := range notes {
				delivered = append(delivered, note.ID)
			}
			return nil
		},
	}
	log.add("note-a", "first steer")
	log.add("note-b", "second steer")

	results := make([]provideriface.ToolExecutionResult, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "c", Name: "read", Arguments: `{}`})
			if err != nil {
				t.Error(err)
			}
			results[i] = result
		}(i)
	}
	wg.Wait()
	total := 0
	for _, result := range results {
		if result.Output != `{"status":"ok"}` {
			t.Fatalf("tool payload changed: %q", result.Output)
		}
		total += strings.Count(result.TextForModel, "first steer") + strings.Count(result.TextForModel, "second steer")
		if block := strings.Index(result.TextForModel, "<queued_message_from_the_person>"); block >= 0 && !strings.Contains(result.TextForModel[:block], "ok") {
			t.Fatalf("queued block must follow the tool output: %q", result.TextForModel)
		}
	}
	if total != 2 || len(delivered) != 2 {
		t.Fatalf("notes delivered %d times with %d receipts, want 2 and 2", total, len(delivered))
	}

	log.add("note-c", "third steer")
	invoker.Inner = queuedNoteStubInvoker{result: provideriface.ToolExecutionResult{Output: "restarting", RestartTurn: true}}
	restart, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "r", Name: "plan_manage"})
	if err != nil || strings.Contains(restart.TextForModel, "third steer") {
		t.Fatalf("a restarting tool result must not carry notes: %q %v", restart.TextForModel, err)
	}
	if tracker.Cursor() != 2 {
		t.Fatalf("restart claimed the note: cursor %d, want 2", tracker.Cursor())
	}
	invoker.Inner = queuedNoteStubInvoker{result: provideriface.ToolExecutionResult{Output: "plain"}}
	next, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "n", Name: "read"})
	if err != nil || strings.Count(next.TextForModel, "third steer") != 1 {
		t.Fatalf("the next tool result must carry the note once: %q %v", next.TextForModel, err)
	}
}
