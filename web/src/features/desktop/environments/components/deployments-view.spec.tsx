import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { DeploymentsView } from './deployments-view'
import type { Deployment, DeploymentLease, Environment } from '../types/environments'

const mockEnvironments: Environment[] = [
  {
    id: 'env-1',
    name: 'Go 1.24 Testbench',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    mode: 'deployable',
    role: 'testing',
    container: { image: 'golang:1.24' },
    provisioning: { strategy: { kind: 'local_mount' } },
    deployment_policy: { reuse: true, max_instances: 1, release_behavior: 'restart' },
    created_at: 1000,
    updated_at: 1000,
  },
]

const mockDeployments: Deployment[] = [
  {
    id: 'dep-1',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    environment_id: 'env-1',
    connection_id: 'conn-1',
    name: 'Active Test Runner',
    status: 'running',
    health: 'healthy',
    runtime: {
      container_id: 'docker-cont-9876543210ab',
      endpoint: 'http://127.0.0.1:18080',
      assigned_ports: [
        { container_port: 8080, host_port: 18080, protocol: 'tcp' },
      ],
    },
    lifecycle: {
      created_at: 1000,
      started_at: 1100,
      ready_at: 1200,
      last_active_at: 1500,
    },
    created_at: 1000,
    updated_at: 1000,
  },
  {
    id: 'dep-2',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    environment_id: 'env-1',
    connection_id: 'conn-1',
    name: 'Idle Standby Instance',
    status: 'stopped',
    health: 'unknown',
    runtime: {
      container_id: 'docker-cont-112233445566',
    },
    lifecycle: {
      created_at: 500,
      stopped_at: 600,
    },
    created_at: 500,
    updated_at: 600,
  },
]

const mockLeases: Record<string, DeploymentLease> = {
  'dep-1': {
    id: 'lease-101',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    deployment_id: 'dep-1',
    environment_id: 'env-1',
    consumer_type: 'session',
    consumer_id: 'sess-e2e-run-45',
    active: true,
    acquired_at: 1000,
  },
}

test('DeploymentsView renders deployments with status, health, and runtime endpoints', () => {
  const markup = renderToStaticMarkup(
    <DeploymentsView
      workspaceId="ws-1"
      deployments={mockDeployments}
      activeLeases={mockLeases}
      environments={mockEnvironments}
      onRefresh={() => {}}
      onStartDeployment={async () => {}}
      onStopDeployment={async () => {}}
      onReleaseDeployment={async () => {}}
      onDestroyDeployment={async () => {}}
    />,
  )

  // Verify deployment titles
  assert.match(markup, /Active Test Runner/)
  assert.match(markup, /Idle Standby Instance/)

  // Verify status badges
  assert.match(markup, /data-testid="deployment-status-badge"/)
  assert.match(markup, /running/i)
  assert.match(markup, /stopped/i)

  // Verify health badges
  assert.match(markup, /data-testid="deployment-health-badge"/)
  assert.match(markup, /healthy/i)

  // Verify primary endpoint link
  assert.match(markup, /data-testid="deployment-endpoint-link"/)
  assert.match(markup, /http:\/\/127\.0\.0\.1:18080/)

  // Verify assigned ports
  assert.match(markup, /data-testid="deployment-assigned-ports"/)
  assert.match(markup, /8080 → 18080 \(tcp\)/)

  // Verify container ID
  assert.match(markup, /docker-cont-9/)
})

test('DeploymentsView displays active lease and consumer ownership details', () => {
  const markup = renderToStaticMarkup(
    <DeploymentsView
      workspaceId="ws-1"
      deployments={mockDeployments}
      activeLeases={mockLeases}
      environments={mockEnvironments}
      onRefresh={() => {}}
      onStartDeployment={async () => {}}
      onStopDeployment={async () => {}}
      onReleaseDeployment={async () => {}}
      onDestroyDeployment={async () => {}}
    />,
  )

  // Lease badge
  assert.match(markup, /data-testid="deployment-lease-badge"/)
  assert.match(markup, /Leased \(session\)/)

  // Consumer details
  assert.match(markup, /data-testid="deployment-lease-details"/)
  assert.match(markup, /session: sess-e2e-run-45/)

  // Release lease action button for leased deployment
  assert.match(markup, /data-testid="release-lease-btn-dep-1"/)
  assert.match(markup, /Release Lease/)
})

test('DeploymentsView renders lifecycle controls for running and stopped instances', () => {
  const markup = renderToStaticMarkup(
    <DeploymentsView
      workspaceId="ws-1"
      deployments={mockDeployments}
      activeLeases={mockLeases}
      environments={mockEnvironments}
      onRefresh={() => {}}
      onStartDeployment={async () => {}}
      onStopDeployment={async () => {}}
      onReleaseDeployment={async () => {}}
      onDestroyDeployment={async () => {}}
    />,
  )

  // Stop button for running dep-1
  assert.match(markup, /data-testid="stop-dep-btn-dep-1"/)

  // Start button for stopped dep-2
  assert.match(markup, /data-testid="start-dep-btn-dep-2"/)

  // Destroy buttons for both
  assert.match(markup, /data-testid="destroy-dep-btn-dep-1"/)
  assert.match(markup, /data-testid="destroy-dep-btn-dep-2"/)
})

test('DeploymentsView renders empty state when no deployments exist', () => {
  const markup = renderToStaticMarkup(
    <DeploymentsView
      workspaceId="ws-1"
      deployments={[]}
      activeLeases={{}}
      environments={mockEnvironments}
      onRefresh={() => {}}
      onStartDeployment={async () => {}}
      onStopDeployment={async () => {}}
      onReleaseDeployment={async () => {}}
      onDestroyDeployment={async () => {}}
    />,
  )

  assert.match(markup, /data-testid="empty-deployments"/)
  assert.match(markup, /No deployments found/)
})
