import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { buildStructuredToolMessage } from '../services/tool-message'
import { toolActivityPresentation } from '../services/tool-activity'
import { ToolMessageView } from './chat-markdown'

function message(input: {
  tool: string
  state?: 'running' | 'done' | 'error'
  lifecycleStatus?: string
  argumentsText?: string
  outputText?: string
  error?: string
}) {
  const built = buildStructuredToolMessage({
    tool: input.tool,
    state: input.state,
    lifecycleStatus: input.lifecycleStatus,
    argumentsText: input.argumentsText,
    outputText: input.outputText,
    error: input.error,
  })
  assert.ok(built)
  if (!built) throw new Error('expected structured tool message')
  return built
}

function markup(input: Parameters<typeof message>[0]): string {
  return renderToStaticMarkup(<ToolMessageView toolMessage={message(input)} />)
}

test('running edit renders an accessible stable activity card without raw arguments', () => {
  const rendered = markup({ tool: 'edit', state: 'running', argumentsText: '{"path":"web/src/app.tsx","old_string":"unrendered argument body"}' })
  assert.match(rendered, /data-tool-activity-state="running"/)
  assert.match(rendered, /Editing…/)
  assert.match(rendered, /web\/src\/app\.tsx/)
  assert.match(rendered, /role="status"/)
  assert.match(rendered, /aria-live="polite"/)
  assert.match(rendered, /aria-busy="true"/)
  assert.match(rendered, /min-h-\[4\.25rem\]/)
  assert.match(rendered, /w-full/)
  assert.match(rendered, /max-w-full/)
  assert.match(rendered, /self-stretch/)
  assert.match(rendered, /motion-safe:animate-\[pulse_2\.8s_ease-in-out_infinite\]/)
  assert.match(rendered, /motion-reduce:animate-none/)
  assert.doesNotMatch(rendered, />Active</)
  assert.doesNotMatch(rendered, /unrendered argument body/)
})

test('plan and task starts use specialized concise labels', () => {
  const plan = markup({ tool: 'plan_manage', state: 'running', argumentsText: '{"action":"start_checkpoint","checkpoint_title":"Live cards"}' })
  const task = markup({ tool: 'task', state: 'running', argumentsText: '{"description":"Inspect card flow","launch_count":2}' })
  assert.match(plan, /Planning…/)
  assert.match(plan, /Live cards/)
  assert.match(plan, /data-tool-activity-kind="plan"/)
  assert.match(task, /Launching subagents…/)
  assert.match(task, /Inspect card flow/)
  assert.match(task, /data-tool-activity-kind="task"/)
  assert.doesNotMatch(plan, /action&quot;/)
  assert.doesNotMatch(task, /launch_count/)
})

test('manage-worktree start shows the requested action and selection metadata', () => {
  const recall = markup({ tool: 'manage_worktree', state: 'running', argumentsText: '{"action":"recall","task_call_id":"task-call-1"}' })
  const integrate = markup({ tool: 'manage-worktree', state: 'running', argumentsText: '{"action":"integrate","session_ids":["child-a","child-b"]}' })
  assert.match(recall, /Running Manage Worktree…/)
  assert.match(recall, /recall · task-call-1/)
  assert.match(integrate, /integrate · 2 sessions/)
})

test('task stream result content supersedes the start shell in place', () => {
  const task = message({ tool: 'task', state: 'running' })
  task.taskRows = [{
    launchIndex: 1, childSessionId: 'child-1', status: 'running', phase: 'tool.started', agent: 'coder',
    assignmentLabel: 'Implement cards', modelLabel: '', tool: 'edit', time: '', previewKind: '', previewText: '',
    launchStartedAtMs: 1, currentToolStartedAtMs: 1, elapsedMs: 0, currentToolMs: 0, terminal: false,
  }]
  const rendered = renderToStaticMarkup(<ToolMessageView toolMessage={task} />)
  assert.match(rendered, /Subagent stream/)
  assert.doesNotMatch(rendered, /data-testid="desktop-tool-activity-card"/)
})

test('generic start has useful fallback copy', () => {
  const rendered = markup({ tool: 'custom_tool', state: 'running' })
  assert.match(rendered, /Running Custom Tool…/)
  assert.match(rendered, /data-tool-activity-kind="generic"/)
})

test('resolved, failed, and cancelled presentations are terminal and nonanimated', () => {
  assert.deepEqual(toolActivityPresentation('edit', 'done'), {
    kind: 'edit', state: 'done', title: 'Edit complete', statusLabel: 'Done', announcement: 'Edit complete',
  })
  assert.equal(toolActivityPresentation('plan_manage', 'error').title, 'Plan failed')
  assert.equal(toolActivityPresentation('task', 'done', 'cancelled').title, 'Subagents cancelled')

  const cancelled = markup({ tool: 'task', state: 'done', lifecycleStatus: 'cancelled' })
  assert.match(cancelled, /data-tool-activity-state="cancelled"/)
  assert.match(cancelled, /Subagents cancelled/)
  assert.doesNotMatch(cancelled, /aria-busy/)
  assert.doesNotMatch(cancelled, /animate-\[pulse/)

  const failed = markup({ tool: 'edit', state: 'error', error: 'permission denied' })
  assert.match(failed, /permission denied/)
  assert.match(failed, /data-tool-activity-state="error"/)
  assert.doesNotMatch(failed, /data-tool-activity-state="running"/)
})

test('completed structured result replaces the activity shell', () => {
  const running = markup({ tool: 'edit', state: 'running', argumentsText: '{"path":"web/src/app.tsx"}' })
  const done = markup({ tool: 'edit', state: 'done', argumentsText: '{"path":"web/src/app.tsx"}', outputText: '{"path":"web/src/app.tsx","old_string_preview":"old","new_string_preview":"new"}' })
  assert.match(running, /data-testid="desktop-tool-activity-card"/)
  assert.doesNotMatch(done, /data-testid="desktop-tool-activity-card"/)
  assert.match(done, /Changes/)
  assert.match(done, /old/)
  assert.match(done, /new/)
})
