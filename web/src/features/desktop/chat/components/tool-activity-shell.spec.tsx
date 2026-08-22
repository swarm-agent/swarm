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

test('manage-video shows polished active and result cards with user metadata', () => {
  const active = markup({
    tool: 'manage_video',
    state: 'running',
    argumentsText: JSON.stringify({ action: 'create_project', title: 'Launch film', output_preset: 'landscape_1080p' }),
  })
  assert.match(active, /data-manage-video-card/)
  assert.match(active, /Setting up video project…/)
  assert.match(active, /Setting up a video project · Launch film/)
  assert.match(active, /In progress/)
  assert.match(active, /motion-safe:animate-\[pulse_1\.8s_ease-in-out_infinite\]/)
  assert.doesNotMatch(active, /animate-spin/)
  assert.doesNotMatch(active, /initial_timeline/)

  const result = markup({
    tool: 'manage_video',
    state: 'done',
    outputText: JSON.stringify({
      action: 'render_status', status: 'ready', progress: 1, project_id: 'vproj_1', revision_id: 'vrev_2', job_id: 'vren_3',
      presentation: { kind: 'video', title: 'Render status updated', activity_label: 'Checking render progress', subject: 'Launch film' },
      render_job: { status: 'ready', progress: 1, output_preset: 'landscape_1080p', output_width: 1920, output_height: 1080, output_duration_ms: 65000, output_size_bytes: 5242880, revision_number: 2 },
    }),
  })
  assert.match(result, /data-video-action="render_status"/)
  assert.match(result, /Render status updated/)
  assert.match(result, /Launch film/)
  assert.match(result, /Landscape 1080p/)
  assert.match(result, /1920 × 1080/)
  assert.match(result, /1 min 5 sec/)
  assert.match(result, /5\.0 MB/)
  assert.match(result, /vproj_1/)
  assert.match(result, /vrev_2/)
  assert.match(result, /vren_3/)
  assert.doesNotMatch(result, /output_digest_sha256/)

  const transcript = markup({
    tool: 'manage_video',
    state: 'done',
    argumentsText: JSON.stringify({ action: 'read_transcript', transcript_ref: 'transcript_1' }),
    outputText: JSON.stringify({
      action: 'read_transcript', status: 'ok', source_names: ['ycfinalwithaudio.mp4'],
      presentation: { kind: 'video', title: 'Transcript ready', activity_label: 'Reading video transcript', subject: 'ycfinalwithaudio.mp4', source_names: ['ycfinalwithaudio.mp4'] },
      transcript: { duration_ms: 184000, language: 'en', validation: 'validated' },
    }),
  })
  assert.match(transcript, /Source video/)
  assert.match(transcript, /ycfinalwithaudio\.mp4/)
  assert.doesNotMatch(transcript, /source_fingerprint/)
})

test('manage-worktree start shows the requested action and selection metadata', () => {
  const recall = markup({ tool: 'manage_worktree', state: 'running', argumentsText: '{"action":"recall","task_call_id":"task-call-1"}' })
  const integrate = markup({ tool: 'manage-worktree', state: 'running', argumentsText: '{"action":"integrate","session_ids":["child-a","child-b"]}' })
  assert.match(recall, /Running Manage Worktree…/)
  assert.match(recall, /recall · task-call-1/)
  assert.match(integrate, /integrate · 2 sessions/)
})

test('completed manage-worktree recall and integrate results render as structured cards', () => {
  const recall = markup({
    tool: 'manage_worktree',
    state: 'done',
    outputText: JSON.stringify({
      action: 'recall',
      task_call_id: 'task-call-1',
      total: 2,
      state_counts: { committed: 1, integrated: 1 },
      children: [
        { child_session_id: 'child-a', title: 'API work', child_state: 'committed', child_branch: 'agent/api' },
        { child_session_id: 'child-b', title: 'UI work', child_state: 'integrated', child_branch: 'agent/ui' },
      ],
    }),
  })
  assert.match(recall, /data-manage-worktree-card/)
  assert.match(recall, /Worktree children/)
  assert.match(recall, /API work/)
  assert.match(recall, /Committed/)
  assert.match(recall, /agent\/api/)
  assert.doesNotMatch(recall, /Action:/)

  const integrate = markup({
    tool: 'manage-worktree',
    state: 'done',
    outputText: JSON.stringify({
      action: 'integrate',
      task_call_id: 'task-call-1',
      selected_count: 2,
      selection: 'complete_task_call',
      child_states: { 'child-a': 'integrated', 'child-b': 'integrated' },
      resulting_parent_head: 'deadbeef',
    }),
  })
  assert.match(integrate, /data-manage-worktree-card/)
  assert.match(integrate, /Worktrees integrated/)
  assert.match(integrate, /2 children integrated/)
  assert.match(integrate, /Complete task call/)
  assert.match(integrate, /deadbeef/)
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
