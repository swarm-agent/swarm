package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Reading one long message in chunks over HTTP:
//
//	GET /v3/sessions/{id}/messages?message_id=&message_seq=&content_offset=[&max_chars=]
//
// returns {"message": ...} with the chunk of that exact message from
// content_offset on, cut by tool.ChunkMessageContent (the rule the native
// read_task_session and read_messages use). A chunk that does not reach the
// end says truncated, content_offset (where it ends) and total_bytes; MCP
// swarm_get_session reads on through this route.

// sessionMessageChunkMaxChars bounds one chunk (its content as JSON).
const sessionMessageChunkMaxChars = 20000

// parseMessageContinuationQuery reads message_id, message_seq and
// content_offset from a query. It is nil when none was given; a partial or
// invalid continuation is refused.
func parseMessageContinuationQuery(query url.Values) (*tool.MessageContinuation, error) {
	if !query.Has("message_id") && !query.Has("message_seq") && !query.Has("content_offset") {
		return nil, nil
	}
	next := tool.MessageContinuation{MessageID: strings.TrimSpace(query.Get("message_id"))}
	seq, seqErr := strconv.ParseUint(strings.TrimSpace(query.Get("message_seq")), 10, 64)
	offset, offsetErr := 0, error(nil)
	if raw := strings.TrimSpace(query.Get("content_offset")); raw != "" {
		offset, offsetErr = strconv.Atoi(raw)
	}
	if next.MessageID == "" || seqErr != nil || seq < 1 || offsetErr != nil || offset < 0 {
		return nil, errors.New("continuing a message needs message_id, a positive message_seq and a content_offset that is not negative")
	}
	next.MessageSeq, next.ContentOffset = seq, offset
	return &next, nil
}

// readSessionMessageChunk answers the chunk read of the session messages
// route; the caller has authorized the session read.
func (s *Server) readSessionMessageChunk(w http.ResponseWriter, r *http.Request, sessionID string, next tool.MessageContinuation) {
	maxChars := sessionMessageChunkMaxChars
	if raw := strings.TrimSpace(r.URL.Query().Get("max_chars")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, errors.New("max_chars must be a positive integer"))
			return
		}
		maxChars = min(parsed, sessionMessageChunkMaxChars)
	}
	messages, err := s.sessions.ListSessionMessages(sessionID, next.MessageSeq-1, 1)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(messages) == 0 || messages[0].ID != next.MessageID || messages[0].GlobalSeq != next.MessageSeq {
		writeError(w, http.StatusNotFound, errors.New("message "+next.MessageID+" (message_seq "+strconv.FormatUint(next.MessageSeq, 10)+") was not found in session "+sessionID))
		return
	}
	chunk, err := tool.ChunkMessageContent(messages[0].Content, next.ContentOffset, maxChars)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "max_chars": maxChars, "message": messageChunkItem(messages[0], chunk)})
}

// messageChunkItem is one message, or one chunk of it, as the chunked reads
// return it. A chunk that does not reach the content's end is truncated and
// says where it ends (content_offset) and the full size (total_bytes); a
// chunk after the first says where it starts (content_start).
func messageChunkItem(message pebblestore.MessageSnapshot, chunk tool.MessageContentChunk) map[string]any {
	item := map[string]any{"id": message.ID, "role": message.Role, "content": chunk.Content, "global_seq": message.GlobalSeq, "created_at": message.CreatedAt}
	if chunk.Start > 0 {
		item["content_start"] = chunk.Start
	}
	if chunk.Truncated || chunk.Start > 0 {
		item["content_offset"], item["total_bytes"] = chunk.End, chunk.TotalBytes
	}
	if chunk.Truncated {
		item["truncated"] = true
	}
	return item
}
