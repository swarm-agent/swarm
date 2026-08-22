import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./workspace-home-identity.tsx', import.meta.url), 'utf8')

test('workspace home identity keeps the canonical themed Swarm SVG as fallback artwork', () => {
  assert.match(source, /viewBox="0 0 400 400"/)
  assert.match(source, /x="20" y="20" width="360" height="360" rx="90"/)
  assert.match(source, /x="180" y="180" width="40" height="40" rx="10"/)
  assert.match(source, /fill="currentColor"/)
  assert.match(source, /workspace\.iconPNGDataURL \? \(/)
  assert.doesNotMatch(source, /image\/svg|\.svg|innerHTML/)
})

test('workspace mark opens a PNG-only icon manager for every workspace', () => {
  assert.match(source, /aria-label="Manage workspace icons"/)
  assert.match(source, /aria-label="Workspace icon manager"/)
  assert.match(source, /Custom icons must be PNG files smaller than 1 MB/)
  assert.match(source, /accept="image\/png,.png"/)
  assert.match(source, /file\.type !== 'image\/png'/)
  assert.match(source, /workspaces\.map/)
  assert.match(source, /onSetWorkspaceIcon\?\.\(targetPath, dataURL\)/)
  assert.match(source, /onSetWorkspaceIcon\?\.\(path, ''\)/)
  assert.match(source, /Use default Swarm icon/)
})

test('workspace icon manager uses an effective compact responsive width', () => {
  assert.match(source, /style=\{\{ width: 'min\(546px, calc\(100vw - 24px\)\)' \}\}/)
  assert.doesNotMatch(source, /420px/)
})

test('workspace home control opens an inline dropdown instead of the Alt+W picker', () => {
  assert.match(source, /workspaces\.length > 1 && Boolean\(onSelectWorkspace\)/)
  assert.match(source, /aria-haspopup="menu"/)
  assert.match(source, /role="menu"/)
  assert.match(source, /role="menuitemradio"/)
  assert.match(source, /onSelectWorkspace\?\.\(entry\)/)
  assert.doesNotMatch(source, /onOpenWorkspacePicker/)
  assert.match(source, /<h1[^>]*>\{workspace\.workspaceName\}<\/h1>/)
})
