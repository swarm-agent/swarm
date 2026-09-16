import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { EnvironmentsView } from './environments-view'
import type { Connection, Environment, WorkspaceSettings } from '../types/environments'

const mockConnections: Connection[] = [
  {
    id: 'conn-local-1',
    accountScopeID: 'acc-1',
    workspaceID: 'ws-1',
    name: 'Local Docker Daemon',
    kind: 'local_docker',
    capabilities: { supports_docker: true, supports_direct_mount: true },
    created_at: 1000,
    updated_at: 1000,
  } as Connection,
  {
    id: 'conn-ssh-1',
    accountScopeID: 'acc-1',
    workspaceID: 'ws-1',
    name: 'GPU Test Box',
    kind: 'ssh',
    ssh: { host: '192.168.1.50', user: 'ubuntu', port: 22 },
    capabilities: { supports_ssh: true, supports_docker: true },
    created_at: 2000,
    updated_at: 2000,
  } as Connection,
]

const mockEnvironments: Environment[] = [
  {
    id: 'env-1',
    accountScopeID: 'acc-1',
    workspaceID: 'ws-1',
    name: 'Go 1.24 Testbench',
    description: 'Hermetic test runner container',
    mode: 'deployable',
    role: 'testing',
    preferred_connection_id: 'conn-local-1',
    container: {
      image: 'golang:1.24-bookworm',
      working_dir: '/workspace',
      exposed_ports: [{ container_port: 8080, host_port: 18080, protocol: 'tcp' }],
      privileged: false,
    },
    provisioning: {
      strategy: {
        kind: 'local_mount',
        local_mount: {
          container_path: '/workspace',
          host_path: '/workspaces/project',
        },
      },
    },
    deployment_policy: {
      reuse: true,
      max_instances: 2,
      release_behavior: 'restart',
    },
    resources: {
      cpu_limit: '2.0',
      memory_limit: '4Gi',
    },
    created_at: 1000,
    updated_at: 1000,
  } as Environment,
  {
    id: 'env-2',
    accountScopeID: 'acc-1',
    workspaceID: 'ws-1',
    name: 'Node 22 Interactive Sandbox',
    mode: 'attached',
    role: 'development',
    preferred_connection_id: 'conn-ssh-1',
    container: {
      image: 'node:22-bookworm',
      working_dir: '/app',
    },
    provisioning: {
      strategy: {
        kind: 'remote_existing_path',
        remote_existing_path: {
          remote_path: '/opt/workspaces/app',
          container_path: '/app',
        },
      },
    },
    deployment_policy: {
      reuse: false,
      max_instances: 1,
      release_behavior: 'recreate',
    },
    created_at: 2000,
    updated_at: 2000,
  } as Environment,
]

const mockSettings: WorkspaceSettings = {
  workspace_id: 'ws-1',
  account_scope_id: 'acc-1',
  default_test_environment_id: 'env-1',
}

test('EnvironmentsView renders reusable definitions with mode, role, and details', () => {
  const markup = renderToStaticMarkup(
    <EnvironmentsView
      workspaceId="ws-1"
      environments={mockEnvironments}
      connections={mockConnections}
      settings={mockSettings}
      onRefresh={() => {}}
      onSaveEnvironment={async () => {}}
      onDeleteEnvironment={async () => {}}
      onSetDefaultTestEnvironment={async () => {}}
      onDeployEnvironment={async () => {}}
    />,
  )

  // Verify titles and descriptions
  assert.match(markup, /Go 1\.24 Testbench/)
  assert.match(markup, /Hermetic test runner container/)
  assert.match(markup, /Node 22 Interactive Sandbox/)

  // Verify mode and role badges
  assert.match(markup, /deployable/)
  assert.match(markup, /testing/)
  assert.match(markup, /attached/)
  assert.match(markup, /development/)

  // Verify container image
  assert.match(markup, /golang:1\.24-bookworm/)
  assert.match(markup, /node:22-bookworm/)
})

test('EnvironmentsView displays default testbench badge for configured test environment', () => {
  const markup = renderToStaticMarkup(
    <EnvironmentsView
      workspaceId="ws-1"
      environments={mockEnvironments}
      connections={mockConnections}
      settings={mockSettings}
      onRefresh={() => {}}
      onSaveEnvironment={async () => {}}
      onDeleteEnvironment={async () => {}}
      onSetDefaultTestEnvironment={async () => {}}
      onDeployEnvironment={async () => {}}
    />,
  )

  // env-1 is the default testbench
  assert.match(markup, /data-testid="default-testbench-badge"/)
  assert.match(markup, /Default Testbench/)

  // env-2 should have a button to set as default testbench
  assert.match(markup, /data-testid="set-default-test-btn-env-2"/)
  assert.match(markup, /Set as Default Testbench/)
})

test('EnvironmentsView displays source provisioning strategy and preferred connection', () => {
  const markup = renderToStaticMarkup(
    <EnvironmentsView
      workspaceId="ws-1"
      environments={mockEnvironments}
      connections={mockConnections}
      settings={mockSettings}
      onRefresh={() => {}}
      onSaveEnvironment={async () => {}}
      onDeleteEnvironment={async () => {}}
      onSetDefaultTestEnvironment={async () => {}}
      onDeployEnvironment={async () => {}}
    />,
  )

  // Local mount strategy details
  assert.match(markup, /Local Mount: \/home\/user\/project → \/workspace/)

  // Remote path strategy details
  assert.match(markup, /Remote Path: \/opt\/workspaces\/app → \/app/)

  // Preferred connection resolution
  assert.match(markup, /Local Docker Daemon \(Local\)/)
  assert.match(markup, /GPU Test Box \(SSH\)/)

  // Release behavior
  assert.match(markup, /restart \(Max: 2\)/)
  assert.match(markup, /recreate \(Max: 1\)/)
})

test('EnvironmentsView renders empty state when no environments configured', () => {
  const markup = renderToStaticMarkup(
    <EnvironmentsView
      workspaceId="ws-1"
      environments={[]}
      connections={mockConnections}
      settings={mockSettings}
      onRefresh={() => {}}
      onSaveEnvironment={async () => {}}
      onDeleteEnvironment={async () => {}}
      onSetDefaultTestEnvironment={async () => {}}
      onDeployEnvironment={async () => {}}
    />,
  )

  assert.match(markup, /data-testid="empty-environments"/)
  assert.match(markup, /No environments defined/)
  assert.match(markup, /Define Testbench Environment/)
})
