import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  buildStructuredToolMessage,
  describeToolActivity,
  extractArtifactToolData,
} from '../services/tool-message'
import {
  normalizeDesktopPlanFinalHandoff,
  normalizeDesktopSessionPlan,
} from '../services/session-plan-record'
import {
  desktopV3ArtifactViewerHref,
  desktopV3ArtifactViewerSearch,
  normalizeDesktopV3ArtifactCatalogEntry,
  normalizeDesktopV3ArtifactSelection,
  type DesktopV3ArtifactCatalogEntry,
} from '../../session-v3/artifact-api'
import { ManageArtifactCard, ToolMessageView } from './chat-markdown'
import {
  DesktopV3RenderItemView,
  selectDesktopV3SuggestedPrompt,
} from './desktop-v3-existing-conversation-pane'

test('timeline parses successful manage_artifact tool message into typed artifact card', () => {
  const outputJson = {
    tool: 'manage_artifact',
    action: 'create',
    status: 'ok',
    path_id: 'run.manage-artifact.v1',
    artifact: {
      id: 'var-concept-1',
      collection_id: 'col-brainstorm',
      session_id: 'sess-abc-123',
      event_seq: 7,
      filename: 'prototype.html',
      media_type: 'text/html',
      label: 'Interactive Prototype',
      description: 'Self-contained HTML prototype',
      status: 'ready',
      category: 'visual',
    },
    reference: {
      session_id: 'sess-abc-123',
      collection_id: 'col-brainstorm',
      variant_id: 'var-concept-1',
      event_seq: 7,
    },
  }

  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_art_1',
    outputText: JSON.stringify(outputJson),
  })

  assert(Boolean(toolMessage), 'toolMessage should be created')
  assert.equal(toolMessage?.artifactData?.action, 'create')
  assert.equal(toolMessage?.artifactData?.artifact?.artifactId, 'var-concept-1')
  assert.equal(toolMessage?.artifactData?.artifact?.sessionId, 'sess-abc-123')
  assert.equal(toolMessage?.artifactData?.artifact?.collectionId, 'col-brainstorm')
  assert.equal(toolMessage?.artifactData?.artifact?.eventSeq, 7)
  assert.equal(toolMessage?.artifactData?.artifact?.label, 'Interactive Prototype')
  assert.equal(toolMessage?.artifactData?.artifact?.mediaType, 'text/html')
  assert.equal(toolMessage?.artifactData?.reference?.variant_id, 'var-concept-1')

  const markup = renderToStaticMarkup(
    <ToolMessageView
      toolMessage={toolMessage!}
      artifactHref={(entry) => `/ws-1/${entry.sessionId}?artifactSession=${entry.sessionId}&collection=${entry.collectionId}&artifact=${entry.artifactId}`}
    />,
  )

  assert(markup.includes('data-testid="desktop-artifact-tool-card"'), 'must include artifact card testid')
  assert(markup.includes('Interactive Prototype'), 'must display artifact label')
  assert(markup.includes('Self-contained HTML prototype'), 'must display artifact description')
  assert(markup.includes('text/html'), 'must display media type')
  assert(markup.includes('col: col-brainstorm'), 'must display collection info')
  assert(markup.includes('Open in viewer'), 'must provide viewer open affordance')
  assert(markup.includes('data-testid="artifact-preview-link"'), 'must expose an immediately clickable artifact preview')
  assert(markup.includes('data-artifact-preview-thumbnail'), 'must render the artifact thumbnail surface')
  assert(markup.includes('/ws-1/sess-abc-123?artifactSession=sess-abc-123&amp;collection=col-brainstorm&amp;artifact=var-concept-1'), 'must link to exact viewer route')
})

test('artifact inspection reads stay compact and do not duplicate the artifact preview', () => {
  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_art_read_1',
    argumentsText: JSON.stringify({
      action: 'read',
      session_id: 'sess-abc-123',
      collection_id: 'col-brainstorm',
      variant_id: 'var-concept-1',
      event_seq: 7,
    }),
    outputText: JSON.stringify({
      action: 'read',
      status: 'ok',
      artifact: {
        id: 'var-concept-1',
        collection_id: 'col-brainstorm',
        session_id: 'sess-abc-123',
        event_seq: 7,
        filename: 'prototype.html',
        media_type: 'text/html',
        label: 'Interactive Prototype',
        status: 'ready',
        previewable: true,
      },
      content: '<html>inspected</html>',
    }),
  })
  assert(Boolean(toolMessage), 'toolMessage should be created')

  const markup = renderToStaticMarkup(
    <ToolMessageView
      toolMessage={toolMessage!}
      artifactHref={() => '/ws-1/sess-abc-123?artifact=var-concept-1'}
    />,
  )

  assert(markup.includes('data-artifact-inspection-activity'), 'inspection should use a compact activity presentation')
  assert(markup.includes('data-testid="desktop-artifact-inspection-card"'))
  assert(!markup.includes('data-testid="desktop-artifact-tool-card"'), 'inspection must not render the publication card')
  assert(!markup.includes('data-artifact-preview-thumbnail'), 'inspection must not mount another artifact preview')
  assert(!markup.includes('Open in viewer'), 'inspection must not duplicate artifact actions')
})

test('timeline artifact actions fail closed when the exact ready identity is incomplete', () => {
  const outputJson = {
    tool: 'manage_artifact',
    action: 'create',
    status: 'ok',
    artifact: {
      id: 'var-incomplete',
      session_id: 'sess-abc-123',
      collection_id: 'col-brainstorm',
      filename: 'prototype.html',
      media_type: 'text/html',
      label: 'Incomplete Prototype',
      status: 'ready',
    },
  }
  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_art_incomplete',
    outputText: JSON.stringify(outputJson),
  })!
  const markup = renderToStaticMarkup(
    <ToolMessageView
      toolMessage={toolMessage}
      onArtifactSelections={() => assert.fail('incomplete artifact identity must not be sent to chat')}
    />,
  )
  assert(!markup.includes('data-testid="add-artifact-to-chat-button"'))
  assert(!markup.includes('data-testid="use-artifact-design-button"'))
  assert(markup.includes('data-artifact-preview-thumbnail'), 'display-only artifact metadata still renders a preview surface')
  assert(!markup.includes('data-testid="artifact-preview-button"'), 'a preview without a concrete route must not render an inert button')
})

test('image generation artifact timeline shows a pending canvas before the provider result arrives', () => {
  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_image_pending',
    argumentsText: JSON.stringify({ action: 'generate_image', prompt: 'A mountain at sunrise' }),
    state: 'running',
  })
  assert(Boolean(toolMessage), 'toolMessage should be created')

  const markup = renderToStaticMarkup(<ToolMessageView toolMessage={toolMessage!} />)
  assert(markup.includes('Image generation'), 'must identify the operation as image generation')
  assert(markup.includes('Generating your image'), 'must explain the pending state')
  assert(markup.includes('data-testid="image-generation-pending"'), 'must render the animated pending canvas')
  assert(!markup.includes('text/html'), 'must not invent an HTML media type for image setup or generation')
})

test('ready generated images use a wide clickable timeline preview', () => {
  const outputJson = {
    tool: 'manage_artifact',
    action: 'generate_image',
    status: 'ok',
    artifact: {
      id: 'var-image-1',
      collection_id: 'col-images',
      session_id: 'sess-image-1',
      event_seq: 18,
      filename: 'sunrise.jpg',
      media_type: 'image/jpeg',
      label: 'Mountain sunrise',
      status: 'ready',
      previewable: true,
      category: 'visual',
    },
  }
  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_image_ready',
    outputText: JSON.stringify(outputJson),
  })
  assert(Boolean(toolMessage), 'toolMessage should be created')

  const markup = renderToStaticMarkup(
    <ToolMessageView toolMessage={toolMessage!} artifactHref={() => '/ws/sess-image-1?artifact=var-image-1'} />,
  )
  assert(markup.includes('Image ready'), 'must communicate successful image generation')
  assert(markup.includes('data-artifact-preview-presentation="wide"'), 'must use the wide image presentation')
  assert(markup.includes('data-testid="artifact-preview-link"'), 'must keep the full preview clickable')
})

test('image capability lookup uses meaningful metadata without a fake artifact type', () => {
  const toolMessage = buildStructuredToolMessage({
    tool: 'manage_artifact',
    callId: 'call_image_capabilities',
    argumentsText: JSON.stringify({ action: 'image_capabilities' }),
    outputText: JSON.stringify({ tool: 'manage_artifact', action: 'image_capabilities', status: 'ok', image_capabilities: { supported: true } }),
  })
  assert(Boolean(toolMessage), 'toolMessage should be created')

  const markup = renderToStaticMarkup(<ToolMessageView toolMessage={toolMessage!} />)
  assert(markup.includes('Image setup'), 'must explain this is an image setup lookup')
  assert(markup.includes('Image generation options'), 'must give the lookup a meaningful label')
  assert(markup.includes('Options ready'), 'must describe completion without claiming an artifact is ready')
  assert(!markup.includes('text/html'), 'must not display the generic HTML fallback')
})

test('manage_artifact activity descriptor uses artifact category with clean labels', () => {
  const descriptor = describeToolActivity('manage_artifact')
  assert.equal(descriptor.kind, 'artifact')
  assert.equal(descriptor.label, 'Artifact')
  assert.equal(descriptor.activeLabel, 'Creating artifact')

  const hyphenDescriptor = describeToolActivity('manage-artifact')
  assert.equal(hyphenDescriptor.kind, 'artifact')
  assert.equal(hyphenDescriptor.label, 'Artifact')
  assert.equal(hyphenDescriptor.activeLabel, 'Creating artifact')
})

test('final handoff normalizes one and many managed artifacts alongside deliverable metadata', () => {
  const handoff = normalizeDesktopPlanFinalHandoff({
    schema_version: 1,
    title: 'Handoff with multiple concepts',
    overview: 'Exploration produced three interactive designs.',
    impact_bullets: ['Interactive prototypes created'],
    recommendation: {
      decision: 'ship',
      action: 'choose prototype',
      reason: 'All 3 variants are ready for review',
      action_state: 'ready',
      prompt: 'Please review and choose one of the 3 prototypes.',
    },
    suggested_prompts: [
      { label: 'Revise design', prompt: 'Please revise the second variant.' },
    ],
    artifacts: [
      {
        session_id: 'sess-parent',
        collection_id: 'col-deck',
        variant_id: 'var-1',
        event_seq: 10,
        label: 'Light theme variant',
        description: 'Clean bright palette',
        filename: 'light.html',
        media_type: 'text/html',
        category: 'visual',
        previewable: true,
      },
      {
        session_id: 'sess-parent',
        collection_id: 'col-deck',
        variant_id: 'var-2',
        event_seq: 11,
        label: 'Dark theme variant',
        description: 'High contrast dark palette',
        filename: 'dark.html',
        media_type: 'text/html',
        category: 'visual',
        previewable: true,
      },
    ],
    details: {
      result: 'Finished exploring designs',
    },
  })

  assert(Boolean(handoff), 'handoff must normalize successfully')
  assert.equal(handoff?.artifacts.length, 2)
  assert.equal(handoff?.artifacts[0]?.artifactId, 'var-1')
  assert.equal(handoff?.artifacts[0]?.sessionId, 'sess-parent')
  assert.equal(handoff?.artifacts[0]?.collectionId, 'col-deck')
  assert.equal(handoff?.artifacts[0]?.eventSeq, 10)
  assert.equal(handoff?.artifacts[1]?.artifactId, 'var-2')
  assert.equal(handoff?.artifacts[1]?.eventSeq, 11)
})

test('exact viewer navigation search identity and URL resolution', () => {
  const entry: DesktopV3ArtifactCatalogEntry = {
    artifactId: 'var-99',
    collectionId: 'col-ux',
    sessionId: 'sess-55',
    sessionTitle: 'Session',
    workspacePath: '/ws',
    workspaceName: 'my-workspace',
    planId: 'plan-1',
    planTitle: 'Plan',
    checkpointId: 'cp-1',
    checkpointTitle: 'Checkpoint',
    label: 'Wireframe',
    description: 'Wireframe description',
    collectionName: 'UX Iterations',
    collectionDescription: '',
    filename: 'wireframe.html',
    mediaType: 'text/html',
    kind: 'text/html',
    status: 'ready',
    previewable: true,
    category: 'visual',
    updatedAt: 1000,
    eventSeq: 15,
  }

  const search = desktopV3ArtifactViewerSearch(entry)
  assert.deepEqual(search, {
    artifactSession: 'sess-55',
    collection: 'col-ux',
    artifact: 'var-99',
  })

  const href = desktopV3ArtifactViewerHref('my-slug', entry)
  assert.equal(href, '/my-slug/sess-55?artifactSession=sess-55&collection=col-ux&artifact=var-99')
})

test('recommendation action sends ordinary-chat prompt via selectDesktopV3SuggestedPrompt without direct tool execution', () => {
  let submittedPrompt = ''
  const onSuggestedPrompt = (prompt: string) => {
    submittedPrompt = prompt
  }

  selectDesktopV3SuggestedPrompt('Please review the 3 brainstormed concepts.', onSuggestedPrompt)
  assert.equal(submittedPrompt, 'Please review the 3 brainstormed concepts.')

  // Ignored when empty or missing handler
  submittedPrompt = ''
  selectDesktopV3SuggestedPrompt('', onSuggestedPrompt)
  assert.equal(submittedPrompt, '')
  selectDesktopV3SuggestedPrompt('Prompt with no handler', undefined)
  assert.equal(submittedPrompt, '')
})
