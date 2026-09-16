import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkerSessionBanner } from './worker-session-banner'
import { getDesktopV3CacheSnapshot } from '../../state/desktop-v3-cache-store'

test('WorkerSessionBanner renders clear context for scheduled occurrence runs', () => {
  const sessionId = 'occurrence-session-1'
  const authorSessionId = 'worker-author-1'

  const cache = getDesktopV3CacheSnapshot()
  cache.sessionsById[sessionId] = {
    kind: 'full',
    session: {
      id: sessionId,
      title: 'Health Check Run #42',
      metadata: {
        automation_v2_occurrence_id: 'occ-42',
        automation_v2_authoring_session_id: authorSessionId,
        automation_v2_digest: 'digest-1',
        automation_v2_revision: '1',
      },
    },
  } as any

  cache.sessionsById[authorSessionId] = {
    kind: 'full',
    session: {
      id: authorSessionId,
      title: 'Repository Health Check',
      automation_v2: {
        workspace_id: 'ws-1',
        schedule: {
          kind: 'cron',
          cron: '0 18 * * *',
          timezone: 'Europe/Berlin',
        },
      },
    },
  } as any

  cache.automationV2Pages['page-test'] = {
    input: { action: 'list', workspace_id: 'ws-1' },
    loading: false,
    stale: false,
    generation: 1,
    data: {
      records: [
        {
          automation_id: 'auto-1',
          session_id: authorSessionId,
          workspace_id: 'ws-1',
          revision: 1,
          digest: 'digest-1',
          enabled: true,
          cancelled: false,
          document: {
            title: 'Repository Health Check',
            info: { goal: 'Verify repo health' },
            automation_v2: {
              schema_version: 2,
              schedule: { kind: 'cron', cron: '0 18 * * *', timezone: 'Europe/Berlin' },
              missed: 'skip',
              overlap: 'serialize',
              activate_on_accept: true,
              expiration: { kind: 'indefinite' },
            },
            checkpoints: [],
          },
          next_due_at: 1000,
          accepted_at: 1000,
          authorization: { kind: 'indefinite' },
        },
      ],
      progress: {
        record: {} as any,
        observed_at: 1000,
        timezone: 'Europe/Berlin',
        occurrences: [
          {
            id: 'occ-42',
            session_id: sessionId,
            state: 'completed',
            closing_state: 'routine_clean',
            due_at: 1000,
            detail: 'Health check completed cleanly',
          },
        ],
        forecast: [],
        forecast_is_admission: false,
        complete: true,
      },
    },
  }

  const markup = renderToStaticMarkup(
    <WorkerSessionBanner
      sessionId={sessionId}
      workspaceSlug="my-workspace"
    />
  )

  // 1. Context container and accessibility
  assert.match(markup, /data-testid="worker-session-banner"/)
  assert.match(markup, /aria-label="Worker execution context"/)

  // 2. Clear badge identifying it as a scheduled worker run
  assert.match(markup, /Scheduled Run/)

  // 3. Worker title
  assert.match(markup, /Repository Health Check/)

  // 4. Timezone and cadence preserved in worker timezone
  assert.match(markup, /Europe\/Berlin/)
  assert.match(markup, /Daily at 18:00/)

  // 5. Closing state badge
  assert.match(markup, /Routine Clean/)

  // 6. Navigation links back to workers dashboard
  assert.match(markup, /href="\/my-workspace\/workers"/)
  assert.match(markup, /Back to all Workers/)
  assert.match(markup, /Worker Details/)
})

test('WorkerSessionBanner returns null for non-worker sessions', () => {
  const sessionId = 'regular-chat-session'
  const cache = getDesktopV3CacheSnapshot()
  cache.sessionsById[sessionId] = {
    kind: 'full',
    session: {
      id: sessionId,
      title: 'Regular Chat',
      metadata: {},
    },
  } as any

  const markup = renderToStaticMarkup(
    <WorkerSessionBanner
      sessionId={sessionId}
      workspaceSlug="my-workspace"
    />
  )

  assert.equal(markup, '')
})
