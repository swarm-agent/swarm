package ui

import (
	"strings"
	"time"
)

type chatTimelineRenderBlock struct {
	Lines  []chatRenderLine
	Height int
}

type chatTimelineCacheEntry struct {
	Valid        bool
	Width        int
	Generation   uint64
	Role         string
	Text         string
	CreatedAt    int64
	ToolState    string
	MetadataHash string
	Lines        []chatRenderLine
}

type chatLiveAssistantCacheEntry struct {
	Valid                 bool
	Width                 int
	Generation            uint64
	Variant               int
	ParsedText            string
	Lines                 []chatRenderLine
	LastParseAt           time.Time
	LastAttemptWidth      int
	LastAttemptGeneration uint64
	LastAttemptVariant    int
}

var chatLiveAssistantParseMinInterval = 33 * time.Millisecond

func (p *ChatPage) bumpTimelineRenderGeneration() {
	p.timelineRenderGeneration++
	if p.timelineRenderGeneration == 0 {
		p.timelineRenderGeneration = 1
	}
}

func (p *ChatPage) resetTimelineRenderCache() {
	p.liveAssistantRenderCache = chatLiveAssistantCacheEntry{}
	if len(p.timeline) == 0 {
		p.timelineRenderCache = nil
		return
	}
	p.timelineRenderCache = make([]chatTimelineCacheEntry, len(p.timeline))
}

func (p *ChatPage) ensureTimelineRenderCacheLen() {
	if len(p.timelineRenderCache) == len(p.timeline) {
		return
	}
	if len(p.timeline) == 0 {
		p.timelineRenderCache = nil
		return
	}
	if len(p.timelineRenderCache) < len(p.timeline) {
		p.timelineRenderCache = append(p.timelineRenderCache, make([]chatTimelineCacheEntry, len(p.timeline)-len(p.timelineRenderCache))...)
		return
	}
	p.timelineRenderCache = p.timelineRenderCache[:len(p.timeline)]
}

func (p *ChatPage) cachedTimelineMessageLines(index, width int) []chatRenderLine {
	if p == nil || width <= 0 || index < 0 || index >= len(p.timeline) {
		return nil
	}
	p.ensureTimelineRenderCacheLen()

	message := p.timeline[index]
	if p.shouldBypassTimelineMessageCache(message) {
		lines := p.renderTimelineMessageLines(message, width)
		if len(lines) > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		return lines
	}
	entry := &p.timelineRenderCache[index]
	if entry.Valid &&
		entry.Width == width &&
		entry.Generation == p.timelineRenderGeneration &&
		entry.Role == message.Role &&
		entry.Text == message.Text &&
		entry.CreatedAt == message.CreatedAt &&
		entry.ToolState == message.ToolState &&
		entry.MetadataHash == timelineMetadataHash(message.Metadata) {
		return entry.Lines
	}

	lines := p.renderTimelineMessageLines(message, width)
	if len(lines) > 0 {
		lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
	}
	*entry = chatTimelineCacheEntry{
		Valid:        true,
		Width:        width,
		Generation:   p.timelineRenderGeneration,
		Role:         message.Role,
		Text:         message.Text,
		CreatedAt:    message.CreatedAt,
		ToolState:    message.ToolState,
		MetadataHash: timelineMetadataHash(message.Metadata),
		Lines:        lines,
	}
	return entry.Lines
}

func (p *ChatPage) renderTimelineMessageLines(message chatMessageItem, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "user":
		return p.renderUserMessageLines(message, width)
	case "assistant":
		return p.renderAssistantMessageLines(message, width)
	case "reasoning":
		return p.renderReasoningMessageLines(message, width)
	case "tool":
		return p.renderToolMessageLines(message, width)
	case "system":
		lines := make([]chatRenderLine, 0, 4)
		for _, line := range wrapWithPrefix("· ", message.Text, width) {
			lines = append(lines, chatRenderLine{Text: line, Style: p.theme.Warning})
		}
		return lines
	default:
		lines := make([]chatRenderLine, 0, 4)
		for _, line := range wrapWithPrefix("· ", message.Text, width) {
			lines = append(lines, chatRenderLine{Text: line, Style: p.theme.TextMuted})
		}
		return lines
	}
	return nil
}

func (p *ChatPage) cachedLiveAssistantLines(width int) []chatRenderLine {
	if p == nil || width <= 0 {
		return nil
	}
	text := strings.TrimSpace(p.liveAssistant)
	if text == "" {
		p.liveAssistantRenderCache = chatLiveAssistantCacheEntry{}
		return nil
	}
	entry := &p.liveAssistantRenderCache
	if entry.Valid &&
		entry.Width == width &&
		entry.Generation == p.timelineRenderGeneration &&
		entry.Variant == p.assistantVariant &&
		entry.ParsedText == text {
		return cloneRenderLines(entry.Lines)
	}

	now := time.Now()
	attemptLayoutChanged := entry.LastAttemptWidth != width ||
		entry.LastAttemptGeneration != p.timelineRenderGeneration ||
		entry.LastAttemptVariant != p.assistantVariant
	if !attemptLayoutChanged && !entry.LastParseAt.IsZero() && now.Sub(entry.LastParseAt) < chatLiveAssistantParseMinInterval {
		if entry.Valid {
			return cloneRenderLines(entry.Lines)
		}
		return p.liveAssistantParseFallbackLines(width)
	}

	liveMessage := chatMessageItem{
		Role:      "assistant",
		Text:      text,
		CreatedAt: now.UnixMilli(),
	}
	lines, recovered := p.renderLiveAssistantMessageLines(liveMessage, width, entry.Lines)
	entry.LastParseAt = now
	entry.LastAttemptWidth = width
	entry.LastAttemptGeneration = p.timelineRenderGeneration
	entry.LastAttemptVariant = p.assistantVariant
	if recovered {
		if entry.Valid {
			return cloneRenderLines(entry.Lines)
		}
		return lines
	}
	entry.Valid = true
	entry.Width = width
	entry.Generation = p.timelineRenderGeneration
	entry.Variant = p.assistantVariant
	entry.ParsedText = text
	entry.Lines = cloneRenderLines(lines)
	return lines
}

func (p *ChatPage) renderLiveAssistantLines(width int) []chatRenderLine {
	assistantLines := p.cachedLiveAssistantLines(width)
	if len(assistantLines) == 0 {
		return nil
	}
	if p.liveRunVisible() {
		last := assistantLines[len(assistantLines)-1]
		last = appendRenderLineSuffix(last, " "+p.spinnerFrame(), p.thinkingPulseStyle(), width)
		assistantLines[len(assistantLines)-1] = last
	}
	return assistantLines
}

func (p *ChatPage) shouldRenderLiveAssistant() bool {
	if p == nil || strings.TrimSpace(p.liveAssistant) == "" {
		return false
	}
	if p.liveRunVisible() {
		return true
	}
	if p.lifecycle == nil || p.lifecycle.Active {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(p.lifecycle.Phase), "completed")
}

func (p *ChatPage) buildTimelineRenderBlocks(width int) []chatTimelineRenderBlock {
	if width <= 0 {
		return nil
	}

	blocks := make([]chatTimelineRenderBlock, 0, len(p.timeline)+4)
	for i := range p.timeline {
		blocks = appendTimelineRenderBlock(blocks, p.cachedTimelineMessageLines(i, width))
	}

	if p.liveRunVisible() {
		liveTools := p.liveToolEntries(2)
		for _, entry := range liveTools {
			if shouldSuppressLiveToolEntry(entry) {
				continue
			}
			blocks = appendTimelineRenderBlock(blocks, p.withTimelineSpacer(p.renderLiveToolEntryLines(entry, width)))
		}
	}

	if p.shouldRenderLiveAssistant() {
		blocks = appendTimelineRenderBlock(blocks, p.withTimelineSpacer(p.renderLiveAssistantLines(width)))
	}

	return blocks
}

func (p *ChatPage) withTimelineSpacer(lines []chatRenderLine) []chatRenderLine {
	if len(lines) == 0 {
		return nil
	}
	return append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
}

func appendTimelineRenderBlock(blocks []chatTimelineRenderBlock, lines []chatRenderLine) []chatTimelineRenderBlock {
	if len(lines) == 0 {
		return blocks
	}
	return append(blocks, chatTimelineRenderBlock{Lines: lines, Height: len(lines)})
}

func totalTimelineRenderBlockHeight(blocks []chatTimelineRenderBlock) int {
	total := 0
	for _, block := range blocks {
		total += block.Height
	}
	return total
}

func (p *ChatPage) shouldBypassTimelineMessageCache(message chatMessageItem) bool {
	if isManagedToolTimelineMessage(message) && strings.EqualFold(strings.TrimSpace(message.ToolState), "running") {
		return true
	}
	if !isManagedReasoningTimelineMessage(message) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(message.ToolState), "running") {
		return false
	}
	return reasoningTimelineMessageRunID(message) == p.runID && p.liveRunVisible()
}
