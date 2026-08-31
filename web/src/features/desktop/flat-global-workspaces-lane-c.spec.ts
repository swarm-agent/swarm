import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import {
  applyHydrateSnapshot,
  applyRealtimeFrame,
  applyReconnectSnapshot,
  createEmptyDesktopV3CacheState,
} from './state/desktop-v3-cache-reducer'
import {
  hydrateSnapshotFixture,
  projectionA,
  realtimeFrameFixture,
  reconnectFixture,
  sessionA,
  sessionB,
} from './state/desktop-v3-cache.backend-fixtures'
import type {
  SessionSnapshot,
  V3SessionProjection,
  WorkspaceGrant,
  WorkspaceUsageProjection,
} from './state/desktop-v3-cache-types'
import { mapWorkspaceEntry, type WorkspaceEntryWire } from '../workspaces/launcher/types/workspace'

const source = async (relativePath: string): Promise<string> => readFile(new URL(relativePath, import.meta.url), 'utf8')

const primaryGrant: WorkspaceGrant = {
  kind: 'primary',
  workspace_id: 'workspace-a',
  workspace_generation: 7,
  path: '/saved/a',
  name: 'A',
  available: true,
}

const temporaryGrant: WorkspaceGrant = {
  kind: 'temporary',
  path: '/external/session-only',
  name: 'Temporary access',
  available: true,
}

const usageA: WorkspaceUsageProjection = {
  kind: 'primary',
  workspace_id: 'workspace-a',
  workspace_generation: 7,
  name: 'A',
  available: true,
}

const usageUnavailable: WorkspaceUsageProjection = {
  kind: 'primary',
  workspace_id: 'workspace-history',
  workspace_generation: 3,
  name: 'Historical workspace',
  available: false,
}

function sessionWithWorkspaceState(
  session: SessionSnapshot,
  grants: WorkspaceGrant[],
  usage: WorkspaceUsageProjection[],
): SessionSnapshot {
  return { ...session, workspace_grants: grants, workspace_usage: usage }
}

function projectionWithUsage(
  projection: V3SessionProjection,
  usage: WorkspaceUsageProjection[],
): V3SessionProjection {
  return { ...projection, workspace_usage: usage }
}

test('Lane C E2E-036 · saved workspace access and external-path escalation remain distinct policy outcomes', async () => {
  const runtimeScope = await source('../../../../swarmd/internal/run/service_workspace_scope.go')
  const scopeInspection = await source('../../../../swarmd/internal/tool/workspace_scope_request.go')
  const permissionPayload = await source('./permissions/services/permission-payload.ts')

  assert.match(scopeInspection, /if pathWithinAllowedRoots\(resolveAllowedRoots\(scope\), resolvedTarget\) \{/)
  assert.match(scopeInspection, /return ScopeExpansionRequest\{[\s\S]*\}, true, nil/)
  assert.match(runtimeScope, /workspaceScopePermissionRequirement = "workspace_scope"/)
  assert.match(runtimeScope, /WorkspaceGrantTemporary/)
  assert.match(permissionPayload, /permissionRequiresApproval[\s\S]*requirement\)\.toLowerCase\(\) === 'workspace_scope'/)
})

test('Lane C E2E-037/E2E-038 · linked authority is absent and external approval remains session-only', async () => {
  const workspaceHome = await source('../workspaces/pages/workspace-home-page.tsx')
  const workspaceEditor = await source('../workspaces/launcher/components/workspace-editor-modal.tsx')
  const permissionPayload = await source('./permissions/services/permission-payload.ts')
  const permissionModal = await source('./permissions/components/desktop-permission-modal.tsx')

  for (const uiSource of [workspaceHome, workspaceEditor, permissionModal]) {
    assert.doesNotMatch(uiSource, /Add To Workspace|Add linked folder|linkedDirectories|addLinkedDirectories|removeLinkedDirectory/)
  }
  assert.match(permissionPayload, /label: 'Allow temporarily for this chat'/)
  assert.match(permissionPayload, /const normalizedDecision = 'session_allow'/)
  assert.match(permissionPayload, /This does not add or change a workspace/)
  assert.doesNotMatch(permissionPayload, /label: 'Add To Workspace'/)
  assert.doesNotMatch(permissionModal, /payload\.addToWorkspace/)
})

test('Lane C E2E-039/E2E-040 · the complete catalog is paginated and recognizable metadata is rendered directly', async () => {
  const overviewFetch = await source('../workspaces/launcher/queries/fetch-workspace-overview.ts')
  const picker = await source('./shortcuts/components/desktop-workspace-picker.tsx')

  assert.match(overviewFetch, /workspaces\.push\(\.\.\.\(response\.workspaces \?\? \[\]\)\)/)
  assert.match(overviewFetch, /response\.has_more/)
  assert.match(overviewFetch, /response\.next_cursor/)
  assert.match(overviewFetch, /while \(cursor > 0\)/)
  assert.match(picker, /workspaces\.map\(\(workspace, index\)/)
  assert.match(picker, /\{workspace\.workspaceName\}/)
  assert.match(picker, /\{workspace\.path\}/)
  assert.doesNotMatch(picker, /workspace_scope|requestPermission|onResolvePermission/)
})

test('Lane C E2E-039 · flat catalog mapping retains stable identity and rejects linked membership semantics', () => {
  const wire: WorkspaceEntryWire = {
    path: '/saved/a',
    workspace_id: 'workspace-a',
    workspace_generation: 7,
    state: 'available',
    local_workspace_binding_id: 'binding-a',
    workspace_name: 'A',
    directories: ['/saved/a'],
    is_git_repo: true,
    sort_index: 1,
    added_at: 10,
    updated_at: 11,
    last_selected_at: 12,
    active: true,
    worktree_enabled: true,
  }

  const mapped = mapWorkspaceEntry(wire)
  assert.equal(mapped.workspaceId, 'workspace-a')
  assert.equal(mapped.workspaceGeneration, 7)
  assert.equal(mapped.state, 'available')
  assert.deepEqual(mapped.directories, [mapped.path])
})

test('Lane C E2E-041/E2E-043 · usage is observational, path-free identity metadata', () => {
  assert.equal('path' in usageA, false)
  assert.equal('path' in usageUnavailable, false)
  assert.deepEqual(
    Object.keys(usageA).sort(),
    ['available', 'kind', 'name', 'workspace_generation', 'workspace_id'],
  )

  const state = createEmptyDesktopV3CacheState()
  const hydratedSession = sessionWithWorkspaceState(sessionB, [primaryGrant, temporaryGrant], [usageA])
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionB.id]: hydratedSession },
    projections_by_session: { [sessionB.id]: projectionWithUsage({ ...projectionA, session_id: sessionB.id }, [usageA]) },
  }), [sessionB.id])

  assert.deepEqual(state.sessionsById[sessionB.id]?.kind === 'full'
    ? state.sessionsById[sessionB.id].session.workspace_grants
    : [], [primaryGrant, temporaryGrant])
  assert.deepEqual(state.projectionsBySession[sessionB.id]?.workspace_usage, [usageA])
  assert.notDeepEqual(state.sessionsById[sessionB.id]?.kind === 'full'
    ? state.sessionsById[sessionB.id].session.workspace_grants
    : [], state.projectionsBySession[sessionB.id]?.workspace_usage)
})

test('Lane C E2E-042 · targeted hydration replaces stale grants and usage with durable typed state', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionB.id] = {
    kind: 'full',
    session: sessionWithWorkspaceState(sessionB, [], []),
    needsHydrate: true,
  }

  const hydratedSession = sessionWithWorkspaceState(sessionB, [primaryGrant, temporaryGrant], [usageA])
  const hydratedProjection = projectionWithUsage(projectionA, [usageA])
  hydratedProjection.session_id = sessionB.id

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionB.id]: hydratedSession },
    projections_by_session: { [sessionB.id]: hydratedProjection },
  }), [sessionB.id])

  assert.equal(state.sessionsById[sessionB.id]?.kind, 'full')
  const session = state.sessionsById[sessionB.id]?.kind === 'full'
    ? state.sessionsById[sessionB.id].session
    : null
  assert.deepEqual(session?.workspace_grants, [primaryGrant, temporaryGrant])
  assert.deepEqual(session?.workspace_usage, [usageA])
  assert.deepEqual(state.projectionsBySession[sessionB.id]?.workspace_usage, [usageA])
})

test('Lane C E2E-044 · realtime projection updates usage without manufacturing a client grant', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionB.id] = {
    kind: 'full',
    session: sessionWithWorkspaceState(sessionB, [primaryGrant], [usageA]),
    needsHydrate: false,
  }

  const realtimeUsage = [usageA, usageUnavailable]
  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      projection: projectionWithUsage({ ...projectionA, session_id: sessionB.id }, realtimeUsage),
    }),
  })

  assert.deepEqual(state.projectionsBySession[sessionB.id]?.workspace_usage, realtimeUsage)
  const session = state.sessionsById[sessionB.id]?.kind === 'full'
    ? state.sessionsById[sessionB.id].session
    : null
  assert.deepEqual(session?.workspace_grants, [primaryGrant])
  assert.equal(session?.workspace_grants?.some((grant) => grant.workspace_id === 'workspace-history'), false)
})

test('Lane C · V3 session workspace update replaces the durable client session snapshot', () => {
  const state = createEmptyDesktopV3CacheState()
  const before = sessionWithWorkspaceState(sessionB, [primaryGrant], [usageA])
  state.sessionsById[sessionB.id] = { kind: 'full', session: before, needsHydrate: false }
  state.projectionsBySession[sessionB.id] = { ...projectionA, session_id: sessionB.id, last_event_seq: 1, projection_high_watermark_seq: 1 }

  const nextGrant: WorkspaceGrant = {
    kind: 'primary', workspace_id: 'workspace-next', workspace_generation: 3,
    path: '/saved/next', name: 'Next', available: true,
  }
  const nextUsage: WorkspaceUsageProjection = {
    kind: 'primary', workspace_id: 'workspace-next', workspace_generation: 3,
    name: 'Next', available: true,
  }
  const moved = sessionWithWorkspaceState({
    ...sessionB,
    workspace_path: '/saved/next',
    workspace_name: 'Next',
    updated_at: 30,
    metadata: {
      ...(sessionB.metadata ?? {}),
      swarm_v3_source_workspace_id: 'workspace-next',
      swarm_v3_source_workspace_path: '/saved/next',
    },
  }, [nextGrant], [nextUsage])
  const projection = projectionWithUsage({ ...projectionA, session_id: sessionB.id, last_event_seq: 2, projection_high_watermark_seq: 2, updated_at: 30 }, [nextUsage])

  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      session_id: sessionB.id,
      event_type: 'session.workspace.updated',
      event: {
        id: 'evt-workspace-moved', session_id: sessionB.id, seq: 2,
        event_type: 'session.workspace.updated', payload: { session: moved }, ts_unix_ms: 30,
      },
      projection,
    }),
  })

  const current = state.sessionsById[sessionB.id]
  assert.equal(current?.kind, 'full')
  assert.equal(current?.kind === 'full' ? current.session.workspace_path : '', '/saved/next')
  assert.equal(current?.kind === 'full' ? current.session.metadata?.swarm_v3_source_workspace_id : '', 'workspace-next')
  assert.deepEqual(state.projectionsBySession[sessionB.id]?.workspace_usage, [nextUsage])
})

test('Lane C E2E-045 · reconnect converges on server grants and usage without duplicate client authority', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionA.id] = {
    kind: 'full',
    session: sessionWithWorkspaceState(sessionA, [temporaryGrant, temporaryGrant], []),
    needsHydrate: false,
  }

  const reconnectedSession = sessionWithWorkspaceState(sessionA, [primaryGrant, temporaryGrant], [usageA])
  applyReconnectSnapshot(state, reconnectFixture({
    sessions_by_id: { [sessionA.id]: reconnectedSession },
    projections_by_session: { [sessionA.id]: projectionWithUsage(projectionA, [usageA]) },
  }))

  const session = state.sessionsById[sessionA.id]?.kind === 'full'
    ? state.sessionsById[sessionA.id].session
    : null
  assert.deepEqual(session?.workspace_grants, [primaryGrant, temporaryGrant])
  assert.deepEqual(session?.workspace_usage, [usageA])
  assert.deepEqual(state.projectionsBySession[sessionA.id]?.workspace_usage, [usageA])
})

test('Lane C E2E-046 · unavailable historical identities remain recognizable but path-free', () => {
  const state = createEmptyDesktopV3CacheState()
  const historicalSession = sessionWithWorkspaceState(sessionB, [], [usageUnavailable])

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionB.id]: historicalSession },
    projections_by_session: {
      [sessionB.id]: projectionWithUsage({ ...projectionA, session_id: sessionB.id }, [usageUnavailable]),
    },
  }), [sessionB.id])

  const historical = state.projectionsBySession[sessionB.id]?.workspace_usage?.[0]
  assert.equal(historical?.workspace_id, 'workspace-history')
  assert.equal(historical?.name, 'Historical workspace')
  assert.equal(historical?.available, false)
  assert.equal(historical ? 'path' in historical : true, false)
  const grants = state.sessionsById[sessionB.id]?.kind === 'full'
    ? state.sessionsById[sessionB.id].session.workspace_grants ?? []
    : []
  assert.equal(grants.some((grant) => grant.workspace_id === historical?.workspace_id), false)
})
