import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('Desktop V3 composer keeps its frame height stable when the input receives focus', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const frameClass = source.match(/DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME = "([^"]+)"/)?.[1]

  assert.ok(frameClass, 'expected the canonical composer frame class')
  assert.doesNotMatch(frameClass, /focus-within:(?:p|m)[tblrxy]?-/)
  assert.match(frameClass, /pb-\[calc\(0\.75rem\+var\(--app-safe-area-bottom\)\)\]/)
})

test('Desktop V3 composer hides compact controls unless the persisted preference is enabled', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(source, /showCompactButton = false/)
  assert.equal((source.match(/showCompactButton && onCompact \? compactButton\(\) : null/g) ?? []).length, 2)
  assert.doesNotMatch(source, /mobile.*plus|plus.*menu/i)
})

test('Desktop V3 composer opens task actions from a borderless plus trigger and primes without submitting', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const actions = await readFile(new URL('./desktop-composer-action-menu.tsx', import.meta.url), 'utf8')

  assert.equal((source.match(/<DesktopComposerActionMenu disabled=\{composerDisabled\} onPrimeTask=\{handlePrimeTask\} \/>/g) ?? []).length, 1)
  assert.match(source, /data-composer-bottom-row>\s*<DesktopComposerActionMenu disabled=\{composerDisabled\} onPrimeTask=\{handlePrimeTask\} \/>/)
  assert.doesNotMatch(source, /data-composer-input-row>\s*<DesktopComposerActionMenu/)
  assert.match(source, /primedTaskMode === 'plan'[\s\S]*?`\/task plan \$\{visibleDraft\}`/)
  assert.match(source, /primedTaskMode === 'action'[\s\S]*?`\/task \$\{visibleDraft\}`/)
  assert.match(source, /textarea\.focus\(\)/)
  assert.match(source, /textarea\.setSelectionRange\(cursorPosition, cursorPosition\)/)
  assert.match(source, /resizeTextareaElement\(textarea\)/)
  assert.doesNotMatch(source.match(/const handlePrimeTask[\s\S]*?(?=\n  const handleSubmitClick)/)?.[0] ?? '', /onSubmit|handleSubmitClick/)

  assert.match(actions, /<Plus size=\{18\}/)
  assert.match(actions, /aria-label="Open composer actions"/)
  assert.match(actions, /aria-haspopup="menu"/)
  assert.match(actions, /aria-expanded=\{open\}/)
  assert.match(actions, /onClick=\{toggleMenu\}/)
  assert.match(actions, /className="inline-flex h-9 w-9[^\"]*border-0[^\"]*bg-transparent[^\"]*shadow-none/)
  assert.doesNotMatch(actions.match(/aria-label="Open composer actions"[\s\S]*?<\/button>/)?.[0] ?? '', /\bborder border-/)

  assert.match(actions, /role="menu"/)
  assert.match(actions, /aria-label=\{view === 'task' \? 'Task type' : 'Composer actions'\}/)
  assert.match(actions, /data-testid="desktop-composer-actions-menu"/)
  assert.match(actions, /data-testid="desktop-composer-task-submenu"/)
  assert.equal((actions.match(/role="menuitem"/g) ?? []).length, 3)
  assert.match(actions, /role="menuitem"[\s\S]*?primeTask\('action'\)[\s\S]*?>Action</)
  assert.match(actions, /role="menuitem"[\s\S]*?primeTask\('plan'\)[\s\S]*?>Plan</)
  assert.match(actions, /role="menuitem"[\s\S]*?className="flex w-full items-center/)
  assert.match(actions, />Task</)
  assert.match(actions, /Start the work right away/)
  assert.match(actions, /Review the approach before work starts/)
  assert.match(actions, /Send your next message to a background agent in a managed worktree\./)
  assert.doesNotMatch(actions, /role="dialog"|role="tooltip"|desktop-composer-task-row|justify-between/)

  assert.match(actions, /const primeTask[\s\S]*?closeMenu\(\)[\s\S]*?onPrimeTask\(mode\)/)
  assert.match(actions, /document\.addEventListener\('pointerdown', handlePointerDown\)/)
  assert.match(actions, /event\.key === 'Escape'/)
  assert.match(actions, /if \(disabled\) closeMenu\(\)/)
  assert.match(source, /if \(dictationEnabledRef\.current\) stopDictation\(false\)/)
  assert.doesNotMatch(actions, /createPortal/)
})

test('Desktop V3 composer warnings and errors can be dismissed without a refresh', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(source, /aria-label="Dismiss composer error"/)
  assert.match(source, /onClick=\{\(\) => setDismissedComposerError\(visibleComposerError\)\}/)
  assert.match(source, /aria-label="Dismiss dictation warning"/)
  assert.match(source, /onClick=\{dismissDictationWarning\}/)
  assert.match(source, /const dismissDictationWarning = \(\) => \{[\s\S]*?setDictationError\(null\)[\s\S]*?stopDictation\(false\)/)
  assert.equal((source.match(/min-h-11 min-w-11 shrink-0 touch-manipulation/g) ?? []).length, 2)
})

test('Desktop V3 composer keeps the canonical joined plan and model control beside the plus menu', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const control = await readFile(new URL('./composer-plan-model-control.tsx', import.meta.url), 'utf8')

  assert.equal((source.match(/<ComposerPlanModelControl/g) ?? []).length, 1)
  assert.equal((source.match(/renderComposerControl\(openPicker, open\)/g) ?? []).length, 2)
  assert.equal((source.match(/<DesktopComposerActionMenu/g) ?? []).length, 1)
  assert.doesNotMatch(source, /iteration/i)
  assert.doesNotMatch(source, /<ModePicker/)
  assert.match(source, /const planToggle = \(\) => onModeSelect\?\.\(mode === 'plan' \? 'auto' : 'plan'\)/)
  assert.match(control, /data-composer-plan-model-control/)
  assert.doesNotMatch(control, /iteration/i)
  assert.match(control, /NotepadText/)
  assert.match(control, /onPlanToggle/)
  assert.match(control, /aria-pressed=\{planEnabled\}/)
  assert.match(control, /aria-haspopup="menu"/)
  assert.match(control, />\{modelLabel\}<\/span>/)
  assert.doesNotMatch(control, /profileName|profileLabel|primaryLabel|showSeparateModel|\bBot\b|agentName|Swarm/)
})
