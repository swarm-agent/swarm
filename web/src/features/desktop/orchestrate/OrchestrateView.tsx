import { useEffect, useState } from 'react'
import {
  Activity,
  ArrowRight,
  Bot,
  Brain,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Eye,
  EyeOff,
  FileCheck,
  FileCode,
  Film,
  FolderGit2,
  GitBranch,
  Layers,
  Maximize2,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Send,
  Sparkles,
  Terminal,
  Volume2,
  X,
  Zap,
} from 'lucide-react'
import {
  MOCK_AUTOMATIONS,
  MOCK_CHAT_MESSAGES,
  MOCK_PROJECTS,
  MOCK_RUNNING_TASKS,
} from './orchestrate-mock-data'
import {
  ORCHESTRATE_THEME_IDS,
  ORCHESTRATE_THEMES,
} from './orchestrate-themes'
import {
  MediaDeliverable,
  OrchestrateThemeId,
  OrchestratorMessage,
  ProjectSummary,
  RunningAutomation,
  RunningTask,
} from './orchestrate-types'

export interface OrchestrateViewProps {
  workspaceSlug?: string
  onNavigateHome?: () => void
  initialThemeId?: OrchestrateThemeId
}

export function OrchestrateView({
  workspaceSlug: _workspaceSlug,
  onNavigateHome,
  initialThemeId = 'apple_peach',
}: OrchestrateViewProps) {
  const [currentThemeId, setCurrentThemeId] = useState<OrchestrateThemeId>(initialThemeId)
  const theme = ORCHESTRATE_THEMES[currentThemeId] || ORCHESTRATE_THEMES.apple_peach

  const [projects] = useState<ProjectSummary[]>(MOCK_PROJECTS)
  const [selectedProjectId, setSelectedProjectId] = useState<string>(projects[0].id)
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  // Automations state & collapse toggle
  const [automations, setAutomations] = useState<RunningAutomation[]>(MOCK_AUTOMATIONS)
  const [showAutomations, setShowAutomations] = useState(true)

  // Tasks state (which encapsulate their attached deliverables)
  const [tasks, setTasks] = useState<RunningTask[]>(MOCK_RUNNING_TASKS)
  const [taskFilter, setTaskFilter] = useState<'all' | 'running' | 'needs_review' | 'completed'>('all')

  // Video preview modal
  const [activeVideoPreview, setActiveVideoPreview] = useState<MediaDeliverable | null>(null)
  const [isPlayingVideo, setIsPlayingVideo] = useState(false)
  const [isMuted, setIsMuted] = useState(false)
  const [isFullscreen, setIsFullscreen] = useState(false)

  // Chat state
  const [messages, setMessages] = useState<OrchestratorMessage[]>(MOCK_CHAT_MESSAGES)
  const [inputText, setInputText] = useState('')
  const [isTyping, setIsTyping] = useState(false)

  // Live telemetry pulse simulation
  const [telemetryTick, setTelemetryTick] = useState(0)
  const [refreshingTelemetry, setRefreshingTelemetry] = useState(false)

  useEffect(() => {
    const interval = setInterval(() => {
      setTelemetryTick((prev) => (prev + 1) % 100)
    }, 1500)
    return () => clearInterval(interval)
  }, [])

  const handleRefreshTelemetry = () => {
    setRefreshingTelemetry(true)
    setTimeout(() => {
      setTelemetryTick((prev) => (prev + 7) % 100)
      setRefreshingTelemetry(false)
    }, 400)
  }

  // Filter tasks based on status
  const filteredTasks = tasks.filter((t) => {
    if (taskFilter === 'all') return true
    return t.status === taskFilter
  })

  // Total deliverables across all tasks
  const totalDeliverablesCount = tasks.reduce(
    (acc, t) => acc + (t.deliverables ? t.deliverables.length : 0),
    0
  )

  const handleAcceptDeliverable = (taskId: string, deliverableId: string, e: React.MouseEvent) => {
    e.stopPropagation()
    setTasks((prev) =>
      prev.map((task) => {
        if (task.id !== taskId && !task.deliverables?.some((d) => d.id === deliverableId)) {
          return task
        }
        return {
          ...task,
          deliverables: task.deliverables?.map((d) =>
            d.id === deliverableId ? { ...d, status: 'accepted' as const } : d
          ),
        }
      })
    )
  }

  const handleSendMessage = (textToSend?: string) => {
    const text = textToSend || inputText
    if (!text.trim()) return

    const userMsg: OrchestratorMessage = {
      id: `msg-${Date.now()}`,
      sender: 'user',
      text: text.trim(),
      timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }

    setMessages((prev) => [...prev, userMsg])
    if (!textToSend) setInputText('')
    setIsTyping(true)

    // Simulate intelligent orchestrator response creating tasks with attached deliverables
    setTimeout(() => {
      let replyText = `Understood. Analyzing project context for "${selectedProject.name}"...`
      const newDeliverableIds: string[] = []

      if (text.toLowerCase().includes('video')) {
        const newTaskId = `task-${Date.now()}`
        const vid1Id = `deliv-vid-${Date.now()}-1`
        const vid2Id = `deliv-vid-${Date.now()}-2`
        const vid3Id = `deliv-vid-${Date.now()}-3`
        newDeliverableIds.push(vid1Id, vid2Id, vid3Id)

        const newTask: RunningTask = {
          id: newTaskId,
          title: 'Make 3 Social Media Videos for Feature Launch',
          agentType: 'designer',
          status: 'running',
          workspaceTarget: 'swarm-social',
          elapsed: 'Just now',
          subtasks: [
            { id: `st-${Date.now()}-1`, title: 'Hydrate 3 prompt themes via Router', completed: true },
            { id: `st-${Date.now()}-2`, title: 'Batch dispatch to Video Generation Worker', completed: true },
            { id: `st-${Date.now()}-3`, title: 'Synthesize kinetic audio & visual transitions', completed: false },
          ],
          deliverables: [
            {
              id: vid1Id,
              title: 'Dynamic Interface Reveal (Part 1/3)',
              type: 'video',
              videoAspect: '9:16',
              duration: '0:15',
              status: 'ready',
              createdAt: 'Just now',
              author: 'Video Swarm Worker',
              prompt: 'Sleek kinetic interface animation zooming through warm peach glass panels with particle reflections',
              metrics: { renderTime: '24s', tokens: '980', views: 'Preview ready' },
            },
            {
              id: vid2Id,
              title: 'Subagent Lineage In Motion (Part 2/3)',
              type: 'video',
              videoAspect: '9:16',
              duration: '0:15',
              status: 'ready',
              createdAt: 'Just now',
              author: 'Video Swarm Worker',
              prompt: 'Branching tree nodes animating into isolated worktrees with emerald status pips',
              metrics: { renderTime: '26s', tokens: '1.0k', views: 'Preview ready' },
            },
            {
              id: vid3Id,
              title: 'Instant Telemetry Stream (Part 3/3)',
              type: 'video',
              videoAspect: '16:9',
              duration: '0:20',
              status: 'generating',
              createdAt: 'Rendering now...',
              author: 'Video Swarm Worker',
              prompt: 'High-speed terminal stream running critical test tiers with instant green checkmarks',
              metrics: { renderTime: 'In progress', tokens: '1.1k' },
            },
          ],
        }

        replyText = `Dispatched to Video Swarm Worker! Created task "Make 3 Social Media Videos for Feature Launch" with 3 video deliverables attached directly to the task. 2 clips are already rendered and ready for your review.`

        setTasks((prev) => [newTask, ...prev])

        setAutomations((prev) => [
          {
            id: `auto-video-${Date.now()}`,
            name: `Social Promo Batch #${prev.length + 1}`,
            kind: 'trigger',
            status: 'running',
            progressPercent: 40,
            currentStep: 'Composing audio soundtrack & subtitle streams',
            totalRuns: 1,
          },
          ...prev,
        ])
      } else if (text.toLowerCase().includes('test') || text.toLowerCase().includes('testbench')) {
        const newTaskId = `task-${Date.now()}`
        const newTask: RunningTask = {
          id: newTaskId,
          title: 'Run Isolated Systemd-Nspawn Testbench on Slot-1',
          agentType: 'coder',
          status: 'running',
          workspaceTarget: selectedProject.repoPath,
          elapsed: 'Just now',
          subtasks: [
            { id: `st-${Date.now()}-1`, title: 'Acquire slot-1 nspawn deployment lease', completed: true },
            { id: `st-${Date.now()}-2`, title: 'Run critical fast suite checks', completed: true },
            { id: `st-${Date.now()}-3`, title: 'Run deep durability & agents tiers', completed: false },
          ],
        }

        replyText = `Triggered isolated systemd-nspawn testbench lease on slot-1 for "${selectedProject.repoPath}". Verifying pre-push hermetic invariants.`

        setTasks((prev) => [newTask, ...prev])

        setAutomations((prev) => [
          {
            id: `auto-test-${Date.now()}`,
            name: `Testbench Verification (slot-1)`,
            kind: 'trigger',
            status: 'running',
            progressPercent: 50,
            currentStep: 'Executing hermetic critical suites',
            totalRuns: 1,
          },
          ...prev,
        ])
      } else if (text.toLowerCase().includes('token') || text.toLowerCase().includes('memory')) {
        replyText = `Project memory audit for ${selectedProject.name}:\n• Lean project memory: 420 tokens\n• Zero AGENTS.md bloat: Saved ~14,200 tokens from orchestrator prompt.\n• Delegated subagents receive repo-specific rules only upon worktree launch.`
      } else {
        replyText = `I have registered your request and linked it to "${selectedProject.name}". Tracking ongoing tasks and attached deliverables in your Project Canvas.`
      }

      const botMsg: OrchestratorMessage = {
        id: `msg-reply-${Date.now()}`,
        sender: 'orchestrator',
        text: replyText,
        timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        linkedDeliverableIds: newDeliverableIds,
      }

      setMessages((prev) => [...prev, botMsg])
      setIsTyping(false)
    }, 700)
  }

  return (
    <div
      className={`relative flex h-screen w-screen overflow-hidden p-3 gap-3 ${theme.bgClass} ${theme.textPrimaryClass} font-sans`}
      style={theme.customVars as React.CSSProperties}
    >
      {/* ─────────────────────────────────────────────────────────────
          1. SUBTLE AMBIENT WARM PEACH LIGHTING ATMOSPHERE
         ───────────────────────────────────────────────────────────── */}
      <div className="pointer-events-none absolute inset-0 overflow-hidden">
        {/* Soft top-right warm peach glow */}
        <div className="absolute -top-32 -right-32 h-[38rem] w-[38rem] rounded-full bg-[#ff8c69]/[0.08] blur-[150px]" />
        {/* Soft bottom-left glowing apricot warmth */}
        <div className="absolute -bottom-32 -left-32 h-[34rem] w-[34rem] rounded-full bg-[#ffa387]/[0.05] blur-[140px]" />
        {/* Subtle center ambient radiance */}
        <div className="absolute top-1/3 left-1/3 h-[28rem] w-[28rem] rounded-full bg-[#f38563]/[0.03] blur-[130px]" />
      </div>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 1: LEFT SIDEBAR (PROJECTS & CONTEXT HUD)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative flex w-72 flex-shrink-0 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.035] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
        {/* Top Headerless Brand Capsule & Theme Switcher */}
        <div className="p-3 border-b border-white/[0.07] bg-white/[0.02]">
          <div className="flex items-center justify-between pb-2.5">
            <div className="flex items-center gap-2">
              <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-gradient-to-tr from-[#ff7a50] to-[#ffa387] text-white shadow-[0_2px_10px_rgba(255,122,80,0.4)]">
                <Sparkles size={14} />
              </div>
              <div>
                <div className="flex items-center gap-1.5">
                  <span className="text-xs font-bold tracking-tight text-white">Orchestrate</span>
                  <span className="rounded-full bg-[#ff8c69]/15 border border-[#ff8c69]/30 px-1.5 py-0.2 text-[8px] font-semibold text-[#ffa387]">
                    HUB
                  </span>
                </div>
                <div className="text-[10px] text-neutral-400 truncate max-w-[130px]">
                  {theme.subtitle}
                </div>
              </div>
            </div>

            {onNavigateHome && (
              <button
                onClick={onNavigateHome}
                className="rounded-full px-2.5 py-1 text-[10px] font-medium text-neutral-400 hover:bg-white/10 hover:text-white transition-all border border-white/10"
                title="Exit Mockup"
              >
                Exit
              </button>
            )}
          </div>

          {/* Compact 5 Apple Dark Warm Peach Themes Selector */}
          <div className="flex items-center justify-between gap-1 rounded-xl bg-black/40 p-1 border border-white/[0.08]">
            {ORCHESTRATE_THEME_IDS.map((tId) => {
              const t = ORCHESTRATE_THEMES[tId]
              const isActive = tId === currentThemeId
              return (
                <button
                  key={tId}
                  onClick={() => setCurrentThemeId(tId)}
                  className={`flex flex-1 items-center justify-center gap-1 py-1 text-[10px] transition-all rounded-lg ${
                    isActive
                      ? 'bg-white/[0.18] text-white font-semibold shadow-sm border border-white/20'
                      : 'text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.05]'
                  }`}
                  title={`${t.name}: ${t.subtitle}`}
                >
                  <span
                    className={`h-1.5 w-1.5 rounded-full ${isActive ? 'scale-125' : 'opacity-60'}`}
                    style={{ backgroundColor: t.accentColor }}
                  />
                  <span className="truncate">{t.name.split(' ')[0]}</span>
                </button>
              )
            })}
          </div>
        </div>

        {/* Projects Switcher Section */}
        <div className="p-3 border-b border-white/[0.07]">
          <div className="flex items-center justify-between pb-2">
            <span className="text-[11px] font-semibold uppercase tracking-wider text-neutral-400 flex items-center gap-1.5">
              Projects
            </span>
            <button className="flex items-center gap-1 px-2 py-0.5 text-[10px] font-medium rounded-full bg-white/[0.08] text-white hover:bg-white/[0.15] backdrop-blur-md transition-all">
              <Plus size={11} /> New
            </button>
          </div>

          <div className="flex flex-col gap-1.5">
            {projects.map((proj) => {
              const isSelected = proj.id === selectedProjectId
              return (
                <button
                  key={proj.id}
                  onClick={() => setSelectedProjectId(proj.id)}
                  className={`group flex items-center justify-between p-2 text-left rounded-2xl transition-all ${
                    isSelected
                      ? 'bg-white/[0.1] border border-white/20 shadow-[0_4px_16px_rgba(0,0,0,0.25)] text-white'
                      : 'border border-transparent hover:bg-white/[0.04] text-neutral-400 hover:text-neutral-200'
                  }`}
                >
                  <div className="flex items-center gap-2 min-w-0">
                    <div
                      className={`flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg transition-all ${
                        isSelected
                          ? 'bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30'
                          : 'bg-white/[0.05] text-neutral-400'
                      }`}
                    >
                      <FolderGit2 size={13} />
                    </div>
                    <div className="flex flex-col min-w-0">
                      <span className="text-xs font-semibold truncate">{proj.name}</span>
                      <span className="text-[10px] truncate text-neutral-500">
                        {proj.repoPath}
                      </span>
                    </div>
                  </div>
                  {isSelected && (
                    <span className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-[#ff8c69] shadow-[0_0_8px_rgba(255,140,105,0.9)]" />
                  )}
                </button>
              )
            })}
          </div>
        </div>

        {/* Project Context & Telemetry HUD */}
        <div className="flex-1 space-y-2.5 overflow-y-auto p-3 text-xs">
          {/* Git & Worktree Status Block */}
          <div className="p-2.5 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
            <div className="flex items-center justify-between pb-1.5 border-b border-white/[0.06]">
              <span className="flex items-center gap-1.5 font-medium text-[11px] text-neutral-200">
                <GitBranch size={13} className="text-[#ff8c69]" />
                Repository Status
              </span>
              <span className="flex items-center gap-1 text-[10px] text-emerald-400 font-medium">
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                Clean
              </span>
            </div>
            <div className="mt-1.5 space-y-1 text-[11px] text-neutral-400">
              <div className="flex justify-between">
                <span>Branch:</span>
                <span className="font-mono text-neutral-200">{selectedProject.branch}</span>
              </div>
              <div className="flex justify-between">
                <span>Linked Repos:</span>
                <span className="font-mono text-neutral-200">
                  {selectedProject.linkedWorkspaces.length} roots
                </span>
              </div>
              <div className="flex justify-between">
                <span>Worktrees:</span>
                <span className="font-mono text-neutral-200">3 isolated</span>
              </div>
            </div>
          </div>

          {/* Automation Health & Live Telemetry HUD */}
          <div className="p-2.5 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
            <div className="flex items-center justify-between pb-1.5 border-b border-white/[0.06]">
              <span className="flex items-center gap-1.5 font-medium text-[11px] text-neutral-200">
                <Activity size={13} className="text-[#ff8c69]" />
                Automation Status
              </span>
              <div className="flex items-center gap-1.5">
                <button
                  onClick={handleRefreshTelemetry}
                  className="text-neutral-400 hover:text-neutral-200 transition-colors"
                  title="Refresh Telemetry"
                >
                  <RefreshCw
                    size={11}
                    className={refreshingTelemetry ? 'animate-spin text-[#ff8c69]' : ''}
                  />
                </button>
                <span className="text-[10px] text-neutral-400 font-mono">
                  {24 + (telemetryTick % 5)}ms
                </span>
              </div>
            </div>
            <div className="mt-1.5 space-y-1 text-[11px]">
              <div className="flex items-center justify-between">
                <span className="text-neutral-400">Active Workers:</span>
                <span className="font-medium text-emerald-400">
                  {automations.filter((a) => a.status === 'running').length} running
                </span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-neutral-400">Next Scheduled:</span>
                <span className="text-neutral-300">02:00 UTC (3h)</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-neutral-400">Total Deliverables:</span>
                <span className="rounded-full bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 px-2 py-0.2 text-[10px] font-semibold">
                  {totalDeliverablesCount} generated
                </span>
              </div>
            </div>
          </div>

          {/* Lean Memory (projects.md) HUD */}
          <div className="p-2.5 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
            <div className="flex items-center justify-between pb-1.5 border-b border-white/[0.06]">
              <span className="flex items-center gap-1.5 font-medium text-[11px] text-neutral-200">
                <Brain size={13} className="text-[#ff8c69]" />
                Project Memory
              </span>
              <span className="rounded-full bg-emerald-950/80 px-2 py-0.2 text-[9px] font-semibold text-emerald-400 border border-emerald-800/60">
                LEAN
              </span>
            </div>
            <p className="mt-1.5 text-[10px] leading-relaxed text-neutral-400">
              <strong>Zero AGENTS.md Bloat:</strong> Primary orchestrator reads only lean memory
              (420 tokens). Subagents receive full rules loaded strictly upon worktree dispatch.
            </p>
            <div className="mt-2 p-2 font-mono text-[10px] text-neutral-300 rounded-xl bg-black/40 border border-white/[0.06]">
              # {selectedProject.name}
              <br />
              - Go daemon + Vite desktop
              <br />- Subagents isolated in worktrees
            </div>
          </div>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (STACKED AUTOMATIONS & TASKS WITH DELIVERABLES)
         ───────────────────────────────────────────────────────────── */}
      <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.03] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
        {/* TOP: STACKED & COLLAPSIBLE/HIDEABLE AUTOMATIONS SECTION */}
        <section className="border-b border-white/[0.07] bg-white/[0.02] flex flex-col flex-shrink-0 transition-all duration-300">
          {/* Automations Section Header */}
          <div className="flex items-center justify-between p-3.5">
            <div className="flex items-center gap-2.5">
              <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30">
                <Zap size={14} />
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs font-bold uppercase tracking-wider text-neutral-100">
                  Active Automations
                </span>
                <span className="rounded-full bg-[#ff8c69]/15 border border-[#ff8c69]/30 px-2 py-0.5 text-[10px] font-semibold text-[#ffa387]">
                  {automations.length} Active · {automations.filter((a) => a.status === 'running').length} Running
                </span>
              </div>
            </div>

            <div className="flex items-center gap-2">
              <button
                onClick={() => handleSendMessage('make 3 social media videos')}
                className="flex items-center gap-1.5 rounded-full bg-gradient-to-r from-[#ff7a50] to-[#ff9472] px-3 py-1 text-xs font-semibold text-white shadow-[0_2px_10px_rgba(255,122,80,0.35)] hover:brightness-105 active:scale-95 transition-all"
              >
                <Film size={12} /> + Trigger Video Swarm
              </button>
              <button
                onClick={() => handleSendMessage('run full testbench')}
                className="flex items-center gap-1.5 rounded-full bg-white/[0.08] hover:bg-white/[0.14] border border-white/[0.12] px-3 py-1 text-xs font-semibold text-white backdrop-blur-md active:scale-95 transition-all"
              >
                <Terminal size={12} /> + Run Testbench
              </button>

              {/* Hide / Show Automations Toggle Button */}
              <button
                onClick={() => setShowAutomations(!showAutomations)}
                className="flex items-center gap-1.5 rounded-full bg-neutral-800 hover:bg-neutral-700 text-neutral-200 border border-neutral-600/80 px-3 py-1 text-xs font-semibold shadow-sm transition-all active:scale-95"
                title={showAutomations ? 'Hide Automations Section' : 'Show Automations Section'}
              >
                {showAutomations ? (
                  <>
                    <EyeOff size={13} className="text-[#ffa387]" />
                    <span>Hide Automations</span>
                    <ChevronUp size={13} />
                  </>
                ) : (
                  <>
                    <Eye size={13} className="text-[#ffa387]" />
                    <span>Show Automations ({automations.length})</span>
                    <ChevronDown size={13} />
                  </>
                )}
              </button>
            </div>
          </div>

          {/* Stacked Vertical Automations List (Shown when not hidden) */}
          {showAutomations && (
            <div className="px-3.5 pb-3.5 flex flex-col gap-2 max-h-56 overflow-y-auto">
              {automations.map((auto) => (
                <div
                  key={auto.id}
                  className="flex items-center justify-between p-2.5 text-xs rounded-2xl border border-white/[0.08] bg-white/[0.035] backdrop-blur-xl shadow-sm hover:bg-white/[0.06] transition-all"
                >
                  {/* Left: Info & status badge */}
                  <div className="flex items-center gap-3 min-w-[260px]">
                    <span
                      className={`text-[9px] uppercase px-2.5 py-0.5 font-bold rounded-full ${
                        auto.status === 'running'
                          ? 'bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/40 flex items-center gap-1'
                          : auto.status === 'scheduled'
                          ? 'bg-amber-950/60 text-amber-300 border border-amber-800/50'
                          : 'bg-white/[0.08] text-neutral-300'
                      }`}
                    >
                      {auto.status === 'running' && (
                        <span className="h-1.5 w-1.5 rounded-full bg-[#ff8c69] animate-pulse" />
                      )}
                      {auto.status}
                    </span>

                    <div className="flex flex-col min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-neutral-100 truncate">{auto.name}</span>
                        <span className="text-[10px] font-mono px-1.5 py-0.2 rounded bg-black/40 text-neutral-400 border border-white/[0.06]">
                          {auto.kind}
                        </span>
                      </div>
                      <span className="text-[10px] text-neutral-400">
                        Total runs: {auto.totalRuns} {auto.lastRun && `· Last run: ${auto.lastRun}`}
                      </span>
                    </div>
                  </div>

                  {/* Middle: Progress or next run summary */}
                  <div className="flex-1 max-w-md px-4">
                    {auto.status === 'running' && auto.progressPercent !== undefined ? (
                      <div className="space-y-1">
                        <div className="flex items-center justify-between text-[10px]">
                          <span className="text-neutral-300 truncate">{auto.currentStep}</span>
                          <span className="font-mono font-semibold text-[#ffa387]">
                            {auto.progressPercent}%
                          </span>
                        </div>
                        <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/[0.08]">
                          <div
                            className="h-full bg-gradient-to-r from-[#ff7a50] to-[#ffa387] rounded-full transition-all duration-500 shadow-[0_0_8px_rgba(255,140,105,0.5)]"
                            style={{ width: `${auto.progressPercent}%` }}
                          />
                        </div>
                      </div>
                    ) : (
                      <p className="text-[11px] text-neutral-400 truncate">
                        {auto.nextRun || auto.outputSummary}
                      </p>
                    )}
                  </div>

                  {/* Right: Quick action */}
                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => handleSendMessage(`run automation ${auto.name}`)}
                      className="px-2.5 py-1 text-[11px] font-semibold rounded-lg bg-white/[0.08] hover:bg-white/[0.16] text-neutral-200 border border-white/10 transition-all"
                    >
                      Trigger Now
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        {/* MAIN BODY: ONGOING TASKS WITH ATTACHED DELIVERABLES */}
        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {/* Header of Tasks section & filter tabs */}
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30">
                <Layers size={14} />
              </div>
              <div>
                <h3 className="text-xs font-bold uppercase tracking-wider text-neutral-100 flex items-center gap-2">
                  Ongoing Tasks & Attached Deliverables
                  <span className="rounded-full bg-white/[0.08] px-2 py-0.5 text-[10px] font-semibold text-neutral-300">
                    {filteredTasks.length} Tasks
                  </span>
                </h3>
                <span className="text-[10px] text-neutral-400">
                  Deliverables are bound directly to the subagent task that produced them
                </span>
              </div>
            </div>

            {/* Filter tabs */}
            <div className="flex items-center gap-1 rounded-full bg-black/40 border border-white/[0.08] p-1">
              {(['all', 'running', 'needs_review', 'completed'] as const).map((filter) => (
                <button
                  key={filter}
                  onClick={() => setTaskFilter(filter)}
                  className={`px-3 py-1 text-xs font-bold capitalize rounded-full transition-all ${
                    taskFilter === filter
                      ? 'bg-white text-neutral-950 shadow-md'
                      : 'text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.05]'
                  }`}
                >
                  {filter === 'all'
                    ? 'All Tasks'
                    : filter === 'needs_review'
                    ? 'Needs Review'
                    : filter}
                </button>
              ))}
            </div>
          </div>

          {/* Tasks List with attached deliverables */}
          <div className="space-y-4">
            {filteredTasks.map((task) => (
              <div
                key={task.id}
                className="p-4 rounded-3xl border border-white/[0.09] bg-white/[0.035] backdrop-blur-xl shadow-[0_8px_32px_rgba(0,0,0,0.3)] hover:border-white/[0.16] transition-all"
              >
                {/* Task Header */}
                <div className="flex items-center justify-between pb-3 border-b border-white/[0.08]">
                  <div className="flex items-center gap-2.5">
                    <span
                      className={`px-2.5 py-0.5 text-[10px] font-bold uppercase tracking-wider rounded-full ${
                        task.agentType === 'coder'
                          ? 'bg-orange-500/20 text-orange-300 border border-orange-500/40'
                          : task.agentType === 'designer'
                          ? 'bg-purple-500/20 text-purple-300 border border-purple-500/40'
                          : 'bg-blue-500/20 text-blue-300 border border-blue-500/40'
                      }`}
                    >
                      {task.agentType}
                    </span>

                    <h4 className="text-sm font-bold text-white tracking-tight">{task.title}</h4>

                    <span className="font-mono text-[10px] text-neutral-300 bg-black/40 px-2 py-0.5 rounded-md border border-white/10">
                      {task.workspaceTarget}
                    </span>
                  </div>

                  <div className="flex items-center gap-2.5">
                    <span className="text-[11px] font-mono text-neutral-400">
                      Elapsed: {task.elapsed}
                    </span>
                    <span
                      className={`text-[10px] uppercase font-bold px-2.5 py-0.5 rounded-full ${
                        task.status === 'running'
                          ? 'bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/40 flex items-center gap-1.5'
                          : task.status === 'needs_review'
                          ? 'bg-amber-500/20 text-amber-300 border border-amber-500/40'
                          : 'bg-emerald-500/20 text-emerald-300 border border-emerald-500/40'
                      }`}
                    >
                      {task.status === 'running' && (
                        <span className="h-1.5 w-1.5 rounded-full bg-[#ff8c69] animate-pulse" />
                      )}
                      {task.status}
                    </span>
                  </div>
                </div>

                {/* Subtasks Progress Checklist */}
                <div className="mt-3 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2 text-xs">
                  {task.subtasks.map((st) => (
                    <div
                      key={st.id}
                      className="flex items-center gap-2 p-1.5 rounded-xl bg-white/[0.02] border border-white/[0.05]"
                    >
                      <CheckCircle2
                        size={13}
                        className={st.completed ? 'text-emerald-400' : 'text-neutral-600'}
                      />
                      <span
                        className={`text-[11px] truncate ${
                          st.completed ? 'text-neutral-400 line-through' : 'text-neutral-200'
                        }`}
                      >
                        {st.title}
                      </span>
                    </div>
                  ))}
                </div>

                {/* Code Diff Preview if available */}
                {task.diffPreview && (
                  <div className="mt-3 p-3 font-mono text-[11px] text-emerald-400 rounded-2xl bg-black/60 border border-emerald-950/80">
                    <pre className="overflow-x-auto">{task.diffPreview}</pre>
                  </div>
                )}

                {/* ATTACHED DELIVERABLES CONTAINER */}
                {task.deliverables && task.deliverables.length > 0 && (
                  <div className="mt-3.5 p-3.5 rounded-2xl bg-black/40 border border-white/[0.1] shadow-inner">
                    <div className="flex items-center justify-between pb-2 mb-3 border-b border-white/[0.08]">
                      <span className="text-xs font-bold text-neutral-200 flex items-center gap-2">
                        <Sparkles size={13} className="text-[#ff8c69]" />
                        Attached Deliverables ({task.deliverables.length})
                      </span>
                      <span className="text-[10px] text-neutral-400">
                        Outputs produced by this subagent run
                      </span>
                    </div>

                    {/* Deliverables Grid for this task */}
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
                      {task.deliverables.map((item) => (
                        <div
                          key={item.id}
                          className="group relative flex flex-col p-3 rounded-2xl border border-white/[0.08] bg-white/[0.04] backdrop-blur-xl shadow-md hover:bg-white/[0.07] hover:border-[#ff8c69]/40 transition-all"
                        >
                          {/* Deliverable Header */}
                          <div className="flex items-center justify-between pb-2">
                            <div className="flex items-center gap-1.5">
                              {item.type === 'video' ? (
                                <Film size={14} className="text-[#ff8c69]" />
                              ) : item.type === 'code' ? (
                                <FileCode size={14} className="text-emerald-400" />
                              ) : (
                                <FileCheck size={14} className="text-amber-400" />
                              )}
                              <span className="text-[10px] font-mono text-neutral-300">
                                {item.author}
                              </span>
                            </div>

                            <span
                              className={`text-[9px] uppercase px-2 py-0.5 font-bold rounded-full ${
                                item.status === 'ready'
                                  ? 'bg-emerald-500/25 text-emerald-300 border border-emerald-500/40'
                                  : item.status === 'generating'
                                  ? 'bg-[#ff8c69]/25 text-[#ffa387] border border-[#ff8c69]/40 animate-pulse'
                                  : item.status === 'accepted'
                                  ? 'bg-emerald-600/30 text-emerald-300 border border-emerald-400/50 flex items-center gap-1'
                                  : 'bg-white/[0.08] text-neutral-400'
                              }`}
                            >
                              {item.status === 'accepted' && <CheckCircle2 size={10} />}
                              {item.status}
                            </span>
                          </div>

                          {/* Thumbnail for Video Deliverables */}
                          {item.type === 'video' && (
                            <div
                              onClick={() => setActiveVideoPreview(item)}
                              className="relative mb-2 aspect-video w-full cursor-pointer overflow-hidden flex items-center justify-center rounded-xl border border-white/[0.1] bg-black/70 shadow-inner group-hover:border-[#ff8c69]/40"
                            >
                              <div className="absolute inset-0 bg-gradient-to-tr from-[#ff7a50]/25 via-[#ffa387]/15 to-transparent opacity-60 group-hover:opacity-100 transition-opacity" />
                              <div className="relative z-10 flex flex-col items-center gap-1">
                                <div className="flex h-9 w-9 items-center justify-center rounded-full bg-white text-neutral-900 shadow-xl transition-transform group-hover:scale-110">
                                  <Play size={14} fill="currentColor" />
                                </div>
                                <span className="text-[10px] font-mono text-neutral-200">
                                  {item.duration || '0:15'} • {item.videoAspect || '9:16'}
                                </span>
                              </div>
                              <span className="absolute bottom-1.5 right-1.5 rounded-md bg-black/80 px-1.5 py-0.5 font-mono text-[9px] text-neutral-300">
                                1080p
                              </span>
                            </div>
                          )}

                          {/* Deliverable Title & Prompt */}
                          <h5 className="text-xs font-bold text-neutral-100 line-clamp-1">
                            {item.title}
                          </h5>
                          <p className="mt-1 text-[11px] line-clamp-2 text-neutral-300 leading-relaxed">
                            {item.prompt}
                          </p>

                          {/* Footer with High-Contrast Action Buttons */}
                          <div className="mt-3 flex items-center justify-between border-t border-white/[0.08] pt-2 text-[10px]">
                            <span className="text-neutral-400 font-mono">{item.createdAt}</span>

                            <div className="flex items-center gap-1.5">
                              {item.status === 'ready' && (
                                <button
                                  onClick={(e) => handleAcceptDeliverable(task.id, item.id, e)}
                                  className="flex items-center gap-1.5 rounded-xl bg-emerald-400 hover:bg-emerald-300 text-neutral-950 font-bold px-3 py-1.5 text-xs shadow-[0_2px_10px_rgba(52,211,153,0.35)] transition-all active:scale-95"
                                  title="Accept and approve this deliverable"
                                >
                                  <CheckCircle2 size={13} className="text-neutral-950" />
                                  <span>Accept Deliverable</span>
                                </button>
                              )}

                              {item.status === 'accepted' && (
                                <div className="flex items-center gap-1 rounded-xl bg-emerald-950/80 text-emerald-300 border border-emerald-500/40 px-3 py-1 text-xs font-semibold">
                                  <CheckCircle2 size={12} className="text-emerald-400" />
                                  <span>Accepted</span>
                                </div>
                              )}

                              {item.type === 'video' && (
                                <button
                                  onClick={() => setActiveVideoPreview(item)}
                                  className="flex items-center gap-1 rounded-xl bg-neutral-800 hover:bg-neutral-700 text-neutral-100 border border-neutral-600/80 px-2.5 py-1.5 text-xs font-semibold shadow-sm transition-all active:scale-95"
                                  title="Open Video Preview"
                                >
                                  <Play size={10} fill="currentColor" />
                                  <span>Preview</span>
                                </button>
                              )}
                            </div>
                          </div>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      </main>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 3: RIGHT SIDEBAR (SWARM ORCHESTRATOR AI CHAT)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative flex w-80 flex-shrink-0 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.035] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
        {/* Clean Header without unnecessary suggestion pills */}
        <div className="p-3.5 border-b border-white/[0.07]">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30">
                <Bot size={15} />
              </div>
              <div className="flex flex-col">
                <span className="text-xs font-bold text-neutral-100">Swarm Orchestrator</span>
                <span className="text-[10px] text-emerald-400 flex items-center gap-1">
                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                  Bound to {selectedProject.name}
                </span>
              </div>
            </div>

            <span className="rounded-full bg-white/[0.06] border border-white/[0.08] px-2 py-0.5 text-[9px] font-semibold text-neutral-300">
              Lean
            </span>
          </div>
        </div>

        {/* Chat Messages Stream */}
        <div className="flex-1 space-y-3 overflow-y-auto p-3.5 text-xs">
          {messages.map((msg) => {
            const isUser = msg.sender === 'user'
            return (
              <div
                key={msg.id}
                className={`flex flex-col ${isUser ? 'items-end' : 'items-start'}`}
              >
                <div
                  className={`max-w-[90%] px-3.5 py-2.5 text-xs leading-relaxed transition-all ${
                    isUser
                      ? 'rounded-2xl rounded-br-sm bg-gradient-to-r from-[#ff7a50] to-[#ff9472] text-white shadow-md font-medium'
                      : 'rounded-2xl rounded-bl-sm bg-white/[0.06] backdrop-blur-xl border border-white/[0.08] text-neutral-200'
                  }`}
                >
                  <p className="whitespace-pre-line">{msg.text}</p>

                  {/* Action link if relevant to video preview */}
                  {msg.linkedDeliverableIds && msg.linkedDeliverableIds.length > 0 && (
                    <div className="mt-2.5 flex flex-col gap-1 border-t border-white/10 pt-2">
                      <button
                        onClick={() => {
                          const target = tasks
                            .flatMap((t) => t.deliverables || [])
                            .find((d) => msg.linkedDeliverableIds?.includes(d.id))
                          if (target) setActiveVideoPreview(target)
                        }}
                        className="flex items-center justify-between rounded-xl bg-white/10 hover:bg-white/20 px-3 py-1.5 text-[11px] font-semibold text-[#ffb299] transition-colors"
                      >
                        <span>Preview Generated Clip</span>
                        <ArrowRight size={11} />
                      </button>
                    </div>
                  )}
                </div>
                <span className="mt-1 text-[9px] text-neutral-500">{msg.timestamp}</span>
              </div>
            )
          })}

          {isTyping && (
            <div className="flex items-center gap-1.5 text-xs text-neutral-400">
              <Sparkles size={12} className="animate-spin text-[#ff8c69]" />
              <span>Orchestrator dispatching tasks...</span>
            </div>
          )}
        </div>

        {/* Chat Input Box */}
        <div className="p-3 border-t border-white/[0.07]">
          <form
            onSubmit={(e) => {
              e.preventDefault()
              handleSendMessage()
            }}
            className="flex items-center gap-1.5"
          >
            <input
              type="text"
              value={inputText}
              onChange={(e) => setInputText(e.target.value)}
              placeholder="Ask orchestrator to delegate, test, or generate..."
              className="flex-1 px-3.5 py-1.5 text-xs rounded-full bg-white/[0.06] border border-white/[0.12] text-white placeholder-neutral-500 focus:outline-none focus:ring-1 focus:ring-[#ff8c69]/60 focus:border-[#ff8c69]/60 transition-all"
            />
            <button
              type="submit"
              disabled={!inputText.trim()}
              className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-r from-[#ff7a50] to-[#ff9472] text-white shadow-md hover:brightness-110 active:scale-95 transition-all disabled:opacity-40"
            >
              <Send size={13} />
            </button>
          </form>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          MODAL: VIDEO DELIVERABLE PREVIEW DIALOG (APPLE GLASS SHEET)
         ───────────────────────────────────────────────────────────── */}
      {activeVideoPreview && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-6 backdrop-blur-xl">
          <div className="relative flex max-w-2xl w-full flex-col p-5 rounded-3xl border border-white/20 bg-[#14110f]/95 backdrop-blur-3xl shadow-[0_20px_60px_rgba(0,0,0,0.85)]">
            <div className="flex items-center justify-between pb-3 border-b border-white/10">
              <div className="flex items-center gap-2">
                <Film size={16} className="text-[#ff8c69]" />
                <h3 className="text-sm font-bold text-neutral-100">{activeVideoPreview.title}</h3>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => setIsFullscreen(!isFullscreen)}
                  className="rounded-full p-1 text-neutral-400 hover:bg-white/10 hover:text-white transition-colors"
                  title="Toggle Fullscreen"
                >
                  <Maximize2 size={14} />
                </button>
                <button
                  onClick={() => {
                    setActiveVideoPreview(null)
                    setIsPlayingVideo(false)
                  }}
                  className="rounded-full p-1 text-neutral-400 hover:bg-white/10 hover:text-white transition-colors"
                >
                  <X size={16} />
                </button>
              </div>
            </div>

            {/* Simulated Video Canvas */}
            <div className="relative my-4 aspect-video w-full overflow-hidden flex items-center justify-center rounded-2xl border border-white/15 bg-black">
              <div
                className="absolute inset-0 bg-gradient-to-tr from-[#ff7a50]/30 via-[#ffa387]/15 to-transparent opacity-60"
                style={{
                  animation: isPlayingVideo ? 'pulse 1.5s ease-in-out infinite' : 'none',
                }}
              />
              <div className="relative z-10 flex flex-col items-center gap-2">
                <button
                  onClick={() => setIsPlayingVideo(!isPlayingVideo)}
                  className="flex h-14 w-14 items-center justify-center rounded-full bg-white text-neutral-900 shadow-2xl transition-transform hover:scale-105 active:scale-95"
                >
                  {isPlayingVideo ? <Pause size={24} /> : <Play size={24} fill="currentColor" />}
                </button>
                <span className="text-xs font-mono text-neutral-200 font-semibold">
                  {isPlayingVideo ? 'Playing Simulated Stream...' : 'Click to Play Render Preview'}
                </span>
              </div>

              {/* Volume toggle in corner */}
              <button
                onClick={() => setIsMuted(!isMuted)}
                className="absolute bottom-2 left-2 z-20 rounded-full bg-black/60 p-1.5 text-neutral-300 hover:text-white backdrop-blur-sm"
                title={isMuted ? 'Unmute' : 'Mute'}
              >
                <Volume2 size={13} className={isMuted ? 'opacity-40' : ''} />
              </button>
            </div>

            {/* Prompt details & actions */}
            <div className="space-y-2 text-xs">
              <div>
                <span className="text-neutral-400 font-semibold">Worker Prompt: </span>
                <span className="text-neutral-200">{activeVideoPreview.prompt}</span>
              </div>
              <div className="flex items-center gap-4 text-neutral-300 font-mono text-[11px]">
                <span>Duration: {activeVideoPreview.duration}</span>
                <span>Aspect: {activeVideoPreview.videoAspect}</span>
                <span>Render Time: {activeVideoPreview.metrics?.renderTime}</span>
              </div>
            </div>

            <div className="mt-4 flex items-center justify-end gap-2 border-t border-white/10 pt-3">
              <button
                onClick={(e) => {
                  // Find parent task and accept deliverable
                  const parentTask = tasks.find((t) =>
                    t.deliverables?.some((d) => d.id === activeVideoPreview.id)
                  )
                  if (parentTask) {
                    handleAcceptDeliverable(parentTask.id, activeVideoPreview.id, e)
                  }
                  setActiveVideoPreview(null)
                }}
                className="flex items-center gap-2 px-5 py-2 text-xs font-bold text-neutral-950 rounded-xl bg-emerald-400 hover:bg-emerald-300 shadow-xl transition-all active:scale-95"
              >
                <CheckCircle2 size={14} className="text-neutral-950" />
                <span>Accept Deliverable</span>
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
