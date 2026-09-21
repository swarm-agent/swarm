import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { renderToStaticMarkup } from 'react-dom/server'
import React from 'react'
import type {
  Deployment,
  DeploymentLease,
  Environment,
  EnvironmentOperation,
  EnvironmentSummary,
  OperationHistoryPage,
  OperationStatus,
} from '../environments/types/environments'
import {
  createInitialWorkspaceState,
  reduceDesktopEnvironmentsState,
  type DesktopEnvironmentsState,
} from './desktop-environments-state'
import {
  DesktopEnvironmentsRuntime,
  msUntilNextMidnight,
} from '../runtime/desktop-environments-runtime'
import { DeploymentsView } from '../environments/components/deployments-view'

// =============================================================================
// Requirement 1: Synthetic realtime duplicate and out-of-order frame rejection
// Threat: Realtime WebSocket replay or out-of-order frame delivery regresses
//         authoritative summary revision or causes redundant fetches.
// Authority: DesktopEnvironmentsRuntime.acceptFrame & reduceDesktopEnvironmentsState
// =============================================================================
test('Requirement-First: synthetic realtime duplicate and out-of-order frames are rejected monotonically', () => {
  let state: DesktopEnvironmentsState = {
    'ws-1': {
      ...createInitialWorkspaceState('ws-1'),
      summaryRevision: 10,
      summary: {
        accountScopeID: 'acc-1',
        workspaceID: 'ws-1',
        revision: 10,
        updated_at: 1000,
        active_deployments: 2,
        running_exec_ops: 1,
        queued_ops: 0,
        running_ops: 1,
        cancelling_ops: 0,
        failed_ops: 0,
        cleanup_failed_ops: 0,
        unknown_ops: 0,
        succeeded_ops: 5,
        cancelled_ops: 1,
        timed_out_ops: 0,
        total_ops: 7,
      },
    },
  }

  let refreshCalledCount = 0
  const runtime = new DesktopEnvironmentsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopEnvironmentsState(state, action)
    },
    fetchDeployments: async () => {
      refreshCalledCount++
      return { deployments: [], activeLeases: {} }
    },
    fetchOperationHistory: async () => {
      return {
        operations: [],
        daily_totals: [],
        summary: state['ws-1'].summary!,
        has_more: false,
      }
    },
    retainRealtime: () => ({ ready: Promise.resolve(), release: () => {} }),
  })

  // Acquire demand on ws-1 so invalidations trigger refresh
  runtime.acquire('ws-1')
  assert.equal(refreshCalledCount, 1, 'initial demand triggers first hydration read')

  // 1. Stale out-of-order frame with summary_revision: 8 (< 10)
  runtime.acceptFrame({
    kind: 'event',
    event: {
      type: 'environment.updated',
      session_id: '__environment__:acc-1:ws-1',
      payload: {
        workspace_id: 'ws-1',
        summary_revision: 8,
      },
    },
  })
  assert.equal(refreshCalledCount, 1, 'stale frame with lower revision must be ignored')

  // 2. Duplicate frame with summary_revision: 10 (== 10)
  runtime.acceptFrame({
    kind: 'event',
    event: {
      type: 'environment.updated',
      payload: {
        workspace_id: 'ws-1',
        summary_revision: 10,
      },
    },
  })
  assert.equal(refreshCalledCount, 1, 'duplicate frame with same revision must be ignored')

  // 3. Unrelated workspace frame must not invalidate ws-1
  runtime.acceptFrame({
    kind: 'event',
    event: {
      type: 'environment.updated',
      payload: {
        workspace_id: 'ws-other',
        summary_revision: 20,
      },
    },
  })
  assert.equal(refreshCalledCount, 1, 'unrelated workspace frame must not refresh demanded ws-1')

  // 4. Fresh frame with higher revision (11 > 10) must trigger refresh
  runtime.acceptFrame({
    kind: 'event',
    event: {
      type: 'environment.updated',
      payload: {
        workspace_id: 'ws-1',
        summary_revision: 11,
      },
    },
  })
  assert.equal(refreshCalledCount, 2, 'higher revision frame triggers invalidation and refresh')
})

// =============================================================================
// Requirement 2: Reconnect/in-flight invalidation and read coalescing
// Threat: Bursty realtime updates or reconnect events cause request storms;
//         concurrent refresh calls race and duplicate network traffic.
// Authority: DesktopEnvironmentsRuntime.refresh, flights, pendingInvalidations
// =============================================================================
test('Requirement-First: concurrent refresh calls coalesce into single flight; reconnect triggers gap repair', async () => {
  let fetchCount = 0
  const resolvers: Array<() => void> = []

  const runtime = new DesktopEnvironmentsRuntime({
    getState: () => ({
      'ws-1': createInitialWorkspaceState('ws-1'),
    }),
    dispatch: () => {},
    fetchDeployments: async () => {
      fetchCount++
      await new Promise<void>((resolve) => resolvers.push(resolve))
      return { deployments: [], activeLeases: {} }
    },
    fetchOperationHistory: async () => {
      return {
        operations: [],
        daily_totals: [],
        summary: {
          accountScopeID: 'acc-1',
          workspaceID: 'ws-1',
          revision: 1,
          updated_at: 1000,
          active_deployments: 0,
          running_exec_ops: 0,
          queued_ops: 0,
          running_ops: 0,
          cancelling_ops: 0,
          failed_ops: 0,
          cleanup_failed_ops: 0,
          unknown_ops: 0,
          succeeded_ops: 0,
          cancelled_ops: 0,
          timed_out_ops: 0,
          total_ops: 0,
        },
        has_more: false,
      }
    },
    retainRealtime: () => ({ ready: Promise.resolve(), release: () => {} }),
  })

  // Demand ws-1
  const lease = runtime.acquire('ws-1')
  assert.equal(fetchCount, 1, 'first acquire starts in-flight read')

  // Three concurrent refresh calls while first is pending
  const p1 = runtime.refresh('ws-1')
  const p2 = runtime.refresh('ws-1')
  const p3 = runtime.refresh('ws-1')
  assert.equal(fetchCount, 1, 'concurrent calls must coalesce into existing flight')

  // Invalidate while flight is active
  runtime.invalidate('ws-1')

  // Resolve the first flight
  resolvers.shift()?.()
  await Promise.all([lease.ready, p1, p2, p3])

  // Verify follow-up flight was scheduled once due to pending invalidation
  assert.equal(fetchCount, 2, 'invalidation during flight triggers exactly one follow-up read')
  resolvers.shift()?.()

  // Test cursor.error triggers gap repair
  runtime.acceptFrame({ kind: 'cursor.error' })
  assert.equal(fetchCount, 3, 'cursor.error triggers reconnect gap repair')
  resolvers.shift()?.()

  lease.release()
})

// =============================================================================
// Requirement 3: No polling in environments page and runtime
// Threat: 5s refetch interval drains resources and introduces polling jitter.
// Authority: pages/environments-page.tsx
// =============================================================================
test('Requirement-First: no refetchInterval or status polling exists in environments page', () => {
  const pageSource = readFileSync(new URL('../environments/pages/environments-page.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(
    pageSource,
    /refetchInterval:\s*\d+/,
    'environments-page.tsx must NOT contain refetchInterval polling churn',
  )
  assert.doesNotMatch(
    pageSource,
    /setInterval\(/,
    'environments-page.tsx must NOT introduce setInterval polling churn',
  )
})

// =============================================================================
// Requirement 4: Daily totals and summary are independent of visible history page rows
// Threat: UI calculates counts by summing loaded table rows, producing wrong
//         numbers when pagination (limit: 1) or filtering is active.
// Authority: reduceDesktopEnvironmentsState & DeploymentsView
// =============================================================================
test('Requirement-First: daily totals and summary counts are authoritative and independent of paginated rows', () => {
  // Authoritative summary says 5 active deployments, 3 executing commands, 40 succeeded.
  const authoritativeSummary: EnvironmentSummary = {
    accountScopeID: 'acc-1',
    workspaceID: 'ws-1',
    revision: 15,
    updated_at: 1000,
    active_deployments: 5,
    running_exec_ops: 3,
    queued_ops: 1,
    running_ops: 3,
    cancelling_ops: 0,
    failed_ops: 2,
    cleanup_failed_ops: 1,
    unknown_ops: 0,
    succeeded_ops: 40,
    cancelled_ops: 2,
    timed_out_ops: 1,
    total_ops: 50,
  }

  // Only 1 operation is loaded on the visible page due to pagination
  const singlePaginatedOperation: EnvironmentOperation = {
    operation_id: 'op-only-one',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    action: 'exec',
    status: 'running',
    revision: 1,
    created_at: 1000,
  }

  const historyPage: OperationHistoryPage = {
    operations: [singlePaginatedOperation],
    next_cursor: 'cursor-page-2',
    has_more: true,
    daily_totals: [
      {
        date: '2026-09-21',
        total_ops: 50,
        succeeded: 40,
        failed: 2,
        cancelled: 2,
        timed_out: 1,
        cleanup_failed: 1,
        unknown: 0,
        running: 3,
        exec_ops: 35,
        deploy_ops: 15,
      },
    ],
    summary: authoritativeSummary,
  }

  const markup = renderToStaticMarkup(
    <DeploymentsView
      workspaceId="ws-1"
      deployments={[]}
      activeLeases={{}}
      environments={[]}
      summary={authoritativeSummary}
      operations={[singlePaginatedOperation]}
      historyPage={historyPage}
      onRefresh={() => {}}
      onStartDeployment={async () => {}}
      onStopDeployment={async () => {}}
      onReleaseDeployment={async () => {}}
      onDestroyDeployment={async () => {}}
    />,
  )

  // Verify authoritative metrics are rendered from summary, NOT from visible rows (which is 0 deployments, 1 operation)
  assert.match(markup, /data-testid="count-active-deployments"[\s\S]*?>5</, 'Active deployments count must be 5 from summary, not loaded rows')
  assert.match(markup, /data-testid="count-executing-commands"[\s\S]*?>3</, 'Executing commands count must be 3 from summary, not loaded rows')
  assert.match(markup, /data-testid="count-daily-succeeded"[\s\S]*?>40</, 'Daily succeeded count must be 40 from summary, not loaded rows')

  // Verify daily totals card shows full 50 Total, even though only 1 operation is rendered
  assert.match(markup, /50 Total/, 'Daily total ops must show 50 from full history daily_totals')
})

// =============================================================================
// Requirement 5: Timezone date filter and bounded day rollover timer
// Threat: Day boundary crossing in local timezone fails to refresh daily totals;
//         interval polling is mistakenly used instead of date boundary timer.
// Authority: msUntilNextMidnight & scheduleDayRollover
// =============================================================================
test('Requirement-First: day rollover timer is bounded to next midnight in selected timezone without polling', () => {
  // Noon UTC
  const fixedNoonUTC = new Date('2026-09-21T12:00:00Z')
  const msUTC = msUntilNextMidnight('UTC', fixedNoonUTC)
  // 12 hours = 12 * 3600 * 1000 = 43,200,000 ms. Add buffer => ~43,201,000 ms.
  assert.ok(msUTC > 43000000 && msUTC < 44000000, `ms until UTC midnight should be ~12 hours, got ${msUTC}`)

  // 23:59:50 UTC (10 seconds to midnight)
  const fixedNearMidnight = new Date('2026-09-21T23:59:50Z')
  const msNearMidnight = msUntilNextMidnight('UTC', fixedNearMidnight)
  // ~11 seconds
  assert.ok(msNearMidnight >= 1000 && msNearMidnight <= 15000, `ms near midnight should be ~11 seconds, got ${msNearMidnight}`)

  // Ensure result is bounded
  assert.ok(msNearMidnight >= 1000, 'must be at least 1000ms')
  assert.ok(msUTC <= 86400000, 'must be at most 24 hours')
})

// =============================================================================
// Requirement 6: Nested cancellation and late response no resurrection
// Threat: Stopping deployment or cancelling operation marks instant fake success
//         or late responses resurrect cancelled/stopped operations.
// Authority: reduceDesktopEnvironmentsState receipt & loadSuccess protection
// =============================================================================
test('Requirement-First: operation cancellation preserves durable receipt and late response cannot resurrect', () => {
  const initialOp: EnvironmentOperation = {
    operation_id: 'op-supervised-1',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    deployment_id: 'dep-1',
    action: 'exec',
    status: 'running',
    revision: 2,
    created_at: 1000,
    observed_at: 2000,
  }

  let state: DesktopEnvironmentsState = {
    'ws-1': {
      ...createInitialWorkspaceState('ws-1'),
      summaryRevision: 2,
      operations: [initialOp],
    },
  }

  // User initiates Cancel
  const cancelReceiptTimestamp = 2500
  state = reduceDesktopEnvironmentsState(state, {
    type: 'environments.recordReceipt',
    workspaceId: 'ws-1',
    receipt: {
      operationId: 'op-supervised-1',
      action: 'cancel',
      status: 'cancelling',
      timestamp: cancelReceiptTimestamp,
    },
  })

  // Verify status is immediately 'cancelling' (durable receipt, NOT fake instant success)
  assert.equal(state['ws-1'].operations[0].status, 'cancelling')
  assert.equal(state['ws-1'].lastReceipt?.status, 'cancelling')

  // Now simulate a late network response from an older in-flight fetch observed at t=1800 (< 2500)
  // claiming the operation was still 'running'
  const staleLateOp: EnvironmentOperation = {
    ...initialOp,
    status: 'running',
    observed_at: 1800,
  }

  state = reduceDesktopEnvironmentsState(state, {
    type: 'environments.loadSuccess',
    workspaceId: 'ws-1',
    requestId: 'req-late',
    summary: {
      accountScopeID: 'acc-1',
      workspaceID: 'ws-1',
      revision: 2,
      updated_at: 1800,
      active_deployments: 1,
      running_exec_ops: 1,
      queued_ops: 0,
      running_ops: 1,
      cancelling_ops: 0,
      failed_ops: 0,
      cleanup_failed_ops: 0,
      unknown_ops: 0,
      succeeded_ops: 0,
      cancelled_ops: 0,
      timed_out_ops: 0,
      total_ops: 1,
    },
    deployments: [],
    activeLeases: {},
    historyPage: {
      operations: [staleLateOp],
      daily_totals: [],
      summary: {} as any,
      has_more: false,
    },
  })

  // Verify the operation was NOT resurrected to 'running' by the late response!
  const currentOp = state['ws-1'].operations.find((o) => o.operation_id === 'op-supervised-1')
  assert.equal(
    currentOp?.status,
    'cancelling',
    'Late response must NOT resurrect a cancelled operation back to running',
  )
})
