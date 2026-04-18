package ui

import "github.com/gdamore/tcell/v2"

func (p *ChatPage) SetPasteActive(active bool) {
	wasActive := p.pasteActive
	p.pasteActive = active
	if !active {
		p.flushPasteBuffer()
	}
	if !wasActive && active {
		p.lastPasteBatchSize = 0
	}
}

func (p *ChatPage) InputPasteBuffered() int {
	if p == nil {
		return 0
	}
	return len(p.pasteBuffer)
}

func (p *ChatPage) LastPasteBatchSize() int {
	if p == nil {
		return 0
	}
	return p.lastPasteBatchSize
}

func (p *ChatPage) flushPasteBuffer() {
	if p == nil {
		return
	}
	if len(p.pasteBuffer) == 0 {
		p.lastPasteBatchSize = 0
		return
	}
	batch := string(p.pasteBuffer)
	p.pasteBuffer = p.pasteBuffer[:0]
	before := p.input
	inserted := 0
	p.input, p.inputCursor, inserted = insertMultilineAtCursor(p.input, p.inputCursor, batch, chatMaxInputRunes)
	if inserted > 0 {
		p.lastPasteBatchSize = inserted
	} else {
		p.lastPasteBatchSize = 0
	}
	p.maybeWarnLargeInput(before, p.input)
	p.syncComposerPalettes()
}

func (p *ChatPage) handleInputPasteKey(ev *tcell.EventKey) bool {
	if ev == nil {
		return false
	}
	if p.askUserModalActive() {
		if p.askUserInputMode {
			return p.handleAskUserInputKey(ev)
		}
		return p.handleAskUserModalKey(ev)
	}
	switch ev.Key() {
	case tcell.KeyRune:
		r := ev.Rune()
		if normalized, ok := normalizeMultilineRune(r); ok {
			if p.commandPaletteActive() || p.mentionPaletteActive() {
				p.flushPasteBuffer()
				before := p.input
				p.input, p.inputCursor, _ = insertMultilineAtCursor(p.input, p.inputCursor, string(normalized), chatMaxInputRunes)
				p.lastPasteBatchSize = 0
				p.maybeWarnLargeInput(before, p.input)
				p.syncComposerPalettes()
				return true
			}
			p.pasteBuffer = append(p.pasteBuffer, normalized)
			if len(p.pasteBuffer) >= singleLinePasteFlushChunkRunes {
				p.flushPasteBuffer()
				return true
			}
			return false
		}
		return false
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		p.pasteBuffer = append(p.pasteBuffer, '\n')
		if len(p.pasteBuffer) >= singleLinePasteFlushChunkRunes {
			p.flushPasteBuffer()
			return true
		}
		return false
	case tcell.KeyTab, tcell.KeyBacktab:
		p.pasteBuffer = append(p.pasteBuffer, ' ')
		if len(p.pasteBuffer) >= singleLinePasteFlushChunkRunes {
			p.flushPasteBuffer()
			return true
		}
		return false
	default:
		if p.keybinds != nil {
			if p.keybinds.Match(ev, KeybindChatBackspace) {
				if len(p.pasteBuffer) > 0 {
					p.pasteBuffer = p.pasteBuffer[:len(p.pasteBuffer)-1]
					p.lastPasteBatchSize = len(p.pasteBuffer)
					return false
				}
				return false
			}
			if p.keybinds.Match(ev, KeybindChatClear) {
				p.pasteBuffer = p.pasteBuffer[:0]
				p.lastPasteBatchSize = 0
				return true
			}
		}
		return false
	}
}

func (p *ChatPage) HandlePasteKey(ev *tcell.EventKey) bool {
	if p == nil || !p.pasteActive {
		return false
	}
	return p.handleInputPasteKey(ev)
}
