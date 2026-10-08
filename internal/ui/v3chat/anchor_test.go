package v3chat

import (
	"fmt"
	"testing"
)

func TestLayoutReuseAndAnchoring(t *testing.T) {
	markdownCalls := 0
	styles := PageStyles{
		RenderMarkdown: func(content string, width int) []MarkdownLine {
			markdownCalls++
			return []MarkdownLine{{Text: content}}
		},
	}
	page := NewPage(nil, styles)

	state := NewState()
	// Add 5 messages
	for i := 1; i <= 5; i++ {
		state.Messages = append(state.Messages, Message{
			ID:        fmt.Sprintf("msg-%d", i),
			Role:      "assistant",
			Content:   fmt.Sprintf("This is message %d content", i),
			GlobalSeq: uint64(i),
		})
	}

	// 1. Initial layout
	layout1 := page.getOrCreateLayout(state, 80, 20, styles)
	if layout1 == nil {
		t.Fatal("expected layout")
	}
	total1 := layout1.TotalRows()
	if total1 == 0 {
		t.Fatal("expected rows > 0")
	}
	initialCalls := markdownCalls

	// 2. Second call with unchanged state/width reuses layout without markdown rendering
	layout2 := page.getOrCreateLayout(state, 80, 20, styles)
	if layout2 != layout1 {
		t.Fatal("expected identical layout pointer on unchanged state")
	}
	if markdownCalls != initialCalls {
		t.Fatalf("expected 0 additional markdown calls, got %d", markdownCalls-initialCalls)
	}

	// 3. Anchoring: simulate user scrolled back to message 2
	anchor := layout1.AnchorAt(2) // line 2 of transcript
	if !anchor.Valid {
		t.Fatal("expected valid anchor")
	}

	// Append 10 more messages at the bottom
	for i := 6; i <= 15; i++ {
		state.Messages = append(state.Messages, Message{
			ID:        fmt.Sprintf("msg-%d", i),
			Role:      "assistant",
			Content:   fmt.Sprintf("Appended message %d", i),
			GlobalSeq: uint64(i),
		})
	}
	// Bump content revision
	if page.runtime != nil && page.runtime.Store() != nil {
		page.runtime.Store().revision++
		page.runtime.Store().contentRevision++
	}

	// Invalidate layout cache to trigger reflow
	page.cachedLayout = nil
	layout3 := page.getOrCreateLayout(state, 80, 20, styles)
	total3 := layout3.TotalRows()
	if total3 <= total1 {
		t.Fatalf("expected total3 > total1, got %d <= %d", total3, total1)
	}

	// Resolve the previous anchor in the new layout
	resolvedRow, ok := layout3.ResolveAnchor(anchor)
	if !ok {
		t.Fatal("failed to resolve anchor in updated layout")
	}
	if resolvedRow != 2 {
		t.Fatalf("resolved anchor row = %d, want 2 (reading position moved!)", resolvedRow)
	}
}

func TestViewportAnchorStreamingStability(t *testing.T) {
	styles := PageStyles{
		RenderMarkdown: func(content string, width int) []MarkdownLine {
			return []MarkdownLine{{Text: content}}
		},
	}
	page := NewPage(nil, styles)

	state := NewState()
	state.Messages = append(state.Messages, Message{
		ID:        "msg-history-1",
		Role:      "user",
		Content:   "Historical prompt",
		GlobalSeq: 1,
	}, Message{
		ID:        "msg-history-2",
		Role:      "assistant",
		Content:   "Historical response line 1\nHistorical response line 2\nHistorical response line 3",
		GlobalSeq: 2,
	})

	layout1 := page.getOrCreateLayout(state, 80, 10, styles)
	// Anchor at row 1 (inside history)
	anchor := layout1.AnchorAt(1)
	if !anchor.Valid {
		t.Fatal("expected valid anchor")
	}

	// Now assistant streams a live segment at the bottom
	state.Live = map[string]LiveSegment{
		"run:out": {
			RunID:     "run",
			StreamID:  "out",
			Text:      "Streaming chunk 1...",
			GlobalSeq: 3,
		},
	}
	page.cachedLayout = nil
	layout2 := page.getOrCreateLayout(state, 80, 10, styles)

	// Anchored row must stay at exactly 1
	rowAfterChunk1, ok := layout2.ResolveAnchor(anchor)
	if !ok || rowAfterChunk1 != 1 {
		t.Fatalf("row moved during streaming: %d, want 1", rowAfterChunk1)
	}

	// Stream more chunks
	state.Live["run:out"] = LiveSegment{
		RunID:     "run",
		StreamID:  "out",
		Text:      "Streaming chunk 1... Streaming chunk 2... More lines\nEven more lines\nEnd of chunk",
		GlobalSeq: 4,
	}
	page.cachedLayout = nil
	layout3 := page.getOrCreateLayout(state, 80, 10, styles)

	rowAfterChunk2, ok := layout3.ResolveAnchor(anchor)
	if !ok || rowAfterChunk2 != 1 {
		t.Fatalf("row moved during streaming chunk 2: %d, want 1", rowAfterChunk2)
	}

	// Live segment completes and becomes durable message
	delete(state.Live, "run:out")
	state.Messages = append(state.Messages, Message{
		ID:        "msg-3",
		Role:      "assistant",
		Content:   "Streaming chunk 1... Streaming chunk 2... More lines\nEven more lines\nEnd of chunk",
		RunID:     "run",
		GlobalSeq: 5,
	})
	page.cachedLayout = nil
	layout4 := page.getOrCreateLayout(state, 80, 10, styles)

	rowAfterDurable, ok := layout4.ResolveAnchor(anchor)
	if !ok || rowAfterDurable != 1 {
		t.Fatalf("row moved after durable reconciliation: %d, want 1", rowAfterDurable)
	}
}

func TestMarkdownRenderCacheIncrementalEviction(t *testing.T) {
	cache := newMarkdownRowCache(10)
	for i := 0; i < 15; i++ {
		key := fmt.Sprintf("key-%d", i)
		cache.Put(key, []renderRow{{text: fmt.Sprintf("row-%d", i)}})
	}

	// Cache must not exceed maxSize
	cache.mu.Lock()
	count := len(cache.entries)
	cache.mu.Unlock()
	if count > 10 {
		t.Fatalf("cache count %d > maxSize 10", count)
	}

	// The newest items must be present
	if _, ok := cache.Get("key-14"); !ok {
		t.Fatal("expected key-14 in cache")
	}
	if _, ok := cache.Get("key-13"); !ok {
		t.Fatal("expected key-13 in cache")
	}

	// The oldest item (key-0) must have been evicted incrementally
	if _, ok := cache.Get("key-0"); ok {
		t.Fatal("expected key-0 to be evicted")
	}
}
