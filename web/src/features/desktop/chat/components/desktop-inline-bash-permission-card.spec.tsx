import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'

import type { DesktopPermissionRecord } from '../../types/realtime'
import { parseBashIntentMetadata } from '../services/bash-intent-metadata'
import { DesktopInlineBashPermissionCard } from './desktop-inline-bash-permission-card'

function permission(explanation: string[]): DesktopPermissionRecord {
  return {
    id: `bash-${explanation.length}`,
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName: 'bash',
    toolArguments: JSON.stringify({
      command: 'npm run build',
      explanation,
      category: 'write',
      critical: false,
    }),
    status: 'pending',
    decision: '',
    reason: '',
    requirement: 'permission',
    mode: 'auto',
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
  }
}

function render(explanation: string[]): string {
  return renderToStaticMarkup(
    <DesktopInlineBashPermissionCard
      permission={permission(explanation)}
      pendingCount={1}
      sessionMode="auto"
      onResolve={async () => undefined}
    />,
  )
}

test('parseBashIntentMetadata preserves precise list metadata', () => {
  assert.deepEqual(
    parseBashIntentMetadata(JSON.stringify({
      command: 'python3 listener.py',
      explanation: [
        'Create a Python process that listens on TCP ports 8080 and 8443.',
        'Bind both listeners to 0.0.0.0 so they accept connections from public network interfaces.',
      ],
      category: 'write',
      critical: true,
    })),
    {
      command: 'python3 listener.py',
      explanation: [
        'Create a Python process that listens on TCP ports 8080 and 8443.',
        'Bind both listeners to 0.0.0.0 so they accept connections from public network interfaces.',
      ],
      category: 'write',
      critical: true,
    },
  )
})

test('parseBashIntentMetadata rejects requests without required metadata', () => {
  assert.equal(parseBashIntentMetadata('{"command":"pwd"}'), null)
  assert.equal(parseBashIntentMetadata('{"command":"pwd","explanation":[],"category":"read","critical":false}'), null)
  assert.equal(parseBashIntentMetadata('{"command":"pwd","explanation":["Print the directory."],"category":"inspect","critical":false}'), null)
})

test('pending Bash card renders one routine explanation as compact prose', () => {
  const markup = render(['Build the workspace.'])

  assert.match(markup, /<p[^>]*>Build the workspace\.<\/p>/)
  assert.doesNotMatch(markup, /<ul/)
})

test('pending Bash card renders several material effects as a semantic list', () => {
  const markup = render([
    'Start the development server on TCP port 4173.',
    'Expose the listener on 0.0.0.0 to other network interfaces.',
  ])

  assert.match(markup, /<ul[^>]*list-disc/)
  assert.match(markup, /<li[^>]*>Start the development server on TCP port 4173\.<\/li>/)
  assert.match(markup, /<li[^>]*>Expose the listener on 0\.0\.0\.0 to other network interfaces\.<\/li>/)
})
