import {
  DeployedWorker,
  MediaDeliverable,
  OrchestratorMessage,
  ProjectSummary,
  RunningAutomation,
  RunningTask,
} from './orchestrate-types'

export const MOCK_PROJECTS: ProjectSummary[] = [
  {
    id: 'proj-swarm-platform',
    name: 'Swarm Platform',
    slug: 'swarm-platform',
    description: 'Local-first AI daemon, V3 session runtime & Desktop UI',
    repoPath: '~/swarm-go',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: ['swarm-go', 'swarmcrit', 'work'],
    activeWorkersCount: 3,
    pendingDeliverablesCount: 4,
    runningTasksCount: 2,
  },
  {
    id: 'proj-social-studio',
    name: 'Social Media Studio',
    slug: 'social-studio',
    description: 'Multi-part AI video pipeline & automated social distributor',
    repoPath: '~/swarm-social',
    branch: 'main',
    gitStatus: 'clean',
    linkedWorkspaces: ['swarm-social'],
    activeWorkersCount: 2,
    pendingDeliverablesCount: 6,
    runningTasksCount: 1,
  },
  {
    id: 'proj-cloud-infra',
    name: 'Cloud Infra & Security',
    slug: 'cloud-infra',
    description: 'GCP Workload Identity Federation & dynamic lease brokers',
    repoPath: '~/swarmcrit',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: ['swarmcrit'],
    activeWorkersCount: 1,
    pendingDeliverablesCount: 1,
    runningTasksCount: 0,
  },
]

export const MOCK_AUTOMATIONS: RunningAutomation[] = [
  {
    id: 'auto-video-gen',
    name: 'Social Video Batch Generator',
    kind: 'trigger',
    status: 'running',
    progressPercent: 60,
    currentStep: 'Rendering 6/10',
    lastRun: 'just now',
    outputSummary: 'Rendering 6/10',
    totalRuns: 42,
  },
  {
    id: 'auto-nightly-testbench',
    name: 'Nightly Systemd-Nspawn Testbench',
    kind: 'cron',
    status: 'scheduled',
    nextRun: 'Scheduled for 02:00 In 3h 15m',
    lastRun: 'yesterday',
    outputSummary: 'Scheduled for 02:00 In 3h 15m',
    totalRuns: 184,
  },
  {
    id: 'auto-pr-reviewer',
    name: 'GitHub PR Coder Auto-Reviewer',
    kind: 'trigger',
    status: 'idle',
    lastRun: '1 hour ago',
    outputSummary: 'Idle',
    totalRuns: 56,
  },
  {
    id: 'auto-token-audit',
    name: 'Token Velocity & Bloat Monitor',
    kind: 'interval',
    status: 'running',
    progressPercent: 85,
    currentStep: 'Compacting session data',
    lastRun: '15m ago',
    outputSummary: 'Compacting session data',
    totalRuns: 310,
  },
]

export const MOCK_DELIVERABLES: MediaDeliverable[] = [
  {
    id: 'deliv-vid-1',
    title: 'Neon Cyber Lattice',
    type: 'video',
    thumbnailType: 'cyber_lattice',
    videoAspect: '16:9',
    duration: '0:15',
    status: 'ready',
    createdAt: '2 mins ago',
    author: 'Video Swarm Worker',
    prompt: 'Cinematic cyber lattice with neon nodes and volumetric lighting.',
    metrics: { renderTime: '42s', tokens: '1.2k', views: 'Preview ready' },
  },
  {
    id: 'deliv-vid-2',
    title: 'Neural Core Genesis',
    type: 'video',
    thumbnailType: 'neural_core',
    videoAspect: '16:9',
    duration: '0:15',
    status: 'ready',
    createdAt: '4 mins ago',
    author: 'Video Swarm Worker',
    prompt: 'Floating obsidian cube radiating energy into a digital haze.',
    metrics: { renderTime: '38s', tokens: '1.1k', views: 'Preview ready' },
  },
  {
    id: 'deliv-vid-3',
    title: 'Orbital Data Flow',
    type: 'video',
    thumbnailType: 'orbital_data',
    videoAspect: '16:9',
    duration: '0:20',
    status: 'ready',
    createdAt: '7 mins ago',
    author: 'Video Swarm Worker',
    prompt: 'Satellite relay with light-speed cryptographic signals.',
    metrics: { renderTime: '55s', tokens: '1.4k', views: 'Preview ready' },
  },
]

export const MOCK_DEPLOYED_WORKERS: DeployedWorker[] = [
  {
    id: 'worker-video-swarm',
    name: 'Video Swarm Dispatcher',
    role: 'Multi-scene AI video story & media synthesizer',
    triggerKind: 'trigger',
    scheduleLabel: 'On-demand webhook & chat dispatch',
    activeJobsCount: 3,
    completedJobsCount: 42,
    status: 'active',
    currentJobTitle: 'Batch rendering 3/10 social launch clips',
    assignedTaskIds: ['task-101'],
  },
  {
    id: 'worker-pr-review',
    name: 'Code Reviewer & Verifier',
    role: 'AST inspection, test runner & diff auditor',
    triggerKind: 'cron',
    scheduleLabel: 'Hourly :00 + on commit push',
    activeJobsCount: 1,
    completedJobsCount: 184,
    status: 'active',
    currentJobTitle: 'Auditing composer quick command patch',
    assignedTaskIds: ['task-102'],
  },
  {
    id: 'worker-testbench',
    name: 'Systemd-Nspawn Testbench Runner',
    role: 'Hermetic container test pool executor',
    triggerKind: 'cron',
    scheduleLabel: 'Nightly 02:00 UTC',
    activeJobsCount: 0,
    completedJobsCount: 96,
    status: 'idle',
    assignedTaskIds: [],
  },
  {
    id: 'worker-session-compactor',
    name: 'Pebble Token Compactor',
    role: 'Pebble V3 bloat & memory optimizer',
    triggerKind: 'interval',
    scheduleLabel: 'Every 30m interval',
    activeJobsCount: 1,
    completedJobsCount: 310,
    status: 'busy',
    currentJobTitle: 'Compacting event store logs & telemetry',
    assignedTaskIds: [],
  },
]

// Base tasks with rich attached deliverables
const BASE_TASKS: RunningTask[] = [
  {
    id: 'task-101',
    title: 'Make 3 Social Media Videos for Feature Launch',
    subtitle: 'Design → Generate → Polish → Deliver',
    agentType: 'designer',
    status: 'running',
    workspaceTarget: 'swarm-social',
    elapsed: '2m 15s',
    subtasks: [
      { id: 'st-1', title: 'Hydrate 3 prompt themes via Router', completed: true },
      { id: 'st-2', title: 'Batch dispatch to Video Generation Worker', completed: true },
      { id: 'st-3', title: 'Synthesize kinetic audio & visual transitions', completed: false },
    ],
    stepTimeline: [
      { step: 1, label: 'Themes', status: 'complete' },
      { step: 2, label: 'Generate', status: 'processing' },
      { step: 3, label: 'Edit', status: 'pending' },
      { step: 4, label: 'Deliver', status: 'pending' },
    ],
    deliverables: [
      {
        id: 'deliv-vid-1',
        title: 'Neon Cyber Lattice',
        type: 'video',
        thumbnailType: 'cyber_lattice',
        videoAspect: '16:9',
        duration: '0:15',
        status: 'ready',
        createdAt: '1m ago',
        author: 'Video Swarm Worker',
        prompt: 'Cinematic cyber lattice with neon nodes and volumetric lighting.',
        metrics: { renderTime: '42s', tokens: '1.2k', views: 'Preview ready' },
      },
      {
        id: 'deliv-vid-2',
        title: 'Neural Core Genesis',
        type: 'video',
        thumbnailType: 'neural_core',
        videoAspect: '16:9',
        duration: '0:15',
        status: 'ready',
        createdAt: '2m ago',
        author: 'Video Swarm Worker',
        prompt: 'Floating obsidian cube radiating energy into a digital haze.',
        metrics: { renderTime: '38s', tokens: '1.1k', views: 'Preview ready' },
      },
      {
        id: 'deliv-vid-3',
        title: 'Orbital Data Flow',
        type: 'video',
        thumbnailType: 'orbital_data',
        videoAspect: '16:9',
        duration: '0:20',
        status: 'ready',
        createdAt: 'Just now',
        author: 'Video Swarm Worker',
        prompt: 'Satellite relay with light-speed cryptographic signals.',
        metrics: { renderTime: '55s', tokens: '1.4k', views: 'Preview ready' },
      },
    ],
  },
  {
    id: 'task-102',
    title: 'Fix composer quick command popup trigger',
    subtitle: 'Update keybinding logic and event handling.',
    agentType: 'coder',
    status: 'needs_review',
    workspaceTarget: 'swarm-go',
    elapsed: '1m 45s',
    subtasks: [
      { id: 'st-21', title: 'Locate slash command detection in composer.tsx', completed: true },
      { id: 'st-22', title: 'Fix keystroke event propagation', completed: true },
      { id: 'st-23', title: 'Pass keyboard navigation unit test', completed: true },
    ],
    diffPreview: `- if (text.endsWith('/')) {\n+ if (text.endsWith('/')) {\n    setShowQuickCommands(true)\n  }`,
    diffLines: [
      { lineNum: 12, type: 'del', text: "if (text.endsWith('/')) {" },
      { lineNum: 13, type: 'add', text: "if (text.endsWith('/')) {" },
      { lineNum: 14, type: 'normal', text: '    setShowQuickCommands(true)' },
      { lineNum: 15, type: 'normal', text: '  }' },
    ],
    deliverables: [
      {
        id: 'deliv-code-1',
        title: 'PR #108: Fix composer quick command slash trigger & focus trap',
        type: 'code',
        status: 'ready',
        createdAt: '5m ago',
        author: 'Coder Subagent',
        prompt: 'Register slash command listener with keyboard navigation guards in composer.tsx',
        metrics: { tokens: '2.8k' },
      },
    ],
  },
]

// Generate 100 realistic tasks for high-density testing & scalability
function generate100MockTasks(): RunningTask[] {
  const tasks: RunningTask[] = [
    {
      ...BASE_TASKS[0],
      workerId: 'worker-video-swarm',
      workerName: 'Video Swarm Dispatcher',
      priority: 'high',
      tags: ['video', 'creative', 'launch'],
      stageIndex: 2,
      totalStages: 4,
    },
    {
      ...BASE_TASKS[1],
      workerId: 'worker-pr-review',
      workerName: 'Code Reviewer & Verifier',
      priority: 'critical',
      tags: ['code', 'ui', 'bugfix'],
      stageIndex: 3,
      totalStages: 3,
    },
  ]

  const agentPool: Array<'coder' | 'designer' | 'finder' | 'swarm'> = ['coder', 'designer', 'finder', 'swarm']
  const workspacePool = ['swarm-go', 'swarm-social', 'swarmcrit', 'work']
  const workerPool = [
    { id: 'worker-video-swarm', name: 'Video Swarm Dispatcher' },
    { id: 'worker-pr-review', name: 'Code Reviewer & Verifier' },
    { id: 'worker-testbench', name: 'Systemd-Nspawn Testbench Runner' },
    { id: 'worker-session-compactor', name: 'Pebble Token Compactor' },
  ]

  const sampleTitles = [
    { title: 'Optimize Pebble V3 transaction batching', cat: 'code', tags: ['db', 'perf'] },
    { title: 'Generate 9:16 vertical TikTok promo stories', cat: 'video', tags: ['video', 'social'] },
    { title: 'Run hermetic local-testbench fast test gate', cat: 'testbench', tags: ['ci', 'tests'] },
    { title: 'Audit model token usage across child sessions', cat: 'audit', tags: ['tokens', 'metrics'] },
    { title: 'Refactor desktop sidebar workspace navigation', cat: 'code', tags: ['ui', 'frontend'] },
    { title: 'Generate 4K YouTube cinematic trailer intro', cat: 'video', tags: ['video', 'media'] },
    { title: 'Validate Workload Identity Federation tokens', cat: 'security', tags: ['gcp', 'auth'] },
    { title: 'Sync project workspace attachments into Git', cat: 'code', tags: ['git', 'storage'] },
    { title: 'Evaluate Gemini 3.8 Flash low-thinking benchmark', cat: 'ai', tags: ['models', 'eval'] },
    { title: 'Compact WebSocket realtime outbox records', cat: 'db', tags: ['realtime', 'memory'] },
  ]

  for (let i = 3; i <= 100; i++) {
    const template = sampleTitles[(i - 3) % sampleTitles.length]
    const assignedWorker = workerPool[i % workerPool.length]
    let status: RunningTask['status'] = 'queued'
    if (i <= 6) status = 'running'
    else if (i <= 14) status = 'needs_review'
    else if (i <= 28) status = 'completed'
    else status = 'queued'

    const stageIdx = status === 'completed' ? 4 : status === 'running' ? 2 : status === 'needs_review' ? 3 : 0
    const agent = agentPool[i % agentPool.length]

    tasks.push({
      id: `task-${100 + i}`,
      title: `${template.title} #${i}`,
      subtitle: `Automated ${template.cat} execution pipeline for ${assignedWorker.name}`,
      agentType: agent,
      status,
      workspaceTarget: workspacePool[i % workspacePool.length],
      elapsed: status === 'queued' ? 'queued' : `${(i % 12) + 1}m ${(i * 7) % 60}s`,
      workerId: assignedWorker.id,
      workerName: assignedWorker.name,
      priority: i % 10 === 0 ? 'critical' : i % 3 === 0 ? 'high' : 'medium',
      tags: template.tags,
      stageIndex: stageIdx,
      totalStages: 4,
      subtasks: [
        { id: `st-${i}-1`, title: 'Initialize task worktree & dependencies', completed: stageIdx >= 1 },
        { id: `st-${i}-2`, title: 'Execute primary agent sub-pipeline', completed: stageIdx >= 2 },
        { id: `st-${i}-3`, title: 'Package deliverables & generate test diff', completed: stageIdx >= 3 },
        { id: `st-${i}-4`, title: 'Verify integration barriers & ship', completed: stageIdx >= 4 },
      ],
      stepTimeline: [
        { step: 1, label: 'Init', status: stageIdx >= 1 ? 'complete' : 'pending' },
        { step: 2, label: 'Exec', status: stageIdx === 2 ? 'processing' : stageIdx > 2 ? 'complete' : 'pending' },
        { step: 3, label: 'Audit', status: stageIdx === 3 ? 'processing' : stageIdx > 3 ? 'complete' : 'pending' },
        { step: 4, label: 'Ship', status: stageIdx >= 4 ? 'complete' : 'pending' },
      ],
      deliverables:
        template.cat === 'video'
          ? [
              {
                id: `deliv-gen-${i}`,
                title: `Asset Render #${i}`,
                type: 'video',
                thumbnailType: i % 2 === 0 ? 'cyber_lattice' : 'orbital_data',
                videoAspect: '16:9',
                duration: '0:15',
                status: status === 'completed' ? 'accepted' : 'ready',
                createdAt: `${i}m ago`,
                author: assignedWorker.name,
                prompt: `Automated cinematic render for ${template.title}`,
                metrics: { renderTime: '34s', tokens: '1.1k' },
              },
            ]
          : undefined,
    })
  }

  return tasks
}

export const MOCK_RUNNING_TASKS: RunningTask[] = generate100MockTasks()
export const MOCK_100_TASKS = MOCK_RUNNING_TASKS

export const MOCK_CHAT_MESSAGES: OrchestratorMessage[] = [
  {
    id: 'msg-1',
    sender: 'user',
    text: 'Hey Swarm, make me 10 social media videos for our upcoming feature launch.',
    timestamp: '3:42 PM',
  },
  {
    id: 'msg-2',
    sender: 'orchestrator',
    text: "I've analyzed your project brief and created a video generation workflow. I'll use high-energy themes with cyber lattice, neural genesis, and orbital relay aesthetics.\n\n3 videos are currently rendering and will be ready shortly.",
    timestamp: '3:43 PM',
    videoProgressCard: {
      title: 'Video generation in progress...',
      progressPercent: 60,
      clipsLabel: '6/10 clips',
      aspect: '16:9',
    },
  },
  {
    id: 'msg-3',
    sender: 'orchestrator',
    text: 'Your first 3 videos are ready! They follow your feature launch theme and are optimized for social media.\n\nWould you like me to proceed with the remaining 7 variations, or make any adjustments?',
    timestamp: '3:45 PM',
    actionButton: {
      label: 'Preview all 3 videos',
      action: 'preview_all_videos',
    },
    linkedDeliverableIds: ['deliv-vid-1', 'deliv-vid-2', 'deliv-vid-3'],
  },
]
