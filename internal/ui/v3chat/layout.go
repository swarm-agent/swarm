package v3chat

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ItemLayout represents the pre-rendered layout of a single timeline item.
type ItemLayout struct {
	Key       string      // Stable unique key (e.g. "msg:<id>", "tool:<id>", "live:<streamID>", "reasoning:<id>", "perm:<id>", "draft", "pending:<id>", "worker_review")
	Kind      string      // "message", "tool", "live", "reasoning", "permission", "draft", "pending", "worker_review"
	Signature string      // Change detection token
	Rows      []renderRow // Rendered rows for this item
	RowCount  int         // len(Rows)
	RowOffset int         // Starting line offset in the aggregated transcript
	CopyCount int         // Number of copy blocks in this item
}

// ViewportAnchor represents a pinned logical reading position.
type ViewportAnchor struct {
	ItemKey  string // Key of the ItemLayout at the top of the viewport
	ItemLine int    // Line offset within that item (0-based)
	Valid    bool
}

// TranscriptLayout holds the structured timeline layout with fast slice lookups.
type TranscriptLayout struct {
	Width           int
	AvailableHeight int
	ContentRev      uint64
	Items           []ItemLayout
	TotalRowsCount  int
	KeyToIndex      map[string]int
	DynamicStateKey string
}

func (l *TranscriptLayout) TotalRows() int {
	if l == nil {
		return 0
	}
	return l.TotalRowsCount
}

// Slice returns only the visible rows falling in [start, end), allocating at most end-start rows.
func (l *TranscriptLayout) Slice(start, end int) []renderRow {
	if l == nil || len(l.Items) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end > l.TotalRowsCount {
		end = l.TotalRowsCount
	}
	if start >= end {
		return nil
	}
	out := make([]renderRow, 0, end-start)
	idx := sort.Search(len(l.Items), func(i int) bool {
		return l.Items[i].RowOffset+l.Items[i].RowCount > start
	})
	for i := idx; i < len(l.Items); i++ {
		item := &l.Items[i]
		if item.RowOffset >= end {
			break
		}
		itemStart := maxInt(0, start-item.RowOffset)
		itemEnd := minInt(item.RowCount, end-item.RowOffset)
		if itemStart < itemEnd {
			out = append(out, item.Rows[itemStart:itemEnd]...)
		}
	}
	return out
}

func (l *TranscriptLayout) AllRows() []renderRow {
	if l == nil {
		return nil
	}
	return l.Slice(0, l.TotalRowsCount)
}

// AnchorAt finds the item key and line offset at the given absolute row index.
func (l *TranscriptLayout) AnchorAt(row int) ViewportAnchor {
	if l == nil || row < 0 || row >= l.TotalRowsCount || len(l.Items) == 0 {
		return ViewportAnchor{Valid: false}
	}
	idx := sort.Search(len(l.Items), func(i int) bool {
		return l.Items[i].RowOffset+l.Items[i].RowCount > row
	})
	if idx < len(l.Items) {
		item := &l.Items[idx]
		return ViewportAnchor{
			ItemKey:  item.Key,
			ItemLine: row - item.RowOffset,
			Valid:    true,
		}
	}
	return ViewportAnchor{Valid: false}
}

// ResolveAnchor converts an anchor back to an absolute row index.
func (l *TranscriptLayout) ResolveAnchor(anchor ViewportAnchor) (int, bool) {
	if l == nil || !anchor.Valid || anchor.ItemKey == "" || len(l.Items) == 0 {
		return 0, false
	}
	if idx, ok := l.KeyToIndex[anchor.ItemKey]; ok && idx < len(l.Items) {
		item := &l.Items[idx]
		row := item.RowOffset + anchor.ItemLine
		if row < 0 {
			row = 0
		}
		if row > l.TotalRowsCount {
			row = l.TotalRowsCount
		}
		return row, true
	}
	return 0, false
}

// ResolveAnchorFallback handles live-to-durable transition when a live segment stream
// has finished and become a durable message with the same RunID.
func (l *TranscriptLayout) ResolveAnchorFallback(anchor ViewportAnchor) (int, bool) {
	if l == nil || !anchor.Valid || len(l.Items) == 0 {
		return 0, false
	}
	if strings.HasPrefix(anchor.ItemKey, "live:") {
		parts := strings.Split(anchor.ItemKey, ":")
		runID := ""
		if len(parts) >= 3 {
			runID = parts[2]
		}
		for i := len(l.Items) - 1; i >= 0; i-- {
			item := &l.Items[i]
			if item.Kind == "message" && (runID == "" || strings.Contains(item.Signature, runID)) {
				row := item.RowOffset + anchor.ItemLine
				if row < 0 {
					row = 0
				}
				if row > l.TotalRowsCount {
					row = l.TotalRowsCount
				}
				return row, true
			}
		}
	}
	return 0, false
}

// FindRowIndexForAction returns the row index of an action target (e.g. final handoff action).
func (l *TranscriptLayout) FindRowIndexForAction(action string) (int, bool) {
	if l == nil || action == "" {
		return 0, false
	}
	for _, item := range l.Items {
		for lineIdx, row := range item.Rows {
			for _, target := range row.actions {
				if target.action == action {
					return item.RowOffset + lineIdx, true
				}
			}
		}
	}
	return 0, false
}

// markdownRowCache provides bounded incremental caching for RenderMarkdown lines.
type markdownRowCache struct {
	mu      sync.Mutex
	entries map[string][]renderRow
	order   []string
	maxSize int
}

func newMarkdownRowCache(maxSize int) *markdownRowCache {
	return &markdownRowCache{
		entries: make(map[string][]renderRow, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

func (c *markdownRowCache) Get(key string) ([]renderRow, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, ok := c.entries[key]
	return rows, ok
}

func (c *markdownRowCache) Put(key string, rows []renderRow) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		c.entries[key] = rows
		return
	}
	if len(c.entries) >= c.maxSize {
		evictCount := c.maxSize / 10
		if evictCount < 1 {
			evictCount = 1
		}
		for i := 0; i < evictCount && len(c.order) > 0; i++ {
			oldKey := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldKey)
		}
	}
	c.entries[key] = rows
	c.order = append(c.order, key)
}

func markdownCacheKey(width int, content string) string {
	if len(content) <= 128 {
		return fmt.Sprintf("%d:%s", width, content)
	}
	var h uint64 = 14695981039346656037
	for i := 0; i < len(content); i++ {
		h ^= uint64(content[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%d:%d:%x", width, len(content), h)
}

// itemLayoutCache provides bounded incremental caching of rendered item rows.
type itemLayoutCache struct {
	mu      sync.Mutex
	entries map[string]cachedItemEntry
	order   []string
	maxSize int
}

type cachedItemEntry struct {
	rows      []renderRow
	copyCount int
}

func newItemLayoutCache(maxSize int) *itemLayoutCache {
	return &itemLayoutCache{
		entries: make(map[string]cachedItemEntry, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

func (c *itemLayoutCache) Get(key string) (cachedItemEntry, bool) {
	if c == nil {
		return cachedItemEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok
}

func (c *itemLayoutCache) Put(key string, entry cachedItemEntry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		c.entries[key] = entry
		return
	}
	if len(c.entries) >= c.maxSize {
		evictCount := c.maxSize / 10
		if evictCount < 1 {
			evictCount = 1
		}
		for i := 0; i < evictCount && len(c.order) > 0; i++ {
			oldKey := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldKey)
		}
	}
	c.entries[key] = entry
	c.order = append(c.order, key)
}
