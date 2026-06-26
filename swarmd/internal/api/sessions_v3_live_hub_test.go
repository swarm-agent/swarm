package api

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestV3RealtimeLiveHubCoalescesInterleavedStreamsByKey(t *testing.T) {
	hub := newV3LiveHub()
	sub := hub.subscribe()
	hub.replaceSessions(sub, "acct-1", []string{"session-1"})

	for seq := uint64(1); seq <= 2; seq++ {
		for stream := 0; stream < 100; stream++ {
			patch := v3RealtimeLivePatchForTest("session-1", "run-1", fmt.Sprintf("stream-%03d", stream), int(seq), "x")
			patch.LiveSeqStart = seq
			patch.LiveSeqEnd = seq
			patch.OffsetStart = seq - 1
			patch.OffsetEnd = seq
			hub.publish("acct-1", patch)
		}
	}

	sub.mu.Lock()
	if got := len(sub.pendingByKey); got != 100 {
		sub.mu.Unlock()
		t.Fatalf("pendingByKey len = %d, want 100", got)
	}
	if got := len(sub.readyKeys); got != 100 {
		sub.mu.Unlock()
		t.Fatalf("readyKeys len = %d, want 100", got)
	}
	if got := sub.pendingBytes; got != 200 {
		sub.mu.Unlock()
		t.Fatalf("pendingBytes = %d, want 200", got)
	}
	sub.mu.Unlock()

	patches := sub.drain(100, 1024)
	if got := len(patches); got != 100 {
		t.Fatalf("drained patches = %d, want 100", got)
	}
	for _, patch := range patches {
		if patch.Text != "xx" {
			t.Fatalf("patch %s text = %q, want xx", patch.StreamID, patch.Text)
		}
		if patch.LiveSeqStart != 1 || patch.LiveSeqEnd != 2 {
			t.Fatalf("patch %s seq = %d..%d, want 1..2", patch.StreamID, patch.LiveSeqStart, patch.LiveSeqEnd)
		}
	}
}

func TestV3RealtimeLiveHubOverflowIsSubscriberLocal(t *testing.T) {
	hub := newV3LiveHub()
	fast := hub.subscribe()
	slow := hub.subscribe()
	hub.replaceSessions(fast, "acct-1", []string{"session-1"})
	hub.replaceSessions(slow, "acct-1", []string{"session-1"})

	for i := 0; i < 300; i++ {
		patch := v3RealtimeLivePatchForTest("session-1", "run-1", "slow-stream", i+1, strings.Repeat("x", 1024))
		patch.LiveSeqStart = uint64(i + 1)
		patch.LiveSeqEnd = uint64(i + 1)
		patch.OffsetStart = uint64(i * 1024)
		patch.OffsetEnd = uint64((i + 1) * 1024)
		hub.publish("acct-1", patch)
		_ = fast.drain(16, 64<<10)
	}

	select {
	case <-slow.slow:
	case <-time.After(time.Second):
		t.Fatalf("slow subscriber did not receive slow signal")
	}
	select {
	case <-slow.slow:
		t.Fatalf("slow subscriber received more than one slow signal")
	default:
	}

	hub.mu.RLock()
	_, slowIndexed := hub.bySession[v3LiveSessionKey{AccountScopeID: "acct-1", SessionID: "session-1"}][slow.id]
	_, fastIndexed := hub.bySession[v3LiveSessionKey{AccountScopeID: "acct-1", SessionID: "session-1"}][fast.id]
	hub.mu.RUnlock()
	if slowIndexed {
		t.Fatalf("slow subscriber remained in session index")
	}
	if !fastIndexed {
		t.Fatalf("fast subscriber was removed from session index")
	}

	patch := v3RealtimeLivePatchForTest("session-1", "run-2", "fast-stream", 1, "z")
	hub.publish("acct-1", patch)
	fastFrames := fast.drain(16, 64<<10)
	if len(fastFrames) == 0 {
		t.Fatalf("fast subscriber did not continue receiving patches")
	}
}

func TestV3RealtimeLiveHubSourceGuard(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_live_hub.go")
	if err != nil {
		t.Fatalf("read live hub source: %v", err)
	}
	source := string(body)
	for _, forbidden := range []string{".Text +=", "pending.Patch.Text = pending.Patch.Text +", "chunks []string", "[]string{"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("live hub source contains forbidden accumulation pattern %q", forbidden)
		}
	}
	for _, required := range []string{"bytes.Buffer", "WriteString"} {
		if !strings.Contains(source, required) {
			t.Fatalf("live hub source missing required pattern %q", required)
		}
	}
}

func v3RealtimeLivePatchForTest(sessionID, runID, streamID string, step int, text string) V3RealtimeLivePatch {
	return V3RealtimeLivePatch{
		SessionID:    sessionID,
		RunID:        runID,
		StreamID:     streamID,
		StreamKind:   "assistant_text",
		Operation:    "append",
		Step:         step,
		StepID:       "step-1",
		LiveSeqStart: uint64(step),
		LiveSeqEnd:   uint64(step),
		OffsetStart:  0,
		OffsetEnd:    uint64(len([]byte(text))),
		Text:         text,
		RecordedAt:   time.Now().UnixMilli(),
	}
}
