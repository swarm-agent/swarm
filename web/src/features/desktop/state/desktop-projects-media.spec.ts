import test from 'node:test'
import assert from 'node:assert/strict'
import { mapBackendTask, reduceDesktopProjectsState } from './desktop-projects-state'

// Requirement: realtime project hydration retains result-owned media identity.
// Regression: moving hydration out of OrchestrateView must not drop provenance
// or replace missing result settings with the task's requested settings.
// Authority: mapBackendTask; a pure adapter test is the narrowest boundary.
test('project hydration preserves deliverable provenance without task-setting substitution', () => {
  const provenance = { provider: 'google', model: 'video-model', operation: 'create' }
  const task = mapBackendTask({
    id: 'task-media', agent: 'video', model: 'requested-model', aspect_ratio: '1:1',
    deliverables: [{ id: 'result', kind: 'video', model: 'result-model', aspect_ratio: '16:9',
      resolution: '720p', duration_seconds: 8, video_provenance: provenance },
      { id: 'historical', kind: 'video' }],
  })
  const [result, historical] = task.deliverables!
  assert.equal(result.model, 'result-model')
  assert.equal(result.aspectRatio, '16:9')
  assert.equal(result.videoAspect, '16:9')
  assert.equal(result.resolution, '720p')
  assert.equal(result.durationSeconds, 8)
  assert.deepEqual(result.videoProvenance, provenance)
  assert.equal(historical.model, undefined)
  assert.equal(historical.aspectRatio, undefined)
  assert.equal(historical.videoAspect, undefined)
  assert.equal(historical.videoProvenance, undefined)
})

// Requirement: slot count, prompt and ready state survive reconnect hydration;
// an older project response cannot replace a newer image task revision.
// Authority: reduceDesktopProjectsState, tested directly without transport timers.
test('ten durable image slots survive stale hydration and retain original prompts', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', revision: 4, variant_count: 10, description: 'original café',
    deliverables: Array.from({ length: 10 }, (_, i) => ({ id: `slot-${i}`, kind: 'image', status: i === 0 ? 'ready' : 'queued', media_url: i === 0 ? 'data:image/png;base64,result' : undefined })) })
  const state = { project: { projectId: 'project', tasks: [task], media: [], loading: false, stale: false, generation: 0 } }
  const stale = mapBackendTask({ id: 'images', agent: 'image', revision: 1, deliverables: [{ id: 'slot-0', status: 'pending' }] })
  const next = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'project', tasks: [stale] })
  assert.equal(next.project.tasks[0].deliverables!.length, 10)
  assert.equal(next.project.tasks[0].deliverables![0].status, 'ready')
  assert.equal(next.project.tasks[0].deliverables![0].prompt, 'original café')
})
