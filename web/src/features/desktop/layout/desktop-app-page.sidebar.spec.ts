import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import { DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY } from '../session-v3/api'
import {
  SIDEBAR_SESSION_GROUPS,
  compareSidebarSessions,
  desktopRouteWorkspacePathForSession,
  buildSidebarSessionTree,
  filterInactiveSidebarSessionTrees,
  sessionActivityLabel,
  sessionStatusDetail,
  sessionStatusTone,
  sessionTimerLabel,
  sessionActiveRunIntent,
  sessionSidebarDisplayGroup,
  sidebarWorkspaceContextLabel,
  sidebarRootIDsForSelectionGroup,
  sidebarShouldRenderSelectionToolbar,
  sidebarShouldShowReviewAction,
} from './desktop-app-page'

test('sidebar workspace context shows the Git branch before the workspace name', () => {
  assert.equal(sidebarWorkspaceContextLabel('swarm-go', 'dev'), 'dev · swarm-go')
  assert.equal(sidebarWorkspaceContextLabel(' swarm-go ', ' agent/sidebar-label '), 'agent/sidebar-label · swarm-go')
  assert.equal(sidebarWorkspaceContextLabel('swarm-go', ''), 'swarm-go')
})

test('global sidebar hides Tasks and Tools without deleting the Tasks implementation', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /aria-label="Open tasks"/)
  assert.doesNotMatch(source, />Tasks<\/span>/)
  assert.match(source, /const openTodoModal = useCallback/)
  assert.match(source, /fetchWorkspaceTodos\(normalizedPath, 'user'\)/)
  assert.match(source, /<WorkspaceTodoModal/)
  assert.doesNotMatch(source, /to="\/tools"/)
  assert.doesNotMatch(source, /\bLayoutGrid\b/)
})

test('sidebar header renders workspace context instead of the swarm role label', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const headerStart = source.indexOf('{editingSidebarSwarmName ? (')
  const headerEnd = source.indexOf('<SidebarActionRail', headerStart)
  const headerSource = source.slice(headerStart, headerEnd)

  assert.ok(headerStart >= 0 && headerEnd > headerStart)
  assert.match(headerSource, /\{sidebarWorkspaceContext\}/)
  assert.doesNotMatch(headerSource, /currentSwarmRoleLabel/)
})

test('plan Git panel stays content-sized and scrolls only its file list when constrained', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const panelStart = source.indexOf('const planSidebarGitPanel =')
  const panelEnd = source.indexOf('const sidebarContent =', panelStart)
  const panelSource = source.slice(panelStart, panelEnd)

  assert.ok(panelStart >= 0 && panelEnd > panelStart)
  assert.match(panelSource, /desktop-plan-git-sidebar[^\n]*flex min-h-0 min-w-0 flex-col overflow-hidden/)
  assert.doesNotMatch(panelSource, /desktop-plan-git-sidebar[^\n]*(?:h-full|flex-1)/)
  assert.match(panelSource, /min-h-0 shrink overflow-hidden/)
  assert.doesNotMatch(panelSource, /min-h-0 flex-1 overflow-hidden/)
  assert.match(panelSource, /data-plan-git-file-list[^\n]*|overflow-y-auto[^\n]*data-plan-git-file-list/)
  assert.match(panelSource, /shrink-0[^\n]*data-plan-git-commit|data-plan-git-commit[^\n]*shrink-0/)
  assert.doesNotMatch(panelSource, /max-h-48/)
})

test('plan Git commit form submits on Enter through the shared commit handler and shows a success toast', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const handlerStart = source.indexOf('const handleGitCommit = async () =>')
  const handlerEnd = source.indexOf('const planSidebarGitPanel =', handlerStart)
  const handlerSource = source.slice(handlerStart, handlerEnd)
  const modalStart = source.indexOf('{gitCommitModal ? <Dialog>')
  const modalEnd = source.indexOf('<GitDetailsOverlay', modalStart)
  const modalSource = source.slice(modalStart, modalEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handlerSource, /await commitWorkspaceChanges/)
  assert.match(handlerSource, /setDesktopToast\(\{ message: 'Changes committed successfully\.', tone: 'success' \}\)/)
  assert.match(handlerSource, /const archiveAfterCommit = !modal\.worktree && Boolean\(modal\.sessionId\) && gitCommitArchive/)
  assert.match(handlerSource, /await archiveDesktopV3Sessions\(\[modal\.sessionId\]\)/)
  assert.match(handlerSource, /Changes committed and session archived\./)
  assert.match(handlerSource, /commitSucceeded && integration/)
  assert.match(handlerSource, /setGitIntegrateError\(message\)/)
  assert.ok(modalStart >= 0 && modalEnd > modalStart)
  assert.match(modalSource, /<form[^>]*onSubmit=/)
  assert.match(modalSource, /void handleGitCommit\(\)/)
  assert.match(modalSource, /<Button type="submit"/)
  assert.match(modalSource, /Archive session after integration/)
  assert.match(modalSource, /disabled=\{gitCommitBusy \|\| !gitCommitIntegrate\}/)
  assert.match(modalSource, /!gitCommitModal\.worktree && gitCommitModal\.sessionId/)
  assert.match(modalSource, /Archive session after commit/)
  assert.match(modalSource, /Archives this chat only after the commit succeeds\./)
  assert.match(modalSource, /gitIntegrateModal[\s\S]*void handleGitIntegrate\(\)/)
  assert.equal((modalSource.match(/commitWorkspaceChanges/g) ?? []).length, 0)
})

test('plan Git sidebar renders session-scoped worktree commits and an integration action', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const panelStart = source.indexOf('const planSidebarGitPanel =')
  const panelEnd = source.indexOf('const focusedSidebarContent =', panelStart)
  const panelSource = source.slice(panelStart, panelEnd)

  assert.ok(panelStart >= 0 && panelEnd > panelStart)
  assert.match(source, /activeSessionCommits = activeSessionWorktree \? gitSnapshot\?\.session_commits/)
  assert.match(panelSource, /data-plan-git-session-commits/)
  assert.match(panelSource, /Session commits/)
  assert.match(panelSource, /commit\.short_hash/)
  assert.match(panelSource, /data-plan-git-integrate/)
  assert.match(panelSource, /Integrate into/)
})

test('main sidebar focus mode stays collapsed without adding a top bar or touching the plan sidebar', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const layoutStart = source.indexOf('data-testid="desktop-workspace-sidebar"')
  const layoutEnd = source.indexOf('<DesktopV3ExistingConversationPane', layoutStart)
  const layoutSource = source.slice(layoutStart, layoutEnd)

  assert.ok(layoutStart >= 0 && layoutEnd > layoutStart)
  const focusSidebarStart = source.indexOf('const focusedSidebarContent =')
  const focusSidebarEnd = source.indexOf('const sidebarContent =', focusSidebarStart)
  const focusSidebarSource = source.slice(focusSidebarStart, focusSidebarEnd)

  assert.doesNotMatch(source, /FocusSessionNavigator|desktop-focus-session-navigator|Needs-review and in-progress chat tabs|role="tablist"/)
  assert.doesNotMatch(source, /showActiveChats|Focused on|<span>Focus<\/span>|<Eye/)
  assert.ok(focusSidebarStart >= 0 && focusSidebarEnd > focusSidebarStart)
  assert.match(focusSidebarSource, /data-testid="desktop-focus-sidebar-controls"/)
  assert.match(focusSidebarSource, /className="h-12 w-12 min-w-12 p-0"[\s\S]*onClick=\{\(\) => setSidebarDisplayMode\('full'\)\}[\s\S]*aria-label="Expand sidebar to full width"[\s\S]*title="Full-width sidebar"[\s\S]*<ChevronRight size=\{28\}/)
  assert.doesNotMatch(focusSidebarSource, /aria-pressed|border-\[var\(--app-primary\)\]|bg-\[var\(--app-selection-bg\)\][\s\S]*aria-label="Expand sidebar to full width"/)
  assert.match(focusSidebarSource, /Back to launcher/)
  assert.match(focusSidebarSource, /notificationAttentionVisible[\s\S]*aria-label="Open notifications"/)
  assert.match(focusSidebarSource, /handleOpenSettingsTab\('account'\)[\s\S]*aria-label="Open settings"[\s\S]*<Settings/)
  assert.doesNotMatch(focusSidebarSource, /renderSidebarSessionGroups|globalFlattenedSessionNodes|aria-label="Sessions"|Open \$\{label\}|<Bot/)
  assert.match(layoutSource, /focusMode \? 'sm:w-\[56px\]' : 'sm:w-\[320px\]'/)
  assert.match(layoutSource, /onClick=\{\(\) => setSidebarDisplayMode\('focus'\)\}[\s\S]*aria-label="Enter focus mode"[\s\S]*title="Focus mode"[\s\S]*<ChevronLeft size=\{14\}/)
  assert.doesNotMatch(layoutSource, /<Maximize/)
  assert.match(layoutSource, /\{focusMode \? focusedSidebarContent : sidebarContent\}/)
  assert.match(source, /const updateAttentionVisible = !updateDevMode && \(updateActionEnabled \|\| updateRunning \|\| Boolean\(updateError\)\)/)
  assert.doesNotMatch(layoutSource, /sm:w-\[240px\]|setSidebarDisplayMode\('compact'\)|Use thin sidebar|title="Thin"/)

  const planPane = await readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  assert.match(planPane, /loadDesktopSidebarDisplayMode/)
  assert.match(planPane, /effectiveDesktopSidebarDisplayMode/)
  assert.doesNotMatch(planPane, /main-sidebar-focus-state|FocusSessionNavigator/)
})

test('sidebar keeps review controls first and opens session-independent main-worktree Git details', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const rendererStart = source.indexOf('function renderSidebarSessionGroups')
  const rendererEnd = source.indexOf('export function DesktopAppPage', rendererStart)
  const rendererSource = source.slice(rendererStart, rendererEnd)
  const toolbarIndex = rendererSource.indexOf('data-sidebar-review-toolbar')
  const headingIndex = rendererSource.indexOf('data-sidebar-needs-review-heading')
  const gitEntryStart = rendererSource.indexOf('data-sidebar-dirty-git-entry')
  const gitEntryEnd = rendererSource.indexOf('</button>', gitEntryStart)
  const gitEntrySource = rendererSource.slice(gitEntryStart, gitEntryEnd)
  const handlerStart = source.indexOf('const openMainWorktreeGitPanel = useCallback')
  const handlerEnd = source.indexOf('const closeGitPanel', handlerStart)
  const handlerSource = source.slice(handlerStart, handlerEnd)
  const overlayStart = source.lastIndexOf('<GitDetailsOverlay')
  const overlayEnd = source.indexOf('/>', overlayStart)
  const overlaySource = source.slice(overlayStart, overlayEnd)

  assert.ok(rendererStart >= 0 && rendererEnd > rendererStart)
  assert.ok(toolbarIndex >= 0 && headingIndex > toolbarIndex)
  assert.match(rendererSource, /data-sidebar-review-toolbar[\s\S]*\{groupControls\}[\s\S]*data-sidebar-needs-review-heading/)
  assert.match(rendererSource, /gitAheadCount: number[\s\S]*gitBehindCount: number[\s\S]*gitDirtyCount: number/)
  assert.match(gitEntrySource, /onClick=\{input\.onOpenGit\}/)
  assert.match(gitEntrySource, /↑\{input\.gitAheadCount\} ↓\{input\.gitBehindCount\}/)
  assert.match(gitEntrySource, /input\.gitDirtyCount > 0 \? `\$\{input\.gitDirtyCount\} dirty` : 'clean'/)
  assert.doesNotMatch(gitEntrySource, /<GitBranch|\bGit ·|min-h-|border|bg-\[var\(--app-warning-bg\)\]|Commit/)
  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handlerSource, /gitStatusQueryKey\(normalizedPath\)/)
  assert.doesNotMatch(handlerSource, /sessionId|selectedGitSessionId/)
  assert.match(source, /gitAheadCount: topWorkspaceGitAheadCount[\s\S]*gitBehindCount: topWorkspaceGitBehindCount[\s\S]*gitDirtyCount: topWorkspaceGitDirtyCount[\s\S]*onOpenGit: \(\) => openMainWorktreeGitPanel\(topWorkspacePath, topWorkspaceLabel\)/)
  assert.ok(overlayStart >= 0 && overlayEnd > overlayStart)
  assert.match(overlaySource, /snapshot=\{gitPanel \? topWorkspaceGitSnapshot : null\}/)
  assert.match(overlaySource, /topWorkspaceGitStatusQuery\.isFetching/)
  assert.match(overlaySource, /topWorkspaceGitStatusQuery\.error/)
  assert.doesNotMatch(overlaySource, /selectedGitSessionId|gitRealtimeErrors|gitStatusQuery\.error/)
  assert.deepEqual(SIDEBAR_SESSION_GROUPS.slice(0, 2).map((group) => group.id), ['needs_review', 'in_progress'])
})

test('workspace dropdown rows create chats without icons and the standalone message-square precedes worktree', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const dropdownStart = source.indexOf('<div ref={workspaceDropdownRef}')
  const dropdownEnd = source.indexOf('{needsReviewCleanupOpen ?', dropdownStart)
  const dropdownSource = source.slice(dropdownStart, dropdownEnd)
  const menuStart = dropdownSource.indexOf('role="menu"')
  const menuEnd = dropdownSource.indexOf(') : null}', menuStart)
  const menuSource = dropdownSource.slice(menuStart, menuEnd)
  const newChatIndex = dropdownSource.indexOf('handleStartNewSessionInWorkspace(topWorkspacePath, topWorkspaceLabel)')
  const worktreeIndex = dropdownSource.indexOf('openWorktreeSessionModal({')

  assert.ok(dropdownStart >= 0 && dropdownEnd > dropdownStart)
  assert.ok(menuStart >= 0 && menuEnd > menuStart)
  assert.doesNotMatch(dropdownSource, /<select|<option/)
  assert.match(dropdownSource, /bg-\[var\(--app-surface\)\][^\n]*text-\[var\(--app-text\)\]/)
  assert.match(dropdownSource, /border-\[var\(--app-border\)\]/)
  assert.match(menuSource, /role="menuitem"[\s\S]*onClick=\{\(\) => \{[\s\S]*setWorkspaceDropdownOpen\(false\)[\s\S]*handleStartNewSessionInWorkspace\(workspace\.path, workspace\.workspaceName\)/)
  assert.match(menuSource, /aria-label=\{`New chat in \$\{workspace\.workspaceName\}`\}/)
  assert.doesNotMatch(menuSource, /<Plus/)
  assert.ok(newChatIndex >= 0 && worktreeIndex > newChatIndex)
  assert.match(dropdownSource, /handleStartNewSessionInWorkspace\(topWorkspacePath, topWorkspaceLabel\)[\s\S]*aria-label=\{`New chat in \$\{topWorkspaceLabel\}`\}[\s\S]*<MessageSquare[\s\S]*openWorktreeSessionModal\(\{/)
  assert.doesNotMatch(dropdownSource, /handleWorkspaceSelect|menuitemradio|aria-checked|event\.stopPropagation\(\)/)
})

test('sidebar card places compact metadata at the top right and swaps it for actions on hover', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const rowStart = source.indexOf('const SessionRow = memo')
  const rowEnd = source.indexOf('interface RenderSidebarSessionGroupsInput', rowStart)
  const rowSource = source.slice(rowStart, rowEnd)
  const topRowStart = rowSource.indexOf('<div className="flex min-w-0 items-start justify-between gap-2">')
  const secondRowStart = rowSource.indexOf('<div className="mt-0.5 flex min-w-0 items-center', topRowStart)
  const topRowSource = rowSource.slice(topRowStart, secondRowStart)
  const secondRowSource = rowSource.slice(secondRowStart)
  const metadataIndex = topRowSource.indexOf('data-sidebar-session-metadata-icons')
  const actionIndex = topRowSource.indexOf('data-sidebar-session-action-icons')
  const statusIndex = topRowSource.indexOf('{showStatusCircle ? (')

  assert.ok(rowStart >= 0 && rowEnd > rowStart)
  assert.ok(topRowStart >= 0 && secondRowStart > topRowStart)
  assert.ok(metadataIndex >= 0 && actionIndex > metadataIndex && statusIndex > actionIndex)
  assert.match(rowSource, /const workspaceLabel = sessionWorkspaceLabel\(session\)/)
  assert.match(rowSource, /const branchLabel = sessionBranchLabel\(session\)/)
  assert.match(rowSource, /const showWorktreeChip = Boolean\(session\.worktreeEnabled\)/)
  assert.match(rowSource, /const showBranchLabel = !session\.worktreeEnabled && Boolean\(branchLabel\)/)
  assert.match(secondRowSource, /\{workspaceLabel\}[\s\S]*showBranchLabel \?[\s\S]*\{branchLabel\}/)
  assert.doesNotMatch(secondRowSource, /data-sidebar-session-metadata-icons|<GitBranch|<ListTodo|<NotepadText/)
  assert.match(rowSource, /const showTaskChip = Boolean\(backgroundInfo\)/)
  assert.match(rowSource, /const showActivePlan = session\.mode === 'plan' && sessionHasCanonicalActiveRun\(session\)/)
  assert.match(topRowSource, /relative inline-flex h-4 w-14 shrink-0 items-center justify-end[^>]*data-sidebar-session-corner-controls/)
  assert.match(topRowSource, /group-hover:opacity-0 group-focus-within:opacity-0[\s\S]*data-sidebar-session-metadata-icons/)
  assert.match(topRowSource, /data-sidebar-session-metadata-icons[\s\S]*<GitBranch size=\{12\}[^>]*aria-label="Worktree session"/)
  assert.match(topRowSource, /data-sidebar-session-metadata-icons[\s\S]*<ListTodo size=\{12\}[^>]*aria-label="Task session"/)
  assert.match(topRowSource, /data-sidebar-session-metadata-icons[\s\S]*<NotepadText size=\{12\}[^>]*aria-label="Active plan"/)
  assert.match(topRowSource, /data-sidebar-session-action-icons[\s\S]*\{pinActionControl\}[\s\S]*\{archiveActionControl\}[\s\S]*\{actionMenu\}/)
  assert.match(rowSource, /opacity-0[^']*group-hover:opacity-100 group-focus-within:opacity-100/)
  assert.doesNotMatch(rowSource, /rounded-full border[^\n]*(?:Worktree|Task|Active plan)/)
  assert.doesNotMatch(rowSource, /showDetailsRow|backgroundInfo\.badge|backgroundInfo\?\.targetLabel|>background</)
  assert.doesNotMatch(rowSource, /fallbackSwarmName|routeOptions|sessionOriginLabel/)
})

test('sidebar cards use explicit selection mode without hover-driven checkboxes or navigation', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const rowStart = source.indexOf('const SessionRow = memo')
  const rowEnd = source.indexOf('interface RenderSidebarSessionGroupsInput', rowStart)
  const rowSource = source.slice(rowStart, rowEnd)

  assert.ok(rowStart >= 0 && rowEnd > rowStart)
  assert.match(rowSource, /if \(selectionMode && depth === 0\) \{\s*event\.preventDefault\(\)\s*onToggleSelected\?\.\(session\.id, event\.shiftKey\)\s*return/)
  assert.match(rowSource, /\{selectionMode && depth === 0 \? \(\s*<input\s*type="checkbox"/)
  assert.doesNotMatch(rowSource, /group-hover:(?:mr-2|w-4|opacity-100)/)
  assert.doesNotMatch(rowSource, /checkboxRevealSuppressed|checkboxPointerInsideRef|checkboxFocusInsideRef/)
})

test('sidebar rename input keeps space key events away from the row shortcut', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const rowStart = source.indexOf('const SessionRow = memo')
  const rowEnd = source.indexOf('interface RenderSidebarSessionGroupsInput', rowStart)
  const rowSource = source.slice(rowStart, rowEnd)
  const renameInputStart = rowSource.indexOf('value={renameDraft}')
  const renameInputEnd = rowSource.indexOf('className="h-6 w-full', renameInputStart)
  const renameInputSource = rowSource.slice(renameInputStart, renameInputEnd)

  assert.ok(renameInputStart >= 0 && renameInputEnd > renameInputStart)
  assert.match(renameInputSource, /onKeyDown=\{\(event\) => \{\s*event\.stopPropagation\(\)/)
  assert.match(renameInputSource, /if \(event\.key === 'Escape'\) \{ event\.preventDefault\(\); setRenameError\(null\); setRenaming\(false\) \}/)
})

test('selection mode exits only through explicit clear or successful archive', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.equal((source.match(/setSidebarSelectionMode\(false\)/g) ?? []).length, 2)
  assert.doesNotMatch(source, /sidebarShouldClearSelectionForSessionChange/)
})

test('search result activation clears selection before hydration, including same-session results', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const handlerStart = source.indexOf('const handleOpenSearchResult = useCallback')
  const handlerEnd = source.indexOf('useEffect(() => {', handlerStart)
  const handlerSource = source.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handlerSource, /handleClearSidebarSelection\(\)\s*void selectAndHydrateDesktopV3Session\(sessionId\)/)
})

test('session route workspace ignores unbound remote paths from hydrated sessions', () => {
  const session = makeSession('remote-session', {
    workspacePath: '/remote/workspaces/swarm-go',
    metadata: {
      swarm_v3_source_workspace_path: '/remote/workspaces/swarm-go',
      local_workspace_binding_id: 'remote-binding',
    },
  })
  const localWorkspacePaths = new Set(['/local/swarm-go'])

  assert.equal(desktopRouteWorkspacePathForSession(session, new Map(), localWorkspacePaths), '')
  assert.equal(
    desktopRouteWorkspacePathForSession(
      session,
      new Map([['remote-binding', '/local/swarm-go']]),
      localWorkspacePaths,
    ),
    '/local/swarm-go',
  )
})

test('session route workspace accepts authoritative local metadata paths', () => {
  const session = makeSession('local-session', {
    workspacePath: '/runtime/swarm-go',
    metadata: { swarm_v3_source_workspace_path: '/local/swarm-go' },
  })

  assert.equal(
    desktopRouteWorkspacePathForSession(session, new Map(), new Set(['/local/swarm-go'])),
    '/local/swarm-go',
  )
})

function activeRunIntent(
  sessionId: string,
  runId: string,
  createdAt: number,
  updatedAt = createdAt,
  timing: { startedAt?: number; cumulativeDurationMs?: number } = {},
) {
  return {
    sessionId,
    runId,
    status: 'running',
    blockedReason: '',
    createdAt,
    startedAt: timing.startedAt ?? createdAt,
    cumulativeDurationMs: timing.cumulativeDurationMs,
    updatedAt,
    eventSeq: 1,
  }
}

function makeSession(id: string, overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id,
    title: id,
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 1_000,
    createdAt: 500,
    permissionsHydrated: false,
    lifecycle: null,
    runIntent: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
      step: 0,
      toolName: null,
    sidebarToolName: null,
      toolCallId: null,
      toolArguments: null,
      toolOutput: '',
      retainedToolName: null,
      retainedToolCallId: null,
      retainedToolArguments: null,
      retainedToolOutput: '',
      retainedToolState: null,
      toolHistory: [],
      summary: null,
      lastEventType: null,
      lastEventAt: null,
      error: null,
      seq: 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    ...overrides,
  }
}

test('sidebar labels render the direct stream tool label', () => {
  const session = makeSession('live-tool', {
    updatedAt: 10_000,
    runIntent: activeRunIntent('live-tool', 'run-live-tool', 10_000, 12_500),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.delta',
      toolName: 'search',
      sidebarToolName: 'read',
      toolCallId: 'call-search',
      summary: 'search',
    },
  })

  assert.equal(sessionStatusTone(session), 'running')
  assert.equal(sessionActivityLabel(session), 'read')
  assert.equal(sessionTimerLabel(session, 15_250), '5s')
  assert.equal(sessionStatusDetail(session, 15_250), 'just now')
})

test('sidebar labels do not fall back to retained tool state', () => {
  const session = makeSession('retained-tool', {
    updatedAt: 10_000,
    runIntent: activeRunIntent('retained-tool', 'run-retained-tool', 10_000, 12_500),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-retained-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.completed',
      toolName: null,
    sidebarToolName: null,
      retainedToolName: 'bash',
      retainedToolState: 'done',
      summary: 'Assistant responding…',
    },
  })

  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar labels do not fall back to live tool history', () => {
  const session = makeSession('history-tool', {
    updatedAt: 10_000,
    runIntent: activeRunIntent('history-tool', 'run-history-tool', 10_000, 12_500),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-history-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.delta',
      toolName: null,
    sidebarToolName: null,
      summary: 'Streaming response…',
      toolHistory: [
        {
          key: 'history-tool',
          sessionId: 'history-tool',
          runId: 'run-history-tool',
          stepId: 'step-1',
          callId: 'call-read',
          toolInstanceId: 'step-1:call-read',
          toolName: 'read',
          toolArguments: null,
          toolOutput: '',
          state: 'running',
          step: 1,
          seq: 22,
          startedAt: 11_000,
          updatedAt: 12_500,
          completedAt: null,
        },
      ],
    },
  })

  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar active sort keeps earlier active positions above newer live activity', () => {
  const olderRunWithFreshToolEvent = makeSession('older-run-fresh-tool', {
    updatedAt: 20_000,
    createdAt: 1_000,
    runIntent: activeRunIntent('older-run-fresh-tool', 'run-older', 1_000, 20_000),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-older',
      startedAt: 1_000,
      lastEventAt: 20_000,
      lastEventType: 'session.tool.delta',
      toolName: 'bash',
    },
  })
  const newerRunWithoutFreshEvent = makeSession('newer-run-stale', {
    updatedAt: 10_500,
    createdAt: 10_000,
    runIntent: activeRunIntent('newer-run-stale', 'run-newer', 10_000, 10_500),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-newer',
      startedAt: 10_000,
      lastEventAt: 10_500,
      lastEventType: 'session.assistant.delta',
    },
  })

  assert.equal(compareSidebarSessions(olderRunWithFreshToolEvent, newerRunWithoutFreshEvent, 20_500) < 0, true)
})

test('sidebar active sort keeps first-started position order when an older row streams', () => {
  const sessions = [
    makeSession('0', { updatedAt: 50_000, createdAt: 50_000, runIntent: activeRunIntent('0', 'run-0', 50_000), live: { ...makeSession('base').live, status: 'running', startedAt: 50_000, lastEventAt: 50_000 } }),
    makeSession('1', { updatedAt: 40_000, createdAt: 40_000, runIntent: activeRunIntent('1', 'run-1', 40_000), live: { ...makeSession('base').live, status: 'running', startedAt: 40_000, lastEventAt: 40_000 } }),
    makeSession('2', { updatedAt: 30_000, createdAt: 30_000, runIntent: activeRunIntent('2', 'run-2', 30_000), live: { ...makeSession('base').live, status: 'running', startedAt: 30_000, lastEventAt: 30_000 } }),
    makeSession('3', { updatedAt: 100_000, createdAt: 20_000, runIntent: activeRunIntent('3', 'run-3', 20_000, 100_000), live: { ...makeSession('base').live, status: 'running', startedAt: 20_000, lastEventAt: 100_000 } }),
    makeSession('4', { updatedAt: 10_000, createdAt: 10_000, runIntent: activeRunIntent('4', 'run-4', 10_000), live: { ...makeSession('base').live, status: 'running', startedAt: 10_000, lastEventAt: 10_000 } }),
  ]

  assert.deepEqual([...sessions].sort((left, right) => compareSidebarSessions(left, right, 100_500)).map((session) => session.id), ['4', '3', '2', '1', '0'])
})

test('sidebar active sort positions a restarted old conversation by its new run start', () => {
  const existingActive = makeSession('existing-active', {
    updatedAt: 60_000,
    createdAt: 60_000,
    runIntent: activeRunIntent('existing-active', 'run-existing', 60_000),
    live: { ...makeSession('base').live, status: 'running', startedAt: 60_000, lastEventAt: 60_000 },
  })
  const restartedOldConversation = makeSession('restarted-old', {
    updatedAt: 100_000,
    createdAt: 1_000,
    runIntent: activeRunIntent('restarted-old', 'run-restarted', 100_000),
    live: { ...makeSession('base').live, status: 'running', startedAt: 100_000, lastEventAt: 100_000 },
  })

  assert.equal(compareSidebarSessions(existingActive, restartedOldConversation, 100_500) < 0, true)
})

test('sidebar inactivity filter hides stale ordinary trees but protects selected, pinned, running, and active descendants', () => {
  const now = 100 * 60 * 60 * 1000
  const staleAt = now - 13 * 60 * 60 * 1000
  const recentAt = now - 2 * 60 * 60 * 1000
  const sessions = [
    makeSession('stale', { updatedAt: staleAt }),
    makeSession('selected', { updatedAt: staleAt }),
    makeSession('pinned', { updatedAt: staleAt, metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true } }),
    makeSession('recent', { updatedAt: recentAt }),
    makeSession('parent', { updatedAt: staleAt }),
    makeSession('child', { updatedAt: recentAt, metadata: { parent_session_id: 'parent', requested_subagent: 'finder' } }),
  ]
  const result = filterInactiveSidebarSessionTrees(buildSidebarSessionTree(sessions, now), now, 12, 'selected')
  assert.deepEqual(result.nodes.map((node) => node.session.id).sort(), ['parent', 'pinned', 'recent', 'selected'])
  assert.equal(result.hiddenCount, 1)
  assert.equal(filterInactiveSidebarSessionTrees(buildSidebarSessionTree(sessions, now), now, null).hiddenCount, 0)
})

test('sidebar keeps managed deploy sessions as independent roots while preserving lineage metadata', () => {
  const parent = makeSession('launching-session', { updatedAt: 10_000 })
  const managed = makeSession('managed-session', {
    updatedAt: 20_000,
    metadata: {
      parent_session_id: parent.id,
      lineage_kind: 'session_deploy',
      deployment_proposal_id: 'proposal-1',
    },
  })

  const nodes = buildSidebarSessionTree([parent, managed], 30_000)
  assert.deepEqual(nodes.map((node) => node.session.id), [managed.id, parent.id])
  assert.equal(nodes[0]?.depth, 0)
  assert.deepEqual(nodes[0]?.children, [])
})

test('sidebar manual pin moves an active chat into the Pinned display group', () => {
  const pinnedIdle = makeSession('pinned-idle', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true },
  })
  const staleIdle = makeSession('stale-idle', {
    updatedAt: 80_000,
    createdAt: 70_000,
  })

  assert.equal(sessionSidebarDisplayGroup(pinnedIdle), 'pinned')
  assert.equal(sessionSidebarDisplayGroup(staleIdle), 'active_chats')
  assert.equal(compareSidebarSessions(pinnedIdle, staleIdle, 100_500) < 0, true)
})

test('sidebar global selection includes roots across every visible display group', () => {
  const now = 100_000
  const nodes = buildSidebarSessionTree([
    makeSession('review', { metadata: { swarm_v3_sidebar_group: 'needs_review' } }),
    makeSession('progress', { metadata: { swarm_v3_sidebar_group: 'in_progress' } }),
    makeSession('pinned', { metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true } }),
    makeSession('chat'),
  ], now)

  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, null), ['review', 'progress', 'pinned', 'chat'])
  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, 'needs_review'), ['review'])
  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, 'active_chats'), ['chat'])
})

test('sidebar selection toolbar renders only in the master group', () => {
  assert.equal(sidebarShouldRenderSelectionToolbar(true, 'needs_review', 'needs_review'), true)
  assert.equal(sidebarShouldRenderSelectionToolbar(true, 'needs_review', 'active_chats'), false)
  assert.equal(sidebarShouldRenderSelectionToolbar(false, 'needs_review', 'needs_review'), false)
  assert.equal(sidebarShouldRenderSelectionToolbar(true, null, 'needs_review'), false)
})

test('sidebar review action is limited to Needs Review outside archive selection mode', () => {
  assert.equal(sidebarShouldShowReviewAction('needs_review', false), true)
  assert.equal(sidebarShouldShowReviewAction('needs_review', true), false)
  assert.equal(sidebarShouldShowReviewAction('active_chats', false), false)
})

test('sidebar renders contextual controls for active groups without an Archived section', () => {
  assert.deepEqual(SIDEBAR_SESSION_GROUPS.map((group) => group.id), [
    'needs_review',
    'in_progress',
    'pinned',
    'active_chats',
  ])
  assert.deepEqual(
    SIDEBAR_SESSION_GROUPS.filter((group) => group.showInactiveThreshold).map((group) => group.id),
    ['active_chats'],
  )
})

test('sidebar manual pin sort ignores pins for in-progress plan sessions', () => {
  const pinnedInProgressPlan = makeSession('pinned-in-progress-plan', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: {
      [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true,
      swarm_v3_sidebar_group: 'in_progress',
    },
  })
  const staleIdle = makeSession('stale-idle', {
    updatedAt: 80_000,
    createdAt: 70_000,
  })

  assert.equal(sessionSidebarDisplayGroup(pinnedInProgressPlan), 'in_progress')
  assert.equal(compareSidebarSessions(staleIdle, pinnedInProgressPlan, 100_500) < 0, true)
})

test('sidebar active sort keeps active DB sessions pinned above recent idle rows', () => {
  const active = makeSession('active', {
    updatedAt: 1_000,
    runIntent: activeRunIntent('active', 'run-active', 1_000, 1_500),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-active',
      startedAt: 1_000,
      lastEventAt: 1_500,
    },
  })
  const recentIdle = makeSession('recent-idle', {
    updatedAt: 20_000,
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(active, recentIdle, 20_500) < 0, true)
})

test('sidebar stopped sort keeps active rows above a newly stopped long session', () => {
  const active = makeSession('active', {
    updatedAt: 40_000,
    createdAt: 30_000,
    runIntent: activeRunIntent('active', 'run-active', 30_000, 40_000),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-active',
      startedAt: 30_000,
      lastEventAt: 40_000,
    },
  })
  const stoppedLongSession = makeSession('stopped-long', {
    updatedAt: 100_000,
    createdAt: 1_000,
    lifecycle: {
      sessionId: 'stopped-long',
      runId: 'run-stopped-long',
      active: false,
      phase: 'stopped',
      startedAt: 1_000,
      endedAt: 100_000,
      updatedAt: 100_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(active, stoppedLongSession, 100_500) < 0, true)
})

test('sidebar stopped sort resolves long sessions by durable activity instead of start time', () => {
  const stoppedLongSession = makeSession('stopped-long', {
    updatedAt: 100_000,
    createdAt: 1_000,
    lifecycle: {
      sessionId: 'stopped-long',
      runId: 'run-stopped-long',
      active: false,
      phase: 'stopped',
      startedAt: 1_000,
      endedAt: 100_000,
      updatedAt: 100_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })
  const previouslyNewerIdle = makeSession('previously-newer-idle', {
    updatedAt: 90_000,
    createdAt: 80_000,
    lifecycle: {
      sessionId: 'previously-newer-idle',
      runId: 'run-previously-newer-idle',
      active: false,
      phase: 'stopped',
      startedAt: 80_000,
      endedAt: 90_000,
      updatedAt: 90_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(stoppedLongSession, previouslyNewerIdle, 130_000) < 0, true)
})


test('sidebar needs review sessions without active runs sort by durable last activity', () => {
  const staleNeedsReview = makeSession('stale-needs-review', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: { swarm_v3_sidebar_group: 'needs_review' },
  })
  const recentPaused = makeSession('recent-paused', {
    updatedAt: 90_000,
    createdAt: 80_000,
  })

  assert.equal(compareSidebarSessions(recentPaused, staleNeedsReview, 120_000) < 0, true)
  assert.equal(sessionStatusDetail(staleNeedsReview, 120_000), '1 min')
})

test('sidebar relative timestamps use compact units without a trailing ago', () => {
  const session = makeSession('compact-relative-time', { updatedAt: 1_000 })

  assert.equal(sessionStatusDetail(session, 1_000 + 2 * 60_000), '2 mins')
  assert.equal(sessionStatusDetail(session, 1_000 + 2 * 60 * 60_000), '2 hrs')
  assert.equal(sessionStatusDetail(session, 1_000 + 2 * 24 * 60 * 60_000), '2 days')
})

test('sidebar active status and timer ignore lifecycle/live-only liveness without canonical active run', () => {
  const liveOnly = makeSession('live-only', {
    updatedAt: 20_000,
    lifecycle: {
      sessionId: 'live-only',
      runId: 'run-lifecycle-only',
      active: true,
      phase: 'running',
      startedAt: 1_000,
      endedAt: 0,
      updatedAt: 20_000,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-only',
      startedAt: 1_000,
      lastEventAt: 20_000,
    },
  })

  assert.equal(sessionActiveRunIntent(liveOnly), null)
  assert.equal(sessionStatusTone(liveOnly), 'idle')
  assert.equal(sessionActivityLabel(liveOnly), '')
})

test('sidebar stale inactive lifecycle cannot suppress canonical active timer/status', () => {
  const canonical = makeSession('canonical-active', {
    runIntent: activeRunIntent('canonical-active', 'run-canonical', 1_000, 2_000),
    lifecycle: {
      sessionId: 'canonical-active',
      runId: 'run-canonical',
      active: false,
      phase: 'completed',
      startedAt: 1_000,
      endedAt: 1_500,
      updatedAt: 1_500,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: {
      ...makeSession('base').live,
      status: 'idle',
      runId: null,
      startedAt: null,
    },
  })

  assert.equal(sessionActiveRunIntent(canonical)?.runId, 'run-canonical')
  assert.equal(sessionStatusTone(canonical), 'running')
  assert.equal(sessionTimerLabel(canonical, 2_500), '1s')
})

test('sidebar terminal canonical run intent clears active status even if live remains running', () => {
  const terminal = makeSession('terminal-run', {
    runIntent: {
      ...activeRunIntent('terminal-run', 'run-terminal', 1_000, 2_000),
      status: 'cancelled',
    },
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-terminal',
      startedAt: 1_000,
    },
  })

  assert.equal(sessionActiveRunIntent(terminal), null)
  assert.equal(sessionStatusTone(terminal), 'idle')
  assert.equal(sessionActivityLabel(terminal), '')
})


test('sidebar timer uses backend run timing and cumulative duration instead of created_at', () => {
  const session = makeSession('backend-timed-run', {
    runIntent: activeRunIntent('backend-timed-run', 'run-backend', 1_000, 120_000, {
      startedAt: 120_000,
      cumulativeDurationMs: 90_000,
    }),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-backend',
      startedAt: 1_000,
      lastEventAt: 125_000,
    },
  })

  assert.equal(sessionTimerLabel(session, 125_000), '5s (1m35s)')
})

test('sidebar timer uses compact loop and overall duration pattern', () => {
  const session = makeSession('second-run', {
    runIntent: activeRunIntent('second-run', 'run-second', 1_000, 120_000, {
      startedAt: 120_000,
      cumulativeDurationMs: 90_000,
    }),
  })

  assert.equal(sessionTimerLabel(session, 125_000), '5s (1m35s)')
})

test('sidebar active timer falls back to canonical run created_at when started_at is absent', () => {
  const session = makeSession('created-at-run', {
    runIntent: {
      ...activeRunIntent('created-at-run', 'run-created-at', 10_000, 15_000),
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 15_500), '5s')
  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar pending executor status uses canonical run state and usable timing', () => {
  const session = makeSession('pending-run', {
    runIntent: {
      ...activeRunIntent('pending-run', 'run-pending', 10_000, 10_500),
      status: 'pending_executor',
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 12_500), '2s')
  assert.equal(sessionActivityLabel(session), '')
  assert.notEqual(sessionActivityLabel(session), 'Starting')
  assert.notEqual(sessionActivityLabel(session), 'Pending executor')
  assert.notEqual(sessionActivityLabel(session), 'Pending execution')
})

test('sidebar active timer falls back to canonical run updated_at when start and create times are absent', () => {
  const session = makeSession('updated-at-run', {
    runIntent: {
      ...activeRunIntent('updated-at-run', 'run-updated-at', 0, 10_500),
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 12_500), '2s')
  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar timer does not fall back to live without a canonical active run intent', () => {
  const session = makeSession('no-run-intent', {
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-only',
      startedAt: 1_000,
      lastEventAt: 125_000,
    },
  })

  assert.equal(sessionTimerLabel(session, 125_000), '')
})
