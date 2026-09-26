import { S3StorageDriver } from '../src/storage/adapters/s3.js'
import { MemoryStorageDriver } from '../src/storage/adapters/memory.js'
import { WorkerStorageHub } from '../src/storage/hub.js'
import type { StorageDriver } from '../src/storage/types.js'

export interface SocialMediaWorkerOptions {
  driver?: StorageDriver
  workerId?: string
  topic?: string
  targetChannels?: string[]
}

export async function runSocialMediaWorker(options: SocialMediaWorkerOptions = {}) {
  const workerId = options.workerId || 'social-media-agent'
  const topic = options.topic || 'Swarm S3 & GCP Cloud Storage State Hub Launch'
  const channels = options.targetChannels || ['twitter_x', 'linkedin', 'github_discussions']

  // 1. Initialize Driver
  let driver: StorageDriver
  if (options.driver) {
    driver = options.driver
  } else if (process.env.STORAGE_PROVIDER === 's3' || process.env.STORAGE_PROVIDER === 'gcs') {
    const bucket = process.env.BUCKET_NAME || 'swarm-social-storage'
    const endpoint =
      process.env.STORAGE_ENDPOINT ||
      (process.env.STORAGE_PROVIDER === 'gcs' ? 'https://storage.googleapis.com' : undefined)
    const region = process.env.STORAGE_REGION || (process.env.STORAGE_PROVIDER === 'gcs' ? 'auto' : 'us-east-1')

    driver = new S3StorageDriver({
      bucket,
      endpoint,
      region,
      accessKeyId: process.env.STORAGE_ACCESS_KEY_ID,
      secretAccessKey: process.env.STORAGE_SECRET_ACCESS_KEY,
      forcePathStyle: true,
    })
  } else {
    // Fallback to in-memory driver for safe local dry-run
    console.log('[social-media-worker] No cloud credentials provided; using in-memory driver for demonstration.')
    driver = new MemoryStorageDriver()
  }

  // 2. Initialize Worker Storage Hub
  const hub = new WorkerStorageHub({
    workerId,
    driver,
  })

  console.log(`[social-media-worker] Booting worker "${workerId}" on storage hub...`)

  // 3. Register Worker Definition & Base Context
  await hub.initWorker({
    name: 'Social Media Campaign Worker',
    description: 'Autonomous multi-channel social media marketing and announcement strategist.',
    version: '1.0.0',
    tags: ['marketing', 'social', 'announcements', 'cloud-worker'],
  })

  await hub.saveBaseContext({
    instructions: `You are Swarm's autonomous social media strategist.
Your task is to craft compelling technical announcements that resonate with AI engineers and developers.
Tone: Concise, technical, high-impact, zero corporate fluff.`,
    tools: [
      { name: 'format_thread', description: 'Formats long-form thoughts into numbered Twitter/X posts' },
      { name: 'generate_image_prompt', description: 'Crafts visual design briefs for announcement banners' },
      { name: 'schedule_campaign', description: 'Schedules optimal posting windows across channels' },
    ],
    memory: {
      brand: 'Swarm Agent',
      canonical_url: 'https://github.com/swarm-agent/swarm',
      primary_audience: 'Senior AI Engineers, Cloud Architects, and Autonomous System Developers',
    },
  })

  // 4. Start Session
  const sessionId = `sess_${Date.now()}`
  console.log(`[social-media-worker] Starting session "${sessionId}" for topic: "${topic}"`)

  await hub.startSession(sessionId, {
    topic,
    channels,
    requested_at: new Date().toISOString(),
  })

  // Step 1: Context Analysis
  console.log('[social-media-worker] [25%] Analyzing brand memory and core feature value props...')
  await hub.updateSessionState(sessionId, {
    status: 'running',
    step: 'Analyzing technical architecture and audience persona',
    progress: 25,
  })
  await hub.appendTrace(
    sessionId,
    'Loaded base instructions and memory. Extracted key differentiators: zero-local-execution cloud hub, SigV4 signing, SHA-256 verification.',
    { type: 'analysis' }
  )

  // Simulate short work epoch
  await new Promise((r) => setTimeout(r, 200))

  // Step 2: Content Generation
  console.log('[social-media-worker] [50%] Drafting announcement copy for Twitter/X and LinkedIn...')
  await hub.updateSessionState(sessionId, {
    status: 'running',
    step: 'Drafting multi-channel copy & thread breakdown',
    progress: 50,
  })
  await hub.appendTrace(
    sessionId,
    'Generated 4-post Twitter/X thread and high-impact LinkedIn summary with code snippet callout.',
    { type: 'drafting' }
  )

  await new Promise((r) => setTimeout(r, 200))

  // Step 3: Deliverables Assembly & Checksum Verification
  console.log('[social-media-worker] [75%] Generating campaign artifacts & visual briefs...')
  await hub.updateSessionState(sessionId, {
    status: 'running',
    step: 'Assembling publication files and computing SHA-256 integrity digests',
    progress: 75,
  })

  const announcementCopy = `# 🚀 Swarm Cloud Storage State Hub: Serverless Workers with S3 & GCS

We just landed a huge upgrade to Swarm's autonomous worker infrastructure.

### The Big Idea
Run AI workers anywhere in the cloud (GCP Cloud Run, AWS Lambda, headless VMs) without giving them inbound network access to your dev machine.

1. **Autonomous Execution**: The worker pulls instructions & memory directly from private object storage.
2. **Real-time Live Progress**: Streams progress (0-100%) and trace logs back to \`state.json\`.
3. **Desktop AI Inbox Bridge**: When the worker finishes, it publishes a cryptographically signed deliverable manifest (SHA-256). Swarm's local daemon detects it and presents a 1-click import card directly in your Desktop AI Inbox.

Zero open inbound ports. Zero untrusted code executing locally. Complete visibility.

🔗 Check out the source: https://github.com/swarm-agent/swarm
`

  const campaignSchedule = {
    campaign_name: 'Cloud Storage Hub Release',
    topic,
    scheduled_date: '2026-09-26T14:00:00Z',
    slots: [
      {
        channel: 'twitter_x',
        time: '14:00 UTC',
        type: 'thread',
        posts: [
          '🚀 Introducing Swarm Cloud Storage State Hub: Run autonomous AI workers in S3/GCP with zero local execution risk.',
          '1/ Workers boot off private object storage, read base context (persona, tools, memory), and log live progress directly to S3/GCS.',
          '2/ When complete, workers publish deliverables with SHA-256 integrity digests. Your Desktop daemon picks it up and pops an actionable card right in your AI Inbox.',
          '3/ 1-click review and import straight into your local worktree. Pure sandbox security for cloud workers.',
        ],
      },
      {
        channel: 'linkedin',
        time: '14:30 UTC',
        type: 'article_share',
        summary: 'Architecting zero-trust cloud workers with S3 SigV4 state hubs and Desktop AI Inbox verification.',
      },
    ],
  }

  const visualBrief = {
    title: 'Swarm Cloud Worker Architecture Card',
    dimensions: '1200x675',
    aspect_ratio: '16:9',
    prompt:
      'Dark technical infographic showing cloud workers in Google Cloud and AWS syncing with an S3 bucket in the center, and a local workstation receiving verified notifications via a secure shield badge. Clean blueprint style, cyan and emerald accents.',
  }

  // 4. Publish Deliverables to Bucket
  console.log('[social-media-worker] [100%] Publishing deliverables to storage bucket...')
  const deliverableId = `deliv_${Date.now()}`
  const publishedManifest = await hub.publishDeliverable({
    id: deliverableId,
    title: `Social Media Campaign: ${topic}`,
    summary: 'Complete multi-channel announcement package with Twitter/X thread, campaign schedule, and visual graphics brief.',
    author: workerId,
    sessionId,
    files: [
      {
        name: 'announcement-thread.md',
        content: announcementCopy,
        contentType: 'text/markdown',
      },
      {
        name: 'campaign-schedule.json',
        content: JSON.stringify(campaignSchedule, null, 2),
        contentType: 'application/json',
      },
      {
        name: 'visual-prompts.json',
        content: JSON.stringify(visualBrief, null, 2),
        contentType: 'application/json',
      },
    ],
    metadata: {
      topic,
      channels,
      version: '1.0.0',
    },
    actions: [
      {
        id: 'import_to_social_workspace',
        label: 'Import to swarm-social',
        endpoint: `/v1/storage/deliverables/${workerId}/${deliverableId}/import`,
        style: 'primary',
      },
    ],
  })

  await hub.updateSessionState(sessionId, {
    status: 'completed',
    step: 'Finished campaign generation and published deliverable manifest',
    progress: 100,
  })

  await hub.appendTrace(
    sessionId,
    `Successfully published deliverable ${deliverableId} with ${publishedManifest.files.length} verified files.`,
    { type: 'completion' }
  )

  console.log(`[social-media-worker] ✅ Successfully published deliverable "${deliverableId}"!`)
  console.log(`[social-media-worker] Files with verified SHA-256 digests:`)
  for (const f of publishedManifest.files) {
    console.log(`  - ${f.name} (${f.sizeBytes} bytes, SHA-256: ${f.sha256.substring(0, 16)}...)`)
  }

  return {
    workerId,
    sessionId,
    deliverableId,
    manifest: publishedManifest,
  }
}

// Direct CLI execution
if (import.meta.url === `file://${process.argv[1]}`) {
  runSocialMediaWorker()
    .then((res) => {
      console.log('[social-media-worker] Done!', res.deliverableId)
      process.exit(0)
    })
    .catch((err) => {
      console.error('[social-media-worker] Failed:', err)
      process.exit(1)
    })
}
