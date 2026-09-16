import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { WorkerSessionBanner, handleBannerLinkClick } from './worker-session-banner'
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

test('handleBannerLinkClick intercepts primary clicks for client-side navigation without full page reload', () => {
  let defaultPrevented = false
  let navigated = false

  const mockEvent = {
    defaultPrevented: false,
    button: 0,
    preventDefault: () => { defaultPrevented = true },
  }

  const handled = handleBannerLinkClick(mockEvent, () => { navigated = true })
  assert.equal(handled, true)
  assert.equal(defaultPrevented, true)
  assert.equal(navigated, true)
})

test('handleBannerLinkClick delegates to routerNavigate when onNavigate is not provided', () => {
  let defaultPrevented = false
  let routerNavigated = false

  const mockEvent = {
    defaultPrevented: false,
    button: 0,
    preventDefault: () => { defaultPrevented = true },
  }

  const handled = handleBannerLinkClick(mockEvent, undefined, () => { routerNavigated = true })
  assert.equal(handled, true)
  assert.equal(defaultPrevented, true)
  assert.equal(routerNavigated, true)
})

test('handleBannerLinkClick does not prevent default for modifier keys or middle clicks', () => {
  let defaultPrevented = false
  let navigated = false

  // Middle click (button 1)
  const middleClick = {
    defaultPrevented: false,
    button: 1,
    preventDefault: () => { defaultPrevented = true },
  }
  assert.equal(handleBannerLinkClick(middleClick, () => { navigated = true }), false)
  assert.equal(defaultPrevented, false)
  assert.equal(navigated, false)

  // Cmd-click (metaKey)
  const cmdClick = {
    defaultPrevented: false,
    button: 0,
    metaKey: true,
    preventDefault: () => { defaultPrevented = true },
  }
  assert.equal(handleBannerLinkClick(cmdClick, () => { navigated = true }), false)
  assert.equal(defaultPrevented, false)
  assert.equal(navigated, false)

  // Ctrl-click (ctrlKey)
  const ctrlClick = {
    defaultPrevented: false,
    button: 0,
    ctrlKey: true,
    preventDefault: () => { defaultPrevented = true },
  }
  assert.equal(handleBannerLinkClick(ctrlClick, () => { navigated = true }), false)
  assert.equal(defaultPrevented, false)
  assert.equal(navigated, false)

  // Alt-click (altKey)
  const altClick = {
    defaultPrevented: false,
    button: 0,
    altKey: true,
    preventDefault: () => { defaultPrevented = true },
  }
  assert.equal(handleBannerLinkClick(altClick, () => { navigated = true }), false)
  assert.equal(defaultPrevented, false)
  assert.equal(navigated, false)

  // Shift-click (shiftKey)
  const shiftClick = {
    defaultPrevented: false,
    button: 0,
    shiftKey: true,
    preventDefault: () => { defaultPrevented = true },
  }
  assert.equal(handleBannerLinkClick(shiftClick, () => { navigated = true }), false)
  assert.equal(defaultPrevented, false)
  assert.equal(navigated, false)
})

test('WorkerSessionBanner links render with cursor-pointer and proper href destinations', () => {
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
      },
    },
  } as any

  let targetNavigated: string | undefined = undefined
  const markup = renderToStaticMarkup(
    <WorkerSessionBanner
      sessionId={sessionId}
      workspaceSlug="my-workspace"
      onNavigateToWorkers={(target) => { targetNavigated = target }}
    />
  )

  assert.match(markup, /href="\/my-workspace\/workers"/)
  assert.match(markup, /title="Back to all Workers"/)
  assert.match(markup, /cursor-pointer/)
  assert.match(markup, /Worker Details/)
  assert.match(markup, /href="\/my-workspace\/workers\/auto-1"/)
})
