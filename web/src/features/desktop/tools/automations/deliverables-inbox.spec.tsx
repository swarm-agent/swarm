import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { DeliverableCard } from './deliverables-inbox'
import type { DeliverableRecord } from '../../state/desktop-deliverables-api'

test('DeliverableCard: renders premade tweets in social post card with approve button', () => {
  const socialDeliverable: DeliverableRecord = {
    id: 'deliv_123',
    account_id: 'acct_1',
    worker_id: 'worker_social_bot',
    title: 'Daily Social Media Thread for Launch',
    kind: 'social_post',
    status: 'pending_review',
    payload: {
      posts: [
        { text: '1/2 Announcing Swarm V3 distributed workers...' },
        { text: '2/2 Local-first agent inbox with one-click approval.' },
      ],
    },
    action_contract: {
      action: 'publish_x_post',
      target_secret_ref: 'gcp:x-api-key',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={socialDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onDelete={() => {}}
    />
  )

  // Card header & title
  assert.match(markup, /Daily Social Media Thread for Launch/)
  assert.match(markup, /worker_social_bot/)
  assert.match(markup, /pending review/)

  // Premade thread preview
  assert.match(markup, /PREMADE THREAD \(2 POSTS\)/)
  assert.match(markup, /Tweet 1 of 2/)
  assert.match(markup, /Tweet 2 of 2/)
  assert.match(markup, /Announcing Swarm V3 distributed workers/)
  assert.match(markup, /Local-first agent inbox with one-click approval/)

  // Action buttons
  assert.match(markup, /data-testid="approve-publish-btn"/)
  assert.match(markup, /Approve &amp; Publish to X|Approve & Publish to X/)
  assert.match(markup, /data-testid="dismiss-btn"/)
  assert.match(markup, /Dismiss/)
})

test('DeliverableCard: renders alert deliverable with warning details', () => {
  const alertDeliverable: DeliverableRecord = {
    id: 'deliv_alert_456',
    account_id: 'acct_1',
    worker_id: 'ci_sentinel',
    title: 'Worker Alert: CI Test Drift',
    kind: 'alert',
    status: 'pending_review',
    summary: 'Branch origin/dev has 2 uncommitted modified files',
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={alertDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /Worker Alert: CI Test Drift/)
  assert.match(markup, /Branch origin\/dev has 2 uncommitted modified files/)
  assert.match(markup, /ci_sentinel/)
})

test('DeliverableCard: renders publication receipt when approved', () => {
  const publishedDeliverable: DeliverableRecord = {
    id: 'deliv_pub_789',
    account_id: 'acct_1',
    worker_id: 'worker_social_bot',
    title: 'Published Tweet Announcement',
    kind: 'social_post',
    status: 'published',
    action_result: {
      published_to: 'x',
      status: 'published',
      post_count: 2,
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
    reviewed_at: 1789990100000,
    reviewed_by: 'user_roy',
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={publishedDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /Publication Receipt/)
  assert.match(markup, /Target: x/)
  assert.doesNotMatch(markup, /data-testid="approve-publish-btn"/)
})

test('DeliverableCard: renders media deliverable with video and image previews', () => {
  const mediaDeliverable: DeliverableRecord = {
    id: 'deliv_media_101',
    account_id: 'acct_1',
    worker_id: 'designer_bot',
    title: 'Brand Launch Video & Hero Banner',
    kind: 'media',
    status: 'pending_review',
    payload: {
      video_url: '/artifacts/video_render.mp4',
      image_url: '/artifacts/hero_banner.png',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={mediaDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /Brand Launch Video &amp; Hero Banner|Brand Launch Video & Hero Banner/)
  assert.match(markup, /MEDIA DELIVERABLE ASSETS/)
  assert.match(markup, /src="\/artifacts\/video_render\.mp4"/)
  assert.match(markup, /src="\/artifacts\/hero_banner\.png"/)
  assert.match(markup, /data-testid="request-changes-btn"/)
})

test('DeliverableCard: renders code patch with unified diff and additions/deletions', () => {
  const codeDeliverable: DeliverableRecord = {
    id: 'deliv_code_202',
    account_id: 'acct_1',
    worker_id: 'bugfix_coder',
    title: 'Fix(auth): prevent division by zero in token limiter',
    kind: 'code_patch',
    status: 'pending_review',
    payload: {
      branch: 'agent/fix-token-limiter',
      diff: '--- a/limiter.go\n+++ b/limiter.go\n@@ -10,3 +10,4 @@\n- rate := total / step\n+ if step <= 0 { return 0 }\n+ rate := total / step',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={codeDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /CODE PATCH \/ DIFF/)
  assert.match(markup, /agent\/fix-token-limiter/)
  assert.match(markup, /if step &lt;= 0 { return 0 }|if step <= 0 { return 0 }/)
})

test('DeliverableCard: renders revision feedback callout when status is needs_revision', () => {
  const needsRevisionDeliv: DeliverableRecord = {
    id: 'deliv_rev_303',
    account_id: 'acct_1',
    worker_id: 'worker_social_bot',
    title: 'Revised Product Announcement',
    kind: 'social_post',
    status: 'needs_revision',
    revision_feedback: {
      requested_at: 1789991000000,
      requested_by: 'user_roy',
      notes: 'Make the headline punchier and remove 2 hashtags',
      tags: ['Tone', 'Length'],
    },
    created_at: 1789990000000,
    updated_at: 1789991000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={needsRevisionDeliv}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /Awaiting Worker Revision/)
  assert.match(markup, /Make the headline punchier and remove 2 hashtags/)
  assert.match(markup, /#Tone/)
  assert.match(markup, /#Length/)
})
