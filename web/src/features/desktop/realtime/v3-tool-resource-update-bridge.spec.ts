import test from 'node:test'
import assert from 'node:assert/strict'
import { QueryClient } from '@tanstack/react-query'

import { deriveDesktopV3ToolResourceUpdate, handleDesktopV3ToolResourceUpdate } from './v3-tool-resource-update-bridge'
import type { CacheEvent } from '../state/desktop-v3-cache-types'

function toolCompletedEvent(toolName: string, output: unknown): CacheEvent {
  return {
    source: 'realtime',
    sessionId: 'session-1',
    eventType: 'session.tool.completed',
    payload: {
      tool_name: toolName,
      output: typeof output === 'string' ? output : JSON.stringify(output),
    },
  }
}

function toolDeltaEvent(toolName: string, output: unknown): CacheEvent {
  return {
    ...toolCompletedEvent(toolName, output),
    eventType: 'session.tool.delta',
  }
}

function queryClientRecorder(): { client: QueryClient; invalidated: unknown[]; refetched: unknown[] } {
  const client = new QueryClient()
  const invalidated: unknown[] = []
  const refetched: unknown[] = []
  client.invalidateQueries = ((filters: { queryKey?: unknown }) => {
    invalidated.push(filters.queryKey)
    return Promise.resolve()
  }) as QueryClient['invalidateQueries']
  client.refetchQueries = ((filters: { queryKey?: unknown }) => {
    refetched.push(filters.queryKey)
    return Promise.resolve()
  }) as QueryClient['refetchQueries']
  return { client, invalidated, refetched }
}

test('V3 tool resource bridge refreshes manage-agent applied mutations from completed realtime events', () => {
  const { client, invalidated, refetched } = queryClientRecorder()
  const event = toolCompletedEvent('manage_agent', { status: 'ok', applied: true, action: 'create' })

  const result = handleDesktopV3ToolResourceUpdate(event, client)

  assert.equal(result.applied, true)
  assert.equal(result.toolName, 'manage-agent')
  assert.deepEqual(result.resources, ['agent-state'])
  assert.deepEqual(invalidated, [['agent-state'], ['agent-state', 'settings']])
  assert.deepEqual(refetched, [['agent-state'], ['agent-state', 'settings']])
})

test('V3 tool resource bridge normalizes hyphenated manage-agent tool names', () => {
  const result = deriveDesktopV3ToolResourceUpdate(
    toolCompletedEvent('manage-agent', { status: 'ok', applied: 'true', action: 'update' }),
  )

  assert.ok(result)
  assert.equal(result.toolName, 'manage-agent')
  assert.deepEqual(Array.from(result.resources), ['agent-state'])
})

test('V3 tool resource bridge refreshes manage-theme workspace mutations from completed realtime events', () => {
  const { client, invalidated, refetched } = queryClientRecorder()
  const event = toolCompletedEvent('manage_theme', {
    status: 'ok',
    applied: true,
    action: 'set',
    apply_to: 'workspace',
    change: {
      kind: 'theme_change',
      target: 'workspace_theme',
      operation: 'set',
      workspace_path: '/workspace/project',
    },
  })

  const result = handleDesktopV3ToolResourceUpdate(event, client)

  assert.equal(result.applied, true)
  assert.equal(result.toolName, 'manage-theme')
  assert.deepEqual(result.resources, ['ui-settings', 'workspace-overview'])
  assert.deepEqual(invalidated, [['ui-settings'], ['workspace-overview']])
  assert.deepEqual(refetched, [['ui-settings'], ['workspace-overview']])
})

test('V3 tool resource bridge ignores manage-agent preview and failed results', () => {
  const preview = toolCompletedEvent('manage-agent', { status: 'proposed_create', applied: false, action: 'create' })
  const failed = toolCompletedEvent('manage_agent', { status: 'failed', applied: false, error: 'permission denied' })

  assert.equal(deriveDesktopV3ToolResourceUpdate(preview), null)
  assert.equal(deriveDesktopV3ToolResourceUpdate(failed), null)
})

test('V3 tool resource bridge ignores unrelated tools and non-completed tool events', () => {
  const unrelated = toolCompletedEvent('websearch', { status: 'ok', applied: true })
  const delta = toolDeltaEvent('manage-theme', { status: 'ok', applied: true, apply_to: 'workspace' })

  assert.equal(deriveDesktopV3ToolResourceUpdate(unrelated), null)
  assert.equal(deriveDesktopV3ToolResourceUpdate(delta), null)
})

test('V3 tool resource bridge parses nested raw output JSON records', () => {
  const event: CacheEvent = {
    source: 'realtime',
    sessionId: 'session-1',
    eventType: 'session.tool.completed',
    payload: {
      tool_name: 'manage-theme',
      output: JSON.stringify({ path_id: 'run.v3.provider-tool-result.v1', completed_output: '{"summary":"applied set","applied":true,"change":{"target":"account_theme"}}' }),
      raw_output: JSON.stringify({ applied: true, change: { target: 'account_theme' } }),
    },
  }

  const result = deriveDesktopV3ToolResourceUpdate(event)

  assert.ok(result)
  assert.deepEqual(Array.from(result.resources), ['ui-settings'])
})