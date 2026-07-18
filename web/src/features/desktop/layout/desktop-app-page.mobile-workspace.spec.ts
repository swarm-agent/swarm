import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const appPage = new URL('./desktop-app-page.tsx', import.meta.url)
const router = new URL('../../../app/router.tsx', import.meta.url)
const newSessionPane = new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url)
const chatHeader = new URL('../chat/components/desktop-v3-chat-header.tsx', import.meta.url)

test('mobile workspace presents only Task and Worktree actions while desktop keeps New session', async () => {
  const source = await readFile(appPage, 'utf8')
  const start = source.indexOf('data-testid="mobile-workspace-home"')
  const end = source.indexOf('const handleWorkspaceSelect', start)
  const mobile = source.slice(start, end)
  const desktop = `${source.slice(0, start)}${source.slice(end)}`

  assert.ok(start >= 0 && end > start)
  const task = mobile.indexOf('>Task</span>')
  const worktree = mobile.indexOf('>Worktree</span>')
  assert.ok(task >= 0 && worktree > task)
  assert.doesNotMatch(mobile, /New session/i)
  assert.match(desktop, /label: 'New session'/)
  assert.match(desktop, /handleStartNewSessionInWorkspace\(topWorkspacePath, topWorkspaceLabel\)/)
  assert.match(mobile, /<ListChecks[^>]+aria-hidden="true"/)
  assert.match(mobile, /<GitBranch[^>]+aria-hidden="true"/)
  assert.match(mobile, /min-h-20 touch-manipulation/)
})

test('mobile workspace exposes active sessions and keeps prior sessions collapsed', async () => {
  const source = await readFile(appPage, 'utf8')

  assert.match(source, /desktopRouteWorkspacePathForSession\(node\.session, workspacePathByBindingId, knownWorkspacePaths\) === routeWorkspace\.path/)
  assert.match(source, /mobileWorkspaceSessionNodes\.filter\(\(node\) => sessionIsMobileActive\(node\.session\)\)/)
  assert.match(source, /mobileWorkspaceSessionNodes\.filter\(\(node\) => !sessionIsMobileActive\(node\.session\)\)/)
  assert.match(source, /group === 'needs_review' \|\| group === 'in_progress'/)
  assert.match(source, />Active sessions<\/h2>/)
  assert.match(source, /aria-expanded=\{mobilePreviousSessionsOpen\}/)
  assert.match(source, /mobilePreviousSessionsOpen \? <div className="mt-2 grid gap-2">/)
})

test('mobile Task and Worktree buttons navigate to dedicated routes', async () => {
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
  assert.match(source, /const handleQueueBackgroundTask = useCallback\(async \(submittedRequest = backgroundTaskRequest\)/)
  assert.match(source, /createWorkspaceAITask\(workspacePath, request, idempotencyKey\)/)
  assert.match(source, /setDesktopToast\(\{ message: 'Task started in the background\. Follow it from Active sessions\.', tone: 'success' \}\)/)
})

test('mobile pages preserve submission, selected workspace, worktree fields, and cancel navigation', async () => {
  const source = await readFile(appPage, 'utf8')

  assert.match(source, /const workspacePath = routeWorkspace\?\.path\.trim\(\) \?\? ''/)
  assert.match(source, /createWorkspaceAITask\(workspacePath, request, idempotencyKey\)/)
  assert.match(source, /presentation: 'page'/)
  assert.match(source, /name="title"[\s\S]*?text-\[16px\]/)
  assert.match(source, /<select[\s\S]*?selectedExistingPath[\s\S]*?text-\[16px\]/)
  assert.match(source, /name="branch"[\s\S]*?text-\[16px\]/)
  assert.match(source, /createDesktopV3CreateOnlySessionOperation\([\s\S]*?worktree: \{ mode: 'on', branchName: branch, existingPath: existingWorktree\?\.path \}/)
  assert.match(source, /mobileCreationPage === 'task'[\s\S]*?navigate\(\{ to: '\/\$workspaceSlug'/)
  assert.match(source, /mobileCreationPage === 'worktree'[\s\S]*?navigate\(\{ to: '\/\$workspaceSlug'/)
  assert.match(source, /onSubmit=\{onSubmit\}/)
  assert.match(source, /onSubmit=\{\(request\) => \{ void handleQueueBackgroundTask\(request\) \}\}/)
  assert.match(source, /event\.preventDefault\(\)\s*onSubmit\(request\)/)
  assert.doesNotMatch(source, /submitTaskOnMobileEnter|restoreMobileDialogInitialView|submitMobileDialog/)
  assert.doesNotMatch(source, /window\.scrollTo|requestAnimationFrame\(resetScroll\)|setTimeout\(resetScroll/)
})

test('desktop toast clears the mobile notch while preserving desktop placement', async () => {
  const source = await readFile(appPage, 'utf8')

  assert.match(source, /left-4 right-4 top-\[calc\(var\(--app-safe-area-top\)\+1rem\)\]/)
  assert.match(source, /sm:left-auto sm:right-6 sm:top-6 sm:max-w-md/)
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
