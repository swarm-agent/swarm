import test from 'node:test'
import assert from 'node:assert/strict'
import { mapBackendTask } from './desktop-projects-state'

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
