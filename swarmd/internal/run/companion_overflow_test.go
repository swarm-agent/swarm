package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// companionMessageReads counts the session's message records read by store
// range scans (V3 and legacy message prefixes) while installed.
type companionMessageReads struct {
	mu       sync.Mutex
	prefixes []string
	records  int
	bytes    int
}

func observeCompanionMessageReads(t *testing.T, sessionID string) *companionMessageReads {
	t.Helper()
	reads := &companionMessageReads{prefixes: []string{pebblestore.V3SessionMessagePrefix(sessionID), pebblestore.MessagePrefix(sessionID)}}
	restore := pebblestore.ObserveScanReadsForTest(func(key string, valueBytes int) {
		for _, prefix := range reads.prefixes {
			if strings.HasPrefix(key, prefix) {
				reads.mu.Lock()
				reads.records++
				reads.bytes += valueBytes
				reads.mu.Unlock()
				return
			}
		}
	})
	t.Cleanup(restore)
	return reads
}

func (r *companionMessageReads) take() (records, bytes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records, bytes = r.records, r.bytes
	r.records, r.bytes = 0, 0
	return records, bytes
}

func companionToolRecord(callID, output string) string {
	raw, _ := json.Marshal(map[string]any{"path_id": v3ProviderManagedToolResultPathID, "type": "tool_result", "tool_name": "bash", "call_id": callID, "arguments": `{"command":"ls"}`, "output": output})
	return string(raw)
}

// Purpose: Companion overflow recovery assembles every retry from bounded
// reads with correct tool pairing. Requirement: companionContextBudgets is a
// fixed, strictly shrinking ladder whose recovery levels hold back
// companionTurnReserveBytes; CompanionV3ContextMessagesAtLevel reads at most
// TailMessages+1 tail records (about TailBytes) plus the context bank's fixed
// number of source excerpts from the store at every level however long the
// history is, sends a tail within TailBytes and a
// block within ContextBlockBytes, never starts the tail with a bare tool
// result whose call was cut, keeps self-contained tool records (call and
// result together) intact, and refuses a level outside the ladder.
// Threat: overflow recovery that scans the whole conversation, a retry that
// does not shrink, or a provider request carrying a tool result without its
// call. Boundary: companionContextBudgets, CompanionV3ContextMessagesAtLevel,
// companionPairedTail over the real Pebble message store, measured with the
// store's scan read observer. Narrowest layer: the assembly function the V3
// executor calls for each attempt, over a real store (the API test covers
// the executor retry loop).
func TestCompanionOverflowBudgetsStayBoundedAtEveryLevel(t *testing.T) {
	for level := 1; level < CompanionContextBudgetLevels; level++ {
		prev, cur := companionContextBudgets[level-1], companionContextBudgets[level]
		if cur.TailMessages >= prev.TailMessages || cur.TailBytes >= prev.TailBytes || cur.ContextBlockBytes >= prev.ContextBlockBytes || cur.TailBytes <= 0 {
			t.Fatalf("level %d budget %+v does not shrink from %+v", level, cur, prev)
		}
		if ceiling := cur.TailBytes + cur.ContextBlockBytes + CompanionTurnReserveBytes; ceiling > prev.TailBytes+prev.ContextBlockBytes {
			t.Fatalf("level %d history ceiling %d (with the %d-byte turn reserve) is not below level %d", level, ceiling, CompanionTurnReserveBytes, level-1)
		}
	}
	if CompanionContextBudgetLevels < 2 {
		t.Fatal("no overflow retry level is defined")
	}

	f := newCompanionTurnFixture(t)
	filler := strings.Repeat("spooler log line ", 14)
	// Thousands of messages, including self-contained tool records.
	for i := 0; i < 700; i++ {
		f.say("user", fmt.Sprintf("Printer question %d: %s", i, filler))
		f.say("assistant", fmt.Sprintf("Answer %d: %s", i, filler))
		f.say("tool", companionToolRecord(fmt.Sprintf("old-%d", i), filler))
	}
	// The newest 40 messages are tool records, with a bare result (no call)
	// exactly at the first message of each level's tail.
	cutStarts := map[int]bool{}
	for _, budget := range companionContextBudgets {
		cutStarts[budget.TailMessages] = true
	}
	var seqs []uint64
	for i := 40; i >= 1; i-- {
		content := companionToolRecord(fmt.Sprintf("call-%d", i), filler)
		if cutStarts[i] {
			content = "bare tool output " + filler
		}
		seqs = append(seqs, f.say("tool", content).GlobalSeq)
	}
	lastSeq := seqs[len(seqs)-1]
	session, _, _ := f.db.GetSession(companionFixtureID)
	if session.MessageCount < 2000 {
		t.Fatalf("fixture has %d messages, want thousands", session.MessageCount)
	}

	reads := observeCompanionMessageReads(t, companionFixtureID)
	for level := 0; level < CompanionContextBudgetLevels; level++ {
		budget := companionContextBudgets[level]
		reads.take()
		messages, err := CompanionV3ContextMessagesAtLevel(f.db, f.session, nil, 0, 100, level)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		records, bytes := reads.take()
		// The tail scan visits at most TailMessages+1 records; the context
		// bank reads at most one source message per retrieved record.
		maxRecords := budget.TailMessages + 1 + companionRetrievedMax + companionDecisionMax + companionWorkMax
		if records == 0 || records > maxRecords || bytes > budget.TailBytes+maxRecords*1024 {
			t.Fatalf("level %d read %d message records (%d bytes) of %d; budget %d messages, %d bytes", level, records, bytes, session.MessageCount, budget.TailMessages, budget.TailBytes)
		}
		if len(messages) < 2 || messages[0].Role != "system" || !strings.HasPrefix(messages[0].Content, companionContextBlockHeader) || len(messages[0].Content) > budget.ContextBlockBytes {
			t.Fatalf("level %d block: %d messages, block %d bytes (budget %d)", level, len(messages), len(messages[0].Content), budget.ContextBlockBytes)
		}
		tail := messages[1:]
		tailBytes := 0
		for _, message := range tail {
			tailBytes += len(message.Content)
		}
		if tailBytes > budget.TailBytes || len(tail) > budget.TailMessages {
			t.Fatalf("level %d tail %d messages / %d bytes, budget %d / %d", level, len(tail), tailBytes, budget.TailMessages, budget.TailBytes)
		}
		// The bare result at the cut was dropped; every remaining tool
		// message carries its own call.
		cut := seqs[len(seqs)-budget.TailMessages]
		if tail[0].GlobalSeq <= cut || len(tail) != budget.TailMessages-1 || tail[len(tail)-1].GlobalSeq != lastSeq {
			t.Fatalf("level %d tail starts at #%d (bare result at #%d), %d messages, ends #%d (want #%d)", level, tail[0].GlobalSeq, cut, len(tail), tail[len(tail)-1].GlobalSeq, lastSeq)
		}
		// (A bare result inside the tail is another level's cut marker; the
		// V3 provider input never sends an undecodable tool message.)
		if tail[0].Role == "tool" && !companionSelfPairedToolMessage(tail[0].Content) {
			t.Fatalf("level %d tail starts with a tool result without its call: #%d", level, tail[0].GlobalSeq)
		}
		if !companionSelfPairedToolMessage(tail[len(tail)-1].Content) {
			t.Fatalf("level %d cut the newest self-contained tool record", level)
		}
	}
	if _, err := CompanionV3ContextMessagesAtLevel(f.db, f.session, nil, 0, 100, CompanionContextBudgetLevels); err == nil {
		t.Fatal("a level outside the fixed ladder was assembled")
	}
	if records, _ := reads.take(); records != 0 {
		t.Fatalf("a refused level read %d messages", records)
	}
}

// Purpose: a Companion is never compacted. Requirement: RunTurn with Compact
// (manual or overflow origin) on a Companion session returns
// ErrCompanionCompactionRefused before reading any message or claiming the
// lifecycle, and listMessagesForMemoryCompaction (the full-history read every
// compaction path uses) refuses a Companion without reading messages, while
// an ordinary session still gets its full compaction history. Threat: an
// overflow or manual compact on a Companion scanning its unbounded
// transcript (session.MessageCount + slack). Boundary: Service.RunTurn's
// compaction entry, listMessagesForMemoryCompaction, measured with the
// store's scan read observer. Narrowest layer: the run service over a real
// store; no provider is reached because the refusal precedes it.
func TestCompanionCompactionIsRefusedWithoutHistoryReads(t *testing.T) {
	f := newCompanionTurnFixture(t)
	for i := 0; i < 300; i++ {
		f.say("user", fmt.Sprintf("message %d about the printer spooler", i))
		f.say("assistant", fmt.Sprintf("reply %d", i))
	}
	f.apply("plain", pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationCreateSession, Session: &pebblestore.SessionSnapshot{ID: "plain", Mode: "auto"}})
	for i := 0; i < 5; i++ {
		f.apply("plain", pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationAppendMessage, Message: &pebblestore.MessageSnapshot{Role: "user", Content: fmt.Sprintf("plain %d", i)}})
	}
	svc := NewService(sessionruntime.NewService(f.db, nil), nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)

	reads := observeCompanionMessageReads(t, companionFixtureID)
	for _, origin := range []string{"overflow", "manual", ""} {
		_, err := svc.RunTurn(context.Background(), companionFixtureID, RunRequest{Prompt: "compact", Compact: true, CompactOrigin: origin}, RunStartMeta{RunID: "compact-" + origin})
		if !errors.Is(err, ErrCompanionCompactionRefused) {
			t.Fatalf("compact (origin %q) on a Companion = %v, want ErrCompanionCompactionRefused", origin, err)
		}
	}
	if _, err := svc.listMessagesForMemoryCompaction(companionFixtureID, true); !errors.Is(err, ErrCompanionCompactionRefused) {
		t.Fatalf("full-history compaction read of a Companion = %v, want refused", err)
	}
	if records, _ := reads.take(); records != 0 {
		t.Fatalf("refused Companion compaction read %d message records", records)
	}
	if session, _, _ := f.db.GetSession(companionFixtureID); session.Lifecycle != nil && session.Lifecycle.Active {
		t.Fatalf("refused compaction left the lifecycle active: %+v", session.Lifecycle)
	}

	// Contrast: an ordinary session keeps its full compaction history.
	plain, err := svc.listMessagesForMemoryCompaction("plain", true)
	if err != nil || len(plain) != 5 {
		t.Fatalf("ordinary compaction history = %d messages, %v; want all 5", len(plain), err)
	}
}
