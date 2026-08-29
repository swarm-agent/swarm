import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const paneURL = new URL('./desktop-v3-new-session-pane.tsx', import.meta.url)
const composerURL = new URL('./desktop-v3-agentic-composer.tsx', import.meta.url)

test('new Desktop chat uses only the routed controller and endpoint before activation', async () => {
  const source = await readFile(paneURL, 'utf8')

  assert.match(source, /new DesktopV3RoutedNewSessionController\(async \(request\)/)
  assert.doesNotMatch(source, /routedController\.startDraft\(\)/)
  assert.match(source, /initialControllerState\.phase === 'failed'/)
  assert.match(source, /postDesktopV3RoutedSessionStart\(request\)/)
  assert.match(source, /controller\.submit\(\{/)
  assert.match(source, /initialPrompt\?: string/)
  assert.match(source, /initialControllerState\.phase === 'failed' \? '' : initialPrompt\.trim\(\)/)
  assert.match(source, /const commandPrompt = initialPrompt\.trim\(\)[\s\S]*routedController\.startDraft\(commandPrompt, snapshot\)/)
  assert.match(source, /const initialCommandStarting = Boolean\(initialCommandPrompt\)[\s\S]*const activationPending = initialCommandStarting[\s\S]*routedState\.phase === 'routing'[\s\S]*routedState\.phase === 'resolved'[\s\S]*const pendingState = routedState\.phase === 'failed' \? routedState\.phase : 'draft'/)
  assert.match(source, /initialCommandPrompt[\s\S]*initialPromptSubmittedRef\.current[\s\S]*handleSubmit\(createDesktopV3RoutedComposerSnapshot\(\{[\s\S]*prompt: initialCommandPrompt[\s\S]*planModeRequested: initialPlanModeRequested/)
  assert.match(source, /controller\.submit\(\{[\s\S]*snapshot: captured[\s\S]*metadata: desktopRoutedSessionMetadata\(\{ source: 'desktop-v3' \}\)/)
  assert.match(source, /controller\.retry\(\)/)
  assert.match(source, /controller\.prepareOperationIdentity\(\)/)
  assert.match(source, /stageDesktopComposerAttachments\(\{/)
  assert.match(source, /routedClientRequestId: identity\.clientRequestId/)
  assert.match(source, /existing: history/)
  assert.match(source, /stagedHistory\.slice\(history\.length\)/)
  assert.match(source, /attachments: snapshot\.attachments/)
  assert.match(source, /snapshot\.attachments\.length > 0 \? 'Please review the attached file\(s\)\.'/)
  assert.match(source, /captured\.attachments\.length !== stagedAttachmentsRef\.current\.length/)
  assert.match(source, /setLocalError\(cause instanceof Error \? cause\.message : 'Routed session start failed\.'\)/)
  assert.match(source, /operationAttachmentsRef\.current = \[\.\.\.stagedAttachmentsRef\.current\]/)
  assert.match(source, /reconcileDesktopComposerStagedAttachments\(submittedAttachments, result\.first_message\)/)
  assert.match(source, /result\.first_message\.media\?\.length/)
  assert.match(source, /onRoutedSessionResolved: \(result: DesktopV3RoutedStartResult\)/)
  assert.match(source, /Promise\.resolve\(\)[\s\S]*resolvedCallbackRef\.current\(routedState\.result\)/)
  assert.match(source, /controller\.acknowledgeResolved\(operationId\)/)
  assert.match(source, /controller\.getState\(\)\.phase === 'resolved'[\s\S]*controller\.rejectResolved\(operationId, error\)/)

  assert.doesNotMatch(source, /\bcreateDesktopV3NewSessionOperation\b/)
  assert.doesNotMatch(source, /\bstartNewDesktopV3Session\b/)
  assert.doesNotMatch(source, /\bappendFirstDesktopV3Message\b/)
  assert.doesNotMatch(source, /modeCommand|pendingWorktreeBranch|initialMode|onModeChange|workspaceSlug|routeOptions|onOpenChats/)
  assert.doesNotMatch(source, /dispatchDesktopV3Cache|retainDesktopV3RealtimeController|bootstrapDesktopV3SidebarMetadataOnly/)
  assert.doesNotMatch(source, /useNavigate|navigate\(|URLSearchParams|promptParam/)
})

test('routed pending and failure stay local and retry the persisted controller identity', async () => {
  const paneSource = await readFile(paneURL, 'utf8')
  const composerSource = await readFile(composerURL, 'utf8')

  assert.match(paneSource, /<DesktopV3ChatHeader[\s\S]*title="New chat"[\s\S]*workspaceName=\{workspace\.workspaceName\}[\s\S]*runStatus=\{headerStatus\}/)
  assert.match(paneSource, /const headerStatus:[\s\S]*activationPending[\s\S]*'Opening…'[\s\S]*'Routing…'[\s\S]*'Start failed'/)
  assert.match(paneSource, /<DesktopV3RoutedPendingShell[\s\S]*state=\{pendingState\}[\s\S]*startPath="router"[\s\S]*pendingPrompt=\{routedState\.prompt\}/)
  assert.match(paneSource, /routedState\.phase === 'failed'[\s\S]*controller\.retry\(\)/)
  assert.match(paneSource, /const visibleAttachments = restoredAttachments\.filter[\s\S]*visibleAttachments\.length !== current\.snapshot\.attachments\.length[\s\S]*controller\.retry\(\)/)
  assert.match(paneSource, /removedStagedAttachmentIdsRef\.current\.add\(stagingId\)/)
  assert.match(paneSource, /stagedAttachmentsRef\.current = visibleAttachments/)
  assert.match(paneSource, /setDraft\(routedState\.snapshot\.prompt\)/)
  assert.doesNotMatch(paneSource, /setWorktreeIntent/)
  assert.match(paneSource, /setMode\(routedState\.snapshot\.planModeRequested \? 'plan' : 'auto'\)/)
  assert.match(paneSource, /setRestoredSnapshot\(routedState\.snapshot\)/)
  assert.match(paneSource, /routedState\.phase === 'draft'/)
  assert.match(paneSource, /createDesktopV3RoutedComposerSnapshot\(\{/)
  assert.match(composerSource, /attachments: desktopComposerStagedMediaInput\(routedStagedAttachments\)/)
  assert.match(composerSource, /selectedAction: selectedWorkspaceAction/)
  assert.match(composerSource, /selectedSkill: selectedWorkspaceSkill/)
  assert.doesNotMatch(composerSource, /worktreePrimed/)
  assert.match(composerSource, /planModeRequested: mode === 'plan'/)
  assert.match(composerSource, /setSelectedWorkspaceAction\(\(routedComposerSnapshot\.selectedAction as WorkspaceAction \| null\) \?\? null\)/)
  assert.match(composerSource, /setSelectedWorkspaceSkill\(\(routedComposerSnapshot\.selectedSkill as WorkspaceSkill \| null\) \?\? null\)/)
  assert.match(composerSource, /routedTextFiles = files\.filter\(isComposerTextFile\)/)
  assert.match(composerSource, /routedMediaFiles = files\.filter\(\(file\) => !isComposerTextFile\(file\)\)/)
  assert.match(composerSource, /textAttachments\.reduce\([\s\S]*appendComposerTextFile\(nextDraft, attachment\.name, attachment\.fileType, attachment\.content\)/)
  assert.match(paneSource, /<DesktopV3AgenticComposer[\s\S]*disabled=\{activationPending\}[\s\S]*busy=\{activationPending\}/)
  assert.match(paneSource, /className="flex min-h-0 flex-1 flex-col overflow-hidden sm:hidden"[\s\S]*data-testid="mobile-workspace-session-list"/)
  assert.match(paneSource, /routedNewSession/)
})

test('all successful direct starts retain the new-chat surface until destination activation', async () => {
  const source = await readFile(paneURL, 'utf8')

  assert.match(source, /const activationPending = initialCommandStarting[\s\S]*routedState\.phase === 'routing'[\s\S]*routedState\.phase === 'resolved'/)
  assert.match(source, /const pendingState = routedState\.phase === 'failed' \? routedState\.phase : 'draft'/)
  assert.match(source, /startPath="router"/)
  assert.match(source, /<DesktopV3AgenticComposer[\s\S]*disabled=\{activationPending\}[\s\S]*busy=\{activationPending\}/)
  assert.doesNotMatch(source, /activationPending \? 'routing'/)
  assert.doesNotMatch(source, /showComposer/)
})

test('Shift+Tab primes Plan only in the routed new-session composer', async () => {
  const source = await readFile(composerURL, 'utf8')

  assert.match(source, /routedNewSession && event\.key === 'Tab' && event\.shiftKey/)
  assert.match(source, /event\.preventDefault\(\)\s*onModeSelect\?\.\('plan'\)/)
  assert.doesNotMatch(source, /event\.preventDefault\(\)\s*onRoutedWorktreeRequestedChange\?\.\(true\)\s*onModeSelect\?\.\('plan'\)/)
  assert.match(source, /aria-keyshortcuts=\{routedNewSession \? 'Shift\+Tab' : undefined\}/)
  assert.match(source, /Shift\+Tab enables Plan for this new session/)
  assert.doesNotMatch(source, /window\.addEventListener\('keydown'[\s\S]*Shift\+Tab/)
})

test('worktree isolation has no routed composer toggle or command', async () => {
  const source = await readFile(composerURL, 'utf8')

  assert.doesNotMatch(source, /worktreePrimed|enable-new-session-worktree|onRoutedWorktreeRequestedChange|worktreeCommandWarning/)
})

test('routed new-chat composer exposes Plan as its only execution-mode choice with a waiting model bar', async () => {
  const source = await readFile(composerURL, 'utf8')

  assert.match(source, /routedNewSession\?: boolean/)
  assert.match(source, /routedNewSession && showModePicker/)
  assert.match(source, /<DesktopComposerPlanToggle[\s\S]*active=\{mode === 'plan'\}[\s\S]*onActiveChange=\{\(active\) => onModeSelect\?\.\(active \? 'plan' : 'auto'\)\}[\s\S]*allowDisable/)
  assert.match(source, /resolvedSessionControls && showModePicker && mode === 'plan'[\s\S]*<DesktopComposerPlanToggle active readOnly/)
  assert.match(source, /onAttach=\{routedNewSession \? \(onRoutedStageAttachments/)
  assert.doesNotMatch(source, /DesktopRoutedWorktreePrime|routedWorktreeRequested/)
  assert.match(source, /statusLabel=\{modelStatusLabel\}/)
  assert.doesNotMatch(source, /Attachments will be available after routed staging is connected/)
})
