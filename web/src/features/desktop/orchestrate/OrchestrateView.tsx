import { useEffect, useState } from 'react'
import {
  Activity,
  ArrowRight,
  Bot,
  Brain,
  CheckCircle2,
  Eye,
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
  Zap,
} from 'lucide-react'
import {
  MOCK_AUTOMATIONS,
  MOCK_CHAT_MESSAGES,
  MOCK_DELIVERABLES,
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

  const [automations, setAutomations] = useState<RunningAutomation[]>(MOCK_AUTOMATIONS)
  const [deliverables, setDeliverables] = useState<MediaDeliverable[]>(MOCK_DELIVERABLES)
  const [mediaFilter, setMediaFilter] = useState<'all' | 'video' | 'code' | 'report'>('all')

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

  const filteredDeliverables = deliverables.filter((d) => {
    if (mediaFilter === 'all') return true
    return d.type === mediaFilter
  })

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

    // Simulate intelligent orchestrator response
    setTimeout(() => {
      let replyText = `Understood. Analyzing project context for "${selectedProject.name}"...`
      const newDelivs: MediaDeliverable[] = []

      if (text.toLowerCase().includes('video')) {
        replyText = `Dispatched to Video Swarm Worker. Generating requested social media variations with localized project themes. I've added the new job to your top automations bar and deliverable canvas.`

        const newVideo: MediaDeliverable = {
          id: `deliv-vid-${Date.now()}`,
          title: `Project Launch Promo (Variation ${deliverables.length + 1})`,
          type: 'video',
          videoAspect: '9:16',
          duration: '0:15',
          status: 'ready',
          createdAt: 'Just now',
          author: 'Video Swarm Worker',
          prompt:
            'Dynamic split-second sequence of high-tech interface components aligning with kinetic typography and warm peach studio lighting',
          metrics: { renderTime: '24s', tokens: '980', views: 'Preview ready' },
        }
        newDelivs.push(newVideo)
        setDeliverables((prev) => [newVideo, ...prev])

        setAutomations((prev) => [
          {
            id: `auto-video-${Date.now()}`,
            name: `Social Promo Batch #${prev.length + 1}`,
            kind: 'trigger',
            status: 'running',
            progressPercent: 30,
            currentStep: 'Composing audio soundtrack & subtitles',
            totalRuns: 1,
          },
          ...prev,
        ])
      } else if (text.toLowerCase().includes('test') || text.toLowerCase().includes('testbench')) {
        replyText = `Triggered isolated systemd-nspawn testbench lease on slot-1 for "${selectedProject.repoPath}". Verifying pre-push hermetic invariants.`
        setAutomations((prev) => [
          {
            id: `auto-test-${Date.now()}`,
            name: `Testbench Verification (slot-1)`,
            kind: 'trigger',
            status: 'running',
            progressPercent: 45,
            currentStep: 'Executing hermetic critical suites',
            totalRuns: 1,
          },
          ...prev,
        ])
      } else if (text.toLowerCase().includes('token') || text.toLowerCase().includes('memory')) {
        replyText = `Project memory audit for ${selectedProject.name}:\n• Lean project memory: 420 tokens\n• AGENTS.md bypass active: Saved ~14,200 tokens from top orchestrator prompt.\n• Delegated subagents receive repo-specific rules only upon worktree launch.`
      } else {
        replyText = `I have updated the project roadmap. The task is registered and being tracked directly in your project canvas.`
      }

      const botMsg: OrchestratorMessage = {
        id: `msg-reply-${Date.now()}`,
        sender: 'orchestrator',
        text: replyText,
        timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        linkedDeliverableIds: newDelivs.map((d) => d.id),
      }

      setMessages((prev) => [...prev, botMsg])
      setIsTyping(false)
    }, 700)
  }

  const handleAcceptDeliverable = (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    setDeliverables((prev) =>
      prev.map((d) => (d.id === id ? { ...d, status: 'accepted' } : d))
    )
  }

  return (
    <div
      className={`relative flex h-screen w-screen flex-col overflow-hidden ${theme.bgClass} ${theme.textPrimaryClass} font-sans`}
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
          2. TOP CONTROL BAR: APPLE-STYLE FLOATING CAPSULE HEADER
         ───────────────────────────────────────────────────────────── */}
      <header className="relative z-30 mx-4 mt-3 mb-2 flex flex-shrink-0 items-center justify-between rounded-2xl border px-4 py-2.5 backdrop-blur-2xl bg-white/[0.05] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.12),0_8px_32px_rgba(0,0,0,0.4)]">
        {/* Left: Brand Identity & Active Project */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2.5">
            <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-gradient-to-tr from-[#ff7a50] to-[#ffa387] text-white shadow-[0_2px_12px_rgba(255,122,80,0.4)]">
              <Sparkles size={16} />
            </div>

            <div className="flex flex-col">
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold tracking-tight text-white/95">
                  Orchestrate
                </span>
                <span className="rounded-full bg-[#ff8c69]/15 border border-[#ff8c69]/30 px-2 py-0.5 text-[9px] font-medium text-[#ffa387]">
                  Project Hub
                </span>
              </div>
              <span className="text-[10px] text-neutral-400">
                {theme.subtitle}
              </span>
            </div>
          </div>

          <div className="mx-1 h-4 w-[1px] bg-white/10" />

          {/* Active Project Breadcrumb Capsule */}
          <div className="flex items-center gap-2 rounded-full bg-white/[0.05] border border-white/[0.08] px-3 py-1 text-xs text-neutral-200">
            <FolderGit2 size={13} className="text-[#ff8c69]" />
            <span className="font-medium">{selectedProject.name}</span>
            <span className="text-[10px] text-neutral-400">
              ({selectedProject.branch})
            </span>
          </div>

          {/* Engine Active Indicator */}
          <div className="hidden md:flex items-center gap-1.5 rounded-full bg-[#ff8c69]/10 border border-[#ff8c69]/20 px-2.5 py-1 text-xs text-[#ffb299]">
            <span className="h-1.5 w-1.5 rounded-full bg-[#ff8c69] animate-pulse" />
            <span className="text-[11px] font-medium">Live Engine Active</span>
          </div>
        </div>

        {/* Right: 5 Apple Dark Warm Peach Themes Switcher & Exit */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-1.5">
            {/* Apple Segmented Control for 5 Themes */}
            <div className="flex items-center gap-1 rounded-full bg-black/40 backdrop-blur-2xl border border-white/[0.08] p-1 shadow-inner">
              {ORCHESTRATE_THEME_IDS.map((tId) => {
                const t = ORCHESTRATE_THEMES[tId]
                const isActive = tId === currentThemeId
                return (
                  <button
                    key={tId}
                    onClick={() => setCurrentThemeId(tId)}
                    className={`relative flex items-center gap-1.5 px-3 py-1 text-xs transition-all ${
                      isActive
                        ? 'rounded-full bg-white/[0.14] text-white font-medium shadow-[0_2px_8px_rgba(0,0,0,0.35),inset_0_1px_0_rgba(255,255,255,0.18)] border border-white/20'
                        : 'rounded-full text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.04]'
                    }`}
                    title={t.subtitle}
                  >
                    <span
                      className={`h-1.5 w-1.5 rounded-full transition-all ${
                        isActive ? 'scale-125 shadow-[0_0_8px_currentColor]' : 'opacity-60'
                      }`}
                      style={{ backgroundColor: t.accentColor }}
                    />
                    <span>{t.name}</span>
                  </button>
                )
              })}
            </div>
          </div>

          {onNavigateHome && (
            <button
              onClick={onNavigateHome}
              className="rounded-full px-3 py-1 text-xs text-neutral-400 hover:bg-white/10 hover:text-neutral-200 transition-colors"
            >
              Exit Mockup
            </button>
          )}
        </div>
      </header>

      {/* ─────────────────────────────────────────────────────────────
          3. MAIN 3-PANEL LAYOUT (APPLE MINIMAL FROSTED GLASS)
         ───────────────────────────────────────────────────────────── */}
      <div className="flex flex-1 overflow-hidden p-4 pt-1 gap-4">
        {/* =========================================================
            PANEL 1: LEFT SIDEBAR (PROJECTS & CONTEXT HUD)
           ========================================================= */}
        <aside className="relative flex w-72 flex-shrink-0 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.035] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
          {/* Projects Switcher Section */}
          <div className="p-3.5 border-b border-white/[0.07]">
            <div className="flex items-center justify-between pb-2.5">
              <span className="text-[11px] font-semibold uppercase tracking-wider text-neutral-400 flex items-center gap-1.5">
                Projects
              </span>
              <button className="flex items-center gap-1 px-2.5 py-0.5 text-[10px] font-medium rounded-full bg-white/[0.08] text-white hover:bg-white/[0.15] backdrop-blur-md transition-all">
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
                    className={`group flex items-center justify-between p-2.5 text-left rounded-2xl transition-all ${
                      isSelected
                        ? 'bg-white/[0.1] border border-white/20 shadow-[0_4px_16px_rgba(0,0,0,0.25)] text-white'
                        : 'border border-transparent hover:bg-white/[0.04] text-neutral-400 hover:text-neutral-200'
                    }`}
                  >
                    <div className="flex items-center gap-2.5 min-w-0">
                      <div
                        className={`flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-xl transition-all ${
                          isSelected
                            ? 'bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30'
                            : 'bg-white/[0.05] text-neutral-400'
                        }`}
                      >
                        <FolderGit2 size={14} />
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
          <div className="flex-1 space-y-3 overflow-y-auto p-3.5 text-xs">
            {/* Git & Worktree Status Block */}
            <div className="p-3 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
              <div className="flex items-center justify-between pb-2 border-b border-white/[0.06]">
                <span className="flex items-center gap-1.5 font-medium text-[11px] text-neutral-200">
                  <GitBranch size={13} className="text-[#ff8c69]" />
                  Repository Status
                </span>
                <span className="flex items-center gap-1 text-[10px] text-emerald-400 font-medium">
                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                  Clean
                </span>
              </div>
              <div className="mt-2 space-y-1.5 text-[11px] text-neutral-400">
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
            <div className="p-3 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
              <div className="flex items-center justify-between pb-2 border-b border-white/[0.06]">
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
                    Pebble V2 · {24 + (telemetryTick % 5)}ms
                  </span>
                </div>
              </div>
              <div className="mt-2 space-y-1.5 text-[11px]">
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
                  <span className="text-neutral-400">Deliverables Ready:</span>
                  <span className="rounded-full bg-[#ff8c69]/15 text-[#ffb299] px-2 py-0.5 text-[10px] font-medium">
                    {deliverables.filter((d) => d.status === 'ready').length} pending
                  </span>
                </div>
              </div>
            </div>

            {/* Lean Memory (projects.md) HUD */}
            <div className="p-3 rounded-2xl border border-white/[0.08] bg-white/[0.03] backdrop-blur-xl shadow-sm">
              <div className="flex items-center justify-between pb-2 border-b border-white/[0.06]">
                <span className="flex items-center gap-1.5 font-medium text-[11px] text-neutral-200">
                  <Brain size={13} className="text-[#ff8c69]" />
                  Project Memory
                </span>
                <span className="rounded-full bg-emerald-950/80 px-2 py-0.2 text-[9px] font-medium text-emerald-400 border border-emerald-800/60">
                  LEAN
                </span>
              </div>
              <p className="mt-2 text-[10px] leading-relaxed text-neutral-400">
                <strong>Zero AGENTS.md Bloat:</strong> The primary orchestrator reads only this
                project’s lean memory (420 tokens). Repositories retain full rules loaded only by
                isolated subagents upon worktree dispatch.
              </p>
              <div className="mt-2.5 p-2 font-mono text-[10px] text-neutral-300 rounded-xl bg-black/40 border border-white/[0.06]">
                # {selectedProject.name}
                <br />
                - Go daemon + Vite desktop
                <br />- Subagents isolated in worktrees
              </div>
            </div>
          </div>
        </aside>

        {/* =========================================================
            PANEL 2: MIDDLE SECTION (PROJECT CANVAS & DELIVERABLES)
           ========================================================= */}
        <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.03] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
          {/* Top: Running Automations & Live Progress Bar */}
          <div className="p-4 border-b border-white/[0.07] bg-white/[0.02] flex flex-col gap-3 flex-shrink-0">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Zap size={15} className="text-[#ff8c69]" />
                <span className="text-xs font-semibold uppercase tracking-wider text-neutral-200">
                  Active Automations & Workers
                </span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => handleSendMessage('make 5 social media videos')}
                  className="flex items-center gap-1.5 rounded-full bg-gradient-to-r from-[#ff7a50] to-[#ff9472] px-3.5 py-1 text-xs font-medium text-white shadow-[0_2px_10px_rgba(255,122,80,0.35)] hover:brightness-105 active:scale-95 transition-all"
                >
                  <Film size={12} /> + Trigger Video Swarm
                </button>
                <button
                  onClick={() => handleSendMessage('run full testbench')}
                  className="flex items-center gap-1.5 rounded-full bg-white/[0.08] hover:bg-white/[0.14] border border-white/[0.1] px-3.5 py-1 text-xs font-medium text-white backdrop-blur-md active:scale-95 transition-all"
                >
                  <Terminal size={12} /> + Run Testbench
                </button>
              </div>
            </div>

            {/* Automation Status Cards Grid */}
            <div className="grid grid-cols-4 gap-2.5">
              {automations.map((auto) => (
                <div
                  key={auto.id}
                  className="p-2.5 text-xs rounded-2xl border border-white/[0.08] bg-white/[0.035] backdrop-blur-xl shadow-sm hover:bg-white/[0.06] transition-all"
                >
                  <div className="flex items-center justify-between pb-1">
                    <span className="font-medium text-neutral-200 truncate max-w-[130px]">{auto.name}</span>
                    <span
                      className={`text-[9px] uppercase px-2 py-0.5 font-medium rounded-full ${
                        auto.status === 'running'
                          ? 'bg-[#ff8c69]/15 text-[#ffa387] border border-[#ff8c69]/30'
                          : auto.status === 'scheduled'
                          ? 'bg-amber-950/60 text-amber-300 border border-amber-800/50'
                          : 'bg-white/[0.06] text-neutral-400'
                      }`}
                    >
                      {auto.status}
                    </span>
                  </div>

                  {auto.status === 'running' && auto.progressPercent !== undefined ? (
                    <div className="mt-1.5 space-y-1">
                      <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/[0.08]">
                        <div
                          className="h-full bg-gradient-to-r from-[#ff7a50] to-[#ffa387] rounded-full transition-all duration-500 shadow-[0_0_8px_rgba(255,140,105,0.5)]"
                          style={{ width: `${auto.progressPercent}%` }}
                        />
                      </div>
                      <span className="text-[10px] block truncate text-neutral-400">
                        {auto.currentStep}
                      </span>
                    </div>
                  ) : (
                    <p className="text-[10px] truncate mt-1 text-neutral-400">
                      {auto.nextRun || auto.outputSummary}
                    </p>
                  )}
                </div>
              ))}
            </div>
          </div>

          {/* Deliverables & Outputs Canvas (The New Mailbox) */}
          <div className="flex-1 overflow-y-auto p-4 space-y-4">
            {/* Filter Tabs & Stats Bar */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-1 rounded-full bg-white/[0.04] border border-white/[0.08] p-1">
                {(['all', 'video', 'code', 'report'] as const).map((filter) => (
                  <button
                    key={filter}
                    onClick={() => setMediaFilter(filter)}
                    className={`px-3 py-1 text-xs font-medium capitalize rounded-full transition-all ${
                      mediaFilter === filter
                        ? 'bg-white text-neutral-900 shadow-md'
                        : 'text-neutral-400 hover:text-neutral-200'
                    }`}
                  >
                    {filter === 'all' ? 'All Deliverables' : `${filter}s`}
                  </button>
                ))}
              </div>

              <div className="flex items-center gap-3 text-xs">
                <span className="text-neutral-400">
                  Showing <strong className="text-neutral-200">{filteredDeliverables.length}</strong> items generated by project
                  workers
                </span>
              </div>
            </div>

            {/* Media & Deliverables Grid */}
            <div className="grid grid-cols-3 gap-4">
              {filteredDeliverables.map((item) => (
                <div
                  key={item.id}
                  onClick={() => item.type === 'video' && setActiveVideoPreview(item)}
                  className="group relative cursor-pointer flex flex-col p-3.5 rounded-2xl border border-white/[0.08] bg-white/[0.035] backdrop-blur-xl shadow-[0_6px_24px_rgba(0,0,0,0.25)] hover:bg-white/[0.065] hover:border-[#ff8c69]/30 hover:scale-[1.008] transition-all duration-300"
                >
                  {/* Top Bar of Deliverable */}
                  <div className="flex items-center justify-between pb-2">
                    <div className="flex items-center gap-1.5">
                      {item.type === 'video' ? (
                        <Film size={14} className="text-[#ff8c69]" />
                      ) : item.type === 'code' ? (
                        <FileCode size={14} className="text-emerald-400" />
                      ) : (
                        <FileCheck size={14} className="text-amber-400" />
                      )}
                      <span className="text-[10px] font-mono text-neutral-400">{item.author}</span>
                    </div>

                    <span
                      className={`text-[9px] uppercase px-2 py-0.5 font-medium rounded-full ${
                        item.status === 'ready'
                          ? 'bg-emerald-950/70 text-emerald-300 border border-emerald-800/60'
                          : item.status === 'generating'
                          ? 'bg-[#ff8c69]/15 text-[#ffa387] border border-[#ff8c69]/30 animate-pulse'
                          : 'bg-white/[0.06] text-neutral-400'
                      }`}
                    >
                      {item.status}
                    </span>
                  </div>

                  {/* Thumbnail / Visual Box */}
                  {item.type === 'video' && (
                    <div className="relative mb-2.5 aspect-video w-full overflow-hidden flex items-center justify-center rounded-xl border border-white/[0.07] bg-black/60 shadow-inner">
                      {/* Ambient Warm Glow Backdrop */}
                      <div className="absolute inset-0 bg-gradient-to-tr from-[#ff7a50]/20 via-[#ffa387]/10 to-transparent opacity-60 transition-opacity group-hover:opacity-90" />

                      <div className="relative z-10 flex flex-col items-center gap-1.5">
                        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-white/95 text-neutral-900 shadow-xl backdrop-blur-md transition-transform group-hover:scale-110">
                          <Play size={16} fill="currentColor" />
                        </div>
                        <span className="text-[10px] font-mono text-neutral-300">
                          {item.duration || '0:15'} • {item.videoAspect || '9:16'}
                        </span>
                      </div>

                      <span className="absolute bottom-1.5 right-2 rounded-md bg-black/80 px-1.5 py-0.5 font-mono text-[9px] text-neutral-300">
                        1080p
                      </span>
                    </div>
                  )}

                  {/* Title & Prompt */}
                  <h4 className="text-xs font-semibold text-neutral-100 line-clamp-1">{item.title}</h4>
                  <p className="mt-1 text-[11px] line-clamp-2 text-neutral-400 leading-relaxed">
                    {item.prompt}
                  </p>

                  {/* Footer & Actions */}
                  <div className="mt-3 flex items-center justify-between border-t border-white/[0.06] pt-2 text-[10px]">
                    <span className="text-neutral-500">{item.createdAt}</span>

                    <div className="flex items-center gap-1.5">
                      {item.status === 'ready' && (
                        <button
                          onClick={(e) => handleAcceptDeliverable(item.id, e)}
                          className="flex items-center gap-1 rounded-full bg-emerald-500/20 hover:bg-emerald-500/35 text-emerald-300 border border-emerald-500/30 px-3 py-1 font-medium backdrop-blur-md transition-all active:scale-95"
                        >
                          <CheckCircle2 size={11} /> Accept
                        </button>
                      )}
                      <button
                        className="rounded-full bg-white/[0.08] hover:bg-white/[0.16] p-1.5 text-neutral-300 transition-colors"
                        title="Expand Preview"
                      >
                        <Eye size={12} />
                      </button>
                    </div>
                  </div>
                </div>
              ))}
            </div>

            {/* Running Subagent Tasks for Review (Session Abstraction) */}
            <div className="mt-6 border-t pt-4 border-white/[0.08]">
              <div className="flex items-center justify-between pb-3">
                <span className="text-xs font-semibold uppercase tracking-wider text-neutral-300 flex items-center gap-1.5">
                  <Layers size={14} className="text-[#ff8c69]" />
                  Active Project Tasks & Subagent Reviews
                </span>
                <span className="text-[11px] text-neutral-500">
                  Sessions are encapsulated inside tasks
                </span>
              </div>

              <div className="space-y-2.5">
                {MOCK_RUNNING_TASKS.map((task) => (
                  <div
                    key={task.id}
                    className="p-3 rounded-2xl border border-white/[0.08] bg-white/[0.035] backdrop-blur-xl shadow-sm"
                  >
                    <div className="flex items-center justify-between pb-2">
                      <div className="flex items-center gap-2">
                        <span
                          className={`px-2 py-0.5 text-[9px] uppercase font-medium rounded-full ${
                            task.agentType === 'coder'
                              ? 'bg-[#ff8c69]/15 text-[#ffa387] border border-[#ff8c69]/30'
                              : 'bg-purple-950/60 text-purple-300 border border-purple-800/50'
                          }`}
                        >
                          {task.agentType}
                        </span>
                        <h4 className="text-xs font-semibold text-neutral-200">{task.title}</h4>
                      </div>

                      <div className="flex items-center gap-2">
                        <span className="text-[10px] font-mono text-neutral-400">
                          {task.elapsed}
                        </span>
                        <span
                          className={`text-[9px] uppercase font-medium px-2 py-0.5 rounded-full ${
                            task.status === 'running'
                              ? 'bg-emerald-950/70 text-emerald-300 border border-emerald-800/60'
                              : 'bg-amber-950/70 text-amber-300 border border-amber-800/60'
                          }`}
                        >
                          {task.status}
                        </span>
                      </div>
                    </div>

                    {/* Subtasks Progress */}
                    <div className="grid grid-cols-2 gap-2 text-[11px]">
                      {task.subtasks.map((st) => (
                        <div key={st.id} className="flex items-center gap-1.5">
                          <CheckCircle2
                            size={12}
                            className={st.completed ? 'text-emerald-400' : 'text-neutral-600'}
                          />
                          <span
                            className={
                              st.completed
                                ? 'text-neutral-500 line-through'
                                : 'text-neutral-300'
                            }
                          >
                            {st.title}
                          </span>
                        </div>
                      ))}
                    </div>

                    {task.diffPreview && (
                      <div className="mt-2 p-2.5 font-mono text-[10px] text-emerald-400 rounded-xl bg-black/50 border border-emerald-950/80">
                        <pre>{task.diffPreview}</pre>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          </div>
        </main>

        {/* =========================================================
            PANEL 3: RIGHT SIDEBAR (SWARM ORCHESTRATOR AI CHAT)
           ========================================================= */}
        <aside className="relative flex w-80 flex-shrink-0 flex-col overflow-hidden rounded-3xl border backdrop-blur-2xl bg-white/[0.035] border-white/[0.09] shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_18px_40px_rgba(0,0,0,0.55)]">
          {/* Header */}
          <div className="p-3.5 border-b border-white/[0.07]">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-[#ff8c69]/20 text-[#ffa387] border border-[#ff8c69]/30">
                  <Bot size={15} />
                </div>
                <div className="flex flex-col">
                  <span className="text-xs font-semibold text-neutral-100">
                    Swarm Orchestrator
                  </span>
                  <span className="text-[10px] text-emerald-400 flex items-center gap-1">
                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                    Bound to {selectedProject.name}
                  </span>
                </div>
              </div>

              <span className="rounded-full bg-white/[0.06] border border-white/[0.08] px-2 py-0.5 text-[9px] text-neutral-400">
                Lean
              </span>
            </div>

            {/* Quick Action Pills */}
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <button
                onClick={() => handleSendMessage('Make 5 social media videos')}
                className="rounded-full bg-white/[0.06] hover:bg-white/[0.12] border border-white/[0.08] px-2.5 py-1 text-[10px] text-neutral-300 hover:text-white transition-all active:scale-95"
              >
                🎥 Make 5 videos
              </button>
              <button
                onClick={() => handleSendMessage('Run testbench on dev branch')}
                className="rounded-full bg-white/[0.06] hover:bg-white/[0.12] border border-white/[0.08] px-2.5 py-1 text-[10px] text-neutral-300 hover:text-white transition-all active:scale-95"
              >
                🧪 Run tests
              </button>
              <button
                onClick={() => handleSendMessage('Audit project token velocity')}
                className="rounded-full bg-white/[0.06] hover:bg-white/[0.12] border border-white/[0.08] px-2.5 py-1 text-[10px] text-neutral-300 hover:text-white transition-all active:scale-95"
              >
                ⚡ Token audit
              </button>
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
                        ? 'rounded-2xl rounded-br-sm bg-gradient-to-r from-[#ff7a50] to-[#ff9472] text-white shadow-md'
                        : 'rounded-2xl rounded-bl-sm bg-white/[0.06] backdrop-blur-xl border border-white/[0.08] text-neutral-200'
                    }`}
                  >
                    <p className="whitespace-pre-line">{msg.text}</p>

                    {/* Action Pills inside AI response */}
                    {msg.actionPills && msg.actionPills.length > 0 && (
                      <div className="mt-2 flex flex-col gap-1 border-t border-white/10 pt-1.5">
                        {msg.actionPills.map((pill, idx) => (
                          <button
                            key={idx}
                            onClick={() => {
                              const target = deliverables.find((d) => d.id === 'deliv-vid-1')
                              if (target) setActiveVideoPreview(target)
                            }}
                            className="flex items-center justify-between rounded-full bg-white/10 hover:bg-white/20 px-2.5 py-1 text-[10px] text-[#ffb299] transition-colors"
                          >
                            <span>{pill.label}</span>
                            <ArrowRight size={10} />
                          </button>
                        ))}
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
      </div>

      {/* ─────────────────────────────────────────────────────────────
          4. MODAL: VIDEO DELIVERABLE PREVIEW DIALOG (APPLE GLASS SHEET)
         ───────────────────────────────────────────────────────────── */}
      {activeVideoPreview && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-6 backdrop-blur-xl">
          <div className="relative flex max-w-2xl w-full flex-col p-5 rounded-3xl border border-white/20 bg-[#14110f]/95 backdrop-blur-3xl shadow-[0_20px_60px_rgba(0,0,0,0.85)]">
            <div className="flex items-center justify-between pb-3 border-b border-white/10">
              <div className="flex items-center gap-2">
                <Film size={16} className="text-[#ff8c69]" />
                <h3 className="text-sm font-semibold text-neutral-100">{activeVideoPreview.title}</h3>
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
                  ✕
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
                <span className="text-xs font-mono text-neutral-200">
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
                <span className="text-neutral-400">Worker Prompt: </span>
                <span className="text-neutral-200">{activeVideoPreview.prompt}</span>
              </div>
              <div className="flex items-center gap-4 text-neutral-400 font-mono text-[11px]">
                <span>Duration: {activeVideoPreview.duration}</span>
                <span>Aspect: {activeVideoPreview.videoAspect}</span>
                <span>Render Time: {activeVideoPreview.metrics?.renderTime}</span>
              </div>
            </div>

            <div className="mt-4 flex items-center justify-end gap-2 border-t border-white/10 pt-3">
              <button
                onClick={() => {
                  handleAcceptDeliverable(
                    activeVideoPreview.id,
                    { stopPropagation: () => {} } as any
                  )
                  setActiveVideoPreview(null)
                }}
                className="flex items-center gap-1.5 px-4 py-1.5 text-xs font-medium text-white rounded-full bg-emerald-600 hover:bg-emerald-500 shadow-lg active:scale-95 transition-all"
              >
                <CheckCircle2 size={13} /> Accept Deliverable
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
