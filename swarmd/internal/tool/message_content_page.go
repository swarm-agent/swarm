package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Paging within one message. A session read bounds the content it returns;
// a message longer than the bound is returned in chunks instead of being cut
// off. Each chunk says where it ends (content_offset, a UTF-8 byte offset into
// the message content) and the message's full size (total_bytes); the next
// chunk is read by naming the exact message (message_id with its message_seq)
// and that offset. Chunks are always cut on a rune boundary, so the chunks of
// a message concatenate to its content exactly. manage_projects
// read_task_session and manage-sessions read_messages share this one rule.

// MessageContentChunk is one chunk of a message's content.
type MessageContentChunk struct {
	Content string
	// Start is the byte offset the chunk starts at; End is where it ends
	// (the next chunk's content_offset).
	Start, End int
	TotalBytes int
	// Truncated reports that content remains after End.
	Truncated bool
}

// ChunkMessageContent returns content from offset on, cut on a rune boundary
// so the chunk's JSON encoding fits budget bytes. A chunk always makes
// progress: when even one rune does not fit, that one rune is returned. An
// offset outside the content or inside a rune is refused, so a stale or
// invented offset never yields a misaligned chunk.
func ChunkMessageContent(content string, offset, budget int) (MessageContentChunk, error) {
	if offset < 0 || offset > len(content) {
		return MessageContentChunk{}, fmt.Errorf("content_offset %d is outside the message (0 to %d bytes)", offset, len(content))
	}
	if offset < len(content) && !utf8.RuneStart(content[offset]) {
		return MessageContentChunk{}, fmt.Errorf("content_offset %d is not on a character boundary; pass the content_offset a read returned", offset)
	}
	rest := content[offset:]
	chunk := fitMessageContent(rest, budget)
	if chunk == "" && rest != "" {
		_, size := utf8.DecodeRuneInString(rest)
		chunk = rest[:size]
	}
	end := offset + len(chunk)
	return MessageContentChunk{Content: chunk, Start: offset, End: end, TotalBytes: len(content), Truncated: end < len(content)}, nil
}

// fitMessageContent cuts text, on a rune boundary, until its JSON encoding
// fits budget bytes.
func fitMessageContent(text string, budget int) string {
	// JSON never encodes text shorter than its bytes plus the two quotes, so
	// only text that could fit is encoded whole; the cut loop below encodes
	// at most budget bytes, keeping a long message's chunks linear to read.
	if len(text)+2 <= budget && encodedMessageContentLen(text) <= budget {
		return text
	}
	cut := min(len(text), max(budget, 0))
	for cut > 0 {
		for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut--
		}
		over := encodedMessageContentLen(text[:cut]) - budget
		if over <= 0 {
			break
		}
		cut -= over
		if cut < 0 {
			cut = 0
		}
	}
	return text[:cut]
}

func encodedMessageContentLen(text string) int {
	raw, _ := json.Marshal(text)
	return len(raw)
}

// EncodedLen is the size of a chunk's content as JSON, the measure its
// budget is spent in.
func (c MessageContentChunk) EncodedLen() int { return encodedMessageContentLen(c.Content) }

// MessageContinuation is the continuation request of a message read: the
// exact message (its id and sequence) and the byte offset to continue from.
type MessageContinuation struct {
	MessageID     string
	MessageSeq    uint64
	ContentOffset int
}

// MessageContinuationFromArgs reads message_id, message_seq and
// content_offset from tool arguments. ok is false when no continuation was
// asked for; a partial or invalid one is refused.
func MessageContinuationFromArgs(args map[string]any) (MessageContinuation, bool, error) {
	id, _ := args["message_id"].(string)
	_, hasSeq := args["message_seq"]
	_, hasOffset := args["content_offset"]
	if id == "" && !hasSeq && !hasOffset {
		return MessageContinuation{}, false, nil
	}
	if id == "" || !hasSeq {
		return MessageContinuation{}, false, errors.New("continuing a message needs message_id, message_seq and content_offset from the read's next")
	}
	seq, err := nonNegativeArg(args["message_seq"], "message_seq")
	if err != nil {
		return MessageContinuation{}, false, err
	}
	if seq < 1 {
		return MessageContinuation{}, false, errors.New("message_seq must be positive")
	}
	offset := int64(0)
	if hasOffset {
		if offset, err = nonNegativeArg(args["content_offset"], "content_offset"); err != nil {
			return MessageContinuation{}, false, err
		}
	}
	return MessageContinuation{MessageID: id, MessageSeq: uint64(seq), ContentOffset: int(offset)}, true, nil
}

func nonNegativeArg(value any, name string) (int64, error) {
	var n int64
	switch v := value.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("%s must be a whole number", name)
		}
		n = int64(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be a whole number", name)
		}
		n = parsed
	default:
		return 0, fmt.Errorf("%s must be a whole number", name)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	return n, nil
}
