import assert from 'node:assert/strict'
import test from 'node:test'
import { classifyVideoProposals, pendingProposalForRevision } from './video-proposal-state'

// Requirement: match the service's exact-revision render gate without accepting
// older pending cuts. Threat: stale initial proposals strand newer confirmed
// native media, or historical pending previews bypass review. Pure identity
// classification is the narrowest layer; backend authority is tested separately.
test('older initial 4s proposal does not block a confirmed native 8s cut', () => {
  const confirmed = { id: 'r4', project_id: 'project', session_id: 'session', timeline: { duration_ms: 8000, fps: 30, clips: [{ media_type: 'video/mp4', native_fps: 60 }] } }
  const older = { id: 'initial', project_id: 'project', session_id: 'session', status: 'pending', working_revision_id: 'r2', duration_ms: 4000 }
  const accepted = { ...older, id: 'accepted', status: 'accepted', working_revision_id: 'r4' }
  const before = JSON.stringify([confirmed, older, accepted])
  const state = classifyVideoProposals([older, accepted], confirmed)
  assert.equal(state.current, null)
  assert.deepEqual(state.stale, [older])
  assert.deepEqual(state.historical, [accepted])
  assert.equal(pendingProposalForRevision([older], { ...confirmed, id: 'r2' }), older)
  assert.equal(confirmed.timeline.fps, 30, 'native media FPS never silently changes timeline FPS')
  assert.equal(JSON.stringify([confirmed, older, accepted]), before, 'classification changes no proposal or cut')
})

test('current actionable proposal blocks only its own working revision and scope', () => {
  const revision = { id: 'working', project_id: 'project', session_id: 'session' }
  const pending = { ...revision, id: 'proposal', status: 'pending', working_revision_id: 'working' }
  assert.equal(classifyVideoProposals([pending], revision).current, pending)
  assert.equal(pendingProposalForRevision([pending], { ...revision, id: 'confirmed' }), null)
  assert.equal(pendingProposalForRevision([pending], { ...revision, project_id: 'foreign' }), null)
  assert.equal(pendingProposalForRevision([pending], { ...revision, session_id: 'foreign' }), null)
  assert.equal(pendingProposalForRevision([pending], null), null)
})
