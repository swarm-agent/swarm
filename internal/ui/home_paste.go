package ui

import "github.com/gdamore/tcell/v2"

func (p *HomePage) SetPasteActive(active bool) {
	wasActive := p.pasteActive
	p.pasteActive = active
	if !active {
		p.flushPasteBuffer()
	}
	if !wasActive && active {
		p.lastPasteBatchSize = 0
	}
}

func (p *HomePage) PromptPasteBuffered() int {
	if p == nil {
		return 0
	}
	return len(p.pasteBuffer)
}

func (p *HomePage) LastPasteBatchSize() int {
	if p == nil {
		return 0
	}
	return p.lastPasteBatchSize
}

func (p *HomePage) flushPasteBuffer() {
	if p == nil {
		return
	}
	if len(p.pasteBuffer) == 0 {
		p.lastPasteBatchSize = 0
		return
	}
	batch := string(p.pasteBuffer)
	p.pasteBuffer = p.pasteBuffer[:0]
	inserted := 0
	p.prompt, p.promptCursor, inserted = insertMultilineAtCursor(p.prompt, p.promptCursor, batch, homeMaxInputRunes)
	if inserted > 0 {
		p.lastPasteBatchSize = inserted
	} else {
		p.lastPasteBatchSize = 0
	}
	p.syncCommandPaletteSelection()
}

func (p *HomePage) handlePromptPasteKey(ev *tcell.EventKey) bool {
	if ev == nil {
		return false
	}
	if p.authDefaultsInfoModal.Visible {
		p.handleAuthDefaultsInfoKey(ev)
		return true
	}
	if p.authModal.Visible {
		p.handleAuthModalKey(ev)
		return true
	}
	if p.vaultModal.Visible {
		p.handleVaultModalKey(ev)
		return true
	}
	if p.workspaceModal.Visible {
		p.handleWorkspaceModalKey(ev)
		return true
	}
	if p.sandboxModal.Visible {
		p.handleSandboxModalKey(ev)
		return true
	}
	if p.worktreesModal.Visible {
		p.handleWorktreesModalKey(ev)
		return true
	}
	if p.mcpModal.Visible {
		p.handleMCPModalKey(ev)
		return true
	}
	if p.modelsModal.Visible {
		p.handleModelsModalKey(ev)
		return true
	}
	if p.agentsModal.Visible {
		p.handleAgentsModalKey(ev)
		return true
	}
	if p.voiceModal.Visible {
		p.handleVoiceModalKey(ev)
		return true
	}
	if p.themeModal.Visible {
		p.handleThemeModalKey(ev)
		return true
	}
	if p.keybindsModal.Visible {
		p.handleKeybindsModalKey(ev)
		return true
	}
	switch ev.Key() {
	case tcell.KeyRune:
		r := ev.Rune()
		if normalized, ok := normalizeMultilineRune(r); ok {
			if p.commandPaletteActive() {
				p.flushPasteBuffer()
				p.prompt, p.promptCursor, _ = insertMultilineAtCursor(p.prompt, p.promptCursor, string(normalized), homeMaxInputRunes)
				p.lastPasteBatchSize = 0
				p.syncCommandPaletteSelection()
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
			if p.keybinds.Match(ev, KeybindHomePromptBackspace) {
				if len(p.pasteBuffer) > 0 {
					p.pasteBuffer = p.pasteBuffer[:len(p.pasteBuffer)-1]
					p.lastPasteBatchSize = len(p.pasteBuffer)
					return false
				}
				return false
			}
			if p.keybinds.Match(ev, KeybindHomePromptClear) {
				p.pasteBuffer = p.pasteBuffer[:0]
				p.lastPasteBatchSize = 0
				return true
			}
		}
		return false
	}
}

func (p *HomePage) HandlePasteKey(ev *tcell.EventKey) bool {
	if p == nil || !p.pasteActive {
		return false
	}
	return p.handlePromptPasteKey(ev)
}
