package codex

import "testing"

// Purpose: retryAwareStreamEmitter.emit must bound buffered tool construction
// bytes without publishing partial calls. Exercise the exact byte boundary and
// rejection at the emitter layer, avoiding a transport fixture or large payload.
func TestRetryAwareStreamEmitterToolByteLimit(t *testing.T) {
	published := 0
	emitter := &retryAwareStreamEmitter{onEvent: func(StreamEvent) { published++ }}
	emitter.beginAttempt()
	emitter.attemptToolBytes = maxCodexResponseBodyBytes - 4
	event := StreamEvent{
		Type:              StreamEventToolCallArgumentsDelta,
		Arguments:         "a",
		ArgumentsDelta:    "b",
		ArgumentsSnapshot: "c",
		Delta:             "d",
	}
	emitter.emit(event)
	if emitter.attemptToolBytes != maxCodexResponseBodyBytes || emitter.replayErr != nil || len(emitter.attemptTools) != 1 {
		t.Fatalf("exact limit must be accepted: bytes=%d err=%v buffered=%d", emitter.attemptToolBytes, emitter.replayErr, len(emitter.attemptTools))
	}
	emitter.emit(StreamEvent{Type: StreamEventToolCallArgumentsDelta, ArgumentsDelta: "x"})
	if emitter.replayErr == nil || emitter.replayErr.Error() != "codex tool construction event limit exceeded" {
		t.Fatalf("expected byte-limit error, got %v", emitter.replayErr)
	}
	if emitter.attemptToolBytes != maxCodexResponseBodyBytes+1 || len(emitter.attemptTools) != 1 || published != 0 {
		t.Fatalf("over-limit event must not be buffered or published: bytes=%d buffered=%d published=%d", emitter.attemptToolBytes, len(emitter.attemptTools), published)
	}
	emitter.beginAttempt()
	if emitter.attemptToolBytes != 0 || len(emitter.attemptTools) != 0 {
		t.Fatal("new attempt must reset buffered tool accounting")
	}
}
