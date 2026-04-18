package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestClampSingleLineInput_NormalizesAndClamps(t *testing.T) {
	got := clampSingleLineInput("ab\x00\ncd\tef", 7)
	want := "ab cd e"
	if got != want {
		t.Fatalf("clampSingleLineInput() = %q, want %q", got, want)
	}
}

func TestAppendSingleLineInput_NormalizesAndClamps(t *testing.T) {
	got := appendSingleLineInput("ab\n", "c\td\re", 6)
	want := "ab c d"
	if got != want {
		t.Fatalf("appendSingleLineInput() = %q, want %q", got, want)
	}
}

func TestChatHandleKey_PasteModeNormalizesAndClampsInput(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	base := strings.Repeat("x", 128)
	p.SetInput(base)
	p.SetPasteActive(true)

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))
	p.SetPasteActive(false)

	got := p.InputValue()
	if utf8.RuneCountInString(got) != utf8.RuneCountInString(base)+2 {
		t.Fatalf("input rune count = %d, want %d", utf8.RuneCountInString(got), utf8.RuneCountInString(base)+2)
	}
	if !strings.HasSuffix(got, " y") {
		t.Fatalf("input = %q, want trailing normalized paste chars", got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") || strings.Contains(got, "\t") {
		t.Fatalf("input = %q, expected normalized single-line content", got)
	}
}

func TestChatInputLimit_IncreasedBeyondLegacyCeiling(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetInput(strings.Repeat("x", 700))

	got := p.InputValue()
	if utf8.RuneCountInString(got) != 700 {
		t.Fatalf("input rune count = %d, want %d", utf8.RuneCountInString(got), 700)
	}
}

func TestHomeInputLimit_IncreasedBeyondLegacyCeiling(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPrompt(strings.Repeat("x", 300))

	got := p.PromptValue()
	if utf8.RuneCountInString(got) != 300 {
		t.Fatalf("prompt rune count = %d, want %d", utf8.RuneCountInString(got), 300)
	}
}

func TestChatPasteBufferFlushOnPasteEnd(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetPasteActive(true)
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModNone))

	if got := p.InputPasteBuffered(); got != 2 {
		t.Fatalf("InputPasteBuffered() = %d, want 2", got)
	}
	if got := p.InputValue(); got != "" {
		t.Fatalf("input before flush = %q, want empty", got)
	}

	p.SetPasteActive(false)

	if got := p.InputPasteBuffered(); got != 0 {
		t.Fatalf("InputPasteBuffered() after flush = %d, want 0", got)
	}
	if got := p.LastPasteBatchSize(); got != 2 {
		t.Fatalf("LastPasteBatchSize() = %d, want 2", got)
	}
	if got := p.InputValue(); got != "ab" {
		t.Fatalf("input after flush = %q, want %q", got, "ab")
	}
}

func TestChatPasteBufferFlushesInChunksDuringPaste(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetPasteActive(true)
	for i := 0; i < singleLinePasteFlushChunkRunes; i++ {
		if changed := p.HandlePasteKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)); i == singleLinePasteFlushChunkRunes-1 {
			if !changed {
				t.Fatalf("HandlePasteKey() final chunk rune changed = false, want true")
			}
		}
	}

	if got := utf8.RuneCountInString(p.InputValue()); got != singleLinePasteFlushChunkRunes {
		t.Fatalf("input rune count during active paste = %d, want %d", got, singleLinePasteFlushChunkRunes)
	}
	if got := p.InputPasteBuffered(); got != 0 {
		t.Fatalf("InputPasteBuffered() after chunk flush = %d, want 0", got)
	}

	p.SetPasteActive(false)
}

func TestChatPasteBufferBackspaceRemovesBufferedRune(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	p.SetPasteActive(true)
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone))
	p.SetPasteActive(false)

	if got := p.InputValue(); got != "a" {
		t.Fatalf("input after buffered backspace = %q, want %q", got, "a")
	}
	if got := p.LastPasteBatchSize(); got != 1 {
		t.Fatalf("LastPasteBatchSize() = %d, want 1", got)
	}
}

func TestHomePasteBufferFlushOnPasteEnd(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPasteActive(true)
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))

	if got := p.PromptPasteBuffered(); got != 2 {
		t.Fatalf("PromptPasteBuffered() = %d, want 2", got)
	}
	if got := p.PromptValue(); got != "" {
		t.Fatalf("prompt before flush = %q, want empty", got)
	}

	p.SetPasteActive(false)

	if got := p.PromptPasteBuffered(); got != 0 {
		t.Fatalf("PromptPasteBuffered() after flush = %d, want 0", got)
	}
	if got := p.LastPasteBatchSize(); got != 2 {
		t.Fatalf("LastPasteBatchSize() = %d, want 2", got)
	}
	if got := p.PromptValue(); got != "xy" {
		t.Fatalf("prompt after flush = %q, want %q", got, "xy")
	}
}

func TestHomePasteBufferFlushesInChunksDuringPaste(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPasteActive(true)
	for i := 0; i < singleLinePasteFlushChunkRunes; i++ {
		if changed := p.HandlePasteKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)); i == singleLinePasteFlushChunkRunes-1 {
			if !changed {
				t.Fatalf("HandlePasteKey() final chunk rune changed = false, want true")
			}
		}
	}

	if got := utf8.RuneCountInString(p.PromptValue()); got != singleLinePasteFlushChunkRunes {
		t.Fatalf("prompt rune count during active paste = %d, want %d", got, singleLinePasteFlushChunkRunes)
	}
	if got := p.PromptPasteBuffered(); got != 0 {
		t.Fatalf("PromptPasteBuffered() after chunk flush = %d, want 0", got)
	}

	p.SetPasteActive(false)
}

func TestHomePasteBufferBackspaceRemovesBufferedRune(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetPasteActive(true)
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))
	p.HandleKey(tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModNone))
	p.SetPasteActive(false)

	if got := p.PromptValue(); got != "x" {
		t.Fatalf("prompt after buffered backspace = %q, want %q", got, "x")
	}
	if got := p.LastPasteBatchSize(); got != 1 {
		t.Fatalf("LastPasteBatchSize() = %d, want 1", got)
	}
}

func BenchmarkAppendSingleLineInput_RuneByRune(b *testing.B) {
	chunk := strings.Repeat("x", 4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		out := ""
		for _, r := range chunk {
			out = appendSingleLineInput(out, string(r), chatMaxInputRunes)
		}
		if out == "" {
			b.Fatalf("unexpected empty output")
		}
	}
}

func BenchmarkAppendSingleLineInput_BulkChunk(b *testing.B) {
	chunk := strings.Repeat("x", 4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		out := appendSingleLineInput("", chunk, chatMaxInputRunes)
		if out == "" {
			b.Fatalf("unexpected empty output")
		}
	}
}

func BenchmarkChatPasteBufferFlush(b *testing.B) {
	chunk := strings.Repeat("x", 4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		p := NewChatPage(ChatPageOptions{SessionID: "session-1"})
		p.SetPasteActive(true)
		for _, r := range chunk {
			p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		}
		p.SetPasteActive(false)
		if p.InputValue() == "" {
			b.Fatalf("unexpected empty flush")
		}
	}
}
