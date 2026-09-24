import { useEffect, useState } from 'react'
import {
  Activity,
  ArrowRight,
  Bot,
  Brain,
  CheckCircle2,
  Cpu,
  Eye,
  FileCheck,
  FileCode,
  Film,
  FolderGit2,
  GitBranch,
  Layers,
  Maximize2,
  Mic,
  Pause,
  Play,
  Plus,
  Radio,
  RefreshCw,
  Send,
  Shield,
  Sliders,
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
import { ORCHESTRATE_THEMES, PRIMARY_THEME_IDS } from './orchestrate-themes'
import {
  MediaDeliverable,
  OrchestrateThemeId,
  OrchestratorMessage,
  ProjectSummary,
  RunningAutomation,
} from './orchestrate-types'

// ─────────────────────────────────────────────────────────────────────────────
// STYLE-SPECIFIC DECORATIVE SUB-COMPONENTS
// ─────────────────────────────────────────────────────────────────────────────

/** Corner Crosshairs for Swarm Tactical engineering layout */
function TacticalCornerCrosshairs({ color = '#f59e0b' }: { color?: string }) {
  return (
    <>
      <span
        className="pointer-events-none absolute -top-1.5 -left-1.5 font-mono text-[10px] select-none leading-none opacity-60"
        style={{ color }}
      >
        +
      </span>
      <span
        className="pointer-events-none absolute -top-1.5 -right-1.5 font-mono text-[10px] select-none leading-none opacity-60"
        style={{ color }}
      >
        +
      </span>
      <span
        className="pointer-events-none absolute -bottom-1.5 -left-1.5 font-mono text-[10px] select-none leading-none opacity-60"
        style={{ color }}
      >
        +
      </span>
      <span
        className="pointer-events-none absolute -bottom-1.5 -right-1.5 font-mono text-[10px] select-none leading-none opacity-60"
        style={{ color }}
      >
        +
      </span>
    </>
  )
}

/** Rackmount metallic chassis screw for Studio Synth */
function StudioChassisScrew({ className = '' }: { className?: string }) {
  return (
    <div
      className={`pointer-events-none relative flex h-3 w-3 items-center justify-center rounded-full bg-gradient-to-br from-slate-400 via-slate-600 to-slate-800 shadow-[inset_0_1px_1px_rgba(255,255,255,0.4),0_1px_2px_rgba(0,0,0,0.8)] border border-slate-700/80 ${className}`}
    >
      <div className="h-1.5 w-[1px] bg-slate-900/90 rotate-45" />
      <div className="h-1.5 w-[1px] bg-slate-900/90 -rotate-45 absolute" />
    </div>
  )
}

/** Realistic Multi-Segment LED VU Meter for Studio Synth */
function StudioVuMeter({ active = true, level = 7 }: { active?: boolean; level?: number }) {
  const segments = [
    { color: 'bg-emerald-500', glow: 'shadow-[0_0_6px_rgba(16,185,129,0.8)]' },
    { color: 'bg-emerald-500', glow: 'shadow-[0_0_6px_rgba(16,185,129,0.8)]' },
    { color: 'bg-emerald-500', glow: 'shadow-[0_0_6px_rgba(16,185,129,0.8)]' },
    { color: 'bg-emerald-500', glow: 'shadow-[0_0_6px_rgba(16,185,129,0.8)]' },
    { color: 'bg-emerald-500', glow: 'shadow-[0_0_6px_rgba(16,185,129,0.8)]' },
    { color: 'bg-amber-400', glow: 'shadow-[0_0_6px_rgba(251,191,36,0.8)]' },
    { color: 'bg-amber-400', glow: 'shadow-[0_0_6px_rgba(251,191,36,0.8)]' },
    { color: 'bg-amber-400', glow: 'shadow-[0_0_6px_rgba(251,191,36,0.8)]' },
    { color: 'bg-red-500', glow: 'shadow-[0_0_6px_rgba(239,68,68,0.9)]' },
    { color: 'bg-red-600', glow: 'shadow-[0_0_8px_rgba(220,38,38,1)]' },
  ]

  return (
    <div className="flex items-center gap-[2px] rounded bg-black/60 p-1 border border-black/80 shadow-[inset_0_1px_2px_rgba(0,0,0,0.9)]">
      {segments.map((seg, i) => {
        const isLit = active && i <= level
        return (
          <div
            key={i}
            className={`h-2.5 w-1 rounded-[1px] transition-all duration-150 ${
              isLit ? `${seg.color} ${seg.glow} opacity-100` : 'bg-slate-800/40 opacity-30'
            }`}
          />
        )
      })}
    </div>
  )
}

/** Hazard diagonal warning stripe for Cyber Console */
function CyberHazardStripe() {
  return (
    <div
      className="h-1 w-full opacity-70"
      style={{
        backgroundImage:
          'repeating-linear-gradient(45deg, #00f3ff, #00f3ff 6px, #03060f 6px, #03060f 12px)',
      }}
    />
  )
}

// ─────────────────────────────────────────────────────────────────────────────
// MAIN ORCHESTRATE VIEW COMPONENT
// ─────────────────────────────────────────────────────────────────────────────

export interface OrchestrateViewProps {
  workspaceSlug?: string
  onNavigateHome?: () => void
  initialThemeId?: OrchestrateThemeId
}

export function OrchestrateView({
  workspaceSlug: _workspaceSlug,
  onNavigateHome,
  initialThemeId = 'apple_glass',
}: OrchestrateViewProps) {
  const [currentThemeId, setCurrentThemeId] = useState<OrchestrateThemeId>(initialThemeId)
  const theme = ORCHESTRATE_THEMES[currentThemeId] || ORCHESTRATE_THEMES.apple_glass

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

  // Live dynamic telemetry pulse simulation for Swarm Tactical & Studio Synth
  const [telemetryTick, setTelemetryTick] = useState(0)
  const [refreshingTelemetry, setRefreshingTelemetry] = useState(false)

  useEffect(() => {
    const interval = setInterval(() => {
      setTelemetryTick((prev) => (prev + 1) % 100)
    }, 1200)
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
        replyText = `Dispatched to Video Swarm Worker! Generating requested social media variations with localized project themes. I've added the new job to your top automations bar and deliverable canvas.`

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
            'Dynamic split-second sequence of high-tech interface components aligning with kinetic typography',
          metrics: { renderTime: '24s', tokens: '980', views: 'Preview ready' },
        }
        newDelivs.push(newVideo)
        setDeliverables((prev) => [newVideo, ...prev])

        // Add a running automation card to top bar
        setAutomations((prev) => [
          {
            id: `auto-video-${Date.now()}`,
            name: `Social Promo Batch #${prev.length + 1}`,
            kind: 'trigger',
            status: 'running',
            progressPercent: 25,
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
    }, 850)
  }

  const handleAcceptDeliverable = (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    setDeliverables((prev) =>
      prev.map((d) => (d.id === id ? { ...d, status: 'accepted' } : d))
    )
  }

  // ───────────────────────────────────────────────────────────────────────────
  // RENDER HELPERS BASED ON ARCHITECTURAL STYLE
  // ───────────────────────────────────────────────────────────────────────────

  const isApple = theme.id === 'apple_glass'
  const isSwarm = theme.id === 'swarm_tactical'
  const isCyber = theme.id === 'cyber_hud'
  const isSynth = theme.id === 'studio_synth'
  const isCraft = theme.id === 'minimalist_craft'

  return (
    <div
      className={`relative flex h-screen w-screen flex-col overflow-hidden ${theme.bgClass} ${theme.textPrimaryClass} ${
        isSwarm || isCyber ? 'font-mono' : 'font-sans'
      }`}
      style={theme.customVars as React.CSSProperties}
    >
      {/* ─────────────────────────────────────────────────────────────
          1. BACKGROUND ATMOSPHERE ACCORDING TO STYLE
         ───────────────────────────────────────────────────────────── */}
      {/* Apple Glass: Floating iridescent ambient orbs */}
      {isApple && (
        <div className="pointer-events-none absolute inset-0 overflow-hidden">
          <div className="absolute -top-32 -left-32 h-[34rem] w-[34rem] rounded-full bg-blue-600/20 blur-[120px] animate-pulse" />
          <div
            className="absolute top-1/3 -right-32 h-[36rem] w-[36rem] rounded-full bg-purple-600/15 blur-[140px]"
            style={{ animation: 'pulse 8s ease-in-out infinite' }}
          />
          <div className="absolute -bottom-32 left-1/3 h-[30rem] w-[30rem] rounded-full bg-indigo-600/15 blur-[120px]" />
        </div>
      )}

      {/* Swarm Tactical: Micro-dot telemetry matrix */}
      {isSwarm && (
        <div
          className="pointer-events-none absolute inset-0 opacity-40"
          style={{
            backgroundImage:
              'radial-gradient(rgba(245, 158, 11, 0.18) 1px, transparent 1px)',
            backgroundSize: '20px 20px',
          }}
        />
      )}

      {/* Cyber Console: Scanline CRT Overlay */}
      {isCyber && (
        <div
          className="pointer-events-none absolute inset-0 opacity-25 z-40"
          style={{
            backgroundImage:
              'repeating-linear-gradient(0deg, rgba(0, 243, 255, 0.08) 0px, rgba(0, 243, 255, 0.08) 1px, transparent 1px, transparent 3px)',
          }}
        />
      )}

      {/* Studio Synth: Analog warm vignette spotlight */}
      {isSynth && (
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top,_var(--tw-gradient-stops))] from-amber-950/20 via-transparent to-black/80" />
      )}

      {/* Minimalist Craft: Subtle Swiss 1px micro-cross grid marks */}
      {isCraft && (
        <div
          className="pointer-events-none absolute inset-0 opacity-20"
          style={{
            backgroundImage:
              'radial-gradient(rgba(255, 255, 255, 0.25) 1px, transparent 1px)',
            backgroundSize: '32px 32px',
          }}
        />
      )}

      {/* ─────────────────────────────────────────────────────────────
          2. TOP CONTROL BAR & 5-STYLE INTERACTIVE SWITCHER
         ───────────────────────────────────────────────────────────── */}
      <header
        className={`relative z-30 flex flex-shrink-0 items-center justify-between transition-all duration-300 ${
          isApple
            ? 'mx-4 mt-3 mb-2 rounded-2xl border px-4 py-2.5 backdrop-blur-2xl bg-white/[0.06] border-white/20 shadow-[inset_0_1px_1px_rgba(255,255,255,0.4),0_8px_32px_rgba(0,0,0,0.37)]'
            : isCyber
            ? 'border-b px-4 py-2 bg-[#080e22]/95 border-cyan-500/50 shadow-[0_0_20px_rgba(0,243,255,0.25)]'
            : isSynth
            ? 'border-b-2 px-5 py-2.5 bg-[#151921] border-[#293140] shadow-[inset_0_2px_4px_rgba(0,0,0,0.8),0_4px_12px_rgba(0,0,0,0.6)]'
            : isCraft
            ? 'border-b px-6 py-2.5 bg-[#12141a] border-white/[0.08]'
            : 'border-b px-4 py-2 bg-[#0e1117] border-amber-500/30'
        }`}
      >
        {isSynth && <StudioChassisScrew className="absolute top-2 left-2" />}
        {isSynth && <StudioChassisScrew className="absolute top-2 right-2" />}
        {isSwarm && <TacticalCornerCrosshairs color="#f59e0b" />}

        {/* Brand / Title Section */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <div
              className={`flex items-center justify-center transition-all ${
                isApple
                  ? 'h-8 w-8 rounded-xl bg-gradient-to-tr from-blue-500 to-indigo-500 text-white shadow-[0_2px_12px_rgba(10,132,255,0.5)]'
                  : isCyber
                  ? 'h-8 w-8 rounded-none border border-cyan-400 bg-cyan-950 text-cyan-300 shadow-[0_0_15px_rgba(0,243,255,0.5)]'
                  : isSynth
                  ? 'h-8 w-8 rounded-md border border-[#3b4457] bg-[#1d232e] text-amber-400 shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_2px_4px_rgba(0,0,0,0.6)]'
                  : isCraft
                  ? 'h-8 w-8 rounded-lg bg-white/[0.08] text-[#ff5500] border border-white/10'
                  : 'h-8 w-8 rounded-none border border-amber-500/60 bg-amber-950/60 text-amber-400'
              }`}
            >
              {isSynth ? <Sliders size={16} /> : isCyber ? <Zap size={16} /> : <Cpu size={16} />}
            </div>

            <div className="flex flex-col">
              <div className="flex items-center gap-2">
                <span
                  className={`font-black tracking-tight ${
                    isApple
                      ? 'text-sm font-semibold tracking-normal text-white'
                      : isCyber
                      ? 'text-xs uppercase tracking-widest text-cyan-400'
                      : isSynth
                      ? 'text-xs font-bold uppercase tracking-wider text-slate-200'
                      : isCraft
                      ? 'text-xs font-semibold tracking-tight text-zinc-100'
                      : 'text-xs uppercase font-mono tracking-wider text-amber-400'
                  }`}
                >
                  {isSwarm
                    ? '[SWARM.ORCHESTRATE]'
                    : isCyber
                    ? 'CYBER//ORCHESTRATE'
                    : isSynth
                    ? 'SWARM CONSOLE MK-V'
                    : isCraft
                    ? 'Swarm / Orchestrate'
                    : 'Swarm Orchestrate'}
                </span>
                <span
                  className={`text-[9px] px-1.5 py-0.2 uppercase font-mono ${
                    isApple
                      ? 'rounded-full bg-blue-500/20 text-blue-300 border border-blue-400/30'
                      : isCyber
                      ? 'bg-cyan-950 text-cyan-400 border border-cyan-500/50'
                      : isSynth
                      ? 'rounded bg-amber-950 text-amber-400 border border-amber-700/50'
                      : isCraft
                      ? 'rounded bg-white/[0.06] text-[#ff5500] font-semibold'
                      : 'bg-amber-950 text-amber-300 border border-amber-600/40'
                  }`}
                >
                  {theme.category}
                </span>
              </div>
              <span className={`text-[10px] ${theme.textSecondaryClass}`}>
                {theme.subtitle}
              </span>
            </div>
          </div>

          <div
            className={`mx-2 h-5 w-[1px] ${
              isApple ? 'bg-white/20' : isCyber ? 'bg-cyan-500/30' : 'bg-slate-700/60'
            }`}
          />

          {/* Active Project Breadcrumb */}
          <div
            className={`flex items-center gap-2 px-3 py-1 text-xs transition-all ${
              isApple
                ? 'rounded-full bg-white/[0.08] backdrop-blur-md border border-white/15'
                : isCyber
                ? 'rounded-none bg-cyan-950/70 border border-cyan-500/40 text-cyan-200'
                : isSynth
                ? 'rounded-md bg-[#11141a] border border-[#2b3342] shadow-inner text-amber-200'
                : isCraft
                ? 'rounded-lg bg-white/[0.04] border border-white/[0.08] text-zinc-300'
                : 'rounded-none bg-black/50 border border-amber-500/30 text-amber-300 font-mono'
            }`}
          >
            <FolderGit2
              size={13}
              className={
                isApple
                  ? 'text-blue-400'
                  : isCyber
                  ? 'text-cyan-400'
                  : isSynth
                  ? 'text-amber-400'
                  : isCraft
                  ? 'text-[#ff5500]'
                  : 'text-amber-400'
              }
            />
            <span className="font-semibold">{selectedProject.name}</span>
            <span className={`text-[10px] ${theme.textSecondaryClass}`}>
              ({selectedProject.branch})
            </span>
          </div>

          {/* Swarm Tactical / Studio Radio status */}
          {isSwarm && (
            <div className="flex items-center gap-1.5 px-2 py-0.5 border border-amber-500/40 text-amber-400 text-[10px] font-mono">
              <Radio size={11} className="animate-pulse" />
              <span>FREQ: 142.8MHz</span>
            </div>
          )}
        </div>

        {/* 5-STYLE INTERACTIVE SWITCHER */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-1.5">
            <span
              className={`text-[10px] font-bold uppercase tracking-wider ${
                isCyber
                  ? 'text-cyan-400'
                  : isSwarm
                  ? 'text-amber-400'
                  : isCraft
                  ? 'text-zinc-400'
                  : 'text-slate-400'
              }`}
            >
              {isCraft ? '00 / STYLE' : 'Style Variations:'}
            </span>

            {/* The 5-variant dock with distinctive visual treatments */}
            <div
              className={`flex items-center gap-1 p-1 transition-all ${
                isApple
                  ? 'rounded-full bg-white/[0.08] backdrop-blur-2xl border border-white/20 shadow-[inset_0_1px_1px_rgba(255,255,255,0.25)]'
                  : isCyber
                  ? 'rounded-none bg-[#0a1024] border border-cyan-500/60 shadow-[0_0_15px_rgba(0,243,255,0.3)]'
                  : isSynth
                  ? 'rounded-lg bg-[#0e1218] border border-[#2a3240] shadow-[inset_0_2px_4px_rgba(0,0,0,0.8)]'
                  : isCraft
                  ? 'rounded-xl bg-white/[0.03] border border-white/[0.08]'
                  : 'rounded-none bg-black/60 border border-amber-500/40 font-mono'
              }`}
            >
              {PRIMARY_THEME_IDS.map((tId, idx) => {
                const t = ORCHESTRATE_THEMES[tId]
                const isActive = tId === currentThemeId
                return (
                  <button
                    key={tId}
                    onClick={() => setCurrentThemeId(tId)}
                    className={`group relative flex items-center gap-1.5 px-3 py-1.5 text-xs transition-all ${
                      isApple
                        ? `rounded-full ${
                            isActive
                              ? 'bg-white text-slate-900 font-semibold shadow-[0_2px_12px_rgba(255,255,255,0.4)]'
                              : 'text-slate-300 hover:text-white hover:bg-white/10'
                          }`
                        : isCyber
                        ? `rounded-none uppercase font-bold text-[11px] ${
                            isActive
                              ? 'bg-gradient-to-r from-cyan-400 to-fuchsia-500 text-black shadow-[0_0_15px_rgba(0,243,255,0.7)]'
                              : 'text-cyan-300/70 hover:text-cyan-200 hover:bg-cyan-950/60'
                          }`
                        : isSynth
                        ? `rounded-md text-[11px] font-semibold ${
                            isActive
                              ? 'bg-gradient-to-b from-amber-400 to-amber-600 text-black shadow-[inset_0_1px_0_rgba(255,255,255,0.4),0_2px_6px_rgba(0,0,0,0.6)]'
                              : 'text-slate-300 hover:bg-white/5 hover:text-white'
                          }`
                        : isCraft
                        ? `rounded-lg text-[11px] font-medium ${
                            isActive
                              ? 'bg-white text-black font-semibold shadow-sm'
                              : 'text-zinc-400 hover:text-zinc-200 hover:bg-white/[0.06]'
                          }`
                        : `rounded-none text-[11px] font-mono ${
                            isActive
                              ? 'bg-amber-500 text-black font-bold shadow-[0_0_10px_rgba(245,158,11,0.5)]'
                              : 'text-amber-400/70 hover:text-amber-300 hover:bg-amber-950/40'
                          }`
                    }`}
                    title={t.subtitle}
                  >
                    {/* Unique Icon / Indicator per style */}
                    <span
                      className={`h-2 w-2 transition-all ${
                        isApple
                          ? 'rounded-full'
                          : isCyber
                          ? 'rounded-none rotate-45'
                          : isSynth
                          ? 'rounded-full shadow-[0_0_6px_currentColor]'
                          : isCraft
                          ? 'rounded-sm'
                          : 'rounded-none'
                      }`}
                      style={{ backgroundColor: t.accentColor }}
                    />
                    <span>
                      {isCraft && <span className="opacity-50 mr-1">0{idx + 1}</span>}
                      {t.name}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>

          {onNavigateHome && (
            <button
              onClick={onNavigateHome}
              className={`rounded px-2.5 py-1 text-xs text-slate-400 hover:bg-white/10 hover:text-slate-200 transition-colors ${
                isCraft ? 'rounded-lg' : isApple ? 'rounded-full' : ''
              }`}
            >
              Exit Mockup
            </button>
          )}
        </div>
      </header>

      {/* ─────────────────────────────────────────────────────────────
          3. MAIN 3-PANEL LAYOUT WITH DYNAMIC PERSONALITY
         ───────────────────────────────────────────────────────────── */}
      <div
        className={`flex flex-1 overflow-hidden transition-all duration-300 ${
          isApple ? 'p-4 pt-1 gap-4' : ''
        }`}
      >
        {/* =========================================================
            PANEL 1: LEFT SIDEBAR (PROJECTS & MISSION CONTROL HUD)
           ========================================================= */}
        <aside
          className={`relative flex w-72 flex-shrink-0 flex-col overflow-hidden transition-all duration-300 ${
            isApple
              ? 'rounded-3xl border backdrop-blur-2xl bg-white/[0.04] border-white/15 shadow-[inset_0_1px_1px_rgba(255,255,255,0.25),0_20px_45px_rgba(0,0,0,0.55)]'
              : isCyber
              ? 'border-r bg-[#070b19]/95 border-cyan-500/40 shadow-[inset_0_0_20px_rgba(0,243,255,0.05)]'
              : isSynth
              ? 'border-r-2 bg-[#141820] border-[#272f3d] shadow-[inset_0_2px_4px_rgba(0,0,0,0.7)]'
              : isCraft
              ? 'border-r bg-[#111319] border-white/[0.08]'
              : 'border-r bg-[#0d1017] border-amber-500/25'
          }`}
        >
          {isSynth && <StudioChassisScrew className="absolute top-2 left-2" />}
          {isSynth && <StudioChassisScrew className="absolute bottom-2 left-2" />}
          {isSwarm && <TacticalCornerCrosshairs color="#f59e0b" />}

          {/* Projects Switcher Section */}
          <div
            className={`p-3.5 border-b transition-colors ${
              isApple
                ? 'border-white/10'
                : isCyber
                ? 'border-cyan-500/30'
                : isSynth
                ? 'border-[#293240]'
                : isCraft
                ? 'border-white/[0.07]'
                : 'border-amber-500/25'
            }`}
          >
            <div className="flex items-center justify-between pb-2.5">
              <span
                className={`text-[11px] font-bold uppercase tracking-wider flex items-center gap-1.5 ${
                  isCyber
                    ? 'text-cyan-400'
                    : isSwarm
                    ? 'text-amber-400'
                    : isCraft
                    ? 'text-zinc-400'
                    : 'text-slate-300'
                }`}
              >
                {isCraft ? '01 / PROJECTS' : isSwarm ? '[01.PROJECTS]' : 'Projects'}
              </span>
              <button
                className={`flex items-center gap-1 px-2 py-0.5 text-[10px] font-semibold transition-all ${
                  isApple
                    ? 'rounded-full bg-white/10 text-white hover:bg-white/20 backdrop-blur-md'
                    : isCyber
                    ? 'rounded-none bg-cyan-950 text-cyan-300 border border-cyan-400 hover:bg-cyan-900'
                    : isSynth
                    ? 'rounded-md bg-[#1f2633] text-amber-300 border border-[#3b475c] hover:bg-[#283244]'
                    : isCraft
                    ? 'rounded-lg bg-white/[0.06] text-zinc-200 hover:bg-white/10'
                    : 'rounded-none bg-amber-950/80 text-amber-300 border border-amber-600/50'
                }`}
              >
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
                    className={`group flex items-center justify-between p-2.5 text-left transition-all ${
                      isApple
                        ? `rounded-2xl ${
                            isSelected
                              ? 'bg-white/[0.12] border border-white/30 shadow-[0_4px_20px_rgba(0,0,0,0.3)]'
                              : 'hover:bg-white/[0.05] border border-transparent'
                          }`
                        : isCyber
                        ? `rounded-none border ${
                            isSelected
                              ? 'bg-cyan-950/80 border-cyan-400 shadow-[0_0_15px_rgba(0,243,255,0.4)]'
                              : 'border-cyan-900/40 hover:border-cyan-500/50 bg-[#091124]'
                          }`
                        : isSynth
                        ? `rounded-lg border ${
                            isSelected
                              ? 'bg-[#1e2533] border-amber-500/60 shadow-[inset_0_1px_1px_rgba(255,255,255,0.1),0_2px_6px_rgba(0,0,0,0.6)]'
                              : 'border-[#262f3f] bg-[#141a24] hover:bg-[#18202c]'
                          }`
                        : isCraft
                        ? `rounded-xl border ${
                            isSelected
                              ? 'bg-[#181c25] border-white/20 text-white'
                              : 'border-transparent hover:border-white/10 hover:bg-white/[0.02]'
                          }`
                        : `rounded-none border font-mono ${
                            isSelected
                              ? 'bg-[#161c28] border-amber-500 text-amber-300'
                              : 'border-slate-800 hover:border-amber-500/40 bg-black/40'
                          }`
                    }`}
                  >
                    <div className="flex items-center gap-2.5 min-w-0">
                      <div
                        className={`flex h-7 w-7 flex-shrink-0 items-center justify-center transition-all ${
                          isApple
                            ? 'rounded-xl bg-white/10 text-white'
                            : isCyber
                            ? 'rounded-none bg-cyan-900/60 text-cyan-300'
                            : isSynth
                            ? 'rounded-md bg-[#252e3d] text-amber-400'
                            : isCraft
                            ? 'rounded-lg bg-white/[0.06] text-zinc-300'
                            : 'rounded-none bg-slate-800 text-amber-400'
                        }`}
                      >
                        <FolderGit2 size={14} />
                      </div>
                      <div className="flex flex-col min-w-0">
                        <span className="text-xs font-semibold truncate">{proj.name}</span>
                        <span className={`text-[10px] truncate ${theme.textSecondaryClass}`}>
                          {proj.repoPath}
                        </span>
                      </div>
                    </div>
                    {isSelected && (
                      <span
                        className={`h-2 w-2 flex-shrink-0 ${
                          isApple
                            ? 'rounded-full bg-blue-400 shadow-[0_0_8px_rgba(96,165,250,0.8)]'
                            : isCyber
                            ? 'rounded-none rotate-45 bg-cyan-400 shadow-[0_0_8px_rgba(0,243,255,1)]'
                            : isSynth
                            ? 'rounded-full bg-amber-400 shadow-[0_0_8px_rgba(251,191,36,1)]'
                            : isCraft
                            ? 'rounded-full bg-[#ff5500]'
                            : 'rounded-none bg-amber-400'
                        }`}
                      />
                    )}
                  </button>
                )
              })}
            </div>
          </div>

          {/* Mission Control HUD for Selected Project */}
          <div className="flex-1 space-y-3.5 overflow-y-auto p-3.5 text-xs">
            {/* Git & Worktree Status Block */}
            <div
              className={`p-3 transition-all ${
                isApple
                  ? 'rounded-2xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-lg'
                  : isCyber
                  ? 'rounded-none border border-cyan-500/40 bg-[#091126] shadow-[0_0_15px_rgba(0,243,255,0.15)]'
                  : isSynth
                  ? 'rounded-lg border border-[#2b3444] bg-[#181f2a] shadow-[inset_0_1px_1px_rgba(255,255,255,0.05),0_2px_4px_rgba(0,0,0,0.6)]'
                  : isCraft
                  ? 'rounded-xl border border-white/[0.08] bg-[#161820]'
                  : 'rounded-none border border-amber-500/30 bg-[#121620] font-mono'
              }`}
            >
              <div className="flex items-center justify-between pb-2 border-b border-white/5">
                <span className="flex items-center gap-1.5 font-bold text-[11px]">
                  <GitBranch
                    size={13}
                    className={
                      isApple
                        ? 'text-blue-400'
                        : isCyber
                        ? 'text-cyan-400'
                        : isSynth
                        ? 'text-amber-400'
                        : isCraft
                        ? 'text-[#ff5500]'
                        : 'text-amber-400'
                    }
                  />
                  {isSwarm ? 'GIT.WORKTREE_HUD' : 'Repository HUD'}
                </span>
                <span className="flex items-center gap-1 text-[10px] text-emerald-400 font-semibold">
                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                  Clean
                </span>
              </div>
              <div className={`mt-2 space-y-1.5 text-[11px] ${theme.textSecondaryClass}`}>
                <div className="flex justify-between">
                  <span>Branch:</span>
                  <span className="font-mono text-slate-200">{selectedProject.branch}</span>
                </div>
                <div className="flex justify-between">
                  <span>Linked Repos:</span>
                  <span className="font-mono text-slate-200">
                    {selectedProject.linkedWorkspaces.length} roots
                  </span>
                </div>
                <div className="flex justify-between">
                  <span>Worktrees:</span>
                  <span className="font-mono text-slate-200">3 isolated</span>
                </div>
              </div>
            </div>

            {/* Automation Health & Live Telemetry HUD */}
            <div
              className={`p-3 transition-all ${
                isApple
                  ? 'rounded-2xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-lg'
                  : isCyber
                  ? 'rounded-none border border-cyan-500/40 bg-[#091126] shadow-[0_0_15px_rgba(0,243,255,0.15)]'
                  : isSynth
                  ? 'rounded-lg border border-[#2b3444] bg-[#181f2a] shadow-[inset_0_1px_1px_rgba(255,255,255,0.05),0_2px_4px_rgba(0,0,0,0.6)]'
                  : isCraft
                  ? 'rounded-xl border border-white/[0.08] bg-[#161820]'
                  : 'rounded-none border border-amber-500/30 bg-[#121620] font-mono'
              }`}
            >
              <div className="flex items-center justify-between pb-2 border-b border-white/5">
                <span className="flex items-center gap-1.5 font-bold text-[11px]">
                  <Activity
                    size={13}
                    className={
                      isApple
                        ? 'text-blue-400'
                        : isCyber
                        ? 'text-cyan-400'
                        : isSynth
                        ? 'text-amber-400'
                        : isCraft
                        ? 'text-[#ff5500]'
                        : 'text-amber-400'
                    }
                  />
                  Automation Health
                </span>
                <div className="flex items-center gap-1.5">
                  <button
                    onClick={handleRefreshTelemetry}
                    className="text-slate-400 hover:text-slate-200 transition-colors"
                    title="Refresh Telemetry"
                  >
                    <RefreshCw
                      size={11}
                      className={refreshingTelemetry ? 'animate-spin text-amber-400' : ''}
                    />
                  </button>
                  {isSynth ? (
                    <StudioVuMeter active level={telemetryTick % 10} />
                  ) : (
                    <span className="text-[10px] font-mono text-cyan-400">Pebble V2</span>
                  )}
                </div>
              </div>
              <div className="mt-2 space-y-1.5 text-[11px]">
                <div className="flex items-center justify-between">
                  <span className={theme.textSecondaryClass}>Active Workers:</span>
                  <span className="font-semibold text-emerald-400">
                    {automations.filter((a) => a.status === 'running').length} running
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className={theme.textSecondaryClass}>Next Scheduled:</span>
                  <span className="text-slate-300">02:00 UTC (3h)</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className={theme.textSecondaryClass}>Deliverables Ready:</span>
                  <span
                    className={`px-1.5 py-0.2 font-bold ${
                      isApple
                        ? 'rounded-full bg-blue-500/20 text-blue-300'
                        : isCyber
                        ? 'rounded-none bg-cyan-950 text-cyan-300 border border-cyan-500'
                        : isSynth
                        ? 'rounded bg-amber-950 text-amber-300 border border-amber-700'
                        : isCraft
                        ? 'rounded-md bg-[#ff5500]/20 text-[#ff5500]'
                        : 'rounded-none bg-amber-500/20 text-amber-300'
                    }`}
                  >
                    {deliverables.filter((d) => d.status === 'ready').length} pending
                  </span>
                </div>
                {isSwarm && (
                  <div className="pt-1.5 border-t border-slate-800 flex items-center justify-between text-[10px] text-amber-400/80">
                    <span className="flex items-center gap-1">
                      <Shield size={10} /> HERMETIC:
                    </span>
                    <span className="font-mono">SLOT-1 [ONLINE]</span>
                  </div>
                )}
              </div>
            </div>

            {/* Lean Memory (projects.md) HUD */}
            <div
              className={`p-3 transition-all ${
                isApple
                  ? 'rounded-2xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-lg'
                  : isCyber
                  ? 'rounded-none border border-cyan-500/40 bg-[#091126] shadow-[0_0_15px_rgba(0,243,255,0.15)]'
                  : isSynth
                  ? 'rounded-lg border border-[#2b3444] bg-[#181f2a] shadow-[inset_0_1px_1px_rgba(255,255,255,0.05),0_2px_4px_rgba(0,0,0,0.6)]'
                  : isCraft
                  ? 'rounded-xl border border-white/[0.08] bg-[#161820]'
                  : 'rounded-none border border-amber-500/30 bg-[#121620] font-mono'
              }`}
            >
              <div className="flex items-center justify-between pb-2 border-b border-white/5">
                <span className="flex items-center gap-1.5 font-bold text-[11px]">
                  <Brain
                    size={13}
                    className={
                      isApple
                        ? 'text-blue-400'
                        : isCyber
                        ? 'text-cyan-400'
                        : isSynth
                        ? 'text-amber-400'
                        : isCraft
                        ? 'text-[#ff5500]'
                        : 'text-amber-400'
                    }
                  />
                  Project Memory
                </span>
                <span className="rounded bg-emerald-950 px-1 text-[9px] font-mono text-emerald-400 border border-emerald-800">
                  LEAN
                </span>
              </div>
              <p className={`mt-2 text-[10px] leading-relaxed ${theme.textSecondaryClass}`}>
                <strong>Zero AGENTS.md Bloat:</strong> The primary orchestrator reads only this
                project’s lean memory (420 tokens). Repositories retain full rules loaded only by
                isolated subagents upon worktree dispatch.
              </p>
              <div
                className={`mt-2.5 p-2 font-mono text-[10px] text-slate-300 ${
                  isApple
                    ? 'rounded-xl bg-black/40 border border-white/10'
                    : isCyber
                    ? 'rounded-none bg-black/80 border border-cyan-500/30 text-cyan-300'
                    : isSynth
                    ? 'rounded bg-black/60 border border-[#262e3d]'
                    : isCraft
                    ? 'rounded-lg bg-black/40 border border-white/10'
                    : 'rounded-none bg-black/80 border border-amber-500/30'
                }`}
              >
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
        <main
          className={`relative flex flex-1 flex-col overflow-hidden transition-all duration-300 ${
            isApple
              ? 'rounded-3xl border backdrop-blur-2xl bg-white/[0.03] border-white/15 shadow-[inset_0_1px_1px_rgba(255,255,255,0.25),0_20px_45px_rgba(0,0,0,0.55)]'
              : isCyber
              ? 'bg-[#050814]/90'
              : isSynth
              ? 'bg-[#11141b]'
              : isCraft
              ? 'bg-[#0f1116]'
              : 'bg-black/40'
          }`}
        >
          {isCyber && <CyberHazardStripe />}
          {isSwarm && <TacticalCornerCrosshairs color="#f59e0b" />}

          {/* Top: Running Automations & Live Progress Bar */}
          <div
            className={`p-4 border-b flex flex-col gap-3 flex-shrink-0 transition-colors ${
              isApple
                ? 'border-white/10 bg-white/[0.02]'
                : isCyber
                ? 'border-cyan-500/30 bg-[#070d1e]'
                : isSynth
                ? 'border-[#293242] bg-[#161b24]'
                : isCraft
                ? 'border-white/[0.08] bg-[#14161d]'
                : 'border-amber-500/25 bg-[#0e1118]'
            }`}
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Zap
                  size={15}
                  className={
                    isApple
                      ? 'text-blue-400'
                      : isCyber
                      ? 'text-cyan-400'
                      : isSynth
                      ? 'text-amber-400'
                      : isCraft
                      ? 'text-[#ff5500]'
                      : 'text-amber-400'
                  }
                />
                <span
                  className={`text-xs font-bold uppercase tracking-wider ${
                    isCraft ? 'text-zinc-200' : ''
                  }`}
                >
                  {isCraft ? '02 / AUTOMATIONS & WORKERS' : 'Active Automations & Workers'}
                </span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => handleSendMessage('make 5 social media videos')}
                  className={`flex items-center gap-1.5 px-3 py-1 text-xs font-semibold transition-all ${
                    isApple
                      ? 'rounded-full bg-gradient-to-r from-blue-500 to-indigo-600 text-white shadow-[0_4px_16px_rgba(10,132,255,0.4)] hover:brightness-110'
                      : isCyber
                      ? 'rounded-none bg-gradient-to-r from-cyan-400 to-fuchsia-500 text-black font-black uppercase tracking-wider shadow-[0_0_15px_rgba(0,243,255,0.6)] hover:brightness-125'
                      : isSynth
                      ? 'rounded-md bg-gradient-to-b from-amber-400 to-amber-600 text-black font-bold shadow-[0_2px_8px_rgba(245,158,11,0.5)] hover:brightness-110 active:translate-y-0.5'
                      : isCraft
                      ? 'rounded-lg bg-[#ff5500] text-white hover:bg-[#ff661a] shadow-[0_2px_12px_rgba(255,85,0,0.35)]'
                      : 'rounded-none bg-amber-500 text-black font-mono font-bold hover:bg-amber-400'
                  }`}
                >
                  <Film size={12} /> + Trigger Video Swarm
                </button>
                <button
                  onClick={() => handleSendMessage('run full testbench')}
                  className={`flex items-center gap-1.5 px-3 py-1 text-xs font-semibold transition-all ${
                    isApple
                      ? 'rounded-full bg-white/10 text-white hover:bg-white/20 backdrop-blur-md'
                      : isCyber
                      ? 'rounded-none bg-cyan-950 text-cyan-300 border border-cyan-400 hover:bg-cyan-900'
                      : isSynth
                      ? 'rounded-md bg-[#242c3b] text-slate-200 border border-[#3c475d] hover:bg-[#2e3748] active:translate-y-0.5'
                      : isCraft
                      ? 'rounded-lg bg-white/[0.06] text-zinc-300 hover:bg-white/10'
                      : 'rounded-none bg-slate-800 text-slate-300 hover:bg-slate-700'
                  }`}
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
                  className={`p-2.5 text-xs transition-all ${
                    isApple
                      ? 'rounded-2xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-md hover:bg-white/[0.08]'
                      : isCyber
                      ? 'rounded-none border border-cyan-500/30 bg-[#0b1429] hover:border-cyan-400 hover:shadow-[0_0_15px_rgba(0,243,255,0.3)]'
                      : isSynth
                      ? 'rounded-lg border border-[#2c3647] bg-[#1a212d] shadow-[inset_0_1px_1px_rgba(255,255,255,0.05),0_2px_4px_rgba(0,0,0,0.6)] hover:border-amber-500/40'
                      : isCraft
                      ? 'rounded-xl border border-white/[0.08] bg-[#171922] hover:border-white/20'
                      : 'rounded-none border border-slate-700/60 bg-[#141822] hover:border-amber-500/50'
                  }`}
                >
                  <div className="flex items-center justify-between pb-1">
                    <span className="font-semibold truncate max-w-[130px]">{auto.name}</span>
                    <span
                      className={`text-[9px] uppercase px-1.5 py-0.5 font-bold ${
                        auto.status === 'running'
                          ? isCyber
                            ? 'bg-cyan-950 text-cyan-400 border border-cyan-400 animate-pulse'
                            : 'bg-cyan-950 text-cyan-400 border border-cyan-800'
                          : auto.status === 'scheduled'
                          ? 'bg-amber-950 text-amber-400 border border-amber-800'
                          : 'bg-slate-800 text-slate-400'
                      } ${isApple ? 'rounded-full' : isCraft ? 'rounded-md' : ''}`}
                    >
                      {auto.status}
                    </span>
                  </div>

                  {auto.status === 'running' && auto.progressPercent !== undefined ? (
                    <div className="mt-1 space-y-1">
                      <div
                        className={`h-1.5 w-full overflow-hidden ${
                          isApple
                            ? 'rounded-full bg-white/10'
                            : isCyber
                            ? 'rounded-none bg-cyan-950'
                            : isSynth
                            ? 'rounded bg-black/60 shadow-inner'
                            : isCraft
                            ? 'rounded-full bg-white/[0.08]'
                            : 'rounded-none bg-slate-800'
                        }`}
                      >
                        <div
                          className={`h-full transition-all duration-500 ${
                            isApple
                              ? 'bg-gradient-to-r from-blue-500 to-indigo-500 rounded-full'
                              : isCyber
                              ? 'bg-cyan-400 shadow-[0_0_10px_rgba(0,243,255,1)]'
                              : isSynth
                              ? 'bg-amber-400'
                              : isCraft
                              ? 'bg-[#ff5500] rounded-full'
                              : 'bg-amber-500'
                          }`}
                          style={{ width: `${auto.progressPercent}%` }}
                        />
                      </div>
                      <span className={`text-[10px] block truncate ${theme.textSecondaryClass}`}>
                        {auto.currentStep}
                      </span>
                    </div>
                  ) : (
                    <p className={`text-[10px] truncate mt-1 ${theme.textSecondaryClass}`}>
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
              <div
                className={`flex items-center gap-1 p-1 ${
                  isApple
                    ? 'rounded-full bg-white/[0.08] backdrop-blur-xl border border-white/15'
                    : isCyber
                    ? 'rounded-none bg-[#091124] border border-cyan-500/40'
                    : isSynth
                    ? 'rounded-lg bg-[#0e1218] border border-[#2b3444] shadow-inner'
                    : isCraft
                    ? 'rounded-xl bg-white/[0.04] border border-white/[0.08]'
                    : 'rounded-none bg-black/50 border border-amber-500/30'
                }`}
              >
                {(['all', 'video', 'code', 'report'] as const).map((filter) => (
                  <button
                    key={filter}
                    onClick={() => setMediaFilter(filter)}
                    className={`px-3 py-1 text-xs font-semibold capitalize transition-all ${
                      mediaFilter === filter
                        ? isApple
                          ? 'rounded-full bg-white text-slate-900 shadow-md'
                          : isCyber
                          ? 'rounded-none bg-cyan-400 text-black font-black uppercase'
                          : isSynth
                          ? 'rounded-md bg-amber-500 text-black font-bold'
                          : isCraft
                          ? 'rounded-lg bg-white text-black font-semibold'
                          : 'rounded-none bg-amber-500 text-black font-mono font-bold'
                        : 'text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    {filter === 'all' ? 'All Deliverables' : `${filter}s`}
                  </button>
                ))}
              </div>

              <div className="flex items-center gap-3 text-xs">
                <span className={theme.textSecondaryClass}>
                  Showing <strong>{filteredDeliverables.length}</strong> items generated by project
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
                  className={`group relative cursor-pointer flex flex-col p-3.5 transition-all duration-300 ${
                    isApple
                      ? 'rounded-3xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-[0_8px_30px_rgba(0,0,0,0.25)] hover:bg-white/[0.09] hover:border-white/30 hover:scale-[1.01]'
                      : isCyber
                      ? 'rounded-none border border-cyan-500/30 bg-[#0a1125] hover:border-cyan-400 hover:shadow-[0_0_25px_rgba(0,243,255,0.4)]'
                      : isSynth
                      ? 'rounded-xl border border-[#2b3546] bg-[#1a212d] shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_4px_8px_rgba(0,0,0,0.6)] hover:border-amber-500/40 hover:shadow-[0_4px_16px_rgba(251,146,60,0.25)]'
                      : isCraft
                      ? 'rounded-2xl border border-white/[0.08] bg-[#161821] hover:border-white/20 hover:shadow-[0_10px_28px_rgba(0,0,0,0.5)]'
                      : 'rounded-none border border-slate-700/60 bg-[#141822] hover:border-amber-500/60'
                  }`}
                >
                  {/* Top Bar of Deliverable */}
                  <div className="flex items-center justify-between pb-2">
                    <div className="flex items-center gap-1.5">
                      {item.type === 'video' ? (
                        <Film
                          size={14}
                          className={
                            isApple
                              ? 'text-blue-400'
                              : isCyber
                              ? 'text-cyan-400'
                              : isSynth
                              ? 'text-amber-400'
                              : isCraft
                              ? 'text-[#ff5500]'
                              : 'text-amber-400'
                          }
                        />
                      ) : item.type === 'code' ? (
                        <FileCode size={14} className="text-emerald-400" />
                      ) : (
                        <FileCheck size={14} className="text-amber-400" />
                      )}
                      <span className="text-[10px] font-mono text-slate-400">{item.author}</span>
                    </div>

                    <span
                      className={`text-[9px] uppercase px-1.5 py-0.5 font-bold ${
                        item.status === 'ready'
                          ? 'bg-emerald-950 text-emerald-400 border border-emerald-800'
                          : item.status === 'generating'
                          ? 'bg-cyan-950 text-cyan-400 border border-cyan-800 animate-pulse'
                          : 'bg-slate-800 text-slate-300'
                      } ${isApple ? 'rounded-full' : isCraft ? 'rounded-md' : ''}`}
                    >
                      {item.status}
                    </span>
                  </div>

                  {/* Thumbnail / Visual Box */}
                  {item.type === 'video' && (
                    <div
                      className={`relative mb-2.5 aspect-video w-full overflow-hidden flex items-center justify-center border transition-all ${
                        isApple
                          ? 'rounded-2xl border-white/10 bg-black/60 shadow-inner'
                          : isCyber
                          ? 'rounded-none border-cyan-500/40 bg-black/80'
                          : isSynth
                          ? 'rounded-lg border-[#2e3749] bg-black/80 shadow-[inset_0_2px_6px_rgba(0,0,0,0.9)]'
                          : isCraft
                          ? 'rounded-xl border-white/[0.08] bg-black/60'
                          : 'rounded-none border-white/10 bg-black/80'
                      }`}
                    >
                      {/* Animated Glow Backdrop */}
                      <div
                        className={`absolute inset-0 opacity-40 transition-opacity group-hover:opacity-70 ${
                          isApple
                            ? 'bg-gradient-to-tr from-blue-900/60 via-purple-900/40 to-slate-900'
                            : isCyber
                            ? 'bg-gradient-to-tr from-cyan-900/70 via-fuchsia-900/50 to-slate-900'
                            : isSynth
                            ? 'bg-gradient-to-tr from-amber-900/60 via-slate-900 to-black'
                            : isCraft
                            ? 'bg-gradient-to-tr from-zinc-800/60 via-stone-900 to-black'
                            : 'bg-gradient-to-tr from-amber-950 via-slate-900 to-black'
                        }`}
                      />

                      <div className="relative z-10 flex flex-col items-center gap-1.5">
                        <div
                          className={`flex h-10 w-10 items-center justify-center transition-transform group-hover:scale-110 ${
                            isApple
                              ? 'rounded-full bg-white/90 text-slate-900 shadow-xl backdrop-blur-md'
                              : isCyber
                              ? 'rounded-none bg-cyan-400 text-black shadow-[0_0_15px_rgba(0,243,255,0.8)]'
                              : isSynth
                              ? 'rounded-full bg-amber-400 text-black shadow-[0_0_12px_rgba(251,191,36,0.8)]'
                              : isCraft
                              ? 'rounded-full bg-[#ff5500] text-white shadow-lg'
                              : 'rounded-full bg-amber-400 text-black'
                          }`}
                        >
                          <Play size={16} fill="currentColor" />
                        </div>
                        <span className="text-[10px] font-mono text-slate-300">
                          {item.duration || '0:15'} • {item.videoAspect || '9:16'}
                        </span>
                      </div>

                      <span className="absolute bottom-1.5 right-2 rounded bg-black/80 px-1 font-mono text-[9px] text-slate-300">
                        1080p
                      </span>
                    </div>
                  )}

                  {/* Title & Prompt */}
                  <h4 className="text-xs font-semibold line-clamp-1">{item.title}</h4>
                  <p className={`mt-1 text-[11px] line-clamp-2 ${theme.textSecondaryClass}`}>
                    {item.prompt}
                  </p>

                  {/* Footer & Actions */}
                  <div className="mt-3 flex items-center justify-between border-t border-white/5 pt-2 text-[10px]">
                    <span className="text-slate-500">{item.createdAt}</span>

                    <div className="flex items-center gap-1.5">
                      {item.status === 'ready' && (
                        <button
                          onClick={(e) => handleAcceptDeliverable(item.id, e)}
                          className={`flex items-center gap-1 px-2.5 py-1 font-semibold transition-all ${
                            isApple
                              ? 'rounded-full bg-emerald-500/25 text-emerald-300 hover:bg-emerald-500/40 backdrop-blur-md'
                              : isCyber
                              ? 'rounded-none bg-emerald-950 text-emerald-300 border border-emerald-500 hover:bg-emerald-900'
                              : isSynth
                              ? 'rounded-md bg-emerald-950/90 text-emerald-300 border border-emerald-700/60 active:translate-y-0.5'
                              : isCraft
                              ? 'rounded-lg bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30'
                              : 'rounded-none bg-emerald-950 text-emerald-300 border border-emerald-800'
                          }`}
                        >
                          <CheckCircle2 size={11} /> Accept
                        </button>
                      )}
                      <button
                        className={`px-2 py-1 text-slate-300 transition-colors ${
                          isApple
                            ? 'rounded-full bg-white/10 hover:bg-white/20'
                            : isCraft
                            ? 'rounded-lg bg-white/[0.06] hover:bg-white/10'
                            : 'rounded bg-white/5 hover:bg-white/10'
                        }`}
                        title="Expand Preview"
                      >
                        <Eye size={11} />
                      </button>
                    </div>
                  </div>
                </div>
              ))}
            </div>

            {/* Running Subagent Tasks for Review (Session Abstraction) */}
            <div className="mt-6 border-t pt-4 border-white/10">
              <div className="flex items-center justify-between pb-3">
                <span className="text-xs font-bold uppercase tracking-wider flex items-center gap-1.5">
                  <Layers
                    size={14}
                    className={
                      isApple
                        ? 'text-blue-400'
                        : isCyber
                        ? 'text-cyan-400'
                        : isSynth
                        ? 'text-amber-400'
                        : isCraft
                        ? 'text-[#ff5500]'
                        : 'text-amber-400'
                    }
                  />
                  {isCraft ? '03 / ACTIVE SUBAGENT TASKS' : 'Active Project Tasks & Subagent Reviews'}
                </span>
                <span className={`text-[11px] ${theme.textSecondaryClass}`}>
                  Sessions are encapsulated inside tasks
                </span>
              </div>

              <div className="space-y-2.5">
                {MOCK_RUNNING_TASKS.map((task) => (
                  <div
                    key={task.id}
                    className={`p-3 transition-all ${
                      isApple
                        ? 'rounded-2xl border border-white/15 bg-white/[0.05] backdrop-blur-xl shadow-md'
                        : isCyber
                        ? 'rounded-none border border-cyan-500/30 bg-[#091124]'
                        : isSynth
                        ? 'rounded-lg border border-[#2b3545] bg-[#1a212d] shadow-inner'
                        : isCraft
                        ? 'rounded-xl border border-white/[0.08] bg-[#161821]'
                        : 'rounded-none border border-slate-700/60 bg-[#141822]'
                    }`}
                  >
                    <div className="flex items-center justify-between pb-2">
                      <div className="flex items-center gap-2">
                        <span
                          className={`px-1.5 py-0.5 text-[9px] uppercase font-bold ${
                            task.agentType === 'coder'
                              ? 'bg-blue-950 text-blue-300 border border-blue-800'
                              : 'bg-purple-950 text-purple-300 border border-purple-800'
                          } ${isApple ? 'rounded-full' : isCraft ? 'rounded-md' : ''}`}
                        >
                          {task.agentType}
                        </span>
                        <h4 className="text-xs font-semibold">{task.title}</h4>
                      </div>

                      <div className="flex items-center gap-2">
                        <span className="text-[10px] font-mono text-slate-400">
                          {task.elapsed}
                        </span>
                        <span
                          className={`text-[9px] uppercase font-bold px-1.5 py-0.5 ${
                            task.status === 'running'
                              ? 'bg-cyan-950 text-cyan-300'
                              : 'bg-amber-950 text-amber-300'
                          } ${isApple ? 'rounded-full' : isCraft ? 'rounded-md' : ''}`}
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
                            className={st.completed ? 'text-emerald-400' : 'text-slate-600'}
                          />
                          <span
                            className={
                              st.completed
                                ? 'text-slate-300 line-through opacity-70'
                                : 'text-slate-200'
                            }
                          >
                            {st.title}
                          </span>
                        </div>
                      ))}
                    </div>

                    {task.diffPreview && (
                      <div
                        className={`mt-2 p-2 font-mono text-[10px] text-emerald-400 border border-emerald-950 ${
                          isApple
                            ? 'rounded-xl bg-black/60'
                            : isCraft
                            ? 'rounded-lg bg-black/60'
                            : 'rounded bg-black/60'
                        }`}
                      >
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
        <aside
          className={`relative flex w-80 flex-shrink-0 flex-col overflow-hidden transition-all duration-300 ${
            isApple
              ? 'rounded-3xl border backdrop-blur-2xl bg-white/[0.04] border-white/15 shadow-[inset_0_1px_1px_rgba(255,255,255,0.25),0_20px_45px_rgba(0,0,0,0.55)]'
              : isCyber
              ? 'border-l bg-[#070b19]/95 border-cyan-500/40 shadow-[inset_0_0_20px_rgba(0,243,255,0.05)]'
              : isSynth
              ? 'border-l-2 bg-[#141820] border-[#272f3d] shadow-[inset_0_2px_4px_rgba(0,0,0,0.7)]'
              : isCraft
              ? 'border-l bg-[#111319] border-white/[0.08]'
              : 'border-l bg-[#0d1017] border-amber-500/25'
          }`}
        >
          {isSynth && <StudioChassisScrew className="absolute top-2 right-2" />}
          {isSynth && <StudioChassisScrew className="absolute bottom-2 right-2" />}
          {isSwarm && <TacticalCornerCrosshairs color="#f59e0b" />}

          {/* Header */}
          <div
            className={`p-3.5 border-b transition-colors ${
              isApple
                ? 'border-white/10'
                : isCyber
                ? 'border-cyan-500/30'
                : isSynth
                ? 'border-[#293240]'
                : isCraft
                ? 'border-white/[0.07]'
                : 'border-amber-500/25'
            }`}
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <div
                  className={`flex h-7 w-7 items-center justify-center transition-all ${
                    isApple
                      ? 'rounded-xl bg-blue-500/20 text-blue-300 border border-blue-400/30'
                      : isCyber
                      ? 'rounded-none bg-cyan-950 text-cyan-300 border border-cyan-400'
                      : isSynth
                      ? 'rounded-md bg-[#222b3a] text-amber-400 border border-[#3b475c]'
                      : isCraft
                      ? 'rounded-lg bg-white/[0.06] text-[#ff5500]'
                      : 'rounded-none bg-amber-500/20 text-amber-400 border border-amber-500/60'
                  }`}
                >
                  <Bot size={15} />
                </div>
                <div className="flex flex-col">
                  <span className="text-xs font-bold">
                    {isSwarm ? '[AI.ORCHESTRATOR]' : 'Swarm Orchestrator'}
                  </span>
                  <span className="text-[10px] text-emerald-400 flex items-center gap-1">
                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                    Bound to {selectedProject.name}
                  </span>
                </div>
              </div>

              {isSynth ? (
                <div className="flex items-center gap-1 px-1.5 py-0.5 rounded bg-black/40 text-amber-400 text-[9px] border border-[#3b4556]">
                  <Mic size={10} />
                  <span>TALKBACK</span>
                </div>
              ) : (
                <span className="rounded bg-black/30 px-1.5 py-0.5 text-[9px] font-mono text-slate-400">
                  Lean
                </span>
              )}
            </div>

            {/* Quick Action Pills */}
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <button
                onClick={() => handleSendMessage('Make 10 social media videos')}
                className={`px-2 py-0.5 text-[10px] transition-all ${
                  isApple
                    ? 'rounded-full bg-white/10 text-white hover:bg-white/20'
                    : isCyber
                    ? 'rounded-none bg-cyan-950/70 border border-cyan-500/40 text-cyan-300 hover:bg-cyan-900'
                    : isSynth
                    ? 'rounded-md bg-[#1f2635] border border-[#323d50] text-amber-300 hover:bg-[#283244]'
                    : isCraft
                    ? 'rounded-lg bg-white/[0.04] border border-white/10 text-zinc-300 hover:bg-white/10'
                    : 'rounded-none border border-slate-700 hover:bg-white/10'
                }`}
              >
                🎥 Make 10 videos
              </button>
              <button
                onClick={() => handleSendMessage('Run testbench on dev branch')}
                className={`px-2 py-0.5 text-[10px] transition-all ${
                  isApple
                    ? 'rounded-full bg-white/10 text-white hover:bg-white/20'
                    : isCyber
                    ? 'rounded-none bg-cyan-950/70 border border-cyan-500/40 text-cyan-300 hover:bg-cyan-900'
                    : isSynth
                    ? 'rounded-md bg-[#1f2635] border border-[#323d50] text-amber-300 hover:bg-[#283244]'
                    : isCraft
                    ? 'rounded-lg bg-white/[0.04] border border-white/10 text-zinc-300 hover:bg-white/10'
                    : 'rounded-none border border-slate-700 hover:bg-white/10'
                }`}
              >
                🧪 Run tests
              </button>
              <button
                onClick={() => handleSendMessage('Audit project token velocity')}
                className={`px-2 py-0.5 text-[10px] transition-all ${
                  isApple
                    ? 'rounded-full bg-white/10 text-white hover:bg-white/20'
                    : isCyber
                    ? 'rounded-none bg-cyan-950/70 border border-cyan-500/40 text-cyan-300 hover:bg-cyan-900'
                    : isSynth
                    ? 'rounded-md bg-[#1f2635] border border-[#323d50] text-amber-300 hover:bg-[#283244]'
                    : isCraft
                    ? 'rounded-lg bg-white/[0.04] border border-white/10 text-zinc-300 hover:bg-white/10'
                    : 'rounded-none border border-slate-700 hover:bg-white/10'
                }`}
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
                    className={`max-w-[90%] px-3 py-2 text-xs leading-relaxed transition-all ${
                      isUser
                        ? isApple
                          ? 'rounded-2xl rounded-br-sm bg-gradient-to-r from-blue-500 to-indigo-600 text-white shadow-md'
                          : isCyber
                          ? 'rounded-none bg-gradient-to-r from-cyan-500 to-fuchsia-600 text-black font-semibold'
                          : isSynth
                          ? 'rounded-md bg-gradient-to-b from-amber-500 to-amber-700 text-black font-semibold'
                          : isCraft
                          ? 'rounded-xl rounded-br-sm bg-[#ff5500] text-white'
                          : 'rounded-none bg-amber-500 text-black font-mono'
                        : isApple
                        ? 'rounded-2xl rounded-bl-sm bg-white/[0.08] backdrop-blur-xl border border-white/15 text-white'
                        : isCyber
                        ? 'rounded-none bg-[#091124] border border-cyan-500/40 text-cyan-200'
                        : isSynth
                        ? 'rounded-md bg-[#19202b] border border-[#2d3748] text-slate-200 shadow-inner'
                        : isCraft
                        ? 'rounded-xl rounded-bl-sm bg-[#161821] border border-white/[0.08] text-zinc-200'
                        : 'rounded-none bg-[#141822] border border-slate-700 text-slate-200'
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
                            className={`flex items-center justify-between px-2 py-1 text-[10px] transition-colors ${
                              isApple
                                ? 'rounded-full bg-white/10 hover:bg-white/20 text-blue-200'
                                : isCyber
                                ? 'rounded-none bg-cyan-950 text-cyan-300 border border-cyan-500/40 hover:bg-cyan-900'
                                : isSynth
                                ? 'rounded bg-[#252f40] text-amber-300 hover:bg-[#2f3b50]'
                                : isCraft
                                ? 'rounded-lg bg-white/[0.06] text-zinc-200 hover:bg-white/10'
                                : 'rounded bg-black/40 text-cyan-300 hover:bg-black/60'
                            }`}
                          >
                            <span>{pill.label}</span>
                            <ArrowRight size={10} />
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                  <span className="mt-1 text-[9px] text-slate-500">{msg.timestamp}</span>
                </div>
              )
            })}

            {isTyping && (
              <div className="flex items-center gap-1.5 text-xs text-slate-400">
                <Sparkles size={12} className="animate-spin text-cyan-400" />
                <span>Orchestrator dispatching tasks...</span>
              </div>
            )}
          </div>

          {/* Chat Input Box */}
          <div
            className={`p-3 border-t transition-colors ${
              isApple
                ? 'border-white/10'
                : isCyber
                ? 'border-cyan-500/30'
                : isSynth
                ? 'border-[#293240]'
                : isCraft
                ? 'border-white/[0.07]'
                : 'border-amber-500/25'
            }`}
          >
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
                className={`flex-1 px-3 py-1.5 text-xs focus:outline-none transition-all ${
                  isApple
                    ? 'rounded-full bg-white/[0.08] border border-white/20 text-white placeholder-slate-400 focus:ring-1 focus:ring-blue-400'
                    : isCyber
                    ? 'rounded-none bg-black/70 border border-cyan-500/50 text-cyan-200 placeholder-cyan-500/50 focus:border-cyan-400 focus:shadow-[0_0_10px_rgba(0,243,255,0.4)]'
                    : isSynth
                    ? 'rounded-md bg-[#0f131a] border border-[#2d3748] text-amber-100 placeholder-slate-500 focus:border-amber-500 shadow-inner'
                    : isCraft
                    ? 'rounded-lg bg-white/[0.04] border border-white/10 text-zinc-100 placeholder-zinc-500 focus:border-white/30'
                    : 'rounded-none bg-black/40 border border-amber-500/40 text-amber-200 placeholder-slate-500 font-mono'
                }`}
              />
              <button
                type="submit"
                disabled={!inputText.trim()}
                className={`flex h-7 w-7 items-center justify-center transition-all disabled:opacity-40 ${
                  isApple
                    ? 'rounded-full bg-blue-500 text-white shadow-md hover:bg-blue-400'
                    : isCyber
                    ? 'rounded-none bg-cyan-400 text-black hover:bg-cyan-300 shadow-[0_0_10px_rgba(0,243,255,0.7)]'
                    : isSynth
                    ? 'rounded-md bg-amber-500 text-black hover:bg-amber-400 shadow-sm active:translate-y-0.5'
                    : isCraft
                    ? 'rounded-lg bg-[#ff5500] text-white hover:bg-[#ff661a]'
                    : 'rounded-none bg-amber-500 text-black'
                }`}
              >
                <Send size={13} />
              </button>
            </form>
          </div>
        </aside>
      </div>

      {/* ─────────────────────────────────────────────────────────────
          4. MODAL: VIDEO DELIVERABLE PREVIEW DIALOG
         ───────────────────────────────────────────────────────────── */}
      {activeVideoPreview && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div
            className={`relative flex max-w-2xl w-full flex-col p-5 transition-all ${
              isApple
                ? 'rounded-3xl border border-white/20 bg-slate-900/90 backdrop-blur-2xl shadow-[0_20px_60px_rgba(0,0,0,0.8)]'
                : isCyber
                ? 'rounded-none border-2 border-cyan-400 bg-[#080d22] shadow-[0_0_40px_rgba(0,243,255,0.4)]'
                : isSynth
                ? 'rounded-xl border-2 border-[#384357] bg-[#161c26] shadow-[inset_0_2px_4px_rgba(0,0,0,0.8),0_10px_30px_rgba(0,0,0,0.8)]'
                : isCraft
                ? 'rounded-2xl border border-white/10 bg-[#14161f] shadow-2xl'
                : 'rounded-none border border-amber-500/50 bg-[#0e1119] font-mono'
            }`}
          >
            {isSynth && <StudioChassisScrew className="absolute top-2 left-2" />}
            {isSynth && <StudioChassisScrew className="absolute top-2 right-2" />}

            <div className="flex items-center justify-between pb-3 border-b border-white/10">
              <div className="flex items-center gap-2">
                <Film
                  size={16}
                  className={
                    isApple
                      ? 'text-blue-400'
                      : isCyber
                      ? 'text-cyan-400'
                      : isSynth
                      ? 'text-amber-400'
                      : isCraft
                      ? 'text-[#ff5500]'
                      : 'text-amber-400'
                  }
                />
                <h3 className="text-sm font-bold">{activeVideoPreview.title}</h3>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => setIsFullscreen(!isFullscreen)}
                  className="rounded p-1 text-slate-400 hover:bg-white/10 hover:text-white"
                  title="Toggle Fullscreen"
                >
                  <Maximize2 size={14} />
                </button>
                <button
                  onClick={() => {
                    setActiveVideoPreview(null)
                    setIsPlayingVideo(false)
                  }}
                  className="rounded p-1 text-slate-400 hover:bg-white/10 hover:text-white"
                >
                  ✕
                </button>
              </div>
            </div>

            {/* Simulated Video Canvas */}
            <div
              className={`relative my-4 aspect-video w-full overflow-hidden flex items-center justify-center border ${
                isApple
                  ? 'rounded-2xl border-white/15 bg-black'
                  : isCyber
                  ? 'rounded-none border-cyan-500/50 bg-black'
                  : isSynth
                  ? 'rounded-lg border-[#2e3748] bg-black shadow-inner'
                  : isCraft
                  ? 'rounded-xl border-white/10 bg-black'
                  : 'rounded-none border-white/10 bg-black'
              }`}
            >
              <div
                className={`absolute inset-0 opacity-60 ${
                  isCyber
                    ? 'bg-gradient-to-tr from-cyan-900 via-fuchsia-900 to-black'
                    : isSynth
                    ? 'bg-gradient-to-tr from-amber-900 via-slate-900 to-black'
                    : 'bg-gradient-to-tr from-blue-900 via-indigo-900 to-black'
                }`}
                style={{
                  animation: isPlayingVideo ? 'pulse 1.5s ease-in-out infinite' : 'none',
                }}
              />
              <div className="relative z-10 flex flex-col items-center gap-2">
                <button
                  onClick={() => setIsPlayingVideo(!isPlayingVideo)}
                  className={`flex h-14 w-14 items-center justify-center transition-transform hover:scale-110 ${
                    isApple
                      ? 'rounded-full bg-white text-slate-900 shadow-2xl'
                      : isCyber
                      ? 'rounded-none bg-cyan-400 text-black shadow-[0_0_20px_rgba(0,243,255,0.9)]'
                      : isSynth
                      ? 'rounded-full bg-amber-400 text-black shadow-[0_0_15px_rgba(251,191,36,0.9)]'
                      : isCraft
                      ? 'rounded-full bg-[#ff5500] text-white shadow-xl'
                      : 'rounded-full bg-amber-400 text-black'
                  }`}
                >
                  {isPlayingVideo ? <Pause size={24} /> : <Play size={24} fill="currentColor" />}
                </button>
                <span className="text-xs font-mono text-slate-200">
                  {isPlayingVideo ? 'Playing Simulated Stream...' : 'Click to Play Render Preview'}
                </span>
              </div>

              {/* Volume toggle in corner */}
              <button
                onClick={() => setIsMuted(!isMuted)}
                className="absolute bottom-2 left-2 z-20 rounded bg-black/60 p-1.5 text-slate-300 hover:text-white backdrop-blur-sm"
                title={isMuted ? 'Unmute' : 'Mute'}
              >
                <Volume2 size={13} className={isMuted ? 'opacity-40' : ''} />
              </button>
            </div>

            {/* Prompt details & actions */}
            <div className="space-y-2 text-xs">
              <div>
                <span className="text-slate-400">Worker Prompt: </span>
                <span className="text-slate-200">{activeVideoPreview.prompt}</span>
              </div>
              <div className="flex items-center gap-4 text-slate-400 font-mono text-[11px]">
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
                className={`flex items-center gap-1.5 px-4 py-1.5 text-xs font-semibold text-white transition-all ${
                  isApple
                    ? 'rounded-full bg-emerald-600 hover:bg-emerald-500 shadow-lg'
                    : isCyber
                    ? 'rounded-none bg-emerald-500 text-black font-black uppercase shadow-[0_0_15px_rgba(16,185,129,0.8)]'
                    : isSynth
                    ? 'rounded-md bg-emerald-600 hover:bg-emerald-500 shadow-md'
                    : isCraft
                    ? 'rounded-lg bg-emerald-600 hover:bg-emerald-500'
                    : 'rounded-none bg-emerald-600'
                }`}
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
