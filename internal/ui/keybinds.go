package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
)

type KeybindID string

type KeybindDefinition struct {
	ID       KeybindID
	Group    string
	Action   string
	Default  string
	Aliases  []string
	Editable bool
}

const (
	KeybindGlobalQuit            KeybindID = "global.quit"
	KeybindGlobalReloadHome      KeybindID = "global.reload_home"
	KeybindGlobalToggleMouse     KeybindID = "global.toggle_mouse"
	KeybindGlobalOpenAgents      KeybindID = "global.open_agents"
	KeybindGlobalOpenModels      KeybindID = "global.open_models"
	KeybindGlobalCycleThinking   KeybindID = "global.cycle_thinking"
	KeybindGlobalCycleRoute      KeybindID = "global.cycle_route"
	KeybindGlobalVoiceInput      KeybindID = "global.voice_input"
	KeybindGlobalShowBackground  KeybindID = "global.show_background"
	KeybindGlobalWorkspacePrev   KeybindID = "global.workspace_prev"
	KeybindGlobalWorkspaceNext   KeybindID = "global.workspace_next"
	KeybindGlobalWorkspaceSlot1  KeybindID = "global.workspace_slot_1"
	KeybindGlobalWorkspaceSlot2  KeybindID = "global.workspace_slot_2"
	KeybindGlobalWorkspaceSlot3  KeybindID = "global.workspace_slot_3"
	KeybindGlobalWorkspaceSlot4  KeybindID = "global.workspace_slot_4"
	KeybindGlobalWorkspaceSlot5  KeybindID = "global.workspace_slot_5"
	KeybindGlobalWorkspaceSlot6  KeybindID = "global.workspace_slot_6"
	KeybindGlobalWorkspaceSlot7  KeybindID = "global.workspace_slot_7"
	KeybindGlobalWorkspaceSlot8  KeybindID = "global.workspace_slot_8"
	KeybindGlobalWorkspaceSlot9  KeybindID = "global.workspace_slot_9"
	KeybindGlobalWorkspaceSlot10 KeybindID = "global.workspace_slot_10"

	KeybindHomeSessionsEnterMode   KeybindID = "home.sessions.enter_mode"
	KeybindHomeSessionsExitMode    KeybindID = "home.sessions.exit_mode"
	KeybindHomeSessionsMoveUp      KeybindID = "home.sessions.move_up"
	KeybindHomeSessionsMoveDown    KeybindID = "home.sessions.move_down"
	KeybindHomeSessionsMoveUpAlt   KeybindID = "home.sessions.move_up_alt"
	KeybindHomeSessionsMoveDownAlt KeybindID = "home.sessions.move_down_alt"
	KeybindHomeSessionsOpen        KeybindID = "home.sessions.open"
	KeybindHomePaletteMoveUp       KeybindID = "home.palette.move_up"
	KeybindHomePaletteMoveDown     KeybindID = "home.palette.move_down"
	KeybindHomePromptBackspace     KeybindID = "home.prompt.backspace"
	KeybindHomePromptClear         KeybindID = "home.prompt.clear"
	KeybindHomePromptComplete      KeybindID = "home.prompt.complete"
	KeybindHomePromptInsertNewline KeybindID = "home.prompt.insert_newline"
	KeybindHomePromptSubmit        KeybindID = "home.prompt.submit"

	KeybindModalClose           KeybindID = "modal.close"
	KeybindModalFocusNext       KeybindID = "modal.focus_next"
	KeybindModalFocusPrev       KeybindID = "modal.focus_prev"
	KeybindModalFocusLeft       KeybindID = "modal.focus_left"
	KeybindModalFocusRight      KeybindID = "modal.focus_right"
	KeybindModalMoveUp          KeybindID = "modal.move_up"
	KeybindModalMoveDown        KeybindID = "modal.move_down"
	KeybindModalMoveUpAlt       KeybindID = "modal.move_up_alt"
	KeybindModalMoveDownAlt     KeybindID = "modal.move_down_alt"
	KeybindModalPageUp          KeybindID = "modal.page_up"
	KeybindModalPageDown        KeybindID = "modal.page_down"
	KeybindModalJumpHome        KeybindID = "modal.jump_home"
	KeybindModalJumpEnd         KeybindID = "modal.jump_end"
	KeybindModalSearchFocus     KeybindID = "modal.search_focus"
	KeybindModalSearchBackspace KeybindID = "modal.search_backspace"
	KeybindModalSearchClear     KeybindID = "modal.search_clear"
	KeybindModalEnter           KeybindID = "modal.enter"

	KeybindAuthFocusCredentialSearch KeybindID = "auth.focus_credential_search"
	KeybindAuthFocusProviders        KeybindID = "auth.focus_providers"
	KeybindAuthFocusCredentials      KeybindID = "auth.focus_credentials"
	KeybindAuthClearSearchAlt        KeybindID = "auth.clear_search_alt"
	KeybindAuthVerify                KeybindID = "auth.verify"
	KeybindAuthRefresh               KeybindID = "auth.refresh"
	KeybindAuthLogin                 KeybindID = "auth.login"
	KeybindAuthSetActive             KeybindID = "auth.set_active"
	KeybindAuthDelete                KeybindID = "auth.delete"
	KeybindAuthNewAPI                KeybindID = "auth.new_api"
	KeybindAuthNewOAuth              KeybindID = "auth.new_oauth"
	KeybindAuthEdit                  KeybindID = "auth.edit"

	KeybindWorkspaceFocusList       KeybindID = "workspace.focus_list"
	KeybindWorkspaceClearSearchAlt  KeybindID = "workspace.clear_search_alt"
	KeybindWorkspaceRefresh         KeybindID = "workspace.refresh"
	KeybindWorkspaceSaveCurrent     KeybindID = "workspace.save_current"
	KeybindWorkspaceNew             KeybindID = "workspace.new"
	KeybindWorkspaceActivate        KeybindID = "workspace.activate"
	KeybindWorkspaceEdit            KeybindID = "workspace.edit"
	KeybindWorkspaceLinkDirectory   KeybindID = "workspace.link_directory"
	KeybindWorkspaceUnlinkDirectory KeybindID = "workspace.unlink_directory"
	KeybindWorkspaceDelete          KeybindID = "workspace.delete"
	KeybindWorkspaceMoveUp          KeybindID = "workspace.move_up"
	KeybindWorkspaceMoveDown        KeybindID = "workspace.move_down"
	KeybindWorkspaceOpenKeybinds    KeybindID = "workspace.open_keybinds"

	KeybindVoiceRefresh KeybindID = "voice.refresh"
	KeybindVoiceTest    KeybindID = "voice.test"

	KeybindModelsFocusProviders        KeybindID = "models.focus_providers"
	KeybindModelsFocusModels           KeybindID = "models.focus_models"
	KeybindModelsRefresh               KeybindID = "models.refresh"
	KeybindModelsAddAuth               KeybindID = "models.add_auth"
	KeybindModelsToggleFavoritesFilter KeybindID = "models.toggle_favorites_filter"
	KeybindModelsToggleFavorite        KeybindID = "models.toggle_favorite"
	KeybindModelsThinkingOff           KeybindID = "models.thinking.off"
	KeybindModelsThinkingLow           KeybindID = "models.thinking.low"
	KeybindModelsThinkingMedium        KeybindID = "models.thinking.medium"
	KeybindModelsThinkingHigh          KeybindID = "models.thinking.high"
	KeybindModelsThinkingXHigh         KeybindID = "models.thinking.xhigh"

	KeybindAgentsFocusProfiles   KeybindID = "agents.focus_profiles"
	KeybindAgentsFocusDetails    KeybindID = "agents.focus_details"
	KeybindAgentsClearSearchAlt  KeybindID = "agents.clear_search_alt"
	KeybindAgentsRefresh         KeybindID = "agents.refresh"
	KeybindAgentsRestoreDefaults KeybindID = "agents.restore_defaults"
	KeybindAgentsResetDefaults   KeybindID = "agents.reset_defaults"
	KeybindAgentsActivate        KeybindID = "agents.activate"
	KeybindAgentsActivateAlt     KeybindID = "agents.activate_alt"
	KeybindAgentsDelete          KeybindID = "agents.delete"
	KeybindAgentsToggleEnabled   KeybindID = "agents.toggle_enabled"
	KeybindAgentsEdit            KeybindID = "agents.edit"
	KeybindAgentsEditAlt         KeybindID = "agents.edit_alt"
	KeybindAgentsNew             KeybindID = "agents.new"
	KeybindAgentsFilterAll       KeybindID = "agents.filter_all"
	KeybindAgentsFilterPrimary   KeybindID = "agents.filter_primary"
	KeybindAgentsFilterSubagent  KeybindID = "agents.filter_subagent"

	KeybindThemeJumpHomeAlt KeybindID = "theme.jump_home_alt"
	KeybindThemeJumpEndAlt  KeybindID = "theme.jump_end_alt"

	KeybindEditorClose               KeybindID = "editor.close"
	KeybindEditorFocusNext           KeybindID = "editor.focus_next"
	KeybindEditorFocusPrev           KeybindID = "editor.focus_prev"
	KeybindEditorMoveUp              KeybindID = "editor.move_up"
	KeybindEditorMoveDown            KeybindID = "editor.move_down"
	KeybindEditorMoveLeft            KeybindID = "editor.move_left"
	KeybindEditorMoveRight           KeybindID = "editor.move_right"
	KeybindEditorBackspace           KeybindID = "editor.backspace"
	KeybindEditorClear               KeybindID = "editor.clear"
	KeybindEditorSubmit              KeybindID = "editor.submit"
	KeybindAgentsEditorSave          KeybindID = "agents_editor.save"
	KeybindAgentsEditorInsertNewline KeybindID = "agents_editor.insert_newline"

	KeybindChatEscape               KeybindID = "chat.escape"
	KeybindChatMoveUp               KeybindID = "chat.move_up"
	KeybindChatMoveDown             KeybindID = "chat.move_down"
	KeybindChatMoveUpAlt            KeybindID = "chat.move_up_alt"
	KeybindChatMoveDownAlt          KeybindID = "chat.move_down_alt"
	KeybindChatPageUp               KeybindID = "chat.page_up"
	KeybindChatPageDown             KeybindID = "chat.page_down"
	KeybindChatJumpHome             KeybindID = "chat.jump_home"
	KeybindChatJumpEnd              KeybindID = "chat.jump_end"
	KeybindChatBackspace            KeybindID = "chat.backspace"
	KeybindChatClear                KeybindID = "chat.clear"
	KeybindChatUserVariantPrev      KeybindID = "chat.user_variant_prev"
	KeybindChatUserVariantNext      KeybindID = "chat.user_variant_next"
	KeybindChatAssistantVariantPrev KeybindID = "chat.assistant_variant_prev"
	KeybindChatAssistantVariantNext KeybindID = "chat.assistant_variant_next"
	KeybindChatCycleMode            KeybindID = "chat.cycle_mode"
	KeybindChatComplete             KeybindID = "chat.complete"
	KeybindChatInsertNewline        KeybindID = "chat.insert_newline"
	KeybindChatSubmit               KeybindID = "chat.submit"

	KeybindPermissionCycleMode    KeybindID = "permission.cycle_mode"
	KeybindPermissionMoveUp       KeybindID = "permission.move_up"
	KeybindPermissionMoveDown     KeybindID = "permission.move_down"
	KeybindPermissionMoveDownAlt  KeybindID = "permission.move_down_alt"
	KeybindPermissionBackspace    KeybindID = "permission.backspace"
	KeybindPermissionClear        KeybindID = "permission.clear"
	KeybindPermissionAlwaysAllow  KeybindID = "permission.always_allow"
	KeybindPermissionAlwaysDeny   KeybindID = "permission.always_deny"
	KeybindPermissionToggleBypass KeybindID = "permission.toggle_bypass"
	KeybindPermissionDeny         KeybindID = "permission.deny"
	KeybindPermissionApprove      KeybindID = "permission.approve"

	KeybindPlanExitCancel      KeybindID = "plan_exit.cancel"
	KeybindPlanExitToggle      KeybindID = "plan_exit.toggle"
	KeybindPlanExitToggleRight KeybindID = "plan_exit.toggle_right"
	KeybindPlanExitToggleLeft  KeybindID = "plan_exit.toggle_left"
	KeybindPlanExitMoveUp      KeybindID = "plan_exit.move_up"
	KeybindPlanExitMoveDown    KeybindID = "plan_exit.move_down"
	KeybindPlanExitMoveUpAlt   KeybindID = "plan_exit.move_up_alt"
	KeybindPlanExitMoveDownAlt KeybindID = "plan_exit.move_down_alt"
	KeybindPlanExitPageUp      KeybindID = "plan_exit.page_up"
	KeybindPlanExitPageDown    KeybindID = "plan_exit.page_down"
	KeybindPlanExitJumpHome    KeybindID = "plan_exit.jump_home"
	KeybindPlanExitJumpEnd     KeybindID = "plan_exit.jump_end"
	KeybindPlanExitConfirm     KeybindID = "plan_exit.confirm"

	KeybindKeybindsModalClose       KeybindID = "keybinds_modal.close"
	KeybindKeybindsModalMoveUp      KeybindID = "keybinds_modal.move_up"
	KeybindKeybindsModalMoveDown    KeybindID = "keybinds_modal.move_down"
	KeybindKeybindsModalMoveUpAlt   KeybindID = "keybinds_modal.move_up_alt"
	KeybindKeybindsModalMoveDownAlt KeybindID = "keybinds_modal.move_down_alt"
	KeybindKeybindsModalEdit        KeybindID = "keybinds_modal.edit"
	KeybindKeybindsModalReset       KeybindID = "keybinds_modal.reset"
	KeybindKeybindsModalResetAll    KeybindID = "keybinds_modal.reset_all"
	KeybindKeybindsModalCancelEdit  KeybindID = "keybinds_modal.cancel_edit"
)

var keybindDefinitions = []KeybindDefinition{
	{ID: KeybindGlobalQuit, Group: "Global", Action: "Quit", Default: "ctrl+c", Editable: true},
	{ID: KeybindGlobalReloadHome, Group: "Global", Action: "Reload home", Default: "ctrl+r", Editable: true},
	{ID: KeybindGlobalToggleMouse, Group: "Global", Action: "Toggle mouse capture", Default: "f8", Editable: true},
	{ID: KeybindGlobalOpenAgents, Group: "Global", Action: "Open agents modal", Default: "ctrl+a", Aliases: []string{"alt+a"}, Editable: true},
	{ID: KeybindGlobalOpenModels, Group: "Global", Action: "Open models modal", Default: "ctrl+m", Aliases: []string{"alt+m", "ctrl+enter"}, Editable: true},
	{ID: KeybindGlobalCycleThinking, Group: "Global", Action: "Cycle thinking", Default: "ctrl+t", Editable: true},
	{ID: KeybindGlobalCycleRoute, Group: "Global", Action: "Cycle chat route", Default: "alt+r", Editable: true},
	{ID: KeybindGlobalVoiceInput, Group: "Global", Action: "Voice input capture", Default: "f9", Editable: true},
	{ID: KeybindGlobalShowBackground, Group: "Global", Action: "Go home", Default: "ctrl+b", Editable: true},
	{ID: KeybindGlobalWorkspacePrev, Group: "Global", Action: "Cycle workspace previous", Default: "", Editable: true},
	{ID: KeybindGlobalWorkspaceNext, Group: "Global", Action: "Cycle workspace next", Default: "", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot1, Group: "Global", Action: "Activate workspace slot 1", Default: "alt+1", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot2, Group: "Global", Action: "Activate workspace slot 2", Default: "alt+2", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot3, Group: "Global", Action: "Activate workspace slot 3", Default: "alt+3", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot4, Group: "Global", Action: "Activate workspace slot 4", Default: "alt+4", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot5, Group: "Global", Action: "Activate workspace slot 5", Default: "alt+5", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot6, Group: "Global", Action: "Activate workspace slot 6", Default: "alt+6", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot7, Group: "Global", Action: "Activate workspace slot 7", Default: "alt+7", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot8, Group: "Global", Action: "Activate workspace slot 8", Default: "alt+8", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot9, Group: "Global", Action: "Activate workspace slot 9", Default: "alt+9", Editable: true},
	{ID: KeybindGlobalWorkspaceSlot10, Group: "Global", Action: "Activate workspace slot 10", Default: "alt+0", Editable: true},

	{ID: KeybindHomeSessionsEnterMode, Group: "Home", Action: "Sessions mode on", Default: "ctrl+down", Editable: true},
	{ID: KeybindHomeSessionsExitMode, Group: "Home", Action: "Sessions mode off", Default: "ctrl+up", Editable: true},
	{ID: KeybindHomeSessionsMoveUp, Group: "Home", Action: "Sessions move up", Default: "up", Editable: true},
	{ID: KeybindHomeSessionsMoveDown, Group: "Home", Action: "Sessions move down", Default: "down", Editable: true},
	{ID: KeybindHomeSessionsMoveUpAlt, Group: "Home", Action: "Sessions move up (alt)", Default: "alt+k", Editable: true},
	{ID: KeybindHomeSessionsMoveDownAlt, Group: "Home", Action: "Sessions move down (alt)", Default: "alt+j", Editable: true},
	{ID: KeybindHomeSessionsOpen, Group: "Home", Action: "Open selected session", Default: "enter", Editable: true},
	{ID: KeybindHomePaletteMoveUp, Group: "Home", Action: "Command palette up", Default: "up", Editable: true},
	{ID: KeybindHomePaletteMoveDown, Group: "Home", Action: "Command palette down", Default: "down", Editable: true},
	{ID: KeybindHomePromptBackspace, Group: "Home", Action: "Prompt backspace", Default: "backspace", Editable: true},
	{ID: KeybindHomePromptClear, Group: "Home", Action: "Prompt clear", Default: "ctrl+u", Editable: true},
	{ID: KeybindHomePromptComplete, Group: "Home", Action: "Prompt complete", Default: "tab", Editable: true},
	{ID: KeybindHomePromptInsertNewline, Group: "Home", Action: "Insert prompt newline", Default: "ctrl+j", Editable: true},
	{ID: KeybindHomePromptSubmit, Group: "Home", Action: "Prompt submit", Default: "enter", Editable: true},

	{ID: KeybindModalClose, Group: "Modal", Action: "Close modal", Default: "esc", Editable: true},
	{ID: KeybindModalFocusNext, Group: "Modal", Action: "Focus next", Default: "tab", Editable: true},
	{ID: KeybindModalFocusPrev, Group: "Modal", Action: "Focus previous", Default: "shift+tab", Editable: true},
	{ID: KeybindModalFocusLeft, Group: "Modal", Action: "Focus left", Default: "left", Editable: true},
	{ID: KeybindModalFocusRight, Group: "Modal", Action: "Focus right", Default: "right", Editable: true},
	{ID: KeybindModalMoveUp, Group: "Modal", Action: "Move up", Default: "up", Editable: true},
	{ID: KeybindModalMoveDown, Group: "Modal", Action: "Move down", Default: "down", Editable: true},
	{ID: KeybindModalMoveUpAlt, Group: "Modal", Action: "Move up (alt)", Default: "alt+k", Editable: true},
	{ID: KeybindModalMoveDownAlt, Group: "Modal", Action: "Move down (alt)", Default: "alt+j", Editable: true},
	{ID: KeybindModalPageUp, Group: "Modal", Action: "Page up", Default: "pgup", Editable: true},
	{ID: KeybindModalPageDown, Group: "Modal", Action: "Page down", Default: "pgdn", Editable: true},
	{ID: KeybindModalJumpHome, Group: "Modal", Action: "Jump home", Default: "home", Editable: true},
	{ID: KeybindModalJumpEnd, Group: "Modal", Action: "Jump end", Default: "end", Editable: true},
	{ID: KeybindModalSearchFocus, Group: "Modal", Action: "Focus search", Default: "/", Editable: true},
	{ID: KeybindModalSearchBackspace, Group: "Modal", Action: "Search backspace", Default: "backspace", Editable: true},
	{ID: KeybindModalSearchClear, Group: "Modal", Action: "Search clear", Default: "ctrl+u", Editable: true},
	{ID: KeybindModalEnter, Group: "Modal", Action: "Confirm/enter", Default: "enter", Editable: true},

	{ID: KeybindAuthFocusCredentialSearch, Group: "Auth Modal", Action: "Focus credential search", Default: "f", Editable: true},
	{ID: KeybindAuthFocusProviders, Group: "Auth Modal", Action: "Focus providers", Default: "p", Editable: true},
	{ID: KeybindAuthFocusCredentials, Group: "Auth Modal", Action: "Focus credentials", Default: "c", Editable: true},
	{ID: KeybindAuthClearSearchAlt, Group: "Auth Modal", Action: "Clear search (alt)", Default: "x", Editable: true},
	{ID: KeybindAuthVerify, Group: "Auth Modal", Action: "Verify Copilot auth", Default: "v", Editable: true},
	{ID: KeybindAuthRefresh, Group: "Auth Modal", Action: "Refresh", Default: "r", Editable: true},
	{ID: KeybindAuthLogin, Group: "Auth Modal", Action: "Login", Default: "l", Editable: true},
	{ID: KeybindAuthSetActive, Group: "Auth Modal", Action: "Set active credential", Default: "a", Editable: true},
	{ID: KeybindAuthDelete, Group: "Auth Modal", Action: "Delete credential", Default: "d", Editable: true},
	{ID: KeybindAuthNewAPI, Group: "Auth Modal", Action: "New API credential", Default: "n", Editable: true},
	{ID: KeybindAuthNewOAuth, Group: "Auth Modal", Action: "New OAuth credential", Default: "o", Editable: true},
	{ID: KeybindAuthEdit, Group: "Auth Modal", Action: "Edit credential", Default: "e", Editable: true},

	{ID: KeybindWorkspaceFocusList, Group: "Workspace Modal", Action: "Focus list", Default: "w", Editable: true},
	{ID: KeybindWorkspaceClearSearchAlt, Group: "Workspace Modal", Action: "Clear search (alt)", Default: "x", Editable: true},
	{ID: KeybindWorkspaceRefresh, Group: "Workspace Modal", Action: "Refresh", Default: "r", Editable: true},
	{ID: KeybindWorkspaceSaveCurrent, Group: "Workspace Modal", Action: "Save current directory", Default: "s", Editable: true},
	{ID: KeybindWorkspaceNew, Group: "Workspace Modal", Action: "Open new editor", Default: "n", Editable: true},
	{ID: KeybindWorkspaceActivate, Group: "Workspace Modal", Action: "Activate selected", Default: "a", Editable: true},
	{ID: KeybindWorkspaceEdit, Group: "Workspace Modal", Action: "Edit selected", Default: "e", Editable: true},
	{ID: KeybindWorkspaceLinkDirectory, Group: "Workspace Modal", Action: "Link directory", Default: "l", Editable: true},
	{ID: KeybindWorkspaceUnlinkDirectory, Group: "Workspace Modal", Action: "Unlink directory", Default: "u", Editable: true},
	{ID: KeybindWorkspaceDelete, Group: "Workspace Modal", Action: "Delete selected", Default: "d", Editable: true},
	{ID: KeybindWorkspaceMoveUp, Group: "Workspace Modal", Action: "Move selected up", Default: "shift+k", Editable: true},
	{ID: KeybindWorkspaceMoveDown, Group: "Workspace Modal", Action: "Move selected down", Default: "shift+j", Editable: true},
	{ID: KeybindWorkspaceOpenKeybinds, Group: "Workspace Modal", Action: "Open keybinds editor", Default: "k", Editable: true},

	{ID: KeybindVoiceRefresh, Group: "Voice Modal", Action: "Refresh", Default: "r", Editable: true},
	{ID: KeybindVoiceTest, Group: "Voice Modal", Action: "Quick test", Default: "t", Editable: true},

	{ID: KeybindModelsFocusProviders, Group: "Models Modal", Action: "Focus providers", Default: "p", Editable: true},
	{ID: KeybindModelsFocusModels, Group: "Models Modal", Action: "Focus models", Default: "m", Editable: true},
	{ID: KeybindModelsRefresh, Group: "Models Modal", Action: "Refresh", Default: "r", Editable: true},
	{ID: KeybindModelsAddAuth, Group: "Models Modal", Action: "Add auth for provider", Default: "n", Editable: true},
	{ID: KeybindModelsToggleFavoritesFilter, Group: "Models Modal", Action: "Toggle favorites filter", Default: "f", Editable: true},
	{ID: KeybindModelsToggleFavorite, Group: "Models Modal", Action: "Toggle favorite for model", Default: "a", Editable: true},
	{ID: KeybindModelsThinkingOff, Group: "Models Modal", Action: "Thinking preset off", Default: "1", Editable: true},
	{ID: KeybindModelsThinkingLow, Group: "Models Modal", Action: "Thinking preset low", Default: "2", Editable: true},
	{ID: KeybindModelsThinkingMedium, Group: "Models Modal", Action: "Thinking preset medium", Default: "3", Editable: true},
	{ID: KeybindModelsThinkingHigh, Group: "Models Modal", Action: "Thinking preset high", Default: "4", Editable: true},
	{ID: KeybindModelsThinkingXHigh, Group: "Models Modal", Action: "Thinking preset xhigh", Default: "5", Editable: true},

	{ID: KeybindAgentsFocusProfiles, Group: "Agents Modal", Action: "Focus profiles", Default: "w", Editable: true},
	{ID: KeybindAgentsFocusDetails, Group: "Agents Modal", Action: "Focus details", Default: "l", Editable: true},
	{ID: KeybindAgentsClearSearchAlt, Group: "Agents Modal", Action: "Clear search (alt)", Default: "x", Editable: true},
	{ID: KeybindAgentsRefresh, Group: "Agents Modal", Action: "Refresh", Default: "r", Editable: true},
	{ID: KeybindAgentsRestoreDefaults, Group: "Agents Modal", Action: "Set Utility AI", Default: "shift+r", Editable: true},
	{ID: KeybindAgentsResetDefaults, Group: "Agents Modal", Action: "Reset all to defaults", Default: "shift+z", Editable: true},
	{ID: KeybindAgentsActivate, Group: "Agents Modal", Action: "Activate selected primary", Default: "a", Editable: true},
	{ID: KeybindAgentsActivateAlt, Group: "Agents Modal", Action: "Activate selected primary (alt)", Default: "u", Editable: true},
	{ID: KeybindAgentsDelete, Group: "Agents Modal", Action: "Delete selected", Default: "d", Editable: true},
	{ID: KeybindAgentsToggleEnabled, Group: "Agents Modal", Action: "Toggle selected enabled", Default: "t", Editable: true},
	{ID: KeybindAgentsEdit, Group: "Agents Modal", Action: "Edit selected", Default: "e", Editable: true},
	{ID: KeybindAgentsEditAlt, Group: "Agents Modal", Action: "Edit selected (alt)", Default: "p", Editable: true},
	{ID: KeybindAgentsNew, Group: "Agents Modal", Action: "Create new agent", Default: "n", Editable: true},
	{ID: KeybindAgentsFilterAll, Group: "Agents Modal", Action: "Filter all", Default: "0", Editable: true},
	{ID: KeybindAgentsFilterPrimary, Group: "Agents Modal", Action: "Filter primary", Default: "1", Editable: true},
	{ID: KeybindAgentsFilterSubagent, Group: "Agents Modal", Action: "Filter subagent", Default: "2", Editable: true},

	{ID: KeybindThemeJumpHomeAlt, Group: "Theme Modal", Action: "Jump home (alt)", Default: "g", Editable: true},
	{ID: KeybindThemeJumpEndAlt, Group: "Theme Modal", Action: "Jump end (alt)", Default: "shift+g", Editable: true},

	{ID: KeybindEditorClose, Group: "Editors", Action: "Close editor", Default: "esc", Editable: true},
	{ID: KeybindEditorFocusNext, Group: "Editors", Action: "Focus next field", Default: "tab", Editable: true},
	{ID: KeybindEditorFocusPrev, Group: "Editors", Action: "Focus previous field", Default: "shift+tab", Editable: true},
	{ID: KeybindEditorMoveUp, Group: "Editors", Action: "Move up", Default: "up", Editable: true},
	{ID: KeybindEditorMoveDown, Group: "Editors", Action: "Move down", Default: "down", Editable: true},
	{ID: KeybindEditorMoveLeft, Group: "Editors", Action: "Move left", Default: "left", Editable: true},
	{ID: KeybindEditorMoveRight, Group: "Editors", Action: "Move right", Default: "right", Editable: true},
	{ID: KeybindEditorBackspace, Group: "Editors", Action: "Backspace", Default: "backspace", Editable: true},
	{ID: KeybindEditorClear, Group: "Editors", Action: "Clear field", Default: "ctrl+u", Editable: true},
	{ID: KeybindEditorSubmit, Group: "Editors", Action: "Submit/next", Default: "enter", Editable: true},
	{ID: KeybindAgentsEditorSave, Group: "Editors", Action: "Save profile changes", Default: "ctrl+y", Editable: true},
	{ID: KeybindAgentsEditorInsertNewline, Group: "Editors", Action: "Insert prompt newline", Default: "ctrl+j", Editable: true},

	{ID: KeybindChatEscape, Group: "Chat", Action: "Escape / leave chat", Default: "esc", Editable: true},
	{ID: KeybindChatMoveUp, Group: "Chat", Action: "Timeline up", Default: "up", Editable: true},
	{ID: KeybindChatMoveDown, Group: "Chat", Action: "Timeline down", Default: "down", Editable: true},
	{ID: KeybindChatMoveUpAlt, Group: "Chat", Action: "Timeline up (alt)", Default: "alt+k", Editable: true},
	{ID: KeybindChatMoveDownAlt, Group: "Chat", Action: "Timeline down (alt)", Default: "alt+j", Editable: true},
	{ID: KeybindChatPageUp, Group: "Chat", Action: "Timeline page up", Default: "pgup", Editable: true},
	{ID: KeybindChatPageDown, Group: "Chat", Action: "Timeline page down", Default: "pgdn", Editable: true},
	{ID: KeybindChatJumpHome, Group: "Chat", Action: "Timeline top", Default: "home", Editable: true},
	{ID: KeybindChatJumpEnd, Group: "Chat", Action: "Timeline bottom", Default: "end", Editable: true},
	{ID: KeybindChatBackspace, Group: "Chat", Action: "Input backspace", Default: "backspace", Editable: true},
	{ID: KeybindChatClear, Group: "Chat", Action: "Input clear", Default: "ctrl+u", Editable: true},
	{ID: KeybindChatUserVariantPrev, Group: "Chat", Action: "User diff variant previous", Default: "f4", Editable: true},
	{ID: KeybindChatUserVariantNext, Group: "Chat", Action: "User diff variant next", Default: "f5", Editable: true},
	{ID: KeybindChatAssistantVariantPrev, Group: "Chat", Action: "Assistant diff variant previous", Default: "f6", Editable: true},
	{ID: KeybindChatAssistantVariantNext, Group: "Chat", Action: "Assistant diff variant next", Default: "f7", Editable: true},
	{ID: KeybindChatCycleMode, Group: "Chat", Action: "Cycle mode", Default: "shift+tab", Editable: true},
	{ID: KeybindChatComplete, Group: "Chat", Action: "Complete command/preset", Default: "tab", Editable: true},
	{ID: KeybindChatInsertNewline, Group: "Chat", Action: "Insert input newline", Default: "ctrl+j", Editable: true},
	{ID: KeybindChatSubmit, Group: "Chat", Action: "Submit input", Default: "enter", Editable: true},

	{ID: KeybindPermissionCycleMode, Group: "Permissions", Action: "Cycle session mode", Default: "shift+tab", Editable: true},
	{ID: KeybindPermissionMoveUp, Group: "Permissions", Action: "Move selection up", Default: "up", Editable: true},
	{ID: KeybindPermissionMoveDown, Group: "Permissions", Action: "Move selection down", Default: "down", Editable: true},
	{ID: KeybindPermissionMoveDownAlt, Group: "Permissions", Action: "Move selection down (alt)", Default: "tab", Editable: true},
	{ID: KeybindPermissionBackspace, Group: "Permissions", Action: "Reason backspace", Default: "backspace", Editable: true},
	{ID: KeybindPermissionClear, Group: "Permissions", Action: "Reason clear", Default: "ctrl+u", Editable: true},
	{ID: KeybindPermissionAlwaysAllow, Group: "Permissions", Action: "Always allow selected", Default: "ctrl+a", Editable: true},
	{ID: KeybindPermissionAlwaysDeny, Group: "Permissions", Action: "Always deny selected", Default: "ctrl+d", Editable: true},
	{ID: KeybindPermissionToggleBypass, Group: "Permissions", Action: "Toggle global permissions", Default: "b", Editable: true},
	{ID: KeybindPermissionDeny, Group: "Permissions", Action: "Deny selected", Default: "esc", Editable: true},
	{ID: KeybindPermissionApprove, Group: "Permissions", Action: "Approve selected", Default: "enter", Editable: true},

	{ID: KeybindPlanExitCancel, Group: "Plan Exit", Action: "Cancel", Default: "esc", Editable: true},
	{ID: KeybindPlanExitToggle, Group: "Plan Exit", Action: "Toggle button", Default: "tab", Editable: true},
	{ID: KeybindPlanExitToggleRight, Group: "Plan Exit", Action: "Toggle right", Default: "right", Editable: true},
	{ID: KeybindPlanExitToggleLeft, Group: "Plan Exit", Action: "Toggle left", Default: "left", Editable: true},
	{ID: KeybindPlanExitMoveUp, Group: "Plan Exit", Action: "Scroll up", Default: "up", Editable: true},
	{ID: KeybindPlanExitMoveDown, Group: "Plan Exit", Action: "Scroll down", Default: "down", Editable: true},
	{ID: KeybindPlanExitMoveUpAlt, Group: "Plan Exit", Action: "Scroll up (alt)", Default: "alt+k", Editable: true},
	{ID: KeybindPlanExitMoveDownAlt, Group: "Plan Exit", Action: "Scroll down (alt)", Default: "alt+j", Editable: true},
	{ID: KeybindPlanExitPageUp, Group: "Plan Exit", Action: "Page up", Default: "pgup", Editable: true},
	{ID: KeybindPlanExitPageDown, Group: "Plan Exit", Action: "Page down", Default: "pgdn", Editable: true},
	{ID: KeybindPlanExitJumpHome, Group: "Plan Exit", Action: "Jump top", Default: "home", Editable: true},
	{ID: KeybindPlanExitJumpEnd, Group: "Plan Exit", Action: "Jump bottom", Default: "end", Editable: true},
	{ID: KeybindPlanExitConfirm, Group: "Plan Exit", Action: "Confirm", Default: "enter", Editable: true},

	{ID: KeybindKeybindsModalClose, Group: "Keybinds Modal", Action: "Close", Default: "esc", Editable: true},
	{ID: KeybindKeybindsModalMoveUp, Group: "Keybinds Modal", Action: "Move up", Default: "up", Editable: true},
	{ID: KeybindKeybindsModalMoveDown, Group: "Keybinds Modal", Action: "Move down", Default: "down", Editable: true},
	{ID: KeybindKeybindsModalMoveUpAlt, Group: "Keybinds Modal", Action: "Move up (alt)", Default: "alt+k", Editable: true},
	{ID: KeybindKeybindsModalMoveDownAlt, Group: "Keybinds Modal", Action: "Move down (alt)", Default: "alt+j", Editable: true},
	{ID: KeybindKeybindsModalEdit, Group: "Keybinds Modal", Action: "Edit selected", Default: "enter", Editable: true},
	{ID: KeybindKeybindsModalReset, Group: "Keybinds Modal", Action: "Reset selected", Default: "r", Editable: true},
	{ID: KeybindKeybindsModalResetAll, Group: "Keybinds Modal", Action: "Reset all", Default: "shift+r", Editable: true},
	{ID: KeybindKeybindsModalCancelEdit, Group: "Keybinds Modal", Action: "Cancel edit", Default: "esc", Editable: true},
}

var keybindDefinitionIndex = buildKeybindDefinitionIndex()

const WorkspaceSlotCount = 10

var workspaceSlotKeybindIDs = [...]KeybindID{
	KeybindGlobalWorkspaceSlot1,
	KeybindGlobalWorkspaceSlot2,
	KeybindGlobalWorkspaceSlot3,
	KeybindGlobalWorkspaceSlot4,
	KeybindGlobalWorkspaceSlot5,
	KeybindGlobalWorkspaceSlot6,
	KeybindGlobalWorkspaceSlot7,
	KeybindGlobalWorkspaceSlot8,
	KeybindGlobalWorkspaceSlot9,
	KeybindGlobalWorkspaceSlot10,
}

type KeyBindings struct {
	values map[KeybindID]string
}

func NewDefaultKeyBindings() *KeyBindings {
	k := &KeyBindings{values: make(map[KeybindID]string, len(keybindDefinitions))}
	for _, def := range keybindDefinitions {
		token, err := NormalizeKeybindToken(def.Default)
		if err != nil {
			continue
		}
		k.values[def.ID] = token
	}
	return k
}

func (k *KeyBindings) Clone() *KeyBindings {
	if k == nil {
		return NewDefaultKeyBindings()
	}
	out := &KeyBindings{values: make(map[KeybindID]string, len(k.values))}
	for id, token := range k.values {
		out.values[id] = token
	}
	return out
}

func (k *KeyBindings) Reset(id KeybindID) {
	def, ok := keybindDefinitionIndex[id]
	if !ok {
		return
	}
	token, err := NormalizeKeybindToken(def.Default)
	if err != nil {
		return
	}
	if k == nil {
		return
	}
	if k.values == nil {
		k.values = make(map[KeybindID]string, len(keybindDefinitions))
	}
	k.values[id] = token
}

func (k *KeyBindings) ResetAll() {
	if k == nil {
		return
	}
	k.values = make(map[KeybindID]string, len(keybindDefinitions))
	for _, def := range keybindDefinitions {
		token, err := NormalizeKeybindToken(def.Default)
		if err != nil {
			continue
		}
		k.values[def.ID] = token
	}
}

func (k *KeyBindings) Set(id KeybindID, raw string) error {
	if k == nil {
		return fmt.Errorf("keybindings are unavailable")
	}
	if _, ok := keybindDefinitionIndex[id]; !ok {
		return fmt.Errorf("unknown keybind: %s", id)
	}
	if k.values == nil {
		k.values = make(map[KeybindID]string, len(keybindDefinitions))
	}
	if strings.TrimSpace(raw) == "" {
		k.values[id] = ""
		return nil
	}
	token, err := NormalizeKeybindToken(raw)
	if err != nil {
		return err
	}
	if err := validateEditableKeybindToken(token); err != nil {
		return err
	}
	k.values[id] = token
	return nil
}

func (k *KeyBindings) SetFromEvent(id KeybindID, ev *tcell.EventKey) error {
	token, ok := KeybindTokenFromEvent(ev)
	if !ok {
		return fmt.Errorf("unsupported key")
	}
	return k.Set(id, token)
}

func (k *KeyBindings) Token(id KeybindID) string {
	if k == nil {
		return defaultKeybindToken(id)
	}
	if token, ok := k.values[id]; ok {
		return token
	}
	return defaultKeybindToken(id)
}

func (k *KeyBindings) Label(id KeybindID) string {
	return FormatKeybindToken(k.Token(id))
}

func (k *KeyBindings) Match(ev *tcell.EventKey, id KeybindID) bool {
	token, ok := KeybindTokenFromEvent(ev)
	if !ok {
		return false
	}
	bound := k.Token(id)
	if bound != "" && token == bound {
		return true
	}
	if def, ok := keybindDefinitionIndex[id]; ok {
		for _, alias := range def.Aliases {
			if alias == token {
				return true
			}
		}
	}
	return false
}

func (k *KeyBindings) MatchAny(ev *tcell.EventKey, ids ...KeybindID) bool {
	for _, id := range ids {
		if k.Match(ev, id) {
			return true
		}
	}
	return false
}

func (k *KeyBindings) ApplyOverrides(overrides map[string]string) {
	if k == nil || len(overrides) == 0 {
		return
	}
	for rawID, rawToken := range overrides {
		id := KeybindID(strings.TrimSpace(rawID))
		if _, ok := keybindDefinitionIndex[id]; !ok {
			continue
		}
		if err := k.Set(id, rawToken); err != nil {
			continue
		}
	}
}

func (k *KeyBindings) SerializeOverrides() map[string]string {
	out := make(map[string]string, len(keybindDefinitions))
	if k == nil {
		return out
	}
	for _, def := range keybindDefinitions {
		current := k.Token(def.ID)
		defaultToken := defaultKeybindToken(def.ID)
		if current == defaultToken {
			continue
		}
		out[string(def.ID)] = current
	}
	return out
}

func KeybindDefinitions() []KeybindDefinition {
	out := make([]KeybindDefinition, 0, len(keybindDefinitions))
	for _, def := range keybindDefinitions {
		copyDef := def
		copyDef.Aliases = append([]string(nil), def.Aliases...)
		out = append(out, copyDef)
	}
	return out
}

func LookupKeybindDefinition(id KeybindID) (KeybindDefinition, bool) {
	def, ok := keybindDefinitionIndex[id]
	if !ok {
		return KeybindDefinition{}, false
	}
	copyDef := def
	copyDef.Aliases = append([]string(nil), def.Aliases...)
	return copyDef, true
}

func WorkspaceSlotKeybindID(slot int) (KeybindID, bool) {
	if slot < 1 || slot > len(workspaceSlotKeybindIDs) {
		return "", false
	}
	return workspaceSlotKeybindIDs[slot-1], true
}

func KeybindTokenFromEvent(ev *tcell.EventKey) (string, bool) {
	if ev == nil {
		return "", false
	}

	modsCtrl := ev.Modifiers()&tcell.ModCtrl != 0
	modsAlt := ev.Modifiers()&tcell.ModAlt != 0
	modsShift := ev.Modifiers()&tcell.ModShift != 0

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if !unicode.IsPrint(r) && r != ' ' {
			return "", false
		}
		if unicode.IsUpper(r) && unicode.IsLetter(r) {
			modsShift = true
			r = unicode.ToLower(r)
		}
		base := ""
		if r == ' ' {
			base = "space"
		} else {
			base = string(r)
		}
		token := composeKeybindToken(base, modsCtrl, modsAlt, modsShift)
		normalized, err := NormalizeKeybindToken(token)
		if err != nil {
			return "", false
		}
		return normalized, true
	}

	base := keybindBaseForKey(ev.Key())
	if base != "" {
		if ev.Key() == tcell.KeyBacktab {
			modsShift = true
		}
		if ev.Key() == tcell.KeyCtrlSpace {
			modsCtrl = true
		}
		token := composeKeybindToken(base, modsCtrl, modsAlt, modsShift)
		normalized, err := NormalizeKeybindToken(token)
		if err != nil {
			return "", false
		}
		return normalized, true
	}

	if ev.Key() >= tcell.KeyCtrlA && ev.Key() <= tcell.KeyCtrlZ {
		if ev.Key() == tcell.KeyCtrlM && !modsCtrl {
			token := composeKeybindToken("enter", false, modsAlt, modsShift)
			normalized, err := NormalizeKeybindToken(token)
			if err != nil {
				return "", false
			}
			return normalized, true
		}
		letter := rune('a' + (ev.Key() - tcell.KeyCtrlA))
		token := composeKeybindToken(string(letter), true, modsAlt, modsShift)
		normalized, err := NormalizeKeybindToken(token)
		if err != nil {
			return "", false
		}
		return normalized, true
	}

	return "", false
}

func NormalizeKeybindToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.EqualFold(raw, "backtab") {
		raw = "shift+tab"
	}
	parts := strings.Split(raw, "+")
	modsCtrl := false
	modsAlt := false
	modsShift := false
	base := ""

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", fmt.Errorf("invalid keybind: %q", raw)
		}
		lower := strings.ToLower(part)
		switch lower {
		case "ctrl", "control", "ctl":
			modsCtrl = true
			continue
		case "alt", "meta", "option":
			modsAlt = true
			continue
		case "shift":
			modsShift = true
			continue
		}
		if i != len(parts)-1 || base != "" {
			return "", fmt.Errorf("invalid keybind: %q", raw)
		}
		base = normalizeKeybindBase(part)
	}

	if base == "" {
		return "", fmt.Errorf("invalid keybind: %q", raw)
	}
	if len(base) == 1 {
		r := []rune(base)[0]
		if unicode.IsUpper(r) && unicode.IsLetter(r) {
			modsShift = true
			base = string(unicode.ToLower(r))
		}
	}

	return composeKeybindToken(base, modsCtrl, modsAlt, modsShift), nil
}

func validateEditableKeybindToken(token string) error {
	if token == "enter" {
		return fmt.Errorf("Enter cannot be assigned as a custom keybind; use reset to restore default Enter actions")
	}
	return nil
}

func FormatKeybindToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "Unbound"
	}
	parts := strings.Split(token, "+")
	if len(parts) == 0 {
		return "Unbound"
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		switch part {
		case "ctrl":
			out = append(out, "Ctrl")
		case "alt":
			out = append(out, "Alt")
		case "shift":
			out = append(out, "Shift")
		case "esc":
			out = append(out, "Esc")
		case "enter":
			out = append(out, "Enter")
		case "tab":
			out = append(out, "Tab")
		case "up":
			out = append(out, "Up")
		case "down":
			out = append(out, "Down")
		case "left":
			out = append(out, "Left")
		case "right":
			out = append(out, "Right")
		case "pgup":
			out = append(out, "PgUp")
		case "pgdn":
			out = append(out, "PgDn")
		case "home":
			out = append(out, "Home")
		case "end":
			out = append(out, "End")
		case "backspace":
			out = append(out, "Backspace")
		case "delete":
			out = append(out, "Delete")
		case "space":
			out = append(out, "Space")
		default:
			if strings.HasPrefix(part, "f") {
				num := strings.TrimPrefix(part, "f")
				if num != "" {
					out = append(out, "F"+num)
					continue
				}
			}
			if len(part) == 1 {
				r := []rune(part)[0]
				if unicode.IsLetter(r) {
					out = append(out, strings.ToUpper(part))
				} else {
					out = append(out, part)
				}
				continue
			}
			out = append(out, part)
		}
	}
	return strings.Join(out, "+")
}

func defaultKeybindToken(id KeybindID) string {
	def, ok := keybindDefinitionIndex[id]
	if !ok {
		return ""
	}
	return def.Default
}

func composeKeybindToken(base string, modsCtrl, modsAlt, modsShift bool) string {
	parts := make([]string, 0, 4)
	if modsCtrl {
		parts = append(parts, "ctrl")
	}
	if modsAlt {
		parts = append(parts, "alt")
	}
	if modsShift {
		parts = append(parts, "shift")
	}
	parts = append(parts, strings.ToLower(strings.TrimSpace(base)))
	return strings.Join(parts, "+")
}

func normalizeKeybindBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "escape":
		return "esc"
	case "return":
		return "enter"
	case "pageup":
		return "pgup"
	case "pagedown", "pgdown":
		return "pgdn"
	case "del":
		return "delete"
	case "bs":
		return "backspace"
	case "space", "spc":
		return "space"
	}
	if len(raw) == 1 {
		return raw
	}
	if strings.HasPrefix(lower, "f") {
		num := strings.TrimPrefix(lower, "f")
		if num != "" {
			allDigits := true
			for _, r := range num {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return lower
			}
		}
	}
	switch lower {
	case "esc", "enter", "tab", "up", "down", "left", "right", "pgup", "pgdn", "home", "end", "backspace", "delete":
		return lower
	default:
		return ""
	}
}

func keybindBaseForKey(key tcell.Key) string {
	switch key {
	case tcell.KeyEsc:
		return "esc"
	case tcell.KeyEnter:
		return "enter"
	case tcell.KeyTab:
		return "tab"
	case tcell.KeyBacktab:
		return "tab"
	case tcell.KeyUp:
		return "up"
	case tcell.KeyDown:
		return "down"
	case tcell.KeyLeft:
		return "left"
	case tcell.KeyRight:
		return "right"
	case tcell.KeyPgUp:
		return "pgup"
	case tcell.KeyPgDn:
		return "pgdn"
	case tcell.KeyHome:
		return "home"
	case tcell.KeyEnd:
		return "end"
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		return "backspace"
	case tcell.KeyDelete:
		return "delete"
	case tcell.KeyCtrlSpace:
		return "space"
	}
	if key >= tcell.KeyF1 && key <= tcell.KeyF64 {
		return fmt.Sprintf("f%d", int(key-tcell.KeyF1)+1)
	}
	return ""
}

func buildKeybindDefinitionIndex() map[KeybindID]KeybindDefinition {
	index := make(map[KeybindID]KeybindDefinition, len(keybindDefinitions))
	for _, def := range keybindDefinitions {
		copyDef := def
		copyDef.Aliases = normalizeAliasList(def.Aliases)
		token, err := NormalizeKeybindToken(def.Default)
		if err != nil {
			token = ""
		}
		copyDef.Default = token
		index[def.ID] = copyDef
	}
	return index
}

func normalizeAliasList(aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		token, err := NormalizeKeybindToken(alias)
		if err != nil || token == "" {
			continue
		}
		normalized = append(normalized, token)
	}
	if len(normalized) == 0 {
		return nil
	}
	sort.Strings(normalized)
	out := normalized[:0]
	prev := ""
	for _, token := range normalized {
		if token == prev {
			continue
		}
		out = append(out, token)
		prev = token
	}
	return append([]string(nil), out...)
}
