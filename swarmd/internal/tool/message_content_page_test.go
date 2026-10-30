package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// longMultiByteMessage is a ~50,000-byte message mixing one- to four-byte
// runes and characters JSON escapes (<, >, &, ", \, newline), so chunk
// boundaries fall inside multi-byte runes and escaped text.
func longMultiByteMessage() string {
	var b strings.Builder
	for i := 0; b.Len() < 50000; i++ {
		b.WriteString("Section ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(" é漢🙂 <a & \"b\"> \\ ü\n")
	}
	return b.String()
}

// Purpose: a message longer than a read's budget is returned in chunks that
// concatenate to the message exactly, each cut on a rune boundary and within
// the budget as JSON, so a long task result is never silently cut off.
// Threat: a read that drops the rest of a long message, splits a multi-byte
// character, loops without progress, or accepts a stale/misaligned offset.
// Boundary: ChunkMessageContent, the one rule behind manage_projects
// read_task_session and manage-sessions read_messages. Narrowest layer: the
// pure chunking function; the reads that use it are covered in their own
// tests (api TestFleetLinkedTaskSessionReadsALongMessageInFull and
// TestManageSessionsReadMessagesPagesALongMessage).
func TestChunkMessageContentReadsALongMessageExactly(t *testing.T) {
	message := longMultiByteMessage()
	if len(message) < 50000 || utf8.RuneCountInString(message) == len(message) {
		t.Fatalf("fixture is %d bytes and must hold multi-byte runes", len(message))
	}
	for _, budget := range []int{1, 7, 1000, 3500, 20000} {
		var got strings.Builder
		offset, chunks := 0, 0
		for {
			chunk, err := ChunkMessageContent(message, offset, budget)
			if err != nil {
				t.Fatalf("budget %d offset %d: %v", budget, offset, err)
			}
			if chunk.Start != offset || chunk.End != offset+len(chunk.Content) || chunk.TotalBytes != len(message) {
				t.Fatalf("budget %d: chunk bounds %d..%d of %d at offset %d", budget, chunk.Start, chunk.End, chunk.TotalBytes, offset)
			}
			if !utf8.ValidString(chunk.Content) || chunk.Content == "" {
				t.Fatalf("budget %d offset %d: chunk is empty or not valid UTF-8", budget, offset)
			}
			if encoded, _ := json.Marshal(chunk.Content); len(encoded) > budget && utf8.RuneCountInString(chunk.Content) > 1 {
				t.Fatalf("budget %d: chunk of %d encoded bytes exceeds the budget", budget, len(encoded))
			}
			got.WriteString(chunk.Content)
			chunks++
			if !chunk.Truncated {
				if chunk.End != len(message) {
					t.Fatalf("budget %d: last chunk ends at %d of %d", budget, chunk.End, len(message))
				}
				break
			}
			offset = chunk.End
		}
		if got.String() != message {
			t.Fatalf("budget %d: %d chunks concatenate to %d bytes, want the %d-byte message exactly", budget, chunks, got.Len(), len(message))
		}
		if budget == 20000 && chunks < 3 {
			t.Fatalf("budget 20000 read the message in %d chunks; the fixture must need several", chunks)
		}
	}

	short := "short é message"
	chunk, err := ChunkMessageContent(short, 0, 20000)
	if err != nil || chunk.Content != short || chunk.Truncated || chunk.Start != 0 || chunk.End != len(short) {
		t.Fatalf("short message changed: %+v %v", chunk, err)
	}

	mid := strings.Index(message, "é") + 1 // inside the two-byte é
	for _, bad := range []int{-1, len(message) + 1, mid} {
		if _, err := ChunkMessageContent(message, bad, 1000); err == nil {
			t.Fatalf("offset %d was accepted", bad)
		}
	}
	if chunk, err := ChunkMessageContent(message, len(message), 1000); err != nil || chunk.Content != "" || chunk.Truncated {
		t.Fatalf("offset at the end: %+v %v", chunk, err)
	}
}

// Purpose: a continuation names one exact message; a partial or invalid one
// is refused instead of silently reading the latest messages. Boundary:
// MessageContinuationFromArgs (both reads parse their arguments with it).
func TestMessageContinuationFromArgsRefusesPartialRequests(t *testing.T) {
	if _, ok, err := MessageContinuationFromArgs(map[string]any{"limit": float64(3)}); ok || err != nil {
		t.Fatalf("a plain read was taken as a continuation: %v %v", ok, err)
	}
	next, ok, err := MessageContinuationFromArgs(map[string]any{"message_id": "m1", "message_seq": float64(7), "content_offset": float64(20000)})
	if err != nil || !ok || next != (MessageContinuation{MessageID: "m1", MessageSeq: 7, ContentOffset: 20000}) {
		t.Fatalf("continuation: %+v %v %v", next, ok, err)
	}
	for _, args := range []map[string]any{
		{"message_id": "m1", "content_offset": float64(10)},
		{"message_seq": float64(7), "content_offset": float64(10)},
		{"content_offset": float64(10)},
		{"message_id": "m1", "message_seq": float64(0)},
		{"message_id": "m1", "message_seq": float64(7), "content_offset": float64(-1)},
		{"message_id": "m1", "message_seq": float64(7), "content_offset": 1.5},
		{"message_id": "m1", "message_seq": "7"},
	} {
		if _, _, err := MessageContinuationFromArgs(args); err == nil {
			t.Fatalf("partial or invalid continuation accepted: %v", args)
		}
	}
}
