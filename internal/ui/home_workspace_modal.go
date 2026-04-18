package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type WorkspaceModalWorkspace struct {
	Name           string
	Path           string
	ThemeID        string
	Directories    []string
	SortIndex      int
	Active         bool
	AddedAt        int64
	UpdatedAt      int64
	LastSelectedAt int64
}

type WorkspaceModalActionKind string

const (
	WorkspaceModalActionRefresh         WorkspaceModalActionKind = "refresh"
	WorkspaceModalActionSave            WorkspaceModalActionKind = "save"
	WorkspaceModalActionSelect          WorkspaceModalActionKind = "select"
	WorkspaceModalActionDelete          WorkspaceModalActionKind = "delete"
	WorkspaceModalActionMove            WorkspaceModalActionKind = "move"
	WorkspaceModalActionAddDirectory    WorkspaceModalActionKind = "add_directory"
	WorkspaceModalActionRemoveDirectory WorkspaceModalActionKind = "remove_directory"
	WorkspaceModalActionOpenKeybinds    WorkspaceModalActionKind = "open_keybinds"
)

type WorkspaceModalAction struct {
	Kind            WorkspaceModalActionKind
	Path            string
	Name            string
	ThemeID         string
	MakeCurrent     bool
	Delta           int
	DirectoryPath   string
	LinkedDirectory string
	StatusHint      string
}

type workspaceModalDetailActionID string

const (
	workspaceModalDetailActionAddDirectory    workspaceModalDetailActionID = "add_directory"
	workspaceModalDetailActionUnlinkDirectory workspaceModalDetailActionID = "unlink_directory"
	workspaceModalDetailActionActivate        workspaceModalDetailActionID = "activate"
	workspaceModalDetailActionEdit            workspaceModalDetailActionID = "edit"
	workspaceModalDetailActionRemoveDirectory workspaceModalDetailActionID = "remove_directory"
	workspaceModalDetailActionMoveUp          workspaceModalDetailActionID = "move_up"
	workspaceModalDetailActionMoveDown        workspaceModalDetailActionID = "move_down"
	workspaceModalDetailActionDelete          workspaceModalDetailActionID = "delete"
	workspaceModalDetailActionSaveCurrent     workspaceModalDetailActionID = "save_current"
	workspaceModalDetailActionNew             workspaceModalDetailActionID = "new"
	workspaceModalDetailActionOpenKeybinds    workspaceModalDetailActionID = "open_keybinds"
	workspaceModalDetailActionRefresh         workspaceModalDetailActionID = "refresh"
)

type workspaceModalDetailAction struct {
	ID            workspaceModalDetailActionID
	Label         string
	Hint          string
	Shortcut      string
	Enabled       bool
	DirectoryPath string
}

type workspaceModalFocus int

const (
	workspaceModalFocusList workspaceModalFocus = iota
	workspaceModalFocusDetails
	workspaceModalFocusSearch
)

type workspaceModalEditor struct {
	Mode                string
	WorkspacePath       string
	Fields              []workspaceModalEditorField
	Selected            int
	SuggestionIndex     int
	ThemePickerVisible  bool
	ThemePickerSelected int
}

type workspaceModalEditorField struct {
	Key         string
	Label       string
	Value       string
	Placeholder string
	Editable    bool
	Options     []string
	Help        string
}

type workspaceModalState struct {
	Visible           bool
	Loading           bool
	Status            string
	Error             string
	Intent            string
	Focus             workspaceModalFocus
	Search            string
	Workspaces        []WorkspaceModalWorkspace
	SelectedWorkspace int
	SelectedAction    int
	ConfirmDelete     bool
	Editor            *workspaceModalEditor
	DirectoryPath     string
	AddDirectoryPath  string
	ActionMenuVisible bool
	CardColumns       int
}

func (p *HomePage) ShowWorkspaceModal() {
	p.workspaceModal.Visible = true
	p.workspaceModal.Focus = workspaceModalFocusList
	p.workspaceModal.Search = ""
	p.workspaceModal.reconcileSelections()
	p.workspaceModal.ConfirmDelete = false
	p.workspaceModal.Editor = nil
	p.workspaceModal.ActionMenuVisible = false
}

func (p *HomePage) HideWorkspaceModal() {
	p.workspaceModal = workspaceModalState{}
	p.pendingWorkspaceAction = nil
}

func (p *HomePage) WorkspaceModalVisible() bool {
	return p.workspaceModal.Visible
}

func (p *HomePage) SetWorkspaceModalLoading(loading bool) {
	p.workspaceModal.Loading = loading
}

func (p *HomePage) SetWorkspaceModalStatus(status string) {
	p.workspaceModal.Status = strings.TrimSpace(status)
	if p.workspaceModal.Status != "" {
		p.workspaceModal.Error = ""
	}
}

func (p *HomePage) SetWorkspaceModalError(err string) {
	p.workspaceModal.Error = strings.TrimSpace(err)
	if p.workspaceModal.Error != "" {
		p.workspaceModal.Loading = false
	}
}

func (p *HomePage) SetWorkspaceModalDirectory(path string) {
	p.workspaceModal.DirectoryPath = strings.TrimSpace(path)
}

func (p *HomePage) SetWorkspaceModalIntent(intent, addDirectoryPath string) {
	p.workspaceModal.Intent = strings.TrimSpace(intent)
	p.workspaceModal.AddDirectoryPath = strings.TrimSpace(addDirectoryPath)
}

func (p *HomePage) WorkspaceModalIntent() string {
	return strings.TrimSpace(p.workspaceModal.Intent)
}

func (p *HomePage) SetWorkspaceModalData(entries []WorkspaceModalWorkspace) {
	selectedPath := p.selectedWorkspaceModalPath()
	p.workspaceModal.Workspaces = append([]WorkspaceModalWorkspace(nil), entries...)
	p.workspaceModal.SelectedWorkspace = p.findWorkspaceModalIndexByPath(selectedPath)
	if p.workspaceModal.SelectedWorkspace < 0 {
		for i, workspace := range p.workspaceModal.Workspaces {
			if workspace.Active {
				p.workspaceModal.SelectedWorkspace = i
				break
			}
		}
	}
	p.workspaceModal.reconcileSelections()
	p.reconcileWorkspaceModalActionSelection(false)
}

func (p *HomePage) OpenWorkspaceModalSaveEditor(path string, allowPathEdit bool) {
	p.openWorkspaceModalSaveEditorForPath(path, allowPathEdit, "")
}

func (p *HomePage) OpenWorkspaceModalSaveAndLinkEditor(path, linkedDirectory string, allowPathEdit bool) {
	p.openWorkspaceModalSaveEditorForPath(path, allowPathEdit, linkedDirectory)
}

func (p *HomePage) OpenWorkspaceModalAddDirectoryEditor(workspacePath, directoryPath string) {
	if workspace, ok := p.workspaceByPath(workspacePath); ok {
		p.openWorkspaceModalAddDirectoryEditorForWorkspace(workspace, directoryPath)
	}
}

func (p *HomePage) OpenWorkspaceModalRemoveDirectoryEditor(workspacePath string) {
	if workspace, ok := p.workspaceByPath(workspacePath); ok {
		p.openWorkspaceModalRemoveDirectoryEditorForWorkspace(workspace)
	}
}

func (p *HomePage) PopWorkspaceModalAction() (WorkspaceModalAction, bool) {
	if p.pendingWorkspaceAction == nil {
		return WorkspaceModalAction{}, false
	}
	action := *p.pendingWorkspaceAction
	p.pendingWorkspaceAction = nil
	return action, true
}

func (p *HomePage) handleWorkspaceModalKey(ev *tcell.EventKey) {
	if p.workspaceModal.Editor != nil {
		p.handleWorkspaceModalEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		if p.workspaceModal.ActionMenuVisible {
			p.workspaceModal.ActionMenuVisible = false
			p.workspaceModal.Focus = workspaceModalFocusList
			p.workspaceModal.ConfirmDelete = false
			return
		}
		p.HideWorkspaceModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext), p.keybinds.Match(ev, KeybindModalFocusPrev):
		if p.workspaceModal.ActionMenuVisible {
			p.workspaceModal.Focus = workspaceModalFocusDetails
			p.reconcileWorkspaceModalActionSelection(false)
		} else {
			p.workspaceModal.Focus = workspaceModalFocusList
		}
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		if p.workspaceModal.ActionMenuVisible {
			p.workspaceModal.ConfirmDelete = false
			p.workspaceModal.ActionMenuVisible = false
			p.workspaceModal.Focus = workspaceModalFocusList
			return
		}
		p.workspaceModal.Focus = workspaceModalFocusList
		p.moveWorkspaceModalSelectionByDirection(-1, 0)
		p.workspaceModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		if p.workspaceModal.ActionMenuVisible {
			p.workspaceModal.Focus = workspaceModalFocusDetails
			p.reconcileWorkspaceModalActionSelection(false)
			return
		}
		p.workspaceModal.Focus = workspaceModalFocusList
		p.moveWorkspaceModalSelectionByDirection(1, 0)
		p.workspaceModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveWorkspaceModalSelectionByDirection(0, -1)
		p.workspaceModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveWorkspaceModalSelectionByDirection(0, 1)
		p.workspaceModal.ConfirmDelete = false
		return
	case ev.Key() == tcell.KeyLeft:
		p.moveWorkspaceModalSelectionByDirection(-1, 0)
		p.workspaceModal.ConfirmDelete = false
		return
	case ev.Key() == tcell.KeyRight:
		p.moveWorkspaceModalSelectionByDirection(1, 0)
		p.workspaceModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleWorkspaceModalEnter()
		return
	}

	if ev.Key() == tcell.KeyRune {
		p.handleWorkspaceModalRune(ev)
	}
}

func (p *HomePage) handleWorkspaceModalRune(ev *tcell.EventKey) {
	r := ev.Rune()

	switch {
	case p.keybinds.Match(ev, KeybindWorkspaceFocusList):
		p.workspaceModal.Focus = workspaceModalFocusList
		p.workspaceModal.ActionMenuVisible = false
	case p.keybinds.Match(ev, KeybindWorkspaceRefresh):
		p.workspaceModalRefresh()
	case p.keybinds.Match(ev, KeybindWorkspaceSaveCurrent):
		p.workspaceModalSaveCurrent()
	case p.keybinds.Match(ev, KeybindWorkspaceNew):
		p.workspaceModalNew()
	case p.keybinds.Match(ev, KeybindWorkspaceActivate):
		p.workspaceModalActivateSelected()
	case p.keybinds.Match(ev, KeybindWorkspaceEdit):
		p.workspaceModalEditSelected()
	case p.keybinds.Match(ev, KeybindWorkspaceLinkDirectory):
		p.workspaceModalAddDirectorySelected()
	case p.keybinds.Match(ev, KeybindWorkspaceUnlinkDirectory):
		p.workspaceModalRemoveDirectorySelected()
	case p.keybinds.Match(ev, KeybindWorkspaceDelete):
		p.workspaceModalDeleteSelected()
	case p.keybinds.Match(ev, KeybindWorkspaceMoveUp):
		p.moveSelectedWorkspace(-1)
	case p.keybinds.Match(ev, KeybindWorkspaceMoveDown):
		p.moveSelectedWorkspace(1)
	case p.keybinds.Match(ev, KeybindWorkspaceOpenKeybinds):
		p.workspaceModalOpenKeybinds()
	case p.keybinds.Match(ev, KeybindModalSearchFocus), p.keybinds.Match(ev, KeybindWorkspaceClearSearchAlt):
		// Search removed from workspace modal.
	default:
		_ = r
	}
}

func (p *HomePage) handleWorkspaceModalEnter() {
	if p.workspaceModal.ActionMenuVisible {
		p.workspaceModal.Focus = workspaceModalFocusDetails
		p.executeWorkspaceModalSelectedAction()
		return
	}
	if _, ok := p.selectedWorkspaceModal(); !ok {
		p.workspaceModal.Status = "Select a workspace first"
		return
	}
	p.workspaceModalEditSelected()
}

func (p *HomePage) workspaceModalRefresh() {
	p.workspaceModal.ActionMenuVisible = false
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:       WorkspaceModalActionRefresh,
		StatusHint: "Refreshing workspaces...",
	})
}

func (p *HomePage) workspaceModalSaveCurrent() {
	p.workspaceModal.ActionMenuVisible = false
	if p.WorkspaceModalIntent() == "add_dir" {
		p.openWorkspaceModalSaveEditorForPath(p.currentWorkspaceModalDirectoryPath(), false, p.currentWorkspaceModalAddDirectoryPath())
		return
	}
	p.openWorkspaceModalSaveEditorForPath(p.currentWorkspaceModalDirectoryPath(), false, "")
}

func (p *HomePage) workspaceModalNew() {
	p.workspaceModal.ActionMenuVisible = false
	if p.WorkspaceModalIntent() == "add_dir" {
		p.openWorkspaceModalSaveEditorForPath("", true, p.currentWorkspaceModalAddDirectoryPath())
		return
	}
	p.openWorkspaceModalSaveEditorForPath("", true, "")
}

func (p *HomePage) workspaceModalActivateSelected() {
	p.workspaceModal.ActionMenuVisible = false
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "No workspace selected"
		return
	}
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:       WorkspaceModalActionSelect,
		Path:       workspace.Path,
		StatusHint: fmt.Sprintf("Activating workspace %s ...", workspace.Name),
	})
}

func (p *HomePage) workspaceModalEditSelected() {
	p.workspaceModal.ActionMenuVisible = false
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "No workspace selected"
		return
	}
	p.openWorkspaceModalEditEditor(workspace)
}

func (p *HomePage) workspaceModalAddDirectorySelected() {
	p.workspaceModal.ActionMenuVisible = false
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "Select or create a workspace first"
		return
	}
	p.openWorkspaceModalAddDirectoryEditorForWorkspace(workspace, p.currentWorkspaceModalAddDirectoryPath())
}

func (p *HomePage) workspaceModalUnlinkDirectory(directoryPath string) {
	p.workspaceModal.ActionMenuVisible = false
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "No workspace selected"
		return
	}
	directoryPath = strings.TrimSpace(directoryPath)
	if directoryPath == "" {
		p.openWorkspaceModalRemoveDirectoryEditorForWorkspace(workspace)
		return
	}
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:          WorkspaceModalActionRemoveDirectory,
		Path:          workspace.Path,
		DirectoryPath: directoryPath,
		StatusHint:    fmt.Sprintf("Removing linked directory %s ...", workspaceModalDisplayPath(directoryPath)),
	})
}

func (p *HomePage) workspaceModalRemoveDirectorySelected() {
	p.workspaceModalUnlinkDirectory("")
}

func (p *HomePage) workspaceModalDeleteSelected() {
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "No workspace selected"
		return
	}
	if !p.workspaceModal.ConfirmDelete {
		p.workspaceModal.ActionMenuVisible = true
		p.workspaceModal.ConfirmDelete = true
		deleteLabel := p.workspaceModalKeyLabel(KeybindWorkspaceDelete)
		if deleteLabel == "" {
			deleteLabel = "the delete keybind"
		}
		p.workspaceModal.Status = fmt.Sprintf("Press %s again to delete %s", deleteLabel, workspace.Path)
		return
	}
	p.workspaceModal.ActionMenuVisible = false
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:       WorkspaceModalActionDelete,
		Path:       workspace.Path,
		StatusHint: fmt.Sprintf("Deleting workspace %s ...", workspace.Path),
	})
	p.workspaceModal.ConfirmDelete = false
}

func (p *HomePage) workspaceModalOpenKeybinds() {
	p.workspaceModal.ActionMenuVisible = false
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:       WorkspaceModalActionOpenKeybinds,
		StatusHint: "Opening keybinds...",
	})
}

func (p *HomePage) openWorkspaceModalSaveEditorForPath(path string, allowPathEdit bool, linkedDirectory string) {
	path = strings.TrimSpace(path)
	if path == "" && !allowPathEdit {
		path = p.currentWorkspaceModalDirectoryPath()
	}
	linkedDirectory = strings.TrimSpace(linkedDirectory)
	existing, hasExisting := p.workspaceByPath(path)
	name := workspaceModalDefaultName(path)
	themeID := ""
	makeCurrent := true
	submitLabel := p.workspaceModalEditorKeyLabel(KeybindEditorSubmit, "Enter")
	tabLabel := p.workspaceModalEditorKeyLabel(KeybindEditorFocusNext, "Tab")
	title := "Create Workspace"
	status := fmt.Sprintf("Choose workspace settings, then press %s to save", submitLabel)
	if hasExisting {
		if trimmed := strings.TrimSpace(existing.Name); trimmed != "" {
			name = trimmed
		}
		if trimmed := strings.TrimSpace(existing.ThemeID); trimmed != "" {
			themeID = trimmed
		}
		makeCurrent = existing.Active
		title = "Edit Workspace"
		status = fmt.Sprintf("Editing %s. Press %s on the last field to save changes.", existing.Path, submitLabel)
	}
	fields := []workspaceModalEditorField{
		{
			Key:         "path",
			Label:       "Path",
			Value:       path,
			Placeholder: "/abs/path",
			Editable:    allowPathEdit,
			Help:        "Primary workspace directory",
		},
		{
			Key:         "name",
			Label:       "Workspace Name",
			Value:       name,
			Placeholder: "workspace",
			Editable:    true,
		},
		{
			Key:      "theme_id",
			Label:    "Theme",
			Value:    workspaceModalNormalizeThemeID(themeID),
			Options:  workspaceModalThemeOptions(),
			Editable: false,
			Help:     "Workspace-specific theme override. Global theme remains the fallback.",
		},
		{
			Key:      "active",
			Label:    "Set Active",
			Value:    workspaceModalBoolValue(makeCurrent),
			Options:  []string{"yes", "no"},
			Editable: false,
		},
	}
	if linkedDirectory != "" {
		fields = append(fields, workspaceModalEditorField{
			Key:         "linked_directory",
			Label:       "Directory to Link",
			Value:       linkedDirectory,
			Placeholder: "~/",
			Editable:    true,
			Help:        fmt.Sprintf("Optional linked directory. %s fills a suggestion; %s on this field saves the workspace and links it.", tabLabel, submitLabel),
		})
		if hasExisting {
			title = "Edit Workspace + Link Directory"
			status = fmt.Sprintf("Editing %s. Press %s on the last field to save changes and link the directory.", existing.Path, submitLabel)
		} else {
			title = "Create Workspace + Link Directory"
			status = fmt.Sprintf("Create the workspace. Press %s on the last field to save it and link the directory.", submitLabel)
		}
	}
	p.workspaceModal.Editor = &workspaceModalEditor{
		Mode:     strings.ToLower(strings.TrimSpace(title)),
		Fields:   fields,
		Selected: workspaceModalInitialEditorIndex(fields),
	}
	p.workspaceModal.Status = status
	p.workspaceModal.Error = ""
	p.workspaceModal.ConfirmDelete = false
}

func (p *HomePage) openWorkspaceModalEditEditor(workspace WorkspaceModalWorkspace) {
	p.openWorkspaceModalSaveEditorForPath(workspace.Path, false, "")
	editor := p.workspaceModal.Editor
	if editor == nil {
		return
	}
	choices := removableWorkspaceModalDirectories(workspace)
	if len(choices) == 0 {
		return
	}
	editor.WorkspacePath = workspace.Path
	editor.Fields = append(editor.Fields, workspaceModalEditorField{
		Key:      "remove_directory",
		Label:    "Unlink Folder",
		Value:    choices[0],
		Options:  choices,
		Editable: false,
		Help:     "Choose a linked folder here, then press u to unlink it.",
	})
	p.workspaceModal.Status = fmt.Sprintf("Editing %s. Move to Unlink Folder and press Enter to delink, or save changes normally.", workspace.Path)
}

func (p *HomePage) openWorkspaceModalAddDirectoryEditorForWorkspace(workspace WorkspaceModalWorkspace, directoryPath string) {
	directoryPath = strings.TrimSpace(directoryPath)
	if directoryPath == "" {
		directoryPath = "~/"
	}
	submitLabel := p.workspaceModalEditorKeyLabel(KeybindEditorSubmit, "Enter")
	tabLabel := p.workspaceModalEditorKeyLabel(KeybindEditorFocusNext, "Tab")
	closeLabel := p.workspaceModalEditorKeyLabel(KeybindEditorClose, "Esc")
	fields := []workspaceModalEditorField{
		{
			Key:      "directory_path",
			Label:    "Directory Path",
			Value:    directoryPath,
			Editable: true,
			Help:     fmt.Sprintf("Type under ~/ or paste an absolute directory path. %s fills a suggestion; %s links it.", tabLabel, submitLabel),
		},
	}
	p.workspaceModal.Editor = &workspaceModalEditor{
		Mode:          "add directory",
		WorkspacePath: workspace.Path,
		Fields:        fields,
		Selected:      workspaceModalInitialEditorIndex(fields),
	}
	p.workspaceModal.Status = fmt.Sprintf("Link another directory to %s. Type a path, then press %s to link it. %s goes back.", workspace.Name, submitLabel, closeLabel)
	p.workspaceModal.Error = ""
	p.workspaceModal.ConfirmDelete = false
}

func removableWorkspaceModalDirectories(workspace WorkspaceModalWorkspace) []string {
	choices := make([]string, 0, len(workspace.Directories))
	for _, directory := range workspace.Directories {
		directory = strings.TrimSpace(directory)
		if directory == "" || directory == strings.TrimSpace(workspace.Path) {
			continue
		}
		choices = append(choices, directory)
	}
	return choices
}

func (p *HomePage) openWorkspaceModalRemoveDirectoryEditorForWorkspace(workspace WorkspaceModalWorkspace) {
	choices := removableWorkspaceModalDirectories(workspace)
	if len(choices) == 0 {
		p.workspaceModal.Status = "This workspace has no linked directories to remove"
		return
	}
	fields := []workspaceModalEditorField{
		{
			Key:      "directory_path",
			Label:    "Linked Directory",
			Value:    choices[0],
			Options:  choices,
			Editable: false,
			Help:     "Primary workspace root cannot be removed",
		},
	}
	p.workspaceModal.Editor = &workspaceModalEditor{
		Mode:          "remove directory",
		WorkspacePath: workspace.Path,
		Fields:        fields,
		Selected:      0,
	}
	p.workspaceModal.Status = fmt.Sprintf("Remove a linked directory from %s.", workspace.Name)
	p.workspaceModal.Error = ""
	p.workspaceModal.ConfirmDelete = false
}

func (p *HomePage) handleWorkspaceModalEditorKey(ev *tcell.EventKey) {
	editor := p.workspaceModal.Editor
	if editor == nil || len(editor.Fields) == 0 {
		return
	}
	if editor.ThemePickerVisible {
		p.handleWorkspaceModalThemePickerKey(ev)
		return
	}
	suggestions := p.workspaceModalEditorSuggestions(editor)

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.workspaceModal.Editor = nil
		p.workspaceModal.Status = "Workspace editor closed"
		return
	case p.keybinds.MatchAny(ev, KeybindEditorMoveDown) && p.workspaceModalEditorPathFieldSelected(editor) && len(suggestions) > 0:
		editor.SuggestionIndex = (editor.SuggestionIndex + 1) % len(suggestions)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorMoveUp) && p.workspaceModalEditorPathFieldSelected(editor) && len(suggestions) > 0:
		editor.SuggestionIndex = (editor.SuggestionIndex - 1 + len(suggestions)) % len(suggestions)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown):
		if p.workspaceModalEditorPathFieldSelected(editor) && p.applyWorkspaceModalSuggestion(editor, suggestions) {
			return
		}
		editor.Selected = (editor.Selected + 1) % len(editor.Fields)
		editor.SuggestionIndex = 0
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp):
		editor.Selected = (editor.Selected - 1 + len(editor.Fields)) % len(editor.Fields)
		editor.SuggestionIndex = 0
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		if p.workspaceModalEditorThemeFieldSelected(editor) {
			p.openWorkspaceModalThemePicker(editor)
			return
		}
		p.cycleWorkspaceModalEditorOption(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		if p.workspaceModalEditorThemeFieldSelected(editor) {
			p.openWorkspaceModalThemePicker(editor)
			return
		}
		p.cycleWorkspaceModalEditorOption(1)
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		field := &editor.Fields[editor.Selected]
		if !field.Editable {
			return
		}
		if len(field.Value) > 0 {
			_, sz := utf8.DecodeLastRuneInString(field.Value)
			if sz > 0 {
				field.Value = field.Value[:len(field.Value)-sz]
			}
		}
		editor.SuggestionIndex = 0
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		field := &editor.Fields[editor.Selected]
		if !field.Editable {
			return
		}
		field.Value = ""
		editor.SuggestionIndex = 0
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if p.workspaceModalEditorThemeFieldSelected(editor) {
			p.openWorkspaceModalThemePicker(editor)
			return
		}
		if editor.Selected < len(editor.Fields)-1 {
			editor.Selected++
			editor.SuggestionIndex = 0
			return
		}
		p.submitWorkspaceModalEditor()
		return
	}

	if ev.Key() != tcell.KeyRune {
		return
	}
	field := &editor.Fields[editor.Selected]
	r := ev.Rune()
	if p.keybinds.Match(ev, KeybindWorkspaceUnlinkDirectory) && p.workspaceModalEditorRemoveDirectoryFieldSelected(editor) {
		p.submitWorkspaceModalEditor()
		return
	}
	if !unicode.IsPrint(r) {
		return
	}
	if field.Editable {
		field.Value += string(r)
		editor.SuggestionIndex = 0
		return
	}
	p.setWorkspaceModalEditorOptionByPrefix(string(r))
}

func (p *HomePage) submitWorkspaceModalEditor() {
	editor := p.workspaceModal.Editor
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

	if removeDirectoryPath := strings.TrimSpace(get("remove_directory")); removeDirectoryPath != "" && p.workspaceModalEditorRemoveDirectoryFieldSelected(editor) {
		p.workspaceModal.Editor = nil
		p.enqueueWorkspaceModalAction(WorkspaceModalAction{
			Kind:          WorkspaceModalActionRemoveDirectory,
			Path:          strings.TrimSpace(editor.WorkspacePath),
			DirectoryPath: removeDirectoryPath,
			StatusHint:    fmt.Sprintf("Removing linked directory %s ...", workspaceModalDisplayPath(removeDirectoryPath)),
		})
		return
	}

	path := strings.TrimSpace(get("path"))
	if editor.Mode == "add directory" {
		directoryPath := strings.TrimSpace(get("directory_path"))
		if directoryPath == "" {
			p.workspaceModal.Error = "Directory path is required"
			return
		}
		p.workspaceModal.Editor = nil
		p.enqueueWorkspaceModalAction(WorkspaceModalAction{
			Kind:          WorkspaceModalActionAddDirectory,
			Path:          strings.TrimSpace(editor.WorkspacePath),
			DirectoryPath: directoryPath,
			StatusHint:    fmt.Sprintf("Linking directory %s ...", directoryPath),
		})
		return
	}
	if editor.Mode == "remove directory" {
		directoryPath := strings.TrimSpace(get("directory_path"))
		if directoryPath == "" {
			p.workspaceModal.Error = "Linked directory is required"
			return
		}
		p.workspaceModal.Editor = nil
		p.enqueueWorkspaceModalAction(WorkspaceModalAction{
			Kind:          WorkspaceModalActionRemoveDirectory,
			Path:          strings.TrimSpace(editor.WorkspacePath),
			DirectoryPath: directoryPath,
			StatusHint:    fmt.Sprintf("Removing linked directory %s ...", workspaceModalDisplayPath(directoryPath)),
		})
		return
	}

	if path == "" {
		p.workspaceModal.Error = "Workspace path is required"
		return
	}
	name := strings.TrimSpace(get("name"))
	if name == "" {
		name = workspaceModalDefaultName(path)
	}
	themeID := workspaceModalNormalizeThemeID(get("theme_id"))
	makeCurrent := parseWorkspaceModalBool(get("active"))
	linkedDirectory := strings.TrimSpace(get("linked_directory"))

	p.workspaceModal.Editor = nil
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:            WorkspaceModalActionSave,
		Path:            path,
		Name:            name,
		ThemeID:         themeID,
		MakeCurrent:     makeCurrent,
		LinkedDirectory: linkedDirectory,
		StatusHint:      fmt.Sprintf("Saving workspace %s ...", name),
	})
}

func (p *HomePage) enqueueWorkspaceModalAction(action WorkspaceModalAction) {
	if action.Kind == "" {
		return
	}
	p.workspaceModal.ActionMenuVisible = false
	p.pendingWorkspaceAction = &action
	p.workspaceModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.workspaceModal.Status = action.StatusHint
	}
	p.workspaceModal.Error = ""
}

func (p *HomePage) advanceWorkspaceModalFocus(delta int) {
	order := []workspaceModalFocus{
		workspaceModalFocusList,
		workspaceModalFocusDetails,
		workspaceModalFocusSearch,
	}
	idx := 0
	for i, focus := range order {
		if focus == p.workspaceModal.Focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	p.setWorkspaceModalFocus(order[idx], order[idx] == workspaceModalFocusDetails)
}

func (p *HomePage) focusWorkspaceModalLeft() {
	switch p.workspaceModal.Focus {
	case workspaceModalFocusSearch:
		p.setWorkspaceModalFocus(workspaceModalFocusDetails, true)
	case workspaceModalFocusDetails:
		p.setWorkspaceModalFocus(workspaceModalFocusList, false)
	default:
		p.setWorkspaceModalFocus(workspaceModalFocusList, false)
	}
}

func (p *HomePage) focusWorkspaceModalRight() {
	switch p.workspaceModal.Focus {
	case workspaceModalFocusList:
		p.setWorkspaceModalFocus(workspaceModalFocusDetails, true)
	case workspaceModalFocusDetails:
		p.setWorkspaceModalFocus(workspaceModalFocusSearch, false)
	}
}

func (p *HomePage) moveWorkspaceModalSelectionByDirection(dx, dy int) {
	if p.workspaceModal.ActionMenuVisible || p.workspaceModal.Focus == workspaceModalFocusDetails {
		if dy != 0 {
			p.moveWorkspaceModalActionSelection(dy)
		}
		return
	}
	matches := p.workspaceFilteredIndexes()
	if len(matches) == 0 {
		return
	}
	columns := p.workspaceModal.CardColumns
	if columns <= 0 {
		columns = 1
	}
	current := p.workspaceModal.SelectedWorkspace
	pos := indexInList(matches, current)
	if pos < 0 {
		pos = 0
	}
	rows := (len(matches) + columns - 1) / columns
	col := pos % columns
	row := pos / columns
	if dx != 0 {
		col += dx
		if col < 0 {
			col = 0
		}
		if col >= columns {
			col = columns - 1
		}
	}
	if dy != 0 {
		row += dy
		if row < 0 {
			row = 0
		}
		if row >= rows {
			row = rows - 1
		}
	}
	target := row*columns + col
	if target >= len(matches) {
		target = len(matches) - 1
	}
	if target < 0 {
		target = 0
	}
	p.workspaceModal.SelectedWorkspace = matches[target]
}

func (p *HomePage) moveWorkspaceModalSelection(delta int) {
	p.moveWorkspaceModalSelectionByDirection(0, delta)
}

func (p *HomePage) setWorkspaceModalFocus(focus workspaceModalFocus, resetAction bool) {
	p.workspaceModal.Focus = focus
	if focus == workspaceModalFocusDetails {
		p.reconcileWorkspaceModalActionSelection(resetAction)
	}
}

func (p *HomePage) moveWorkspaceModalActionSelection(delta int) {
	if delta == 0 || !p.workspaceModal.ActionMenuVisible {
		return
	}
	actions := p.workspaceModalDetailActions()
	if len(actions) == 0 {
		p.workspaceModal.SelectedAction = -1
		return
	}
	current := p.workspaceModal.SelectedAction
	if current < 0 || current >= len(actions) {
		current = workspaceModalFirstEnabledActionIndex(actions)
	}
	start := current
	for {
		current = (current + delta + len(actions)) % len(actions)
		if actions[current].Enabled || current == start {
			break
		}
	}
	p.workspaceModal.SelectedAction = current
}

func (p *HomePage) reconcileWorkspaceModalActionSelection(reset bool) {
	if !p.workspaceModal.ActionMenuVisible {
		p.workspaceModal.SelectedAction = -1
		return
	}
	actions := p.workspaceModalDetailActions()
	if len(actions) == 0 {
		p.workspaceModal.SelectedAction = -1
		return
	}
	if reset {
		p.workspaceModal.SelectedAction = workspaceModalFirstEnabledActionIndex(actions)
		return
	}
	current := p.workspaceModal.SelectedAction
	if current < 0 || current >= len(actions) || !actions[current].Enabled {
		p.workspaceModal.SelectedAction = workspaceModalFirstEnabledActionIndex(actions)
	}
}

func workspaceModalFirstEnabledActionIndex(actions []workspaceModalDetailAction) int {
	for i, action := range actions {
		if action.Enabled {
			return i
		}
	}
	if len(actions) == 0 {
		return -1
	}
	return 0
}

func (p *HomePage) executeWorkspaceModalSelectedAction() {
	if !p.workspaceModal.ActionMenuVisible {
		return
	}
	actions := p.workspaceModalDetailActions()
	if len(actions) == 0 {
		return
	}
	p.reconcileWorkspaceModalActionSelection(false)
	index := p.workspaceModal.SelectedAction
	if index < 0 || index >= len(actions) {
		return
	}
	action := actions[index]
	if !action.Enabled {
		if hint := strings.TrimSpace(action.Hint); hint != "" {
			p.workspaceModal.Status = hint
			return
		}
		p.workspaceModal.Status = fmt.Sprintf("%s is not available here", action.Label)
		return
	}
	switch action.ID {
	case workspaceModalDetailActionAddDirectory:
		p.workspaceModalAddDirectorySelected()
	case workspaceModalDetailActionUnlinkDirectory:
		p.workspaceModalUnlinkDirectory(action.DirectoryPath)
	case workspaceModalDetailActionActivate:
		p.workspaceModalActivateSelected()
	case workspaceModalDetailActionEdit:
		p.workspaceModalEditSelected()
	case workspaceModalDetailActionRemoveDirectory:
		p.workspaceModalRemoveDirectorySelected()
	case workspaceModalDetailActionMoveUp:
		p.moveSelectedWorkspace(-1)
	case workspaceModalDetailActionMoveDown:
		p.moveSelectedWorkspace(1)
	case workspaceModalDetailActionDelete:
		p.workspaceModalDeleteSelected()
	case workspaceModalDetailActionSaveCurrent:
		p.workspaceModalSaveCurrent()
	case workspaceModalDetailActionNew:
		p.workspaceModalNew()
	case workspaceModalDetailActionOpenKeybinds:
		p.workspaceModalOpenKeybinds()
	case workspaceModalDetailActionRefresh:
		p.workspaceModalRefresh()
	}
}

func (p *HomePage) moveSelectedWorkspace(delta int) {
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		p.workspaceModal.Status = "No workspace selected"
		return
	}
	p.enqueueWorkspaceModalAction(WorkspaceModalAction{
		Kind:       WorkspaceModalActionMove,
		Path:       workspace.Path,
		Delta:      delta,
		StatusHint: fmt.Sprintf("Reordering workspace %s ...", workspace.Name),
	})
}

func (p *HomePage) cycleWorkspaceModalEditorOption(delta int) {
	editor := p.workspaceModal.Editor
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return
	}
	if p.workspaceModalEditorThemeFieldSelected(editor) {
		p.openWorkspaceModalThemePicker(editor)
		return
	}
	field := &editor.Fields[editor.Selected]
	if len(field.Options) == 0 {
		return
	}
	index := 0
	for i, option := range field.Options {
		if strings.EqualFold(option, field.Value) {
			index = i
			break
		}
	}
	index = (index + delta + len(field.Options)) % len(field.Options)
	field.Value = field.Options[index]
}

func (p *HomePage) setWorkspaceModalEditorOptionByPrefix(prefix string) {
	editor := p.workspaceModal.Editor
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return
	}
	if p.workspaceModalEditorThemeFieldSelected(editor) {
		p.openWorkspaceModalThemePicker(editor)
		p.setWorkspaceModalThemePickerSelectionByPrefix(prefix)
		return
	}
	field := &editor.Fields[editor.Selected]
	if len(field.Options) == 0 {
		return
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return
	}
	for _, option := range field.Options {
		if strings.HasPrefix(strings.ToLower(option), prefix) {
			field.Value = option
			return
		}
	}
}

func (p *HomePage) deleteWorkspaceModalSearchRune() {
	if p.workspaceModal.Focus != workspaceModalFocusSearch || len(p.workspaceModal.Search) == 0 {
		return
	}
	_, sz := utf8.DecodeLastRuneInString(p.workspaceModal.Search)
	if sz > 0 {
		p.workspaceModal.Search = p.workspaceModal.Search[:len(p.workspaceModal.Search)-sz]
	}
	p.workspaceModal.reconcileSelections()
	p.reconcileWorkspaceModalActionSelection(false)
}

func (p *HomePage) clearWorkspaceModalSearch() {
	p.workspaceModal.Search = ""
	p.workspaceModal.reconcileSelections()
	p.reconcileWorkspaceModalActionSelection(false)
}

func (s *workspaceModalState) reconcileSelections() {
	matches := s.filteredIndexes()
	if len(matches) == 0 {
		s.SelectedWorkspace = -1
		return
	}
	if indexInList(matches, s.SelectedWorkspace) < 0 {
		s.SelectedWorkspace = matches[0]
	}
	if s.SelectedWorkspace < 0 || s.SelectedWorkspace >= len(s.Workspaces) {
		s.SelectedWorkspace = matches[0]
	}
}

func (p *HomePage) selectedWorkspaceModal() (WorkspaceModalWorkspace, bool) {
	idx := p.workspaceModal.SelectedWorkspace
	if idx < 0 || idx >= len(p.workspaceModal.Workspaces) {
		return WorkspaceModalWorkspace{}, false
	}
	return p.workspaceModal.Workspaces[idx], true
}

func (p *HomePage) selectedWorkspaceModalPath() string {
	workspace, ok := p.selectedWorkspaceModal()
	if !ok {
		return ""
	}
	return strings.TrimSpace(workspace.Path)
}

func (p *HomePage) findWorkspaceModalIndexByPath(path string) int {
	path = strings.TrimSpace(path)
	if path == "" {
		return -1
	}
	for i, workspace := range p.workspaceModal.Workspaces {
		if strings.EqualFold(strings.TrimSpace(workspace.Path), path) {
			return i
		}
	}
	return -1
}

func (p *HomePage) workspaceByPath(path string) (WorkspaceModalWorkspace, bool) {
	index := p.findWorkspaceModalIndexByPath(path)
	if index < 0 {
		return WorkspaceModalWorkspace{}, false
	}
	return p.workspaceModal.Workspaces[index], true
}

func (p *HomePage) workspaceFilteredIndexes() []int {
	indexes := make([]int, 0, len(p.workspaceModal.Workspaces))
	for i := range p.workspaceModal.Workspaces {
		indexes = append(indexes, i)
	}
	return indexes
}

func workspaceModalDirectories(workspace WorkspaceModalWorkspace) []string {
	out := make([]string, 0, len(workspace.Directories))
	for _, directory := range workspace.Directories {
		directory = strings.TrimSpace(directory)
		if directory == "" {
			continue
		}
		out = append(out, directory)
	}
	return out
}

func (s *workspaceModalState) filteredIndexes() []int {
	out := make([]int, 0, len(s.Workspaces))
	for i := range s.Workspaces {
		out = append(out, i)
	}
	return out
}

func workspaceModalMatchesQuery(workspace WorkspaceModalWorkspace, query string) bool {
	return true
}

func (p *HomePage) currentWorkspaceModalDirectoryPath() string {
	if path := strings.TrimSpace(p.workspaceModal.DirectoryPath); path != "" {
		return path
	}
	directory := p.primaryDirectory()
	if path := strings.TrimSpace(directory.ResolvedPath); path != "" {
		return path
	}
	if path := strings.TrimSpace(directory.Path); path != "" {
		return path
	}
	return ""
}

func (p *HomePage) currentWorkspaceModalAddDirectoryPath() string {
	path := strings.TrimSpace(p.workspaceModal.AddDirectoryPath)
	if path != "" {
		return path
	}
	return "~/"
}

func (p *HomePage) workspaceModalEditorPathFieldSelected(editor *workspaceModalEditor) bool {
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return false
	}
	field := editor.Fields[editor.Selected]
	if !field.Editable {
		return false
	}
	switch field.Key {
	case "path", "linked_directory", "directory_path":
		return true
	default:
		return false
	}
}

func (p *HomePage) workspaceModalEditorThemeFieldSelected(editor *workspaceModalEditor) bool {
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return false
	}
	return editor.Fields[editor.Selected].Key == "theme_id"
}

func (p *HomePage) workspaceModalEditorRemoveDirectoryFieldSelected(editor *workspaceModalEditor) bool {
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return false
	}
	return editor.Fields[editor.Selected].Key == "remove_directory"
}

func (p *HomePage) openWorkspaceModalThemePicker(editor *workspaceModalEditor) {
	if editor == nil {
		return
	}
	editor.ThemePickerVisible = true
	editor.ThemePickerSelected = p.workspaceModalThemeOptionIndex(editor)
}

func (p *HomePage) closeWorkspaceModalThemePicker(editor *workspaceModalEditor) {
	if editor == nil {
		return
	}
	editor.ThemePickerVisible = false
	editor.ThemePickerSelected = 0
}

func (p *HomePage) workspaceModalThemeOptionIndex(editor *workspaceModalEditor) int {
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return 0
	}
	field := editor.Fields[editor.Selected]
	value := workspaceModalNormalizeThemeID(field.Value)
	for i, option := range field.Options {
		if workspaceModalNormalizeThemeID(option) == value {
			return i
		}
	}
	return 0
}

func (p *HomePage) applyWorkspaceModalThemePickerSelection(editor *workspaceModalEditor) {
	if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return
	}
	field := &editor.Fields[editor.Selected]
	if len(field.Options) == 0 {
		return
	}
	index := editor.ThemePickerSelected
	if index < 0 || index >= len(field.Options) {
		index = 0
	}
	field.Value = field.Options[index]
	p.closeWorkspaceModalThemePicker(editor)
}

func (p *HomePage) setWorkspaceModalThemePickerSelectionByPrefix(prefix string) {
	editor := p.workspaceModal.Editor
	if editor == nil || !editor.ThemePickerVisible || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
		return
	}
	field := editor.Fields[editor.Selected]
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return
	}
	for i, option := range field.Options {
		label := strings.ToLower(workspaceModalDisplayThemeLabel(option))
		if strings.HasPrefix(strings.ToLower(option), prefix) || strings.HasPrefix(label, prefix) {
			editor.ThemePickerSelected = i
			return
		}
	}
}

func (p *HomePage) handleWorkspaceModalThemePickerKey(ev *tcell.EventKey) {
	editor := p.workspaceModal.Editor
	if editor == nil {
		return
	}
	field := editor.Fields[editor.Selected]
	count := len(field.Options)
	if count == 0 {
		p.closeWorkspaceModalThemePicker(editor)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.closeWorkspaceModalThemePicker(editor)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorMoveUp, KeybindEditorFocusPrev):
		editor.ThemePickerSelected = (editor.ThemePickerSelected - 1 + count) % count
		return
	case p.keybinds.MatchAny(ev, KeybindEditorMoveDown, KeybindEditorFocusNext):
		editor.ThemePickerSelected = (editor.ThemePickerSelected + 1) % count
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		editor.ThemePickerSelected = (editor.ThemePickerSelected - 1 + count) % count
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		editor.ThemePickerSelected = (editor.ThemePickerSelected + 1) % count
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		p.applyWorkspaceModalThemePickerSelection(editor)
		return
	}

	if ev.Key() != tcell.KeyRune {
		return
	}
	r := ev.Rune()
	if !unicode.IsPrint(r) {
		return
	}
	p.setWorkspaceModalThemePickerSelectionByPrefix(string(r))
}

func (p *HomePage) workspaceModalEditorSuggestions(editor *workspaceModalEditor) []string {
	if !p.workspaceModalEditorPathFieldSelected(editor) {
		return nil
	}
	field := editor.Fields[editor.Selected]
	return workspaceModalDirectorySuggestions(field.Value, 6)
}

func (p *HomePage) applyWorkspaceModalSuggestion(editor *workspaceModalEditor, suggestions []string) bool {
	if editor == nil || !p.workspaceModalEditorPathFieldSelected(editor) || len(suggestions) == 0 {
		return false
	}
	index := editor.SuggestionIndex
	if index < 0 || index >= len(suggestions) {
		index = 0
	}
	field := &editor.Fields[editor.Selected]
	suggestion := strings.TrimSpace(suggestions[index])
	if suggestion == "" {
		return false
	}
	current := strings.TrimSpace(field.Value)
	if workspaceModalPathsEqual(current, suggestion) {
		return false
	}
	field.Value = suggestion
	return true
}

func (p *HomePage) workspaceModalAddDirectoryNote(selected WorkspaceModalWorkspace, ok bool, currentDir string) string {
	submitLabel := p.workspaceModalEditorKeyLabel(KeybindEditorSubmit, "Enter")
	if !ok {
		if len(p.workspaceModal.Workspaces) > 0 {
			return "Select a workspace first, then open the link-directory editor to add another root."
		}
		return "Create or select a workspace first. Then open the link-directory editor to add another root."
	}
	if workspaceModalWorkspaceContainsPath(selected, currentDir) {
		return fmt.Sprintf("This workspace already covers the current directory. You can still link another root; type it in the editor and press %s.", submitLabel)
	}
	return fmt.Sprintf("Open the link-directory editor, type a path from ~/ or an absolute directory, then press %s to link it.", submitLabel)
}

func (p *HomePage) workspaceModalDetailActions() []workspaceModalDetailAction {
	selected, ok := p.selectedWorkspaceModal()
	currentDir := p.currentWorkspaceModalDirectoryPath()
	addDirectoryLabel := "Link Directory"
	saveCurrentLabel := "Create Workspace from Current Dir"
	newLabel := "New Workspace from Path"
	if p.WorkspaceModalIntent() == "add_dir" {
		saveCurrentLabel = "Create Current Dir Workspace + Link Directory"
		newLabel = "New Workspace + Link Directory"
	}

	actions := []workspaceModalDetailAction{
		{
			ID:       workspaceModalDetailActionAddDirectory,
			Label:    addDirectoryLabel,
			Hint:     p.workspaceModalAddDirectoryNote(selected, ok, currentDir),
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceLinkDirectory),
			Enabled:  ok,
		},
	}
	if ok {
		for _, directory := range workspaceModalDirectories(selected) {
			directory = strings.TrimSpace(directory)
			if directory == "" || directory == strings.TrimSpace(selected.Path) {
				continue
			}
			actions = append(actions, workspaceModalDetailAction{
				ID:            workspaceModalDetailActionUnlinkDirectory,
				Label:         fmt.Sprintf("Unlink %s", workspaceModalDisplayPath(directory)),
				Hint:          fmt.Sprintf("Remove linked root %s from this workspace.", workspaceModalDisplayPath(directory)),
				Shortcut:      "",
				Enabled:       true,
				DirectoryPath: directory,
			})
		}
	}
	actions = append(actions,
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionActivate,
			Label:    "Activate Workspace",
			Hint:     "Switch chat/home context to the selected workspace.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceActivate),
			Enabled:  ok,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionEdit,
			Label:    "Edit Workspace",
			Hint:     "Rename the workspace, choose its theme, or update its active state.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceEdit),
			Enabled:  ok,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionMoveUp,
			Label:    "Move Workspace Up",
			Hint:     "Reorder the selected workspace earlier in the list.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceMoveUp),
			Enabled:  ok,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionMoveDown,
			Label:    "Move Workspace Down",
			Hint:     "Reorder the selected workspace later in the list.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceMoveDown),
			Enabled:  ok,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionDelete,
			Label:    "Delete Workspace",
			Hint:     "Delete the selected saved workspace. You must confirm delete twice.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceDelete),
			Enabled:  ok,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionSaveCurrent,
			Label:    saveCurrentLabel,
			Hint:     "Open workspace setup using the current directory as the primary path.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceSaveCurrent),
			Enabled:  true,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionNew,
			Label:    newLabel,
			Hint:     "Open workspace setup for any path, starting from ~/ suggestions.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceNew),
			Enabled:  true,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionOpenKeybinds,
			Label:    "Open Keybinds",
			Hint:     "Workspace switcher keys are configured in /keybinds, including slots 1-10.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceOpenKeybinds),
			Enabled:  true,
		},
		workspaceModalDetailAction{
			ID:       workspaceModalDetailActionRefresh,
			Label:    "Refresh Workspace List",
			Hint:     "Reload saved workspaces from the backend.",
			Shortcut: p.workspaceModalKeyLabel(KeybindWorkspaceRefresh),
			Enabled:  true,
		},
	)
	return actions
}

func (p *HomePage) workspaceModalKeyLabel(id KeybindID) string {
	if p.keybinds == nil {
		return ""
	}
	label := strings.TrimSpace(p.keybinds.Label(id))
	if strings.EqualFold(label, "unbound") {
		return ""
	}
	return label
}

func (p *HomePage) workspaceModalEditorKeyLabel(id KeybindID, fallback string) string {
	if label := p.workspaceModalKeyLabel(id); label != "" {
		return label
	}
	return fallback
}

func (p *HomePage) workspaceModalEditorFooterLines(editor *workspaceModalEditor, suggestionCount int) []string {
	if editor == nil {
		return nil
	}
	submitLabel := p.workspaceModalEditorKeyLabel(KeybindEditorSubmit, "Enter")
	tabLabel := p.workspaceModalEditorKeyLabel(KeybindEditorFocusNext, "Tab")
	upLabel := p.workspaceModalEditorKeyLabel(KeybindEditorMoveUp, "↑")
	downLabel := p.workspaceModalEditorKeyLabel(KeybindEditorMoveDown, "↓")
	closeLabel := p.workspaceModalEditorKeyLabel(KeybindEditorClose, "Esc")

	if editor.ThemePickerVisible {
		return []string{
			fmt.Sprintf("%s apply selected theme", submitLabel),
			fmt.Sprintf("%s/%s move • type to jump • %s close picker", upLabel, downLabel, closeLabel),
		}
	}

	if p.workspaceModalEditorPathFieldSelected(editor) {
		action := "save this workspace"
		switch {
		case editor.Mode == "add directory":
			action = "link this directory"
		case editor.Mode == "remove directory":
			action = "remove this directory"
		case editor.Selected < len(editor.Fields)-1:
			action = "keep this path and go to the next field"
		case strings.Contains(editor.Mode, "link directory") || strings.Contains(editor.Mode, "add directory"):
			action = "save the workspace and link this directory"
		case strings.HasPrefix(editor.Mode, "edit"):
			action = "save workspace changes"
		}
		lines := []string{fmt.Sprintf("%s %s", submitLabel, action)}
		if suggestionCount > 0 {
			lines = append(lines,
				fmt.Sprintf("%s fills the highlighted suggestion", tabLabel),
				fmt.Sprintf("%s/%s choose a suggestion • %s cancel", upLabel, downLabel, closeLabel),
			)
			return lines
		}
		lines = append(lines,
			fmt.Sprintf("%s fills a suggestion when one is shown", tabLabel),
			fmt.Sprintf("%s cancel", closeLabel),
		)
		return lines
	}

	if p.workspaceModalEditorThemeFieldSelected(editor) {
		return []string{
			fmt.Sprintf("%s open theme picker", submitLabel),
			fmt.Sprintf("Left/Right also opens picker • type to jump • %s cancel", closeLabel),
		}
	}

	if p.workspaceModalEditorRemoveDirectoryFieldSelected(editor) {
		unlinkLabel := p.workspaceModalKeyLabel(KeybindWorkspaceUnlinkDirectory)
		if unlinkLabel == "" {
			unlinkLabel = "u"
		}
		return []string{
			fmt.Sprintf("%s unlink selected folder", unlinkLabel),
			fmt.Sprintf("Left/Right choose linked folder • %s cancel", closeLabel),
		}
	}

	if editor.Selected < len(editor.Fields)-1 {
		return []string{
			fmt.Sprintf("%s next field", submitLabel),
			fmt.Sprintf("Left/Right change options • %s cancel", closeLabel),
		}
	}

	action := "save this workspace"
	switch {
	case editor.Mode == "remove directory":
		action = "remove this directory"
	case strings.Contains(editor.Mode, "link directory") || strings.Contains(editor.Mode, "add directory"):
		action = "save the workspace and link the directory"
	case strings.HasPrefix(editor.Mode, "edit"):
		action = "save workspace changes"
	}
	return []string{
		fmt.Sprintf("%s %s", submitLabel, action),
		fmt.Sprintf("Left/Right change options • %s cancel", closeLabel),
	}
}

func workspaceModalHasRemovableDirectory(workspace WorkspaceModalWorkspace) bool {
	for _, directory := range workspace.Directories {
		directory = strings.TrimSpace(directory)
		if directory == "" || directory == strings.TrimSpace(workspace.Path) {
			continue
		}
		return true
	}
	return false
}

func workspaceModalRemoveDirectoryHint(selected WorkspaceModalWorkspace, ok bool) string {
	if !ok {
		return "Select a workspace first, then remove one of its linked directories."
	}
	if !workspaceModalHasRemovableDirectory(selected) {
		return "This workspace only has its primary root. Add another directory first."
	}
	return "Remove a linked directory from the selected workspace. The primary root stays attached. You can also use /workspace and choose an Unlink action for a specific linked root."
}

func workspaceModalWorkspaceContainsPath(workspace WorkspaceModalWorkspace, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if len(workspace.Directories) == 0 {
		return workspaceModalPathWithinRoot(workspace.Path, target)
	}
	for _, root := range workspace.Directories {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if workspaceModalPathWithinRoot(root, target) {
			return true
		}
	}
	return false
}

func workspaceModalPathWithinRoot(root, target string) bool {
	root = strings.TrimSpace(root)
	target = strings.TrimSpace(target)
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func workspaceModalPathsEqual(left, right string) bool {
	left = workspaceModalNormalizeDirectoryPath(left)
	right = workspaceModalNormalizeDirectoryPath(right)
	return left != "" && left == right
}

func workspaceModalDirectorySuggestions(raw string, limit int) []string {
	if limit <= 0 {
		limit = 6
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	home = filepath.Clean(home)

	query := strings.TrimSpace(raw)
	if query == "" {
		query = "~" + string(filepath.Separator)
	}
	expanded := workspaceModalExpandSuggestionInput(query, home)
	baseDir, prefix := workspaceModalSplitSuggestionInput(query, expanded, home)
	if strings.TrimSpace(baseDir) == "" {
		return nil
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil
	}
	matches := make([]string, 0, limit)
	prefixLower := strings.ToLower(strings.TrimSpace(prefix))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if prefixLower != "" && !strings.HasPrefix(lower, prefixLower) && !strings.Contains(lower, prefixLower) {
			continue
		}
		matches = append(matches, workspaceModalDisplayPath(filepath.Join(baseDir, name)))
	}
	sort.Strings(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func workspaceModalSplitSuggestionInput(query, expanded, home string) (string, string) {
	query = strings.TrimSpace(query)
	expanded = strings.TrimSpace(expanded)
	if query == "" {
		return home, ""
	}
	if strings.HasSuffix(query, string(filepath.Separator)) || query == "~" || query == "~"+string(filepath.Separator) {
		return expanded, ""
	}
	if info, err := os.Stat(expanded); err == nil && info.IsDir() {
		return expanded, ""
	}
	baseDir := filepath.Dir(expanded)
	prefix := filepath.Base(expanded)
	return baseDir, prefix
}

func workspaceModalExpandSuggestionInput(query, home string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return home
	}
	if query == "~" {
		return home
	}
	if strings.HasPrefix(query, "~"+string(filepath.Separator)) {
		return filepath.Join(home, strings.TrimPrefix(query, "~"+string(filepath.Separator)))
	}
	if filepath.IsAbs(query) {
		return filepath.Clean(query)
	}
	if strings.HasPrefix(query, "."+string(filepath.Separator)) || query == "." || strings.HasPrefix(query, ".."+string(filepath.Separator)) || query == ".." {
		cwd, err := os.Getwd()
		if err != nil {
			return filepath.Clean(filepath.Join(home, query))
		}
		return filepath.Clean(filepath.Join(cwd, query))
	}
	return filepath.Clean(filepath.Join(home, query))
}

func workspaceModalNormalizeDirectoryPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = workspaceModalExpandSuggestionInput(path, workspaceModalHomePath())
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return filepath.Clean(path)
}

func workspaceModalHomePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(home)
}

func (p *HomePage) drawWorkspaceModal(s tcell.Screen) {
	if !p.workspaceModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := w - 8
	if modalW > 122 {
		modalW = 122
	}
	if modalW < 76 {
		modalW = w - 2
	}
	modalH := h - 6
	if modalH > 32 {
		modalH = 32
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

	editorSubmitLabel := p.workspaceModalEditorKeyLabel(KeybindEditorSubmit, "Enter")
	editorTabLabel := p.workspaceModalEditorKeyLabel(KeybindEditorFocusNext, "Tab")
	title := "Workspace Manager"
	if p.WorkspaceModalIntent() == "add_dir" {
		title = "Workspace Manager · Link Directory"
	}
	if p.workspaceModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	statusStyle := p.theme.TextMuted
	status := strings.TrimSpace(p.workspaceModal.Status)
	if strings.TrimSpace(p.workspaceModal.Error) != "" {
		status = p.workspaceModal.Error
		statusStyle = p.theme.Error
	}
	if status == "" {
		if p.workspaceModal.ActionMenuVisible {
			status = "Choose an action for the selected workspace. Enter runs it, Esc goes back to cards."
		} else if p.WorkspaceModalIntent() == "add_dir" {
			status = "Start on the current workspace. Use ←/→ to move across cards, Enter to edit, or l to link a directory."
		} else {
			status = "Start on the current workspace. Use ←/→ to move across cards, Enter to edit, or press action keys directly."
		}
	}
	statusLines := workspaceModalWrap(status, rect.W-4)
	statusY := rect.Y + 1
	for i, line := range statusLines {
		y := statusY + i
		if y >= rect.Y+rect.H-1 {
			break
		}
		DrawText(s, rect.X+2, y, rect.W-4, statusStyle, line)
	}

	help := "Arrow keys move cards • Enter/e edit selected workspace • edit screen can unlink linked folders • Esc close"
	if p.workspaceModal.ActionMenuVisible {
		help = "↑/↓ choose workspace action • Enter run action • Esc back to cards"
	}
	if p.WorkspaceModalIntent() == "add_dir" && !p.workspaceModal.ActionMenuVisible {
		help = "Arrow keys move cards • Enter edits selected workspace • l link dir • s save current • n new • Esc close"
	}
	helpLines := workspaceModalWrap(help, rect.W-4)
	footerText := "Workspace switcher keys are configured in /keybinds."
	footerStyle := p.theme.TextMuted
	if p.WorkspaceModalIntent() == "add_dir" {
		footerText = fmt.Sprintf("Link-directory editor accepts ~/ or absolute paths. In the editor, %s links the typed path and %s fills suggestions.", editorSubmitLabel, editorTabLabel)
	}
	if p.workspaceModal.ConfirmDelete {
		footerText = "Delete is armed: press the delete keybind again to confirm"
		footerStyle = p.theme.Warning
	}
	footerLines := workspaceModalWrap(footerText, rect.W-4)
	footerRows := len(helpLines) + len(footerLines)
	footerStartY := rect.Y + rect.H - footerRows

	contentRect := Rect{X: rect.X + 1, Y: statusY + len(statusLines), W: rect.W - 2, H: footerStartY - (statusY + len(statusLines))}
	if contentRect.W < 20 || contentRect.H < 4 {
		return
	}

	compactLayout := contentRect.W < 72 || contentRect.H < 8
	if compactLayout {
		if p.workspaceModal.ActionMenuVisible {
			p.drawWorkspaceModalActionMenu(s, contentRect)
		} else {
			p.drawWorkspaceModalCards(s, contentRect)
		}
	} else {
		p.drawWorkspaceModalCards(s, contentRect)
		if p.workspaceModal.ActionMenuVisible {
			p.drawWorkspaceModalActionMenu(s, contentRect)
		}
	}

	rowY := footerStartY
	for _, line := range helpLines {
		if rowY >= rect.Y+rect.H-1 {
			break
		}
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, line)
		rowY++
	}
	for _, line := range footerLines {
		if rowY >= rect.Y+rect.H-1 {
			break
		}
		DrawText(s, rect.X+2, rowY, rect.W-4, footerStyle, line)
		rowY++
	}

	if p.workspaceModal.Editor != nil {
		p.drawWorkspaceModalEditor(s, rect)
	}
}

func (p *HomePage) drawWorkspaceModalCards(s tcell.Screen, rect Rect) {
	DrawBox(s, rect, p.theme.Border)
	header := "Workspace Cards"
	if p.workspaceModal.Focus == workspaceModalFocusList && !p.workspaceModal.ActionMenuVisible {
		header += " [focus]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)
	matches := p.workspaceFilteredIndexes()
	if len(matches) == 0 {
		p.workspaceModal.CardColumns = 1
		DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.Warning, "No saved workspaces yet")
		emptyHint := "Press s to create the first workspace"
		if p.WorkspaceModalIntent() == "add_dir" {
			emptyHint = "Press s to create the first workspace, then link another directory"
		}
		DrawText(s, rect.X+2, rect.Y+3, rect.W-4, p.theme.TextMuted, emptyHint)
		return
	}
	innerX := rect.X + 1
	innerY := rect.Y + 1
	innerW := rect.W - 2
	innerH := rect.H - 2
	cardMinW := 28
	cardH := 6
	compact := innerW < 56 || innerH < 9
	if compact {
		cardMinW = maxInt(20, innerW)
		cardH = 4
	}
	gap := 1
	columns := maxInt(1, (innerW+gap)/(cardMinW+gap))
	if columns > len(matches) {
		columns = len(matches)
	}
	cardW := (innerW - (columns-1)*gap) / columns
	if compact {
		cardW = innerW
		columns = 1
	} else if cardW < cardMinW {
		columns = maxInt(1, minInt(len(matches), (innerW+gap)/(cardMinW+gap)))
		cardW = maxInt(cardMinW, (innerW-(columns-1)*gap)/columns)
	}
	if columns <= 0 {
		columns = 1
	}
	p.workspaceModal.CardColumns = columns
	rowsVisible := maxInt(1, innerH/(cardH+gap))
	selectedPos := indexInList(matches, p.workspaceModal.SelectedWorkspace)
	if selectedPos < 0 {
		selectedPos = 0
	}
	startRow := 0
	if selectedPos/columns >= rowsVisible {
		startRow = selectedPos/columns - rowsVisible + 1
	}
	for visibleRow := 0; visibleRow < rowsVisible; visibleRow++ {
		actualRow := startRow + visibleRow
		for col := 0; col < columns; col++ {
			idxPos := actualRow*columns + col
			if idxPos >= len(matches) {
				continue
			}
			idx := matches[idxPos]
			workspace := p.workspaceModal.Workspaces[idx]
			cardX := innerX + col*(cardW+gap)
			cardY := innerY + visibleRow*(cardH+gap)
			cardRect := Rect{X: cardX, Y: cardY, W: cardW, H: cardH}
			style := p.theme.Border
			fill := p.theme.Panel
			selected := idx == p.workspaceModal.SelectedWorkspace
			if selected {
				style = p.theme.BorderActive
				fill = p.theme.Primary
			}
			FillRect(s, cardRect, fill)
			DrawBox(s, cardRect, style)
			name := strings.TrimSpace(workspace.Name)
			if name == "" {
				name = workspaceModalDefaultName(workspace.Path)
			}
			badge := ""
			if workspace.Active {
				badge = " [active]"
			}
			textStyle := p.theme.Text
			mutedStyle := p.theme.TextMuted
			if selected {
				textStyle = p.theme.Text.Bold(true)
				mutedStyle = p.theme.TextMuted
			}
			DrawText(s, cardRect.X+2, cardRect.Y+1, cardRect.W-4, textStyle, clampEllipsis(name+badge, cardRect.W-4))
			DrawText(s, cardRect.X+2, cardRect.Y+2, cardRect.W-4, mutedStyle, clampEllipsis(workspaceModalDisplayPath(workspace.Path), cardRect.W-4))
			dirs := len(workspaceModalDirectories(workspace))
			if dirs == 0 {
				dirs = 1
			}
			if compact {
				compactMeta := fmt.Sprintf("dirs %d", dirs)
				if workspace.Active {
					compactMeta += " · active"
				}
				DrawText(s, cardRect.X+2, cardRect.Y+3, cardRect.W-4, mutedStyle, clampEllipsis(compactMeta, cardRect.W-4))
				continue
			}
			meta := fmt.Sprintf("theme %s · dirs %d", workspaceModalDisplayThemeLabel(workspace.ThemeID), dirs)
			slotLabel := fmt.Sprintf("slot %02d", workspace.SortIndex+1)
			if id, ok := WorkspaceSlotKeybindID(workspace.SortIndex + 1); ok {
				if keyLabel := p.workspaceModalKeyLabel(id); keyLabel != "" {
					slotLabel = fmt.Sprintf("%s · %s", slotLabel, keyLabel)
				}
			}
			DrawText(s, cardRect.X+2, cardRect.Y+3, cardRect.W-4, mutedStyle, clampEllipsis(meta, cardRect.W-4))
			orderLine := slotLabel
			if selected {
				orderLine += " · Enter to edit"
			}
			DrawText(s, cardRect.X+2, cardRect.Y+4, cardRect.W-4, mutedStyle, clampEllipsis(orderLine, cardRect.W-4))
		}
	}
}

func (p *HomePage) drawWorkspaceModalActionMenu(s tcell.Screen, parent Rect) {
	width := minInt(46, parent.W-6)
	if width < 28 {
		width = parent.W - 2
	}
	actions := p.workspaceModalDetailActions()
	height := minInt(parent.H-2, maxInt(8, len(actions)+6))
	rect := Rect{X: parent.X + (parent.W-width)/2, Y: parent.Y + (parent.H-height)/2, W: width, H: height}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	header := "Workspace Actions"
	if p.workspaceModal.Focus == workspaceModalFocusDetails {
		header += " [focus]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, header)
	selected, ok := p.selectedWorkspaceModal()
	if ok {
		name := strings.TrimSpace(selected.Name)
		if name == "" {
			name = workspaceModalDefaultName(selected.Path)
		}
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, clampEllipsis(name+" · "+workspaceModalDisplayPath(selected.Path), rect.W-4))
	}
	rowY := rect.Y + 3
	p.reconcileWorkspaceModalActionSelection(false)
	selectedActionIndex := p.workspaceModal.SelectedAction
	for i, action := range actions {
		if rowY >= rect.Y+rect.H-2 {
			break
		}
		prefix := "  "
		style := p.theme.Text
		if !action.Enabled {
			style = p.theme.TextMuted
		}
		if i == selectedActionIndex {
			prefix = "> "
			if action.Enabled {
				style = p.theme.Primary.Bold(true)
			} else {
				style = p.theme.Warning.Bold(true)
			}
		}
		line := prefix + action.Label
		DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(line, rect.W-4))
		rowY++
	}
	if selectedActionIndex >= 0 && selectedActionIndex < len(actions) && rowY < rect.Y+rect.H-1 {
		for _, wrapped := range workspaceModalWrap(strings.TrimSpace(actions[selectedActionIndex].Hint), rect.W-4) {
			if rowY >= rect.Y+rect.H-1 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, wrapped)
			rowY++
		}
	}
}

func (p *HomePage) drawWorkspaceModalEditor(s tcell.Screen, parent Rect) {
	editor := p.workspaceModal.Editor
	if editor == nil {
		return
	}
	width := parent.W - 12
	if width > 96 {
		width = 96
	}
	if width < 48 {
		width = parent.W - 4
	}
	contentWidth := maxInt(1, width-4)
	helpWidth := maxInt(1, width-6)
	suggestions := p.workspaceModalEditorSuggestions(editor)
	footerLines := p.workspaceModalEditorFooterLines(editor, len(suggestions))
	tabLabel := p.workspaceModalEditorKeyLabel(KeybindEditorFocusNext, "Tab")
	height := 4 + len(footerLines)
	for i, field := range editor.Fields {
		line := workspaceModalEditorFieldLine(field, i == editor.Selected)
		height += len(workspaceModalWrap(line, contentWidth))
		if help := strings.TrimSpace(field.Help); help != "" {
			height += len(workspaceModalWrap(help, helpWidth))
		}
		if i == editor.Selected && p.workspaceModalEditorPathFieldSelected(editor) && len(suggestions) > 0 {
			height += len(suggestions) + 1
		}
		if i == editor.Selected && p.workspaceModalEditorThemeFieldSelected(editor) && editor.ThemePickerVisible {
			height += minInt(len(field.Options), 8) + 1
		}
	}
	if height < 11 {
		height = 11
	}
	if maxHeight := parent.H - 2; maxHeight > 0 && height > maxHeight {
		height = maxHeight
	}
	rect := Rect{
		X: parent.X + (parent.W-width)/2,
		Y: parent.Y + (parent.H-height)/2,
		W: width,
		H: height,
	}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)

	title := "Workspace Setup"
	switch {
	case editor.Mode == "add directory":
		title = "Link Directory"
	case editor.Mode == "remove directory":
		title = "Remove Linked Directory"
	case strings.HasPrefix(editor.Mode, "edit") && (strings.Contains(editor.Mode, "link directory") || strings.Contains(editor.Mode, "add directory")):
		title = "Edit Workspace + Link Directory"
	case strings.Contains(editor.Mode, "link directory") || strings.Contains(editor.Mode, "add directory"):
		title = "Create Workspace + Link Directory"
	case strings.HasPrefix(editor.Mode, "edit"):
		title = "Edit Workspace"
	}
	if removable := strings.TrimSpace(func() string {
		for _, field := range editor.Fields {
			if field.Key == "remove_directory" {
				return field.Value
			}
		}
		return ""
	}()); strings.HasPrefix(editor.Mode, "edit") && removable != "" {
		title += " · Unlink Available"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	rowY := rect.Y + 1
	footerY := rect.Y + rect.H - len(footerLines) - 1
	for i, field := range editor.Fields {
		if rowY >= footerY {
			break
		}
		style := p.theme.Text
		line := workspaceModalEditorFieldLine(field, i == editor.Selected)
		if field.Key == "theme_id" && i == editor.Selected {
			style = p.theme.Primary.Bold(true)
		}
		for _, wrapped := range workspaceModalWrap(line, rect.W-4) {
			if rowY >= footerY {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, style, wrapped)
			rowY++
		}
		if help := strings.TrimSpace(field.Help); help != "" {
			for _, wrapped := range workspaceModalWrap(help, rect.W-6) {
				if rowY >= footerY {
					break
				}
				DrawText(s, rect.X+4, rowY, rect.W-6, p.theme.TextMuted, wrapped)
				rowY++
			}
		}
		if i == editor.Selected && p.workspaceModalEditorPathFieldSelected(editor) && len(suggestions) > 0 {
			if rowY < footerY {
				DrawText(s, rect.X+4, rowY, rect.W-6, p.theme.TextMuted, fmt.Sprintf("Suggestions (%s fills highlighted item):", tabLabel))
				rowY++
			}
			for idx, suggestion := range suggestions {
				if rowY >= footerY {
					break
				}
				prefix := "  "
				style := p.theme.TextMuted
				if idx == editor.SuggestionIndex {
					prefix = "> "
					style = p.theme.Primary.Bold(true)
				}
				DrawText(s, rect.X+4, rowY, rect.W-6, style, clampEllipsis(prefix+workspaceModalDisplayPath(suggestion), rect.W-6))
				rowY++
			}
		}
		if i == editor.Selected && p.workspaceModalEditorThemeFieldSelected(editor) && editor.ThemePickerVisible {
			if rowY < footerY {
				DrawText(s, rect.X+4, rowY, rect.W-6, p.theme.TextMuted, "Themes:")
				rowY++
			}
			field := editor.Fields[i]
			start := 0
			visible := minInt(len(field.Options), 8)
			if visible < 1 {
				visible = len(field.Options)
			}
			if editor.ThemePickerSelected >= visible {
				start = editor.ThemePickerSelected - visible + 1
			}
			maxStart := len(field.Options) - visible
			if maxStart < 0 {
				maxStart = 0
			}
			if start > maxStart {
				start = maxStart
			}
			for idx := 0; idx < visible; idx++ {
				optionIndex := start + idx
				if optionIndex >= len(field.Options) || rowY >= footerY {
					break
				}
				option := field.Options[optionIndex]
				prefix := "  "
				style := p.theme.TextMuted
				if optionIndex == editor.ThemePickerSelected {
					prefix = "> "
					style = p.theme.Primary.Bold(true)
				}
				label := workspaceModalDisplayThemeLabel(option)
				DrawText(s, rect.X+4, rowY, rect.W-6, style, clampEllipsis(prefix+label, rect.W-6))
				rowY++
			}
		}
	}
	for i, line := range footerLines {
		DrawText(s, rect.X+2, footerY+i, rect.W-4, p.theme.TextMuted, line)
	}
}

func workspaceModalThemeOptions() []string {
	catalog := ThemeCatalog()
	out := make([]string, 0, len(catalog)+1)
	out = append(out, "inherit")
	for _, item := range catalog {
		out = append(out, item.ID)
	}
	return out
}

func workspaceModalNormalizeThemeID(raw string) string {
	themeID := NormalizeThemeID(raw)
	if themeID == "" {
		return "inherit"
	}
	return themeID
}

func workspaceModalDisplayThemeLabel(raw string) string {
	normalized := workspaceModalNormalizeThemeID(raw)
	if normalized == "inherit" {
		return "inherit (global)"
	}
	if option, ok := ResolveTheme(normalized); ok {
		return option.Name
	}
	return normalized
}

func workspaceModalInitialEditorIndex(fields []workspaceModalEditorField) int {
	for i, field := range fields {
		if field.Editable {
			return i
		}
	}
	return 0
}

func workspaceModalDefaultName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "workspace"
	}
	name := filepath.Base(trimmed)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "workspace"
	}
	return name
}

func workspaceModalBoolValue(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func parseWorkspaceModalBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "n", "no", "false", "0", "off":
		return false
	default:
		return true
	}
}

func boolLabel(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func workspaceModalTimeLabel(unixMillis int64) string {
	if unixMillis <= 0 {
		return "-"
	}
	return time.UnixMilli(unixMillis).Local().Format("2006-01-02 15:04")
}

func workspaceModalDisplayPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "."
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		home = filepath.Clean(home)
		trimmed = filepath.Clean(trimmed)
		if trimmed == home {
			return "~"
		}
		prefix := home + string(filepath.Separator)
		if strings.HasPrefix(trimmed, prefix) {
			return "~" + string(filepath.Separator) + strings.TrimPrefix(trimmed, prefix)
		}
	}
	return trimmed
}

func workspaceModalWrap(text string, width int) []string {
	lines := Wrap(strings.TrimSpace(text), width)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func workspaceModalEditorFieldLine(field workspaceModalEditorField, selected bool) string {
	value := strings.TrimSpace(field.Value)
	if value == "" {
		value = field.Placeholder
		if value == "" {
			value = "-"
		}
	} else {
		switch field.Key {
		case "path", "linked_directory", "directory_path":
			value = workspaceModalDisplayPath(value)
		case "theme_id":
			value = workspaceModalDisplayThemeLabel(value)
		}
	}
	prefix := "  "
	if selected {
		prefix = "> "
	}
	line := fmt.Sprintf("%s%s: %s", prefix, field.Label, value)
	if len(field.Options) > 0 {
		switch field.Key {
		case "theme_id":
			line += "  [picker]"
		default:
			line += "  [" + strings.Join(field.Options, "/") + "]"
		}
	} else if !field.Editable {
		line += "  [locked]"
	}
	return line
}
