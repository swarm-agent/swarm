import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { DesktopSlashCommandPanel } from '../chat/components/desktop-slash-command-panel'
import { buildDesktopSlashPaletteState } from '../chat/services/slash-commands'

// Purpose: the Swarm page must permit browser-native transcript selection and copy,
// without pinning sidebar colors to a dark palette. The OrchestrateView boundary owns
// selection inheritance; the scoped CSS owns the selected theme. Static source/CSS
// assertions are narrow here because rendering a live V3 sidebar requires a session.
test('Swarm sidebar inherits theme and allows native text selection', async () => {
  const view = await readFile(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const css = await readFile(new URL('./swarm-section.css', import.meta.url), 'utf8')
  assert.match(view, /className="swarm-section relative flex h-screen w-screen overflow-hidden p-3 gap-3 font-sans"/)
  assert.match(view, /className="swarm-ai-sidebar /)
  assert.doesNotMatch(view, /'--app-bg': '#0d121f'/)
  assert.match(css, /\.swarm-section \{[^\n]*user-select: text;/)
  assert.match(css, /\.swarm-section \.swarm-ai-sidebar \{[^\n]*background: var\(--swarm-surface\)/)
})

// Purpose: slash hints must stay a compact, vertically stacked view of the canonical
// composer command catalog. The panel owns presentation, while slash-commands owns
// matching. An ordinary draft must not acquire a spurious command palette.
test('slash hints show matching commands in compact stacked rows', () => {
  const slash = buildDesktopSlashPaletteState('/wor')
  assert.ok(slash.matches.length > 0)
  const html = renderToStaticMarkup(<DesktopSlashCommandPanel palette={slash} selectedIndex={0} onHover={() => {}} onSelect={() => {}} />)
  assert.match(html, /role="listbox"/)
  assert.match(html, /role="option" aria-selected="true"/)
  assert.match(html, /grid-cols-\[28px_minmax\(0,1fr\)\]/)
  assert.match(html, /\/workspace|\/worktrees/)
  assert.equal(renderToStaticMarkup(<DesktopSlashCommandPanel palette={buildDesktopSlashPaletteState('hello')} selectedIndex={0} onHover={() => {}} onSelect={() => {}} />), '')
})
