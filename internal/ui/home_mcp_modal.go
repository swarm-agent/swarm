package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type MCPModalServer struct {
	ID          string
	Name        string
	Transport   string
	URL         string
	Command     string
	Args        []string
	Enabled     bool
	Source      string
	EnvCount    int
	HeaderCount int
	CreatedAt   int64
	UpdatedAt   int64
}

type MCPModalActionKind string

const (
	MCPModalActionRefresh    MCPModalActionKind = "refresh"
	MCPModalActionUpsert     MCPModalActionKind = "upsert"
	MCPModalActionDelete     MCPModalActionKind = "delete"
	MCPModalActionSetEnabled MCPModalActionKind = "set-enabled"
)

type MCPModalUpsert struct {
	ID        string
	Name      string
	Transport string
	URL       string
	Command   string
	Args      []string
	Enabled   *bool
	Source    string
}

type MCPModalAction struct {
	Kind       MCPModalActionKind
	ID         string
	Enabled    bool
	Upsert     *MCPModalUpsert
	StatusHint string
}

type mcpModalFocus int

const (
	mcpModalFocusList mcpModalFocus = iota
	mcpModalFocusSearch
)

type mcpModalEditorField struct {
	Key         string
	Label       string
	Value       string
	Placeholder string
}

type mcpModalEditor struct {
	Mode     string
	Fields   []mcpModalEditorField
	Selected int
}

type mcpModalState struct {
	Visible        bool
	Loading        bool
	Status         string
	Error          string
	Focus          mcpModalFocus
	Search         string
	Servers        []MCPModalServer
	SelectedServer int
	ConfirmDelete  bool
	Editor         *mcpModalEditor
}

func (p *HomePage) ShowMCPModal() {
	p.mcpModal.Visible = true
	if p.mcpModal.Focus < mcpModalFocusList || p.mcpModal.Focus > mcpModalFocusSearch {
		p.mcpModal.Focus = mcpModalFocusList
	}
	p.mcpModal.ConfirmDelete = false
	if strings.TrimSpace(p.mcpModal.Status) == "" {
		p.mcpModal.Status = "n remote • l local • e edit • Enter/t toggle • d delete • r refresh • / search"
	}
}

func (p *HomePage) HideMCPModal() {
	p.mcpModal = mcpModalState{}
	p.pendingMCPAction = nil
}

func (p *HomePage) MCPModalVisible() bool {
	return p.mcpModal.Visible
}

func (p *HomePage) SetMCPModalLoading(loading bool) {
	p.mcpModal.Loading = loading
}

func (p *HomePage) SetMCPModalStatus(status string) {
	p.mcpModal.Status = strings.TrimSpace(status)
	if p.mcpModal.Status != "" {
		p.mcpModal.Error = ""
	}
}

func (p *HomePage) SetMCPModalError(err string) {
	p.mcpModal.Error = strings.TrimSpace(err)
	if p.mcpModal.Error != "" {
		p.mcpModal.Loading = false
	}
}

func (p *HomePage) SetMCPModalData(servers []MCPModalServer) {
	selectedID := p.selectedMCPModalID()
	p.mcpModal.Servers = make([]MCPModalServer, 0, len(servers))
	for _, server := range servers {
		copyServer := server
		copyServer.Args = append([]string(nil), server.Args...)
		p.mcpModal.Servers = append(p.mcpModal.Servers, copyServer)
	}
	p.mcpModal.SelectedServer = p.findMCPModalIndexByID(selectedID)
	p.mcpModal.reconcileSelections()
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) PopMCPModalAction() (MCPModalAction, bool) {
	if p.pendingMCPAction == nil {
		return MCPModalAction{}, false
	}
	action := *p.pendingMCPAction
	p.pendingMCPAction = nil
	return action, true
}

func (p *HomePage) enqueueMCPModalAction(action MCPModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingMCPAction = &action
	p.mcpModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.mcpModal.Status = strings.TrimSpace(action.StatusHint)
	}
	p.mcpModal.Error = ""
}

func (p *HomePage) handleMCPModalKey(ev *tcell.EventKey) {
	if p.mcpModal.Editor != nil {
		p.handleMCPModalEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideMCPModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext):
		p.advanceMCPModalFocus(1)
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalFocusPrev):
		p.advanceMCPModalFocus(-1)
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		p.mcpModal.Focus = mcpModalFocusList
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		p.mcpModal.Focus = mcpModalFocusSearch
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveMCPModalSelection(-1)
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveMCPModalSelection(1)
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.deleteMCPModalSearchRune()
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.clearMCPModalSearch()
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleMCPModalEnter()
		return
	}

	if ev.Key() == tcell.KeyRune {
		p.handleMCPModalRune(ev)
	}
}

func (p *HomePage) handleMCPModalRune(ev *tcell.EventKey) {
	r := ev.Rune()
	if p.mcpModal.Focus == mcpModalFocusSearch {
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			p.mcpModal.Search += string(r)
			p.mcpModal.reconcileSelections()
		}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalSearchFocus):
		p.mcpModal.Focus = mcpModalFocusSearch
		return
	case p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveMCPModalSelection(1)
		p.mcpModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveMCPModalSelection(-1)
		p.mcpModal.ConfirmDelete = false
		return
	}

	switch strings.ToLower(string(r)) {
	case "r":
		p.enqueueMCPModalAction(MCPModalAction{
			Kind:       MCPModalActionRefresh,
			StatusHint: "Refreshing MCP servers...",
		})
		p.mcpModal.ConfirmDelete = false
	case "n":
		p.openMCPModalRemoteEditor(false)
	case "l":
		p.openMCPModalLocalEditor(false)
	case "e":
		p.openMCPModalEditorForSelected()
	case "t":
		p.handleMCPModalEnter()
	case "d":
		p.handleMCPModalDeleteSelected()
	default:
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			p.mcpModal.Focus = mcpModalFocusSearch
			p.mcpModal.Search += string(r)
			p.mcpModal.reconcileSelections()
			p.mcpModal.ConfirmDelete = false
		}
	}
}

func (p *HomePage) handleMCPModalEnter() {
	p.toggleMCPModalSelectedEnabled()
}

func (p *HomePage) handleMCPModalDeleteSelected() {
	server, ok := p.selectedMCPModalServer()
	if !ok {
		p.mcpModal.Status = "No MCP server selected"
		p.mcpModal.Error = ""
		p.mcpModal.ConfirmDelete = false
		return
	}
	if !p.mcpModal.ConfirmDelete {
		p.mcpModal.ConfirmDelete = true
		p.mcpModal.Status = fmt.Sprintf("Press d again to delete %s", strings.TrimSpace(server.ID))
		p.mcpModal.Error = ""
		return
	}
	id := strings.TrimSpace(server.ID)
	p.enqueueMCPModalAction(MCPModalAction{
		Kind:       MCPModalActionDelete,
		ID:         id,
		StatusHint: fmt.Sprintf("Deleting MCP server %s...", id),
	})
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) toggleMCPModalSelectedEnabled() {
	server, ok := p.selectedMCPModalServer()
	if !ok {
		p.mcpModal.Status = "No MCP server selected"
		p.mcpModal.Error = ""
		p.mcpModal.ConfirmDelete = false
		return
	}
	enabled := !server.Enabled
	verb := "Disabling"
	if enabled {
		verb = "Enabling"
	}
	id := strings.TrimSpace(server.ID)
	p.enqueueMCPModalAction(MCPModalAction{
		Kind:       MCPModalActionSetEnabled,
		ID:         id,
		Enabled:    enabled,
		StatusHint: fmt.Sprintf("%s MCP server %s...", verb, id),
	})
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) openMCPModalEditorForSelected() {
	server, ok := p.selectedMCPModalServer()
	if !ok {
		p.mcpModal.Status = "No MCP server selected"
		p.mcpModal.Error = ""
		return
	}
	if strings.EqualFold(strings.TrimSpace(server.Transport), "stdio") {
		p.openMCPModalLocalEditor(true)
		return
	}
	p.openMCPModalRemoteEditor(true)
}

func (p *HomePage) openMCPModalRemoteEditor(prefillSelected bool) {
	source := "user"
	id := ""
	name := ""
	url := ""
	enabled := "y"
	mode := "remote"
	if prefillSelected {
		if server, ok := p.selectedMCPModalServer(); ok {
			id = strings.TrimSpace(server.ID)
			name = strings.TrimSpace(server.Name)
			url = strings.TrimSpace(server.URL)
			source = strings.TrimSpace(server.Source)
			enabled = boolYN(server.Enabled)
			mode = "edit-remote"
		}
	}
	if strings.TrimSpace(source) == "" {
		source = "user"
	}
	p.mcpModal.Editor = &mcpModalEditor{
		Mode: mode,
		Fields: []mcpModalEditorField{
			{Key: "id", Label: "ID", Value: id, Placeholder: "server-id"},
			{Key: "name", Label: "Name", Value: name, Placeholder: "Display name (optional)"},
			{Key: "url", Label: "URL", Value: url, Placeholder: "https://example.com/mcp"},
			{Key: "enabled", Label: "Enabled (y/n)", Value: enabled, Placeholder: "y"},
			{Key: "source", Label: "Source", Value: source, Placeholder: "user"},
		},
	}
	p.mcpModal.Status = "Fill remote server fields and press Enter on last field to save"
	p.mcpModal.Error = ""
	p.mcpModal.Loading = false
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) openMCPModalLocalEditor(prefillSelected bool) {
	source := "user"
	id := ""
	name := ""
	command := ""
	args := ""
	enabled := "y"
	mode := "local"
	if prefillSelected {
		if server, ok := p.selectedMCPModalServer(); ok {
			id = strings.TrimSpace(server.ID)
			name = strings.TrimSpace(server.Name)
			command = strings.TrimSpace(server.Command)
			if len(server.Args) > 0 {
				args = strings.TrimSpace(strings.Join(server.Args, " "))
			}
			source = strings.TrimSpace(server.Source)
			enabled = boolYN(server.Enabled)
			mode = "edit-local"
		}
	}
	if strings.TrimSpace(source) == "" {
		source = "user"
	}
	p.mcpModal.Editor = &mcpModalEditor{
		Mode: mode,
		Fields: []mcpModalEditorField{
			{Key: "id", Label: "ID", Value: id, Placeholder: "server-id"},
			{Key: "name", Label: "Name", Value: name, Placeholder: "Display name (optional)"},
			{Key: "command", Label: "Command", Value: command, Placeholder: "npx"},
			{Key: "args", Label: "Args", Value: args, Placeholder: "@modelcontextprotocol/server-filesystem ."},
			{Key: "enabled", Label: "Enabled (y/n)", Value: enabled, Placeholder: "y"},
			{Key: "source", Label: "Source", Value: source, Placeholder: "user"},
		},
	}
	p.mcpModal.Status = "Fill local server fields and press Enter on last field to save"
	p.mcpModal.Error = ""
	p.mcpModal.Loading = false
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) handleMCPModalEditorKey(ev *tcell.EventKey) {
	editor := p.mcpModal.Editor
	if editor == nil {
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.mcpModal.Editor = nil
		p.mcpModal.Status = "Editor closed"
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown):
		editor.Selected = (editor.Selected + 1) % len(editor.Fields)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp):
		editor.Selected = (editor.Selected - 1 + len(editor.Fields)) % len(editor.Fields)
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		field := &editor.Fields[editor.Selected]
		if len(field.Value) > 0 {
			_, sz := utf8.DecodeLastRuneInString(field.Value)
			if sz > 0 {
				field.Value = field.Value[:len(field.Value)-sz]
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		editor.Fields[editor.Selected].Value = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if editor.Selected < len(editor.Fields)-1 {
			editor.Selected++
			return
		}
		p.submitMCPModalEditor()
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if unicode.IsPrint(r) {
			editor.Fields[editor.Selected].Value += string(r)
		}
	}
}

func (p *HomePage) submitMCPModalEditor() {
	editor := p.mcpModal.Editor
	if editor == nil {
		return
	}

	get := func(key string) string {
		for _, field := range editor.Fields {
			if field.Key == key {
				return strings.TrimSpace(field.Value)
			}
		}
		return ""
	}

	id := strings.TrimSpace(get("id"))
	if id == "" {
		p.mcpModal.Error = "MCP server ID is required"
		return
	}
	name := strings.TrimSpace(get("name"))
	source := strings.TrimSpace(get("source"))
	if source == "" {
		source = "user"
	}
	enabledValue := parseYN(get("enabled"))
	enabled := &enabledValue

	upsert := &MCPModalUpsert{
		ID:      id,
		Name:    name,
		Enabled: enabled,
		Source:  source,
	}

	if strings.Contains(editor.Mode, "local") {
		command := strings.TrimSpace(get("command"))
		if command == "" {
			p.mcpModal.Error = "Command is required for local MCP server"
			return
		}
		upsert.Transport = "stdio"
		upsert.Command = command
		args := strings.TrimSpace(get("args"))
		if args != "" {
			upsert.Args = append(upsert.Args, strings.Fields(args)...)
		}
	} else {
		url := strings.TrimSpace(get("url"))
		if url == "" {
			p.mcpModal.Error = "URL is required for remote MCP server"
			return
		}
		upsert.Transport = "http"
		upsert.URL = url
	}

	p.mcpModal.Editor = nil
	p.enqueueMCPModalAction(MCPModalAction{
		Kind:       MCPModalActionUpsert,
		Upsert:     upsert,
		StatusHint: fmt.Sprintf("Saving MCP server %s...", id),
	})
	p.mcpModal.ConfirmDelete = false
}

func (p *HomePage) selectedMCPModalServer() (MCPModalServer, bool) {
	idx := p.mcpModal.SelectedServer
	if idx < 0 || idx >= len(p.mcpModal.Servers) {
		return MCPModalServer{}, false
	}
	return p.mcpModal.Servers[idx], true
}

func (p *HomePage) selectedMCPModalID() string {
	server, ok := p.selectedMCPModalServer()
	if !ok {
		return ""
	}
	return strings.TrimSpace(server.ID)
}

func (p *HomePage) findMCPModalIndexByID(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for i, server := range p.mcpModal.Servers {
		if strings.EqualFold(strings.TrimSpace(server.ID), id) {
			return i
		}
	}
	return -1
}

func (p *HomePage) advanceMCPModalFocus(delta int) {
	order := []mcpModalFocus{mcpModalFocusList, mcpModalFocusSearch}
	idx := 0
	for i, focus := range order {
		if focus == p.mcpModal.Focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	p.mcpModal.Focus = order[idx]
}

func (p *HomePage) moveMCPModalSelection(delta int) {
	if delta == 0 {
		return
	}
	if p.mcpModal.Focus != mcpModalFocusList {
		return
	}
	matches := p.mcpFilteredIndexes()
	if len(matches) == 0 {
		return
	}
	current := p.mcpModal.SelectedServer
	pos := indexInList(matches, current)
	if pos < 0 {
		pos = 0
	}
	pos = (pos + delta + len(matches)) % len(matches)
	p.mcpModal.SelectedServer = matches[pos]
}

func (p *HomePage) deleteMCPModalSearchRune() {
	if p.mcpModal.Focus != mcpModalFocusSearch {
		return
	}
	if len(p.mcpModal.Search) == 0 {
		return
	}
	_, sz := utf8.DecodeLastRuneInString(p.mcpModal.Search)
	if sz > 0 {
		p.mcpModal.Search = p.mcpModal.Search[:len(p.mcpModal.Search)-sz]
	}
	p.mcpModal.reconcileSelections()
}

func (p *HomePage) clearMCPModalSearch() {
	p.mcpModal.Search = ""
	p.mcpModal.reconcileSelections()
}

func (s *mcpModalState) reconcileSelections() {
	matches := s.filteredIndexes()
	if len(matches) == 0 {
		s.SelectedServer = -1
		return
	}
	if indexInList(matches, s.SelectedServer) < 0 {
		s.SelectedServer = matches[0]
	}
}

func (p *HomePage) mcpFilteredIndexes() []int {
	return p.mcpModal.filteredIndexes()
}

func (s *mcpModalState) filteredIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(s.Search))
	out := make([]int, 0, len(s.Servers))
	for i, server := range s.Servers {
		if query != "" && !mcpModalMatchesQuery(server, query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func mcpModalMatchesQuery(server MCPModalServer, query string) bool {
	if query == "" {
		return true
	}
	target := strings.ToLower(strings.Join([]string{
		server.ID,
		server.Name,
		server.Transport,
		server.URL,
		server.Command,
		strings.Join(server.Args, " "),
		server.Source,
	}, " "))
	for _, token := range strings.Fields(query) {
		if !strings.Contains(target, strings.ToLower(token)) {
			return false
		}
	}
	return true
}

func (p *HomePage) drawMCPModal(s tcell.Screen) {
	if !p.mcpModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := w - 8
	if modalW > 120 {
		modalW = 120
	}
	if modalW < 76 {
		modalW = w - 2
	}
	modalH := h - 6
	if modalH > 30 {
		modalH = 30
	}
	if modalH < 18 {
		modalH = h - 2
	}
	rect := Rect{
		X: maxInt(1, (w-modalW)/2),
		Y: maxInt(1, (h-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	title := "MCP Servers"
	if p.mcpModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	statusStyle := p.theme.TextMuted
	status := strings.TrimSpace(p.mcpModal.Status)
	if strings.TrimSpace(p.mcpModal.Error) != "" {
		status = strings.TrimSpace(p.mcpModal.Error)
		statusStyle = p.theme.Error
	}
	if status == "" {
		status = "n remote • l local • e edit • Enter/t toggle • d delete • r refresh • / search"
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(status, rect.W-4))

	searchFocus := ""
	if p.mcpModal.Focus == mcpModalFocusSearch {
		searchFocus = " [edit]"
	}
	searchLine := "search" + searchFocus + ": " + p.mcpModal.Search
	DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.TextMuted, clampEllipsis(searchLine, rect.W-4))

	body := Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6}
	if body.W < 24 || body.H < 5 {
		return
	}
	compactLayout := body.W < 74 || body.H < 8
	if compactLayout {
		if p.mcpModal.Editor != nil || p.mcpModal.Focus == mcpModalFocusSearch {
			p.drawMCPModalDetailPane(s, body)
		} else {
			p.drawMCPModalListPane(s, body)
		}
	} else {
		listW := 52
		if listW > body.W/2+10 {
			listW = body.W / 2
		}
		if listW < 36 {
			listW = 36
		}
		listRect := Rect{X: body.X, Y: body.Y, W: listW, H: body.H}
		detailRect := Rect{X: listRect.X + listRect.W + 1, Y: body.Y, W: body.W - listRect.W - 1, H: body.H}
		if detailRect.W < 28 {
			detailRect.W = 28
		}

		p.drawMCPModalListPane(s, listRect)
		p.drawMCPModalDetailPane(s, detailRect)
	}

	help := "Tab focus • ↑/↓ select • Enter/t toggle • d delete • n/l add • e edit • r refresh • Esc close"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(help, rect.W-4))
	if p.mcpModal.ConfirmDelete {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.Warning, "Delete armed: press d again to confirm")
	} else {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "Use source=user for personal MCP server entries")
	}

	if p.mcpModal.Editor != nil {
		p.drawMCPModalEditor(s, rect)
	}
}

func (p *HomePage) drawMCPModalListPane(s tcell.Screen, rect Rect) {
	border := p.theme.Border
	header := "Servers"
	if p.mcpModal.Focus == mcpModalFocusList {
		border = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, border)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	matches := p.mcpFilteredIndexes()
	rowY := rect.Y + 1
	rows := rect.H - 2
	for i := 0; i < rows && i < len(matches); i++ {
		idx := matches[i]
		server := p.mcpModal.Servers[idx]
		prefix := "  "
		if idx == p.mcpModal.SelectedServer {
			prefix = "> "
		}
		state := "off"
		stateStyle := p.theme.Warning
		if server.Enabled {
			state = "on"
			stateStyle = p.theme.Success
		}
		name := strings.TrimSpace(server.Name)
		if name == "" {
			name = strings.TrimSpace(server.ID)
		}
		line := fmt.Sprintf("%s%s [%s] %s", prefix, name, strings.TrimSpace(server.Transport), strings.TrimSpace(server.ID))
		DrawText(s, rect.X+1, rowY, rect.W-1, p.theme.Text, clampEllipsis(line, rect.W-8))
		DrawTextRight(s, rect.X+rect.W-2, rowY, 6, stateStyle, state)
		rowY++
	}
	if len(matches) == 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no mcp servers")
	}
}

func (p *HomePage) drawMCPModalDetailPane(s tcell.Screen, rect Rect) {
	DrawBox(s, rect, p.theme.Border)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, "Details")

	rowY := rect.Y + 1
	server, ok := p.selectedMCPModalServer()
	if !ok {
		help := strings.TrimSpace(p.mcpModal.Status)
		if help == "" {
			help = "Select a server to inspect or edit it."
		}
		for _, line := range Wrap(help, rect.W-4) {
			if rowY >= rect.Y+rect.H-1 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, clampEllipsis(line, rect.W-4))
			rowY++
		}
		return
	}
	state := "disabled"
	if server.Enabled {
		state = "enabled"
	}
	lines := []string{
		"id: " + strings.TrimSpace(server.ID),
		"name: " + emptyStringFallback(strings.TrimSpace(server.Name), "-"),
		"transport: " + strings.TrimSpace(server.Transport),
		"state: " + state,
		"source: " + emptyStringFallback(strings.TrimSpace(server.Source), "-"),
		"target: " + mcpServerTarget(server),
		fmt.Sprintf("args/env/headers: %d / %d / %d", len(server.Args), server.EnvCount, server.HeaderCount),
		"updated: " + mcpModalTimeLabel(server.UpdatedAt),
		"created: " + mcpModalTimeLabel(server.CreatedAt),
	}
	for _, line := range lines {
		if rowY >= rect.Y+rect.H-8 {
			break
		}
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.Text, clampEllipsis(line, rect.W-4))
		rowY++
	}

	if rowY < rect.Y+rect.H-7 {
		rowY++
	}
	actionLines := []string{
		"n: add remote (http)",
		"l: add local (stdio)",
		"e: edit selected",
		"Enter/t: enable/disable selected",
		"d: delete selected",
		"r: refresh",
	}
	for _, line := range actionLines {
		if rowY >= rect.Y+rect.H-1 {
			break
		}
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, clampEllipsis(line, rect.W-4))
		rowY++
	}
}

func (p *HomePage) drawMCPModalEditor(s tcell.Screen, parent Rect) {
	editor := p.mcpModal.Editor
	if editor == nil {
		return
	}
	height := len(editor.Fields) + 4
	if height < 9 {
		height = 9
	}
	width := parent.W - 12
	if width > 96 {
		width = 96
	}
	if width < 46 {
		width = parent.W - 4
	}
	rect := Rect{
		X: parent.X + (parent.W-width)/2,
		Y: parent.Y + (parent.H-height)/2,
		W: width,
		H: height,
	}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	title := "MCP Server"
	if strings.Contains(editor.Mode, "local") {
		title = "MCP Local Server (stdio)"
	} else {
		title = "MCP Remote Server (http)"
	}
	if strings.Contains(editor.Mode, "edit") {
		title = "Edit " + title
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	rowY := rect.Y + 1
	for i, field := range editor.Fields {
		if rowY >= rect.Y+rect.H-2 {
			break
		}
		style := p.theme.TextMuted
		value := field.Value
		if strings.TrimSpace(value) == "" {
			value = field.Placeholder
			if value == "" {
				value = "-"
			}
		} else {
			style = p.theme.Text
		}
		prefix := "  "
		if i == editor.Selected {
			prefix = "> "
			style = p.theme.Text
		}
		line := fmt.Sprintf("%s%s: %s", prefix, field.Label, value)
		for _, wrapped := range Wrap(line, maxInt(1, rect.W-4)) {
			if rowY >= rect.Y+rect.H-2 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(wrapped, rect.W-4))
			rowY++
		}
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, "Tab/↑/↓ move • Enter next/save • Esc cancel")
}

func boolYN(value bool) string {
	if value {
		return "y"
	}
	return "n"
}

func mcpServerTarget(server MCPModalServer) string {
	if target := strings.TrimSpace(server.URL); target != "" {
		return target
	}
	target := strings.TrimSpace(server.Command)
	if target == "" {
		return "-"
	}
	if len(server.Args) == 0 {
		return target
	}
	return target + " " + strings.Join(server.Args, " ")
}

func mcpModalTimeLabel(unixMillis int64) string {
	if unixMillis <= 0 {
		return "-"
	}
	return time.UnixMilli(unixMillis).Local().Format("2006-01-02 15:04")
}
