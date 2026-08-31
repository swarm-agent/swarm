import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const appPage = new URL('./desktop-app-page.tsx', import.meta.url)
const router = new URL('../../../app/router.tsx', import.meta.url)
const newSessionPane = new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url)
const chatHeader = new URL('../chat/components/desktop-v3-chat-header.tsx', import.meta.url)
const reviewWorktreesModal = new URL('./review-worktrees-modal.tsx', import.meta.url)

test('mobile workspace uses an outlined Task action and workspace picker while desktop keeps New session', async () => {
  const source = await readFile(appPage, 'utf8')
  const start = source.indexOf('data-testid="mobile-workspace-home"')
  const end = source.indexOf('  useEffect(() => {', start)
  const mobile = source.slice(start, end)
  const desktop = `${source.slice(0, start)}${source.slice(end)}`

  assert.ok(start >= 0 && end > start)
  assert.match(mobile, />Workspace<\/p>/)
  assert.match(mobile, /<select[\s\S]*?aria-label="Change workspace"/)
  assert.match(mobile, /value=\{routeWorkspace\.path\}/)
  assert.match(mobile, /mergedSidebarWorkspaceEntries\.map\(\(workspace\) =>/)
  assert.match(mobile, /handleOpenWorkspace\(workspace\.path, workspace\.workspaceName\)/)
  assert.match(mobile, />Task<\/span>/)
  assert.match(mobile, /border border-\[var\(--app-primary\)\][^\"]*bg-\[var\(--app-surface\)\][^\"]*text-\[var\(--app-primary\)\]/)
  assert.doesNotMatch(mobile, /bg-\[var\(--app-primary\)\][^\"]*text-\[var\(--app-primary-text\)\]/)
  assert.doesNotMatch(mobile, />Worktree<\/span>|<GitBranch/)
  assert.doesNotMatch(mobile, /New session/i)
  assert.match(desktop, /label: 'New session'/)
  assert.match(desktop, /handleStartNewSessionInWorkspace\(topWorkspacePath, topWorkspaceLabel\)/)
  assert.match(mobile, /<ListChecks[^>]+aria-hidden="true"/)
  assert.match(mobile, /min-h-11 shrink-0 touch-manipulation/)
})

test('mobile workspace exposes the global flat session list and keeps prior sessions collapsed', async () => {
  const source = await readFile(appPage, 'utf8')
  const globalListStart = source.indexOf('const globalFlattenedSessionNodes = useMemo(')
  const mobileListEnd = source.indexOf('  const visibleSidebarRootIDs', globalListStart)
  const mobileListSource = source.slice(globalListStart, mobileListEnd)

  assert.ok(globalListStart >= 0 && mobileListEnd > globalListStart)
  assert.match(mobileListSource, /globalFlattenedSessionNodes\.filter\(\(node\) => sessionIsMobileActive\(node\.session\)\)/)
  assert.match(mobileListSource, /globalFlattenedSessionNodes\.filter\(\(node\) => !sessionIsMobileActive\(node\.session\)\)/)
  assert.doesNotMatch(mobileListSource, /routeWorkspace|desktopRouteWorkspacePathForSession|mobileWorkspaceSessionNodes/)
  assert.match(source, /group === 'needs_review' \|\| group === 'in_progress'/)
  assert.match(source, /id="mobile-workspace-sessions-heading"[^>]*>Sessions<\/h2>/)
  assert.match(source, /mobileActiveSessionNodes\.length[^\n]*>\{mobileActiveSessionNodes\.length\} active/)
  assert.match(source, /aria-expanded=\{mobilePreviousSessionsOpen\}/)
  assert.match(source, /mobilePreviousSessionsOpen \? <div className="mt-2 grid gap-2">/)
})

test('mobile workspace exposes Video Studio sessions through the canonical viewer route', async () => {
  const source = await readFile(appPage, 'utf8')
  const mobileStart = source.indexOf('data-testid="mobile-workspace-session-scroll"')
  const mobileEnd = source.indexOf('      </section>', mobileStart)
  const mobile = source.slice(mobileStart, mobileEnd)

  assert.ok(mobileStart >= 0 && mobileEnd > mobileStart)
  assert.match(mobile, /id="mobile-video-sessions-heading">Video sessions<\/span>/)
  assert.match(mobile, /videoStudioSessionNodes\.map\(\(node\) => \(/)
  assert.match(mobile, /onSelect=\{handleSelectVideoSidebarSession\}/)
  assert.match(source, /handleSelectVideoSidebarSession[\s\S]*?preferredVideoSessionView\(normalizedSessionId\)[\s\S]*?to: '\/\$workspaceSlug\/video\/\$videoSessionId'/)
  assert.match(mobile, /videoStudioSessionNodes\.length === 0 \? \(/)
})

test('mobile Sessions header and rows preserve the lower Manage worktree control', async () => {
  const source = await readFile(appPage, 'utf8')
  const rendererStart = source.indexOf('function renderSidebarSessionGroups')
  const rendererEnd = source.indexOf('export function DesktopAppPage', rendererStart)
  const renderer = source.slice(rendererStart, rendererEnd)
  const mobileRendererStart = source.indexOf('const renderMobileSessions')
  const mobileRendererEnd = source.indexOf('const mobileSessionQuickMenu', mobileRendererStart)
  const mobileRenderer = source.slice(mobileRendererStart, mobileRendererEnd)
  const headerStart = source.indexOf('data-mobile-active-sessions-header')
  const headerEnd = source.indexOf('<div className="grid min-h-0', headerStart)
  const header = source.slice(headerStart, headerEnd)
  const mainEnd = source.indexOf('</main>')
  const mountedReview = source.slice(mainEnd, source.indexOf('<DesktopQuickSettingsModal', mainEnd))

  assert.ok(rendererStart >= 0 && rendererEnd > rendererStart)
  assert.ok(mobileRendererStart >= 0 && mobileRendererEnd > mobileRendererStart)
  assert.ok(headerStart >= 0 && headerEnd > headerStart)
  assert.match(header, />Sessions<\/h2>/)
  assert.match(header, /mobileActiveSessionNodes\.length/)
  assert.doesNotMatch(header, /Review worktrees|Manage/)
  assert.equal((source.match(/<span>Manage<\/span>/g) ?? []).length, 1)
  assert.match(mobileRenderer, /presentation: 'mobile'/)
  assert.match(renderer, /input\.presentation === 'mobile'[\s\S]*?min-h-11 touch-manipulation[\s\S]*?<span>Manage<\/span>/)
  assert.match(renderer, /aria-label="Review worktrees"/)
  assert.match(renderer, /aria-expanded=\{input\.reviewCleanupOpen\}/)
  assert.match(renderer, /onClick=\{input\.onToggleReviewCleanup\}/)
  assert.match(mobileRenderer, /onToggleReviewCleanup: \(\) => setNeedsReviewCleanupOpen\(\(open\) => !open\)/)
  assert.match(mountedReview, /needsReviewCleanupOpen \? <ReviewWorktreesModal/)
  assert.doesNotMatch(source.slice(0, mainEnd), /needsReviewCleanupOpen \? <ReviewWorktreesModal/)
})

test('review worktrees is a full-height, safe-area-aware mobile view with touch targets', async () => {
  const source = await readFile(reviewWorktreesModal, 'utf8')

  assert.match(source, /data-mobile-review-worktrees/)
  assert.match(source, /max-sm:h-\[100dvh\] max-sm:max-h-none max-sm:w-full max-sm:rounded-none max-sm:border-0/)
  assert.match(source, /max-sm:pt-\[calc\(var\(--app-safe-area-top\)\+1rem\)\]/)
  assert.match(source, /max-sm:pb-\[calc\(var\(--app-safe-area-bottom\)\+1rem\)\]/)
  assert.match(source, /max-sm:grid max-sm:grid-cols-2/)
  assert.match(source, /min-h-11 touch-manipulation/)
  assert.match(source, /\[-webkit-overflow-scrolling:touch\]/)
})

test('mobile Task action and direct Worktree routes navigate to dedicated pages', async () => {
  const source = await readFile(appPage, 'utf8')
  const routerSource = await readFile(router, 'utf8')

  assert.match(routerSource, /path: '\/\$workspaceSlug\/task'[\s\S]*?component: DesktopAppPage/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/worktree'[\s\S]*?component: DesktopAppPage/)
  assert.match(routerSource, /workspaceTaskRoute,[\s\S]*?workspaceWorktreeRoute,/)
  assert.match(source, /matchMedia\('\(max-width: 639px\)'\)\.matches[\s\S]*?to: '\/\$workspaceSlug\/task'/)
  assert.match(source, /matchMedia\('\(max-width: 639px\)'\)\.matches[\s\S]*?to: '\/\$workspaceSlug\/worktree'/)
  assert.match(source, /mobileCreationPage === 'task'[\s\S]*?<BackgroundTaskForm[\s\S]*?presentation="page"/)
  assert.match(source, /mobileCreationPage === 'worktree'[\s\S]*?<WorktreeSessionForm[\s\S]*?presentation="page"/)
})

test('mobile creation routes render full pages and never dialog popouts', async () => {
  const source = await readFile(appPage, 'utf8')
  const taskStart = source.indexOf('function BackgroundTaskForm')
  const worktreeStart = source.indexOf('function WorktreeSessionForm', taskStart)
  const formEnd = source.indexOf('function GitDetailsOverlay', worktreeStart)
  const taskForm = source.slice(taskStart, worktreeStart)
  const worktreeForm = source.slice(worktreeStart, formEnd)

  assert.match(taskForm, /presentation === 'page'[\s\S]*?data-testid="mobile-task-page"/)
  assert.match(worktreeForm, /presentation === 'page'[\s\S]*?data-testid="mobile-worktree-page"/)
  assert.match(taskForm, /if \(presentation === 'page'\)[\s\S]*?return \([\s\S]*?<section/)
  assert.match(worktreeForm, /if \(presentation === 'page'\)[\s\S]*?return \([\s\S]*?<section/)
  assert.match(taskForm, /return \([\s\S]*?<Dialog>[\s\S]*?<DialogPanel/)
  assert.match(worktreeForm, /return \([\s\S]*?<Dialog>[\s\S]*?<DialogPanel/)
  assert.match(source, /backgroundTaskOpen && routeWorkspace && mobileCreationPage !== 'task'/)
  assert.match(source, /mobileCreationPage !== 'worktree' \? <WorktreeSessionForm[\s\S]*?presentation="dialog"/)
  assert.doesNotMatch(source, /max-sm:items-end|max-sm:max-h-\[calc\(100dvh|useMobileVisualViewportHeight/)
})

test('mobile Task page uses the natural composer with background guidance and success feedback', async () => {
  const source = await readFile(appPage, 'utf8')
  const formStart = source.indexOf('function BackgroundTaskForm')
  const pageStart = source.indexOf("if (presentation === 'page')", formStart)
  const dialogStart = source.indexOf('const content = (', pageStart)
  const page = source.slice(pageStart, dialogStart)

  assert.match(page, /Send Swarm a background task\. Sessions start automatically\./)
  assert.match(page, /<DesktopV3AgenticComposer/)
  assert.match(page, /inputLabel="Send Swarm a background task"/)
  assert.match(page, /showModePicker=\{false\}/)
  assert.match(page, /executionLabel="background"/)
  assert.doesNotMatch(page, /<textarea|<form/)
  assert.match(source, /const handleStartBackgroundRouterSession = useCallback\(\(submittedRequest = backgroundTaskRequest\)/)
  assert.match(source, /postDesktopV3BackgroundRouterSessionStart\(\{[\s\S]*?\.\.\.activeWorkspaceAuthority[\s\S]*?input: request[\s\S]*?plan_mode_requested: false/)
  assert.match(source, /Background Router task sent\.', tone: 'success'/)
  assert.match(source, /void launch\.catch\([\s\S]*?tone: 'error'/)
  assert.doesNotMatch(source, /backgroundTaskBusy|setBackgroundTaskBusy|await postDesktopV3BackgroundRouterSessionStart/)
})

test('mobile pages preserve submission, selected workspace, worktree fields, and cancel navigation', async () => {
  const source = await readFile(appPage, 'utf8')

  assert.match(source, /const clientRequestId = `desktop-v3-background-router:/)
  assert.match(source, /postDesktopV3BackgroundRouterSessionStart/)
  assert.match(source, /presentation: 'page'/)
  assert.match(source, /name="title"[\s\S]*?text-\[16px\]/)
  assert.match(source, /<select[\s\S]*?selectedExistingPath[\s\S]*?text-\[16px\]/)
  assert.match(source, /name="branch"[\s\S]*?text-\[16px\]/)
  assert.match(source, /createDesktopV3CreateOnlySessionOperation\([\s\S]*?worktree: \{ mode: 'on', branchName: branch, existingPath: existingWorktree\?\.path \}/)
  assert.match(source, /mobileCreationPage === 'task'[\s\S]*?navigate\(\{ to: '\/\$workspaceSlug'/)
  assert.match(source, /mobileCreationPage === 'worktree'[\s\S]*?navigate\(\{ to: '\/\$workspaceSlug'/)
  assert.match(source, /onSubmit=\{onSubmit\}/)
  assert.match(source, /onSubmit=\{\(request\) => \{ void handleStartBackgroundRouterSession\(request\) \}\}/)
  assert.match(source, /event\.preventDefault\(\)\s*onSubmit\(request\)/)
  assert.doesNotMatch(source, /submitTaskOnMobileEnter|restoreMobileDialogInitialView|submitMobileDialog/)
  assert.doesNotMatch(source, /window\.scrollTo|requestAnimationFrame\(resetScroll\)|setTimeout\(resetScroll/)
})

test('mobile homepage is safe-area-aware while preserving desktop toast placement', async () => {
  const source = await readFile(appPage, 'utf8')
  const pane = await readFile(newSessionPane, 'utf8')

  assert.match(pane, /data-testid="mobile-workspace-session-list"/)
  assert.match(pane, /pt-\[var\(--app-safe-area-top\)\][^\"]*sm:hidden/)
  assert.match(source, /left-4 right-4 top-\[calc\(var\(--app-safe-area-top\)\+1rem\)\]/)
  assert.match(source, /sm:left-auto sm:right-6 sm:top-6 sm:max-w-md/)
})

test('mobile session list is mounted above the composer without replacing the desktop pending shell', async () => {
  const pane = await readFile(newSessionPane, 'utf8')
  const sessionList = pane.indexOf('data-testid="mobile-workspace-session-list"')
  const pendingShell = pane.indexOf('<DesktopV3RoutedPendingShell', sessionList)
  const composer = pane.indexOf('<DesktopV3AgenticComposer', pendingShell)

  assert.ok(sessionList >= 0 && pendingShell > sessionList && composer > pendingShell)
  assert.match(pane.slice(sessionList, pendingShell), /\{mobileSessionQuickMenu\}/)
  assert.match(pane.slice(pendingShell, composer), /className=\{mobileSessionQuickMenu \? 'hidden sm:flex' : undefined\}/)
  assert.match(pane.slice(composer), /routedNewSession/)
})

test('mobile task dictation remains capability-gated', async () => {
  const source = await readFile(appPage, 'utf8')
  const formStart = source.indexOf('function BackgroundTaskForm')
  const formEnd = source.indexOf('function WorktreeSessionForm', formStart)
  const form = source.slice(formStart, formEnd)

  assert.match(source, /speechWindow\.SpeechRecognition \?\? speechWindow\.webkitSpeechRecognition \?\? null/)
  assert.match(form, /dictation\.supported \? \(/)
  assert.match(form, /aria-label=\{dictation\.listening \? 'Stop microphone dictation' : 'Start microphone dictation'\}/)
  assert.match(form, /onChange=\{\(event\) => onRequestChange\(event\.target\.value\)\}/)
})

test('new-session mobile header hides draft identity while preserving desktop identity', async () => {
  const pane = await readFile(newSessionPane, 'utf8')
  const header = await readFile(chatHeader, 'utf8')

  assert.match(pane, /<DesktopV3ChatHeader[\s\S]*?hideMobileIdentity[\s\S]*?\/>/)
  assert.match(header, /!hideMobileIdentity \? \([\s\S]*?<div className="sm:hidden">/)
  assert.match(header, /<div className="hidden min-w-0 sm:block">/)
})
