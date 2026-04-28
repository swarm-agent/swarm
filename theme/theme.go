package theme

import (
	"fmt"
	"strconv"
	"strings"
)

type ThemeOption struct {
	ID      string
	Name    string
	Palette ThemePalette
	Builtin bool
}

type ThemePalette struct {
	Background     string `json:"background,omitempty"`
	Panel          string `json:"panel,omitempty"`
	Element        string `json:"element,omitempty"`
	Border         string `json:"border,omitempty"`
	BorderActive   string `json:"border_active,omitempty"`
	Text           string `json:"text,omitempty"`
	TextMuted      string `json:"text_muted,omitempty"`
	Primary        string `json:"primary,omitempty"`
	Secondary      string `json:"secondary,omitempty"`
	Accent         string `json:"accent,omitempty"`
	Success        string `json:"success,omitempty"`
	Warning        string `json:"warning,omitempty"`
	Error          string `json:"error,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	PromptCursorBG string `json:"prompt_cursor_bg,omitempty"`
	PromptCursorFG string `json:"prompt_cursor_fg,omitempty"`
	CodeBackground string `json:"code_background,omitempty"`
	CodeText       string `json:"code_text,omitempty"`
	CodeKeyword    string `json:"code_keyword,omitempty"`
	CodeType       string `json:"code_type,omitempty"`
	CodeString     string `json:"code_string,omitempty"`
	CodeNumber     string `json:"code_number,omitempty"`
	CodeComment    string `json:"code_comment,omitempty"`
	CodeFunction   string `json:"code_function,omitempty"`
	CodeOperator   string `json:"code_operator,omitempty"`
}

var builtinThemeCatalog = []ThemeOption{
	newBuiltinThemeOption("black", "Black", ThemePalette{
		Background:     "#000000",
		Panel:          "#111111",
		Element:        "#111111",
		Border:         "#27272A",
		BorderActive:   "#3F3F46",
		Text:           "#F5F5F0",
		TextMuted:      "#A8A29A",
		Primary:        "#D6D0C4",
		Secondary:      "#BFB8AA",
		Accent:         "#E7DFC9",
		Success:        "#8FA77A",
		Warning:        "#C9A66B",
		Error:          "#C46C6C",
		PromptCursorBG: "#D6D0C4",
		PromptCursorFG: "#000000",
	}),
	newBuiltinThemeOption("crimson", "Crimson", ThemePalette{
		Background:     "#1E1418",
		Panel:          "#26191E",
		Element:        "#26191E",
		Border:         "#5E3841",
		BorderActive:   "#7A4A55",
		Text:           "#BBB2B6",
		TextMuted:      "#B9A8AD",
		Primary:        "#E36A7A",
		Secondary:      "#F08CA0",
		Accent:         "#D9A066",
		Success:        "#8CCB9B",
		Warning:        "#E4B15A",
		Error:          "#FF6B6B",
		PromptCursorBG: "#E36A7A",
		PromptCursorFG: "#1E1418",
	}),
	newBuiltinThemeOption("nord", "Nord", ThemePalette{
		Background:     "#2E3440",
		Border:         "#434C5E",
		BorderActive:   "#4C566A",
		Text:           "#ECEFF4",
		TextMuted:      "#8B95A7",
		Primary:        "#88C0D0",
		Secondary:      "#81A1C1",
		Accent:         "#8FBCBB",
		Success:        "#A3BE8C",
		Warning:        "#D08770",
		Error:          "#BF616A",
		PromptCursorBG: "#88C0D0",
		PromptCursorFG: "#2E3440",
	}),
	newBuiltinThemeOption("solarized-dark", "Solarized Dark", ThemePalette{
		Background:     "#002B36",
		Panel:          "#073642",
		Element:        "#073642",
		Border:         "#586E75",
		BorderActive:   "#657B83",
		Text:           "#EEE8D5",
		TextMuted:      "#93A1A1",
		Primary:        "#268BD2",
		Secondary:      "#2AA198",
		Accent:         "#6C71C4",
		Success:        "#859900",
		Warning:        "#B58900",
		Error:          "#DC322F",
		PromptCursorBG: "#268BD2",
		PromptCursorFG: "#002B36",
	}),
	newBuiltinThemeOption("dracula", "Dracula", ThemePalette{
		Background:     "#282A36",
		Panel:          "#303341",
		Element:        "#303341",
		Border:         "#44475A",
		BorderActive:   "#6272A4",
		Text:           "#F8F8F2",
		TextMuted:      "#B0B6D0",
		Primary:        "#BD93F9",
		Secondary:      "#8BE9FD",
		Accent:         "#FF79C6",
		Success:        "#50FA7B",
		Warning:        "#F1FA8C",
		Error:          "#FF5555",
		PromptCursorBG: "#BD93F9",
		PromptCursorFG: "#282A36",
	}),
	newBuiltinThemeOption("gruvbox-dark", "Gruvbox Dark", ThemePalette{
		Background:     "#282828",
		Panel:          "#32302F",
		Element:        "#32302F",
		Border:         "#504945",
		BorderActive:   "#665C54",
		Text:           "#EBDBB2",
		TextMuted:      "#A89984",
		Primary:        "#83A598",
		Secondary:      "#D3869B",
		Accent:         "#8EC07C",
		Success:        "#B8BB26",
		Warning:        "#FABD2F",
		Error:          "#FB4934",
		PromptCursorBG: "#83A598",
		PromptCursorFG: "#282828",
	}),
	newBuiltinThemeOption("catppuccin-mocha", "Catppuccin Mocha", ThemePalette{
		Background:     "#1E1E2E",
		Panel:          "#313244",
		Element:        "#313244",
		Border:         "#45475A",
		BorderActive:   "#585B70",
		Text:           "#CDD6F4",
		TextMuted:      "#A6ADC8",
		Primary:        "#89B4FA",
		Secondary:      "#B4BEFE",
		Accent:         "#F5C2E7",
		Success:        "#A6E3A1",
		Warning:        "#F9E2AF",
		Error:          "#F38BA8",
		PromptCursorBG: "#89B4FA",
		PromptCursorFG: "#1E1E2E",
	}),
	newBuiltinThemeOption("tokyo-night", "Tokyo Night", ThemePalette{
		Background:     "#1A1B26",
		Panel:          "#1F2335",
		Element:        "#1F2335",
		Border:         "#3B4261",
		BorderActive:   "#565F89",
		Text:           "#C0CAF5",
		TextMuted:      "#9AA5CE",
		Primary:        "#7AA2F7",
		Secondary:      "#7DCFFF",
		Accent:         "#BB9AF7",
		Success:        "#9ECE6A",
		Warning:        "#E0AF68",
		Error:          "#F7768E",
		PromptCursorBG: "#7AA2F7",
		PromptCursorFG: "#1A1B26",
	}),
	newBuiltinThemeOption("everforest-dark", "Everforest Dark", ThemePalette{
		Background:     "#2D353B",
		Panel:          "#343F44",
		Element:        "#343F44",
		Border:         "#475258",
		BorderActive:   "#56635F",
		Text:           "#D3C6AA",
		TextMuted:      "#9DA9A0",
		Primary:        "#7FBBB3",
		Secondary:      "#A7C080",
		Accent:         "#DBBC7F",
		Success:        "#83C092",
		Warning:        "#E69875",
		Error:          "#E67E80",
		PromptCursorBG: "#7FBBB3",
		PromptCursorFG: "#2D353B",
	}),
	newBuiltinThemeOption("ayu-mirage", "Ayu Mirage", ThemePalette{
		Background:     "#1F2430",
		Panel:          "#242936",
		Element:        "#242936",
		Border:         "#3A4256",
		BorderActive:   "#4B5874",
		Text:           "#CCCAC2",
		TextMuted:      "#97A0B3",
		Primary:        "#73D0FF",
		Secondary:      "#FFD580",
		Accent:         "#D4BFFF",
		Success:        "#87D96C",
		Warning:        "#FFAD66",
		Error:          "#F28779",
		PromptCursorBG: "#73D0FF",
		PromptCursorFG: "#1F2430",
	}),
	newBuiltinThemeOption("one-dark", "One Dark", ThemePalette{
		Background:     "#282C34",
		Panel:          "#2C313A",
		Element:        "#2C313A",
		Border:         "#3E4451",
		BorderActive:   "#4B5263",
		Text:           "#ABB2BF",
		TextMuted:      "#8F96A3",
		Primary:        "#61AFEF",
		Secondary:      "#C678DD",
		Accent:         "#56B6C2",
		Success:        "#98C379",
		Warning:        "#E5C07B",
		Error:          "#E06C75",
		PromptCursorBG: "#61AFEF",
		PromptCursorFG: "#282C34",
	}),
	newBuiltinThemeOption("kanagawa-wave", "Kanagawa Wave", ThemePalette{
		Background:     "#1F1F28",
		Panel:          "#2A2A37",
		Element:        "#2A2A37",
		Border:         "#3D3D51",
		BorderActive:   "#54546D",
		Text:           "#DCD7BA",
		TextMuted:      "#A6A69C",
		Primary:        "#7E9CD8",
		Secondary:      "#957FB8",
		Accent:         "#6A9589",
		Success:        "#98BB6C",
		Warning:        "#DCA561",
		Error:          "#E46876",
		PromptCursorBG: "#7E9CD8",
		PromptCursorFG: "#1F1F28",
	}),
	newBuiltinThemeOption("rose-pine", "Rose Pine", ThemePalette{
		Background:     "#191724",
		Panel:          "#1F1D2E",
		Element:        "#1F1D2E",
		Border:         "#403D52",
		BorderActive:   "#524F67",
		Text:           "#E0DEF4",
		TextMuted:      "#908CAA",
		Primary:        "#C4A7E7",
		Secondary:      "#9CCFD8",
		Accent:         "#EBBCBA",
		Success:        "#31748F",
		Warning:        "#F6C177",
		Error:          "#EB6F92",
		PromptCursorBG: "#C4A7E7",
		PromptCursorFG: "#191724",
	}),
	newBuiltinThemeOption("monokai", "Monokai", ThemePalette{
		Background:     "#272822",
		Panel:          "#2F3129",
		Element:        "#2F3129",
		Border:         "#49483E",
		BorderActive:   "#75715E",
		Text:           "#F8F8F2",
		TextMuted:      "#AEAEA8",
		Primary:        "#66D9EF",
		Secondary:      "#AE81FF",
		Accent:         "#A6E22E",
		Success:        "#A6E22E",
		Warning:        "#E6DB74",
		Error:          "#F92672",
		PromptCursorBG: "#66D9EF",
		PromptCursorFG: "#272822",
	}),
	newBuiltinThemeOption("oceanic-next", "Oceanic Next", ThemePalette{
		Background:     "#1B2B34",
		Panel:          "#22313A",
		Element:        "#22313A",
		Border:         "#405860",
		BorderActive:   "#4F6B73",
		Text:           "#D8DEE9",
		TextMuted:      "#A7ADBA",
		Primary:        "#6699CC",
		Secondary:      "#C594C5",
		Accent:         "#5FB3B3",
		Success:        "#99C794",
		Warning:        "#FAC863",
		Error:          "#EC5F67",
		PromptCursorBG: "#6699CC",
		PromptCursorFG: "#1B2B34",
	}),
	newBuiltinThemeOption("graphite", "Graphite", ThemePalette{
		Background:     "#202428",
		Panel:          "#272C31",
		Element:        "#272C31",
		Border:         "#3A4148",
		BorderActive:   "#4A535D",
		Text:           "#E4E7EB",
		TextMuted:      "#A7B0B8",
		Primary:        "#7CB7FF",
		Secondary:      "#A3C4F3",
		Accent:         "#8FD3C1",
		Success:        "#9FD39A",
		Warning:        "#E6C178",
		Error:          "#E68A8A",
		PromptCursorBG: "#7CB7FF",
		PromptCursorFG: "#202428",
	}),
	newBuiltinThemeOption("cyberpunk", "Cyberpunk", ThemePalette{
		Background:     "#0B0F1A",
		Panel:          "#101729",
		Element:        "#101729",
		Border:         "#22405E",
		BorderActive:   "#2F5D87",
		Text:           "#C8D6FF",
		TextMuted:      "#7F96C9",
		Primary:        "#00E5FF",
		Secondary:      "#7C4DFF",
		Accent:         "#FF4D9D",
		Success:        "#00E676",
		Warning:        "#FFD54F",
		Error:          "#FF5252",
		PromptCursorBG: "#00E5FF",
		PromptCursorFG: "#0B0F1A",
	}),
	newBuiltinThemeOption("emerald-forest", "Emerald Forest", ThemePalette{
		Background:     "#10231C",
		Panel:          "#173229",
		Element:        "#173229",
		Border:         "#2D4A40",
		BorderActive:   "#3E6558",
		Text:           "#D6F5E5",
		TextMuted:      "#9ABAA9",
		Primary:        "#5BD6A1",
		Secondary:      "#7DE2C4",
		Accent:         "#AEEA94",
		Success:        "#86EFAC",
		Warning:        "#F5C16C",
		Error:          "#FF7B7B",
		PromptCursorBG: "#5BD6A1",
		PromptCursorFG: "#10231C",
	}),
	newBuiltinThemeOption("softwhite", "Softwhite", ThemePalette{
		Background:     "#F7F4EC",
		Panel:          "#FFFDF8",
		Element:        "#FFFDF8",
		Border:         "#D6CFC1",
		BorderActive:   "#C2B8A6",
		Text:           "#23201B",
		TextMuted:      "#6F675B",
		Primary:        "#5F5A52",
		Secondary:      "#7A7368",
		Accent:         "#8B7F6A",
		Success:        "#6F8A63",
		Warning:        "#9B7A44",
		Error:          "#A05C5C",
		PromptCursorBG: "#5F5A52",
		PromptCursorFG: "#F7F4EC",
	}),
	newBuiltinThemeOption("paper-ink", "Paper Ink", ThemePalette{
		Background:     "#FAF7F1",
		Panel:          "#F0E9DD",
		Element:        "#F0E9DD",
		Border:         "#B8AD9E",
		BorderActive:   "#9F9280",
		Text:           "#2A2A2A",
		TextMuted:      "#6D665F",
		Primary:        "#1F5E8C",
		Secondary:      "#4E6E81",
		Accent:         "#9E5A2B",
		Success:        "#3A7F4F",
		Warning:        "#A06B22",
		Error:          "#A13333",
		PromptCursorBG: "#1F5E8C",
		PromptCursorFG: "#FAF7F1",
	}),
	newBuiltinThemeOption("sunset-amber", "Sunset Amber", ThemePalette{
		Background:     "#2B1E1A",
		Panel:          "#352522",
		Element:        "#352522",
		Border:         "#5A4037",
		BorderActive:   "#795347",
		Text:           "#F8E7DA",
		TextMuted:      "#C5A892",
		Primary:        "#FF9E64",
		Secondary:      "#FFC857",
		Accent:         "#F97373",
		Success:        "#9ED67A",
		Warning:        "#FFD166",
		Error:          "#FF6B6B",
		PromptCursorBG: "#FF9E64",
		PromptCursorFG: "#2B1E1A",
	}),
	newBuiltinThemeOption("neon-night", "Neon Night", ThemePalette{
		Background:     "#14111F",
		Panel:          "#1B172A",
		Element:        "#1B172A",
		Border:         "#3A3159",
		BorderActive:   "#554683",
		Text:           "#E8E6FF",
		TextMuted:      "#A9A5D7",
		Primary:        "#7AF5FF",
		Secondary:      "#A58BFF",
		Accent:         "#FF7EDB",
		Success:        "#8BFFB2",
		Warning:        "#FFE082",
		Error:          "#FF6E8A",
		PromptCursorBG: "#7AF5FF",
		PromptCursorFG: "#14111F",
	}),
}

func DefaultThemeID() string {
	return "crimson"
}

func BuiltinThemeCatalog() []ThemeOption {
	out := make([]ThemeOption, len(builtinThemeCatalog))
	copy(out, builtinThemeCatalog)
	return out
}

func ResolveBuiltinTheme(id string) (ThemeOption, bool) {
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

func NormalizeThemeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}

	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")

	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				b.WriteRune(r)
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func NewCustomThemeOption(id, name string, palette ThemePalette) (ThemeOption, error) {
	item, err := buildThemeOption(id, name, palette, false)
	if err != nil {
		return ThemeOption{}, err
	}
	if _, exists := ResolveBuiltinTheme(item.ID); exists {
		return ThemeOption{}, fmt.Errorf("theme %q conflicts with builtin theme id", item.ID)
	}
	return item, nil
}

func (p ThemePalette) WithDefaults() ThemePalette {
	if p.Background == "" {
		p.Background = "#2E3440"
	}
	if p.Panel == "" {
		p.Panel = p.Background
	}
	if p.Element == "" {
		p.Element = p.Panel
	}
	if p.Text == "" {
		p.Text = "#E5E9F0"
	}
	if p.TextMuted == "" {
		p.TextMuted = "#9AA3B2"
	}
	if p.Primary == "" {
		p.Primary = "#88C0D0"
	}
	if p.Secondary == "" {
		p.Secondary = "#81A1C1"
	}
	if p.Accent == "" {
		p.Accent = p.Primary
	}
	if p.Success == "" {
		p.Success = "#A3BE8C"
	}
	if p.Warning == "" {
		p.Warning = "#EBCB8B"
	}
	if p.Error == "" {
		p.Error = "#BF616A"
	}
	if p.Border == "" {
		p.Border = p.TextMuted
	}
	if p.BorderActive == "" {
		p.BorderActive = p.Primary
	}
	if p.Prompt == "" {
		p.Prompt = p.Text
	}
	if p.PromptCursorBG == "" {
		p.PromptCursorBG = p.Primary
	}
	if p.PromptCursorFG == "" {
		p.PromptCursorFG = p.Background
	}
	if p.CodeBackground == "" {
		p.CodeBackground = p.Panel
	}
	if p.CodeText == "" {
		p.CodeText = p.Text
	}
	if p.CodeKeyword == "" {
		p.CodeKeyword = p.Primary
	}
	if p.CodeType == "" {
		p.CodeType = p.Secondary
	}
	if p.CodeString == "" {
		p.CodeString = p.Success
	}
	if p.CodeNumber == "" {
		p.CodeNumber = p.Warning
	}
	if p.CodeComment == "" {
		p.CodeComment = p.TextMuted
	}
	if p.CodeFunction == "" {
		p.CodeFunction = p.Accent
	}
	if p.CodeOperator == "" {
		p.CodeOperator = p.TextMuted
	}
	return p
}

func ThemePaletteSlotNames() []string {
	return []string{
		"background",
		"panel",
		"element",
		"border",
		"border-active",
		"text",
		"text-muted",
		"primary",
		"secondary",
		"accent",
		"success",
		"warning",
		"error",
		"prompt",
		"cursor-bg",
		"cursor-fg",
		"code-background",
		"code-text",
		"code-keyword",
		"code-type",
		"code-string",
		"code-number",
		"code-comment",
		"code-function",
		"code-operator",
	}
}

func SetThemePaletteSlot(p ThemePalette, slot string, value string) (ThemePalette, error) {
	canonical := normalizeThemePaletteSlot(slot)
	if canonical == "" {
		return p, fmt.Errorf("unknown theme color slot: %s", strings.TrimSpace(slot))
	}
	normalizedColor, err := normalizeHexColor(value)
	if err != nil {
		return p, err
	}

	switch canonical {
	case "background":
		p.Background = normalizedColor
	case "panel":
		p.Panel = normalizedColor
	case "element":
		p.Element = normalizedColor
	case "border":
		p.Border = normalizedColor
	case "border-active":
		p.BorderActive = normalizedColor
	case "text":
		p.Text = normalizedColor
	case "text-muted":
		p.TextMuted = normalizedColor
	case "primary":
		p.Primary = normalizedColor
	case "secondary":
		p.Secondary = normalizedColor
	case "accent":
		p.Accent = normalizedColor
	case "success":
		p.Success = normalizedColor
	case "warning":
		p.Warning = normalizedColor
	case "error":
		p.Error = normalizedColor
	case "prompt":
		p.Prompt = normalizedColor
	case "cursor-bg":
		p.PromptCursorBG = normalizedColor
	case "cursor-fg":
		p.PromptCursorFG = normalizedColor
	case "code-background":
		p.CodeBackground = normalizedColor
	case "code-text":
		p.CodeText = normalizedColor
	case "code-keyword":
		p.CodeKeyword = normalizedColor
	case "code-type":
		p.CodeType = normalizedColor
	case "code-string":
		p.CodeString = normalizedColor
	case "code-number":
		p.CodeNumber = normalizedColor
	case "code-comment":
		p.CodeComment = normalizedColor
	case "code-function":
		p.CodeFunction = normalizedColor
	case "code-operator":
		p.CodeOperator = normalizedColor
	default:
		return p, fmt.Errorf("unknown theme color slot: %s", strings.TrimSpace(slot))
	}
	return p, nil
}

func newBuiltinThemeOption(id, name string, palette ThemePalette) ThemeOption {
	item, err := buildThemeOption(id, name, palette, true)
	if err != nil {
		panic(err)
	}
	return item
}

func buildThemeOption(id, name string, palette ThemePalette, builtin bool) (ThemeOption, error) {
	palette = palette.WithDefaults()
	for _, entry := range []struct {
		label string
		value string
	}{
		{label: "background", value: palette.Background},
		{label: "panel", value: palette.Panel},
		{label: "element", value: palette.Element},
		{label: "border", value: palette.Border},
		{label: "borderActive", value: palette.BorderActive},
		{label: "text", value: palette.Text},
		{label: "textMuted", value: palette.TextMuted},
		{label: "primary", value: palette.Primary},
		{label: "secondary", value: palette.Secondary},
		{label: "accent", value: palette.Accent},
		{label: "success", value: palette.Success},
		{label: "warning", value: palette.Warning},
		{label: "error", value: palette.Error},
		{label: "codeBackground", value: palette.CodeBackground},
		{label: "codeText", value: palette.CodeText},
		{label: "codeKeyword", value: palette.CodeKeyword},
		{label: "codeType", value: palette.CodeType},
		{label: "codeString", value: palette.CodeString},
		{label: "codeNumber", value: palette.CodeNumber},
		{label: "codeComment", value: palette.CodeComment},
		{label: "codeFunction", value: palette.CodeFunction},
		{label: "codeOperator", value: palette.CodeOperator},
		{label: "prompt", value: palette.Prompt},
		{label: "promptCursorBG", value: palette.PromptCursorBG},
		{label: "promptCursorFG", value: palette.PromptCursorFG},
	} {
		if err := validateHexColor(entry.value); err != nil {
			return ThemeOption{}, fmt.Errorf("%s: %w", entry.label, err)
		}
	}

	id = NormalizeThemeID(id)
	if id == "" {
		return ThemeOption{}, fmt.Errorf("theme id must not be empty")
	}

	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = id
	}

	return ThemeOption{
		ID:      id,
		Name:    displayName,
		Palette: palette,
		Builtin: builtin,
	}, nil
}

func normalizeThemePaletteSlot(slot string) string {
	value := strings.ToLower(strings.TrimSpace(slot))
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	switch value {
	case "bg":
		return "background"
	case "borderactive":
		return "border-active"
	case "textmuted":
		return "text-muted"
	case "cursorbg", "prompt-cursor-bg", "promptcursorbg":
		return "cursor-bg"
	case "cursorfg", "prompt-cursor-fg", "promptcursorfg":
		return "cursor-fg"
	case "codebg", "code-bg", "codebackground":
		return "code-background"
	case "codetext":
		return "code-text"
	case "codekeyword":
		return "code-keyword"
	case "codetype":
		return "code-type"
	case "codestring":
		return "code-string"
	case "codenumber":
		return "code-number"
	case "codecomment":
		return "code-comment"
	case "codefunction":
		return "code-function"
	case "codeoperator":
		return "code-operator"
	default:
		return value
	}
}

func normalizeHexColor(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("color is required")
	}
	if !strings.HasPrefix(value, "#") {
		value = "#" + value
	}
	if err := validateHexColor(value); err != nil {
		return "", err
	}
	return "#" + strings.ToUpper(strings.TrimPrefix(value, "#")), nil
}

func validateHexColor(hex string) error {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return fmt.Errorf("invalid hex color: %s", hex)
	}
	if _, err := strconv.ParseUint(h, 16, 24); err != nil {
		return err
	}
	return nil
}
