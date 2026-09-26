import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { MemoryStorageDriver } from '../storage/adapters/memory.js'
import { WorkerStorageHub } from '../storage/hub.js'
import { runSocialMediaWorker } from '../../examples/social-media-worker.js'

describe('Social Media Worker & Storage Hub Integration', () => {
  it('runs social media campaign workflow and publishes verified deliverables', async () => {
    const driver = new MemoryStorageDriver()

    const result = await runSocialMediaWorker({
      driver,
      workerId: 'social-media-agent',
      topic: 'Swarm S3 State Hub Launch',
      targetChannels: ['twitter_x', 'linkedin'],
    })

    assert.equal(result.workerId, 'social-media-agent')
    assert.ok(result.sessionId.startsWith('sess_'))
    assert.ok(result.deliverableId.startsWith('deliv_'))
    assert.equal(result.manifest.files.length, 3)

    // Verify worker definition in storage
    const hub = new WorkerStorageHub({ workerId: 'social-media-agent', driver })
    const workerDef = await hub.getWorkerManifest()
    assert.ok(workerDef)
    assert.equal(workerDef?.name, 'Social Media Campaign Worker')
    assert.deepEqual(workerDef?.tags, ['marketing', 'social', 'announcements', 'cloud-worker'])

    // Verify base context
    const baseContext = await hub.loadBaseContext()
    assert.ok(baseContext.instructions?.includes("autonomous social media strategist"))
    assert.equal(baseContext.tools?.length, 3)
    assert.equal((baseContext.memory as any)?.brand, 'Swarm Agent')

    // Verify session state
    const sessionState = await hub.getSessionState(result.sessionId)
    assert.ok(sessionState)
    assert.equal(sessionState?.status, 'completed')
    assert.equal(sessionState?.progress, 100)

    // Verify deliverable manifest
    const manifest = await hub.getDeliverableManifest(result.deliverableId)
    assert.ok(manifest)
    assert.equal(manifest?.title, 'Social Media Campaign: Swarm S3 State Hub Launch')
    assert.equal(manifest?.files.length, 3)

    const fileNames = manifest?.files.map((f) => f.name)
    assert.ok(fileNames?.includes('announcement-thread.md'))
    assert.ok(fileNames?.includes('campaign-schedule.json'))
    assert.ok(fileNames?.includes('visual-prompts.json'))

    for (const f of manifest!.files) {
      assert.ok(f.sha256 && f.sha256.length === 64, `File ${f.name} missing valid SHA-256`)
    }
  })
})
