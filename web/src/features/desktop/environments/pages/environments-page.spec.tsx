import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import {
  checkConnection,
  deleteConnection,
  deleteEnvironment,
  destroyDeployment,
  ensureDeployment,
  fetchConnections,
  fetchDeployments,
  fetchEnvironments,
  releaseDeployment,
  saveConnection,
  saveEnvironment,
  setDefaultConnection,
  setDefaultTestEnvironment,
  startDeployment,
  stopDeployment,
} from '../services/environments-api'

const pageSource = readFileSync(new URL('./environments-page.tsx', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')

test('EnvironmentsPage defines three distinct sections/views: Environments, Connections, Deployments', () => {
  assert.match(pageSource, /data-testid="tab-environments"/)
  assert.match(pageSource, /data-testid="tab-connections"/)
  assert.match(pageSource, /data-testid="tab-deployments"/)

  assert.match(pageSource, /role="tablist" aria-label="Environment management views"/)
  assert.match(pageSource, /<EnvironmentsView/)
  assert.match(pageSource, /<ConnectionsView/)
  assert.match(pageSource, /<DeploymentsView/)
})

test('Router registers /environments and /$workspaceSlug/environments routes with reserved segments', () => {
  assert.match(routerSource, /path: '\/environments'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/environments'/)
  assert.match(routerSource, /ROOT_RESERVED_ROUTE_SEGMENTS[\s\S]*?'environments'/)
  assert.match(routerSource, /WORKSPACE_RESERVED_ROUTE_SEGMENTS[\s\S]*?'environments'/)
})

test('Environments API client handles mock responses for connections, environments, and deployments', async () => {
  const originalFetch = globalThis.fetch

  try {
    // 1. Mock fetchConnections
    globalThis.fetch = async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/v1/connections')) {
        return new Response(
          JSON.stringify({
            ok: true,
            connections: [
              {
                id: 'conn-1',
                name: 'Local Docker',
                kind: 'local_docker',
                capabilities: { supports_docker: true },
              },
            ],
            count: 1,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      if (url.includes('/v1/environments')) {
        return new Response(
          JSON.stringify({
            ok: true,
            environments: [
              {
                id: 'env-1',
                name: 'Testbench',
                container: { image: 'alpine:latest' },
              },
            ],
            settings: {
              workspace_id: 'ws-1',
              default_test_environment_id: 'env-1',
            },
            count: 1,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      if (url.includes('/v1/deployments')) {
        return new Response(
          JSON.stringify({
            ok: true,
            deployments: [
              {
                id: 'dep-1',
                status: 'running',
                health: 'healthy',
                runtime: { endpoint: 'http://127.0.0.1:18080' },
              },
            ],
            active_leases: {
              'dep-1': { id: 'lease-1', consumer_type: 'session', active: true },
            },
            count: 1,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response('Not found', { status: 404 })
    }

    const conns = await fetchConnections('ws-1')
    assert.equal(conns.length, 1)
    assert.equal(conns[0].id, 'conn-1')
    assert.equal(conns[0].kind, 'local_docker')

    const envsResult = await fetchEnvironments('ws-1')
    assert.equal(envsResult.environments.length, 1)
    assert.equal(envsResult.environments[0].id, 'env-1')
    assert.equal(envsResult.settings.default_test_environment_id, 'env-1')

    const depsResult = await fetchDeployments('ws-1')
    assert.equal(depsResult.deployments.length, 1)
    assert.equal(depsResult.deployments[0].status, 'running')
    assert.equal(depsResult.activeLeases['dep-1'].active, true)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Environments API client handles connection checks and diagnostics', async () => {
  const originalFetch = globalThis.fetch

  try {
    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body))
      if (body.action === 'check') {
        return new Response(
          JSON.stringify({
            ok: true,
            healthy: true,
            diagnostics: 'Docker daemon responded OK',
            capabilities: { supports_docker: true },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response('Not found', { status: 404 })
    }

    const check = await checkConnection('ws-1', 'conn-1')
    assert.equal(check.ok, true)
    assert.equal(check.healthy, true)
    assert.equal(check.diagnostics, 'Docker daemon responded OK')
    assert.equal(check.capabilities?.supports_docker, true)
  } finally {
    globalThis.fetch = originalFetch
  }
})
