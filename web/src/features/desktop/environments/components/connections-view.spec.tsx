import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { ConnectionsView } from './connections-view'
import { ConnectionSetupDialog } from './connection-setup-dialog'
import type { Connection } from '../types/environments'

const mockConnections: Connection[] = [
  {
    id: 'conn-local-1',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    name: 'Local Docker Daemon',
    description: 'Direct socket daemon',
    kind: 'local_docker',
    local_docker: { socket_path: '/var/run/docker.sock' },
    capabilities: { supports_docker: true, supports_direct_mount: true, remote_os: 'linux', remote_arch: 'x86_64' },
    created_at: 1000,
    updated_at: 1000,
  },
  {
    id: 'conn-ssh-1',
    account_scope_id: 'acc-1',
    workspace_id: 'ws-1',
    name: 'GPU Test Server',
    description: 'Remote SSH host with Docker engine',
    kind: 'ssh',
    ssh: { host: '192.168.1.100', port: 22, user: 'deployer', identity_file: '~/.ssh/id_ed25519' },
    capabilities: { supports_ssh: true, supports_docker: true, remote_os: 'linux' },
    created_at: 2000,
    updated_at: 2000,
  },
]

test('ConnectionsView renders configured connections with details and capabilities', () => {
  const markup = renderToStaticMarkup(
    <ConnectionsView
      workspaceId="ws-1"
      connections={mockConnections}
      defaultConnectionId="conn-local-1"
      onRefresh={() => {}}
      onSaveConnection={async () => {}}
      onDeleteConnection={async () => {}}
      onSetDefaultConnection={async () => {}}
    />,
  )

  // Verify connection names
  assert.match(markup, /Local Docker Daemon/)
  assert.match(markup, /GPU Test Server/)

  // Verify badges
  assert.match(markup, /Local Docker/)
  assert.match(markup, /SSH Host/)

  // Default connection badge
  assert.match(markup, /data-testid="default-connection-badge"/)
  assert.match(markup, /Default Connection/)

  // Sockets and host details
  assert.match(markup, /\/var\/run\/docker\.sock/)
  assert.match(markup, /192\.168\.1\.100:22/)
  assert.match(markup, /deployer/)

  // Capabilities
  assert.match(markup, /Direct Mount/)
  assert.match(markup, /linux\/x86_64/)
})

test('ConnectionsView renders empty state when no connections configured', () => {
  const markup = renderToStaticMarkup(
    <ConnectionsView
      workspaceId="ws-1"
      connections={[]}
      onRefresh={() => {}}
      onSaveConnection={async () => {}}
      onDeleteConnection={async () => {}}
      onSetDefaultConnection={async () => {}}
    />,
  )

  assert.match(markup, /data-testid="empty-connections"/)
  assert.match(markup, /No connections configured/)
  assert.match(markup, /Configure Local Docker/)
})

test('ConnectionSetupDialog enforces secret-free SSH configuration and security guidance', () => {
  const markup = renderToStaticMarkup(
    <ConnectionSetupDialog
      isOpen={true}
      workspaceId="ws-1"
      connection={{ id: '', name: '', kind: 'ssh', account_scope_id: 'acc-1', workspace_id: 'ws-1', capabilities: {}, created_at: 0, updated_at: 0 }}
      onClose={() => {}}
      onSave={async () => {}}
    />,
  )

  // Dialog renders
  assert.match(markup, /data-testid="connection-setup-dialog"/)

  // Security guidance: only paths, no private key contents or passwords stored
  assert.match(markup, /Only file path references are stored/)
  assert.match(markup, /Secret keys\/passwords are never saved in Swarm/)

  // Connectivity test button
  assert.match(markup, /data-testid="test-connection-btn"/)
  assert.match(markup, /Test Connectivity/)
})
