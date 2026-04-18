package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	sharedtheme "swarm-refactor/swarmtui/theme"
)

type Theme struct {
	Background           tcell.Style
	Panel                tcell.Style
	Element              tcell.Style
	Border               tcell.Style
	BorderActive         tcell.Style
	Text                 tcell.Style
	TextMuted            tcell.Style
	Primary              tcell.Style
	Secondary            tcell.Style
	Accent               tcell.Style
	Success              tcell.Style
	Warning              tcell.Style
	Error                tcell.Style
	Prompt               tcell.Style
	PromptCursor         tcell.Style
	MarkdownText         tcell.Style
	MarkdownHeading      tcell.Style
	MarkdownList         tcell.Style
	MarkdownQuote        tcell.Style
	MarkdownCode         tcell.Style
	MarkdownCodeKeyword  tcell.Style
	MarkdownCodeType     tcell.Style
	MarkdownCodeString   tcell.Style
	MarkdownCodeNumber   tcell.Style
	MarkdownCodeComment  tcell.Style
	MarkdownCodeFunction tcell.Style
	MarkdownCodeOperator tcell.Style
	MarkdownRule         tcell.Style
	MarkdownLink         tcell.Style
}

type ThemeOption struct {
	ID      string
	Name    string
	Theme   Theme
	Palette ThemePalette
	Builtin bool
}

type ThemePalette = sharedtheme.ThemePalette

var builtinThemeCatalog = buildBuiltinThemeCatalog()

var customThemeCatalog []ThemeOption

func DefaultThemeID() string {
	return sharedtheme.DefaultThemeID()
}

func BuiltinThemeCatalog() []ThemeOption {
	out := make([]ThemeOption, len(builtinThemeCatalog))
	copy(out, builtinThemeCatalog)
	return out
}

func CustomThemeCatalog() []ThemeOption {
	out := make([]ThemeOption, len(customThemeCatalog))
	copy(out, customThemeCatalog)
	return out
}

func ThemeCatalog() []ThemeOption {
	builtins := BuiltinThemeCatalog()
	custom := CustomThemeCatalog()
	out := make([]ThemeOption, 0, len(builtins)+len(custom))
	out = append(out, builtins...)
	out = append(out, custom...)
	return out
}

func SetCustomThemes(items []ThemeOption) {
	next := make([]ThemeOption, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	builtins := make(map[string]struct{}, len(builtinThemeCatalog))
	for _, item := range builtinThemeCatalog {
		builtins[item.ID] = struct{}{}
	}

	for _, item := range items {
		item.ID = NormalizeThemeID(item.ID)
		if item.ID == "" {
			continue
		}
		if _, exists := builtins[item.ID]; exists {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		if strings.TrimSpace(item.Name) == "" {
			item.Name = strings.ReplaceAll(item.ID, "-", " ")
		}
		item.Builtin = false
		next = append(next, item)
		seen[item.ID] = struct{}{}
	}
	customThemeCatalog = next
}

func ResolveTheme(id string) (ThemeOption, bool) {
	normalized := NormalizeThemeID(id)
	if normalized == "" {
		normalized = DefaultThemeID()
	}

	for _, item := range ThemeCatalog() {
		if normalized == item.ID {
			return item, true
		}
		if normalized == NormalizeThemeID(item.Name) {
			return item, true
		}
	}
	return ThemeOption{}, false
}

func NormalizeThemeID(value string) string {
	return sharedtheme.NormalizeThemeID(value)
}

func NordTheme() Theme {
	item, ok := ResolveTheme(DefaultThemeID())
	if !ok {
		panic("default theme not found")
	}
	return item.Theme
}

func NewCustomThemeOption(id, name string, palette ThemePalette) (ThemeOption, error) {
	shared, err := sharedtheme.NewCustomThemeOption(id, name, palette)
	if err != nil {
		return ThemeOption{}, err
	}
	return buildThemeOption(shared.ID, shared.Name, shared.Palette, shared.Builtin)
}

func buildThemeOption(id, name string, palette ThemePalette, builtin bool) (ThemeOption, error) {
	palette = palette.WithDefaults()

	bg, err := parseHex(palette.Background)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("background: %w", err)
	}
	panel, err := parseHex(palette.Panel)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("panel: %w", err)
	}
	element, err := parseHex(palette.Element)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("element: %w", err)
	}
	border, err := parseHex(palette.Border)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("border: %w", err)
	}
	borderActive, err := parseHex(palette.BorderActive)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("borderActive: %w", err)
	}
	text, err := parseHex(palette.Text)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("text: %w", err)
	}
	textMuted, err := parseHex(palette.TextMuted)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("textMuted: %w", err)
	}
	primary, err := parseHex(palette.Primary)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("primary: %w", err)
	}
	secondary, err := parseHex(palette.Secondary)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("secondary: %w", err)
	}
	accent, err := parseHex(palette.Accent)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("accent: %w", err)
	}
	success, err := parseHex(palette.Success)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("success: %w", err)
	}
	warning, err := parseHex(palette.Warning)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("warning: %w", err)
	}
	errorColor, err := parseHex(palette.Error)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("error: %w", err)
	}
	codeBackground, err := parseHex(palette.CodeBackground)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeBackground: %w", err)
	}
	codeText, err := parseHex(palette.CodeText)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeText: %w", err)
	}
	codeKeyword, err := parseHex(palette.CodeKeyword)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeKeyword: %w", err)
	}
	codeType, err := parseHex(palette.CodeType)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeType: %w", err)
	}
	codeString, err := parseHex(palette.CodeString)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeString: %w", err)
	}
	codeNumber, err := parseHex(palette.CodeNumber)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeNumber: %w", err)
	}
	codeComment, err := parseHex(palette.CodeComment)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeComment: %w", err)
	}
	codeFunction, err := parseHex(palette.CodeFunction)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeFunction: %w", err)
	}
	codeOperator, err := parseHex(palette.CodeOperator)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("codeOperator: %w", err)
	}
	prompt, err := parseHex(palette.Prompt)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("prompt: %w", err)
	}
	cursorBG, err := parseHex(palette.PromptCursorBG)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("promptCursorBG: %w", err)
	}
	cursorFG, err := parseHex(palette.PromptCursorFG)
	if err != nil {
		return ThemeOption{}, fmt.Errorf("promptCursorFG: %w", err)
	}

	id = NormalizeThemeID(id)
	if id == "" {
		return ThemeOption{}, fmt.Errorf("theme id must not be empty")
	}

	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = id
	}

	textStyle := tcell.StyleDefault.Background(bg).Foreground(text)
	accentStyle := tcell.StyleDefault.Background(bg).Foreground(accent)
	secondaryStyle := tcell.StyleDefault.Background(bg).Foreground(secondary)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(textMuted)
	markdownCode := tcell.StyleDefault.Background(codeBackground).Foreground(codeText)
	markdownCodeKeyword := tcell.StyleDefault.Background(codeBackground).Foreground(codeKeyword).Bold(true)
	markdownCodeType := tcell.StyleDefault.Background(codeBackground).Foreground(codeType)
	markdownCodeString := tcell.StyleDefault.Background(codeBackground).Foreground(codeString)
	markdownCodeNumber := tcell.StyleDefault.Background(codeBackground).Foreground(codeNumber)
	markdownCodeComment := tcell.StyleDefault.Background(codeBackground).Foreground(codeComment).Italic(true)
	markdownCodeFunction := tcell.StyleDefault.Background(codeBackground).Foreground(codeFunction)
	markdownCodeOperator := tcell.StyleDefault.Background(codeBackground).Foreground(codeOperator)

	return ThemeOption{
		ID:      id,
		Name:    displayName,
		Palette: palette,
		Builtin: builtin,
		Theme: Theme{
			Background:           tcell.StyleDefault.Background(bg).Foreground(text),
			Panel:                tcell.StyleDefault.Background(panel).Foreground(text),
			Element:              tcell.StyleDefault.Background(element).Foreground(text),
			Border:               tcell.StyleDefault.Background(bg).Foreground(border),
			BorderActive:         tcell.StyleDefault.Background(bg).Foreground(borderActive),
			Text:                 textStyle,
			TextMuted:            mutedStyle,
			Primary:              tcell.StyleDefault.Background(bg).Foreground(primary),
			Secondary:            secondaryStyle,
			Accent:               accentStyle,
			Success:              tcell.StyleDefault.Background(bg).Foreground(success),
			Warning:              tcell.StyleDefault.Background(bg).Foreground(warning),
			Error:                tcell.StyleDefault.Background(bg).Foreground(errorColor),
			Prompt:               tcell.StyleDefault.Background(bg).Foreground(prompt),
			PromptCursor:         tcell.StyleDefault.Background(cursorBG).Foreground(cursorFG),
			MarkdownText:         textStyle,
			MarkdownHeading:      secondaryStyle.Bold(true),
			MarkdownList:         accentStyle,
			MarkdownQuote:        mutedStyle,
			MarkdownCode:         markdownCode,
			MarkdownCodeKeyword:  markdownCodeKeyword,
			MarkdownCodeType:     markdownCodeType,
			MarkdownCodeString:   markdownCodeString,
			MarkdownCodeNumber:   markdownCodeNumber,
			MarkdownCodeComment:  markdownCodeComment,
			MarkdownCodeFunction: markdownCodeFunction,
			MarkdownCodeOperator: markdownCodeOperator,
			MarkdownRule:         tcell.StyleDefault.Background(bg).Foreground(borderActive),
			MarkdownLink:         tcell.StyleDefault.Background(bg).Foreground(primary).Underline(true),
		},
	}, nil
}

func resolveBuiltinThemeByID(id string) (ThemeOption, bool) {
	normalized := NormalizeThemeID(id)
	if normalized == "" {
		return ThemeOption{}, false
	}
	for _, item := range builtinThemeCatalog {
		if normalized == item.ID || normalized == NormalizeThemeID(item.Name) {
			return item, true
		}
	}
	return ThemeOption{}, false
}

func ThemePaletteSlotNames() []string {
	return sharedtheme.ThemePaletteSlotNames()
}

func SetThemePaletteSlot(p ThemePalette, slot string, value string) (ThemePalette, error) {
	return sharedtheme.SetThemePaletteSlot(p, slot, value)
}

func buildBuiltinThemeCatalog() []ThemeOption {
	items := sharedtheme.BuiltinThemeCatalog()
	out := make([]ThemeOption, 0, len(items))
	for _, item := range items {
		option, err := buildThemeOption(item.ID, item.Name, item.Palette, item.Builtin)
		if err != nil {
			panic(err)
		}
		out = append(out, option)
	}
	return out
}

func parseHex(hex string) (tcell.Color, error) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return tcell.ColorDefault, fmt.Errorf("invalid hex color: %s", hex)
	}
	r, err := strconv.ParseInt(h[0:2], 16, 64)
	if err != nil {
		return tcell.ColorDefault, err
	}
	g, err := strconv.ParseInt(h[2:4], 16, 64)
	if err != nil {
		return tcell.ColorDefault, err
	}
	b, err := strconv.ParseInt(h[4:6], 16, 64)
	if err != nil {
		return tcell.ColorDefault, err
	}
	return tcell.NewRGBColor(int32(r), int32(g), int32(b)), nil
}
