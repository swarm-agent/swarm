package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *HomePage) drawVoiceModal(s tcell.Screen) {
	if !p.voiceModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 44 || h < 12 {
		return
	}
	modalW := w - 8
	if modalW > 118 {
		modalW = 118
	}
	if modalW < 76 {
		modalW = w - 2
	}
	if modalW < 44 {
		return
	}
	modalH := h - 6
	if modalH > 32 {
		modalH = 32
	}
	if modalH < 18 {
		modalH = h - 2
	}
	if modalH < 12 {
		return
	}
	rect := Rect{
		X: maxInt(1, (w-modalW)/2),
		Y: maxInt(1, (h-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)

	title := "Voice Controls"
	if p.voiceModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	status := strings.TrimSpace(p.voiceModal.Status)
	statusStyle := p.theme.TextMuted
	if err := strings.TrimSpace(p.voiceModal.Error); err != "" {
		status = err
		statusStyle = p.theme.Error
	}
	if status == "" {
		status = "Enter selects option. Use this modal to pick microphone and STT provider."
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(status, rect.W-4))

	compact := rect.W < 76
	if compact {
		listRect := Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6}
		p.drawVoiceModalListPane(s, listRect)
	} else {
		listRect := Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W/2 - 1, H: rect.H - 6}
		if listRect.W < 32 {
			listRect.W = 32
		}
		detailRect := Rect{
			X: listRect.X + listRect.W + 1,
			Y: listRect.Y,
			W: rect.W - listRect.W - 3,
			H: listRect.H,
		}
		if detailRect.W < 26 {
			detailRect.W = 26
			listRect.W = rect.W - detailRect.W - 3
		}

		p.drawVoiceModalListPane(s, listRect)
		p.drawVoiceModalDetailPane(s, detailRect)
	}

	voiceShortcut := "F9"
	if p.keybinds != nil {
		voiceShortcut = p.keybinds.Label(KeybindGlobalVoiceInput)
	}
	help := fmt.Sprintf("Enter apply  t test  r refresh  %s capture to input  Esc close", voiceShortcut)
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(help, rect.W-4))
}

func (p *HomePage) drawVoiceModalListPane(s tcell.Screen, rect Rect) {
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, "Actions, Devices, Providers")

	rowCount := rect.H - 2
	if rowCount < 1 {
		rowCount = 1
	}
	items := p.voiceModal.Items
	if len(items) == 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no voice options available")
		return
	}

	if p.voiceModal.Selected < p.voiceModal.Scroll {
		p.voiceModal.Scroll = p.voiceModal.Selected
	}
	if p.voiceModal.Selected >= p.voiceModal.Scroll+rowCount {
		p.voiceModal.Scroll = p.voiceModal.Selected - rowCount + 1
	}
	if p.voiceModal.Scroll < 0 {
		p.voiceModal.Scroll = 0
	}
	maxScroll := len(items) - rowCount
	if maxScroll < 0 {
		maxScroll = 0
	}
	if p.voiceModal.Scroll > maxScroll {
		p.voiceModal.Scroll = maxScroll
	}

	for row := 0; row < rowCount; row++ {
		idx := p.voiceModal.Scroll + row
		if idx >= len(items) {
			break
		}
		item := items[idx]
		y := rect.Y + 1 + row
		style := p.theme.TextMuted
		prefix := "  "
		line := item.Title

		if item.Kind == voiceModalItemKindSection {
			style = p.theme.Accent.Bold(true)
			prefix = " "
			line = "[" + strings.TrimSpace(item.Title) + "]"
		} else {
			if idx == p.voiceModal.Selected {
				style = p.theme.Primary.Bold(true)
				prefix = "> "
			} else {
				style = p.theme.Text
			}
			if detail := strings.TrimSpace(item.Detail); detail != "" {
				line = line + " - " + detail
			}
		}
		DrawText(s, rect.X+1, y, rect.W-2, style, clampEllipsis(prefix+line, rect.W-2))
	}
}

func (p *HomePage) drawVoiceModalDetailPane(s tcell.Screen, rect Rect) {
	DrawBox(s, rect, p.theme.Border)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, "Current Selection")

	rowY := rect.Y + 1
	maxY := rect.Y + rect.H - 1
	write := func(style tcell.Style, line string) {
		if rowY >= maxY {
			return
		}
		DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(line, rect.W-4))
		rowY++
	}

	status := p.voiceModal.StatusDTO
	write(p.theme.Text, "path: "+voiceFallback(strings.TrimSpace(status.PathID), "-"))
	write(p.theme.Text, "device: "+voiceFallback(strings.TrimSpace(status.Config.DeviceID), "system default"))
	write(p.theme.Text, "stt profile: "+voiceFallback(strings.TrimSpace(status.Config.STTProfile), "auto"))

	sttProvider := voiceFallback(strings.TrimSpace(status.STT.Provider), "auto")
	sttModel := voiceFallback(strings.TrimSpace(status.STT.Model), "default")
	write(p.theme.Text, fmt.Sprintf("stt: %s / %s", sttProvider, sttModel))
	if reason := strings.TrimSpace(status.STT.Reason); reason != "" {
		write(p.theme.TextMuted, "stt note: "+reason)
	}

	ttsProvider := voiceFallback(strings.TrimSpace(status.TTS.Provider), "disabled")
	ttsVoice := voiceFallback(strings.TrimSpace(status.TTS.Voice), "-")
	write(p.theme.Text, fmt.Sprintf("tts: %s / %s", ttsProvider, ttsVoice))
	if reason := strings.TrimSpace(status.TTS.Reason); reason != "" {
		write(p.theme.TextMuted, "tts note: "+reason)
	}

	if selected, ok := p.voiceModal.selectedItem(); ok {
		if rowY < maxY {
			rowY++
		}
		write(p.theme.TextMuted, "selected option:")
		write(p.theme.Text, selected.Title)
		if detail := strings.TrimSpace(selected.Detail); detail != "" {
			for _, line := range wrapVoiceModalText(detail, rect.W-4) {
				write(p.theme.TextMuted, line)
			}
		}
	}

	if p.voiceModal.LastTest != nil {
		if rowY < maxY {
			rowY++
		}
		result := p.voiceModal.LastTest
		write(p.theme.TextMuted, "last test:")
		target := fmt.Sprintf(
			"%s / %s",
			voiceFallback(strings.TrimSpace(result.Provider), "-"),
			voiceFallback(strings.TrimSpace(result.Model), "-"),
		)
		if profile := strings.TrimSpace(result.Profile); profile != "" {
			target = profile + " -> " + target
		}
		header := fmt.Sprintf(
			"%s - %ds - %d bytes",
			target,
			maxInt(0, result.Seconds),
			maxInt(0, result.AudioBytes),
		)
		write(p.theme.Text, clampEllipsis(header, rect.W-4))
		transcript := strings.TrimSpace(result.Text)
		if transcript == "" {
			write(p.theme.TextMuted, "(empty transcript)")
		} else {
			for _, line := range wrapVoiceModalText(transcript, rect.W-4) {
				write(p.theme.TextMuted, line)
				if rowY >= maxY {
					break
				}
			}
		}
	}
}

func wrapVoiceModalText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" || width < 8 {
		if text == "" {
			return nil
		}
		return []string{clampEllipsis(text, maxInt(1, width))}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{clampEllipsis(text, width)}
	}

	lines := make([]string, 0, 4)
	line := ""
	for _, word := range words {
		if utf8.RuneCountInString(word) > width {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, clampEllipsis(line, width))
				line = ""
			}
			lines = append(lines, clampEllipsis(word, width))
			continue
		}
		if line == "" {
			line = word
			continue
		}
		next := line + " " + word
		if utf8.RuneCountInString(next) <= width {
			line = next
			continue
		}
		lines = append(lines, line)
		line = word
	}
	if strings.TrimSpace(line) != "" {
		lines = append(lines, line)
	}
	return lines
}

func voiceFallback(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
