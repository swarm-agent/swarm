import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopV3ClientEffectRunner, durableClientEffectsFromRealtimeFrame } from './v3-client-effect-runner'
import { realtimeFrameToActions } from '../state/desktop-v3-cache-wire'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'

// Requirement: account catalog control frames hydrate only the shared workspace
// resource; replay duplicates and unrelated chatter must not multiply reads.
// Exercise the wire decoder and actual effect runner at their narrow boundary.
test('workspace catalog wire effects coalesce, dedupe replay and repair reconnect', async () => {
 const frame: RealtimeMessage = { protocol: 'v3.realtime', protocol_version: 1, kind: 'workspace.catalog.updated', endpoint_cursor: 'opaque' }
 assert.equal(realtimeFrameToActions(frame)[0].type, 'realtime.control')
 assert.deepEqual(durableClientEffectsFromRealtimeFrame(frame)?.effects, [{ type: 'refresh_workspaces' }])
 let calls = 0
 const runner = new DesktopV3ClientEffectRunner({ refreshAgents: async () => {}, refreshThemes: async () => {}, refreshProviders: async () => {}, refreshArtifacts: async () => {}, refreshWorkspaces: async () => { calls++ }, reportError: (_, error) => { throw error } })
 for (let i = 0; i < 100; i++) runner.accept(frame)
 await runner.waitForIdle()
 assert.equal(calls, 1)
 runner.accept(frame)
 runner.accept({ ...frame, kind: 'keepalive' })
 await runner.waitForIdle()
 assert.equal(calls, 1)
 runner.refreshWorkspaceCatalog()
 await runner.waitForIdle()
 assert.equal(calls, 2)
})
