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

test('DeliverableCard: renders Twitter/X replica card with avatar, handle, verified badge, hashtags, engagement bar, and media attachments', () => {
  const tweetDeliverable: DeliverableRecord = {
    id: 'deliv_tw_1',
    account_id: 'acct_1',
    worker_id: 'worker_social_bot',
    title: 'Tweet with rich tags and image',
    kind: 'social_post',
    status: 'pending_review',
    payload: {
      platform: 'x',
      posts: [
        {
          text: 'Building autonomous agents with #AI and @SwarmNetwork. Check $SWARM token and https://swarm.run !',
          author_name: 'Swarm DevRel',
          author_handle: 'swarm_dev',
          media_urls: ['/artifacts/social_banner.png'],
        },
      ],
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={tweetDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  // Header & verification
  assert.match(markup, /Swarm DevRel/)
  assert.match(markup, /@swarm_dev/)
  assert.match(markup, /data-testid="twitter-replica-card"/)
  assert.match(markup, /aria-label="Verified account"/)

  // Rich tokens: hashtag, mention, cashtag, url
  assert.match(markup, /#AI/)
  assert.match(markup, /@SwarmNetwork/)
  assert.match(markup, /\$SWARM/)
  assert.match(markup, /href="https:\/\/swarm\.run"/)

  // Image attachment
  assert.match(markup, /src="\/artifacts\/social_banner\.png"/)

  // Engagement action bar
  assert.match(markup, /title="Reply"/)
  assert.match(markup, /title="Repost"/)
  assert.match(markup, /title="Like"/)
  assert.match(markup, /title="Views"/)
  assert.match(markup, /title="Bookmark"/)
  assert.match(markup, /title="Share"/)
})

test('DeliverableCard: renders LinkedIn replica card with avatar, headline, public globe, in logo, rich formatting, and reactions', () => {
  const linkedInDeliverable: DeliverableRecord = {
    id: 'deliv_li_1',
    account_id: 'acct_1',
    worker_id: 'worker_growth_lead',
    title: 'LinkedIn Launch Strategy Announcement',
    kind: 'social_post',
    status: 'pending_review',
    payload: {
      platform: 'linkedin',
      posts: [
        {
          text: 'Excited to announce our new distributed architecture for #AutonomousAgents and enterprise scale.\n\nRead more at https://swarm.dev/blog and follow @Swarm.',
          author_name: 'Swarm Autonomous Lead',
          author_title: 'Head of Autonomous Systems • 1st • Follow',
          media_urls: ['/artifacts/architecture_diagram.png'],
        },
      ],
    },
    action_contract: {
      action: 'publish_linkedin_post',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={linkedInDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  // Header & LinkedIn logo
  assert.match(markup, /data-testid="linkedin-replica-card"/)
  assert.match(markup, /Swarm Autonomous Lead/)
  assert.match(markup, /Head of Autonomous Systems/)
  assert.match(markup, /Public/)

  // Formatted text
  assert.match(markup, /#AutonomousAgents/)
  assert.match(markup, /@Swarm/)
  assert.match(markup, /href="https:\/\/swarm\.dev\/blog"/)

  // Media attachment
  assert.match(markup, /src="\/artifacts\/architecture_diagram\.png"/)

  // Reactions & Action Buttons
  assert.match(markup, /16 comments • 5 reposts/)
  assert.match(markup, /Like/)
  assert.match(markup, /Comment/)
  assert.match(markup, /Repost/)
  assert.match(markup, /Send/)
})

test('DeliverableCard: renders multi-image grid gallery inside social replica', () => {
  const multiImagePost: DeliverableRecord = {
    id: 'deliv_gallery_1',
    account_id: 'acct_1',
    worker_id: 'worker_creative',
    title: 'Visual Campaign with 4 Assets',
    kind: 'social_post',
    status: 'pending_review',
    payload: {
      posts: [
        {
          text: 'Check out our 4 visual iterations for the fall campaign!',
          media_urls: [
            '/artifacts/shot_1.png',
            '/artifacts/shot_2.png',
            '/artifacts/shot_3.png',
            '/artifacts/shot_4.png',
          ],
        },
      ],
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={multiImagePost}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /src="\/artifacts\/shot_1\.png"/)
  assert.match(markup, /src="\/artifacts\/shot_2\.png"/)
  assert.match(markup, /src="\/artifacts\/shot_3\.png"/)
  assert.match(markup, /src="\/artifacts\/shot_4\.png"/)
})

test('DeliverableCard: renders HTML interactive sandbox preview with source toggle and copy', () => {
  const htmlDeliverable: DeliverableRecord = {
    id: 'deliv_html_1',
    account_id: 'acct_1',
    worker_id: 'designer_worker',
    title: 'Interactive 3D Badge Prototype',
    kind: 'media',
    status: 'pending_review',
    payload: {
      html: '<!DOCTYPE html><html><body><h1>Swarm Interactive Badge</h1><script>console.log("ready");</script></body></html>',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={htmlDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /data-testid="html-sandbox-preview"/)
  assert.match(markup, /Interactive Preview/)
  assert.match(markup, /HTML Source/)
  assert.match(markup, /sandbox="allow-scripts allow-same-origin"/)
  assert.match(markup, /Swarm Interactive Badge/)
})

test('DeliverableCard: renders HTML5 audio player card with waveform and download link', () => {
  const audioDeliverable: DeliverableRecord = {
    id: 'deliv_audio_1',
    account_id: 'acct_1',
    worker_id: 'audio_composer',
    title: 'Upbeat Background Track',
    kind: 'media',
    status: 'pending_review',
    payload: {
      audio_url: '/artifacts/upbeat_track.mp3',
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={audioDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /data-testid="audio-player-card"/)
  assert.match(markup, /HTML5 Audio Player/)
  assert.match(markup, /src="\/artifacts\/upbeat_track\.mp3"/)
  assert.match(markup, /href="\/artifacts\/upbeat_track\.mp3"/)
  assert.match(markup, /Download/)
})

test('DeliverableCard: renders JSON inspector card with formatted syntax and copy button', () => {
  const jsonDeliverable: DeliverableRecord = {
    id: 'deliv_json_1',
    account_id: 'acct_1',
    worker_id: 'analyst_worker',
    title: 'Weekly Performance Metrics',
    kind: 'report',
    status: 'pending_review',
    payload: {
      json: {
        active_users: 1250,
        requests_processed: 45000,
        latency_p95_ms: 12.4,
      },
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={jsonDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /data-testid="json-inspector-card"/)
  assert.match(markup, /(&quot;|")active_users(&quot;|"): 1250/)
  assert.match(markup, /(&quot;|")requests_processed(&quot;|"): 45000/)
  assert.match(markup, /(&quot;|")latency_p95_ms(&quot;|"): 12.4/)
  assert.match(markup, /Copy JSON/)
})

test('DeliverableCard: renders Accept / Approve with copy-ready status banner', () => {
  const approvedDeliverable: DeliverableRecord = {
    id: 'deliv_appr_1',
    account_id: 'acct_1',
    worker_id: 'worker_social_bot',
    title: 'Product Launch Thread Approved',
    kind: 'social_post',
    status: 'approved',
    payload: {
      posts: [
        { text: '1/2 Announcing our new release!' },
        { text: '2/2 Try it today.' },
      ],
    },
    created_at: 1789990000000,
    updated_at: 1789990000000,
  }

  const markup = renderToStaticMarkup(
    <DeliverableCard
      deliverable={approvedDeliverable}
      onApprove={() => {}}
      onDismiss={() => {}}
      onRequestChanges={() => {}}
      onDelete={() => {}}
    />
  )

  assert.match(markup, /Accepted &amp; Ready for Use|Accepted & Ready for Use/)
  assert.match(markup, /Copy-Ready/)
  assert.match(markup, /Copy Full Thread/)
})

