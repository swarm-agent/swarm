import { useState } from 'react'
import {
  ArrowRight,
  ArrowUp,
  AtSign,
  Check,
  CheckCircle2,
  Clock,
  Code,
  Download,
  Film,
  Folder,
  Home,
  Inbox,
  Layers,
  Maximize2,
  MoreHorizontal,
  Paperclip,
  Pause,
  Play,
  Plus,
  Search,
  Settings,
  Sparkles,
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
import { ORCHESTRATE_THEME_IDS, ORCHESTRATE_THEMES } from './orchestrate-themes'
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

/**
 * Thumbnail graphic renderer for the video deliverables with modern high-craft space/cyber aesthetics
 */
function DeliverableThumbnail({
  type,
  duration,
  onPlay,
}: {
  type?: string
  duration?: string
  onPlay?: () => void
}) {
  return (
    <div
      onClick={onPlay}
      className="group/thumb relative aspect-video w-full cursor-pointer overflow-hidden rounded-xl border border-slate-800/80 bg-[#090d16] shadow-inner transition-all hover:border-blue-500/40"
    >
      {/* Visual Canvas Artwork based on thumbnail type */}
      {type === 'cyber_lattice' && (
        <div className="absolute inset-0 bg-gradient-to-br from-[#0c162d] via-[#091024] to-[#040814] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-60" viewBox="0 0 200 112">
            <defs>
              <linearGradient id="cyber-grad" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="#3b82f6" stopOpacity="0.8" />
                <stop offset="100%" stopColor="#06b6d4" stopOpacity="0.3" />
              </linearGradient>
            </defs>
            {/* Grid perspective */}
            <path
              d="M 100 20 L 0 112 M 100 20 L 50 112 M 100 20 L 100 112 M 100 20 L 150 112 M 100 20 L 200 112"
              stroke="#38bdf8"
              strokeWidth="0.5"
              strokeOpacity="0.4"
            />
            <path
              d="M 20 100 L 180 100 M 40 85 L 160 85 M 60 70 L 140 70 M 80 55 L 120 55"
              stroke="#60a5fa"
              strokeWidth="0.5"
              strokeOpacity="0.3"
            />
            {/* Neon towers / nodes */}
            <rect x="25" y="45" width="12" height="60" fill="url(#cyber-grad)" rx="1" opacity="0.7" />
            <rect x="55" y="30" width="16" height="75" fill="url(#cyber-grad)" rx="1" opacity="0.9" />
            <rect x="135" y="35" width="15" height="70" fill="url(#cyber-grad)" rx="1" opacity="0.8" />
            <rect x="165" y="50" width="14" height="55" fill="url(#cyber-grad)" rx="1" opacity="0.7" />
            <circle cx="100" cy="30" r="3" fill="#60a5fa" />
            <circle cx="63" cy="28" r="2.5" fill="#38bdf8" />
            <circle cx="142" cy="33" r="2.5" fill="#818cf8" />
          </svg>
        </div>
      )}

      {type === 'neural_core' && (
        <div className="absolute inset-0 bg-gradient-to-br from-[#0a1226] via-[#070d1e] to-[#040711] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-70" viewBox="0 0 200 112">
            <defs>
              <linearGradient id="cube-grad" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="#818cf8" stopOpacity="0.9" />
                <stop offset="100%" stopColor="#3b82f6" stopOpacity="0.4" />
              </linearGradient>
            </defs>
            {/* Energy radial glow */}
            <circle cx="100" cy="56" r="40" fill="#3b82f6" opacity="0.15" filter="blur(8px)" />
            {/* Isometric Obsidian Cube */}
            <path d="M 100 32 L 128 48 L 100 64 L 72 48 Z" fill="#1e293b" stroke="#818cf8" strokeWidth="0.8" />
            <path d="M 72 48 L 100 64 L 100 94 L 72 78 Z" fill="#0f172a" stroke="#6366f1" strokeWidth="0.8" />
            <path d="M 128 48 L 100 64 L 100 94 L 128 78 Z" fill="#1e1b4b" stroke="#3b82f6" strokeWidth="0.8" />
            {/* Neural filaments radiating */}
            <line x1="100" y1="32" x2="100" y2="15" stroke="#93c5fd" strokeWidth="0.8" strokeDasharray="2 2" />
            <line x1="72" y1="48" x2="45" y2="35" stroke="#93c5fd" strokeWidth="0.8" strokeDasharray="2 2" />
            <line x1="128" y1="48" x2="155" y2="35" stroke="#93c5fd" strokeWidth="0.8" strokeDasharray="2 2" />
            <circle cx="100" cy="14" r="2" fill="#bfdbfe" />
            <circle cx="44" cy="34" r="2" fill="#bfdbfe" />
            <circle cx="156" cy="34" r="2" fill="#bfdbfe" />
          </svg>
        </div>
      )}

      {type === 'orbital_data' && (
        <div className="absolute inset-0 bg-gradient-to-b from-[#030712] via-[#081226] to-[#0d2247] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-80" viewBox="0 0 200 112">
            {/* Earth horizon curve */}
            <path
              d="M -20 120 Q 100 65 220 120 Z"
              fill="#1e3a8a"
              opacity="0.8"
            />
            <path
              d="M -20 120 Q 100 64 220 120"
              stroke="#60a5fa"
              strokeWidth="1.5"
              fill="none"
              opacity="0.9"
            />
            {/* Atmosphere glow */}
            <path
              d="M -20 118 Q 100 61 220 118"
              stroke="#93c5fd"
              strokeWidth="3"
              fill="none"
              opacity="0.3"
            />
            {/* Star field */}
            <circle cx="30" cy="20" r="0.8" fill="#fff" opacity="0.8" />
            <circle cx="65" cy="15" r="0.6" fill="#fff" opacity="0.5" />
            <circle cx="140" cy="25" r="0.8" fill="#fff" opacity="0.7" />
            <circle cx="175" cy="18" r="0.6" fill="#fff" opacity="0.6" />
            <circle cx="110" cy="12" r="1" fill="#fff" opacity="0.9" />
            {/* Orbital beam / satellite signal */}
            <line x1="80" y1="40" x2="120" y2="40" stroke="#38bdf8" strokeWidth="0.8" />
            <circle cx="100" cy="40" r="3" fill="#38bdf8" />
            <line x1="100" y1="40" x2="115" y2="78" stroke="#38bdf8" strokeWidth="0.6" strokeDasharray="3 2" />
          </svg>
        </div>
      )}

      {/* Default fallback backdrop if not custom */}
      {!['cyber_lattice', 'neural_core', 'orbital_data'].includes(type || '') && (
        <div className="absolute inset-0 bg-gradient-to-br from-slate-900 to-slate-950" />
      )}

      {/* Center Play Button Overlay */}
      <div className="relative z-10 flex h-full w-full items-center justify-center">
        <div className="flex h-9 w-9 items-center justify-center rounded-full bg-slate-900/80 text-white backdrop-blur-md border border-white/20 shadow-xl transition-transform group-hover/thumb:scale-110">
          <Play size={13} fill="currentColor" className="ml-0.5 text-white" />
        </div>
      </div>

      {/* Duration Pill at bottom right */}
      {duration && (
        <span className="absolute bottom-1.5 right-1.5 z-20 rounded-md bg-black/80 px-1.5 py-0.5 font-mono text-[9px] font-semibold text-slate-300 backdrop-blur-sm border border-white/10">
          {duration}
        </span>
      )}
    </div>
  )
}

export function OrchestrateView({
  workspaceSlug: _workspaceSlug,
  onNavigateHome,
  initialThemeId = 'modern_navy',
}: OrchestrateViewProps) {
  const [currentThemeId, setCurrentThemeId] = useState<OrchestrateThemeId>(initialThemeId)
  const theme = ORCHESTRATE_THEMES[currentThemeId] || ORCHESTRATE_THEMES.modern_navy

  const [projects] = useState<ProjectSummary[]>(MOCK_PROJECTS)
  const [selectedProjectId, setSelectedProjectId] = useState<string>(projects[0].id)
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  // Automations state
  const [automations] = useState<RunningAutomation[]>(MOCK_AUTOMATIONS)

  // Tasks state (which encapsulate their attached deliverables)
  const [tasks, setTasks] = useState<RunningTask[]>(MOCK_RUNNING_TASKS)

  // Video preview modal
  const [activeVideoPreview, setActiveVideoPreview] = useState<MediaDeliverable | null>(null)
  const [isPlayingVideo, setIsPlayingVideo] = useState(false)
  const [isMuted, setIsMuted] = useState(false)
  const [isFullscreen, setIsFullscreen] = useState(false)

  // Chat state
  const [messages, setMessages] = useState<OrchestratorMessage[]>(MOCK_CHAT_MESSAGES)
  const [inputText, setInputText] = useState('')
  const [isTyping, setIsTyping] = useState(false)
  const [activeNavTab, setActiveNavTab] = useState<'home' | 'projects' | 'automations' | 'deliverables' | 'settings'>('home')

  const handleAcceptDeliverable = (taskId: string, deliverableId: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
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

    setTimeout(() => {
      let replyText = `Understood. Analyzing project context for "${selectedProject.name}"...`
      let videoProgress: OrchestratorMessage['videoProgressCard']
      let actionBtn: OrchestratorMessage['actionButton']

      if (text.toLowerCase().includes('video')) {
        replyText =
          "I've initiated the video swarm generation sequence for your feature launch. 3 initial clips are already rendering."
        videoProgress = {
          title: 'Video generation in progress...',
          progressPercent: 75,
          clipsLabel: '7/10 clips',
        }
        actionBtn = {
          label: 'Preview all clips',
          action: 'preview_videos',
        }
      } else if (text.toLowerCase().includes('test') || text.toLowerCase().includes('testbench')) {
        replyText = `Acquiring systemd-nspawn testbench lease for "${selectedProject.repoPath}". Critical suites will execute in isolation.`
      } else {
        replyText = `Task registered and assigned to "${selectedProject.name}". Tracking attached deliverables in your Project Canvas.`
      }

      const botMsg: OrchestratorMessage = {
        id: `msg-reply-${Date.now()}`,
        sender: 'orchestrator',
        text: replyText,
        timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        videoProgressCard: videoProgress,
        actionButton: actionBtn,
      }

      setMessages((prev) => [...prev, botMsg])
      setIsTyping(false)
    }, 700)
  }

  return (
    <div
      className={`relative flex h-screen w-screen overflow-hidden p-3 gap-3 ${theme.bgClass} ${theme.textPrimaryClass} font-sans select-none`}
      style={theme.customVars as React.CSSProperties}
    >
      {/* ─────────────────────────────────────────────────────────────
          PANEL 1: LEFT SIDEBAR (NAVIGATION, PROJECTS & USER HUD)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative flex w-72 flex-shrink-0 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* macOS Window Traffic Lights & App Brand Capsule */}
        <div className="p-3.5 border-b border-slate-800/80">
          {/* Traffic Lights */}
          <div className="flex items-center justify-between pb-3">
            <div className="flex items-center gap-2">
              <span className="h-3 w-3 rounded-full bg-[#ff5f56] border border-[#e0443e]" />
              <span className="h-3 w-3 rounded-full bg-[#ffbd2e] border border-[#dea123]" />
              <span className="h-3 w-3 rounded-full bg-[#27c93f] border border-[#1aab29]" />
            </div>

            {onNavigateHome && (
              <button
                onClick={onNavigateHome}
                className="rounded-full px-2 py-0.5 text-[10px] font-medium text-slate-400 hover:bg-slate-800 hover:text-slate-200 transition-all border border-slate-700/60"
                title="Exit Mockup"
              >
                Exit
              </button>
            )}
          </div>

          {/* App Branding */}
          <div className="flex items-center gap-2.5">
            <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30 shadow-[0_2px_8px_rgba(37,99,235,0.3)]">
              <Sparkles size={14} />
            </div>
            <div>
              <div className="text-xs font-bold tracking-tight text-white">Swarm Orchestrate</div>
              <div className="text-[10px] text-slate-400">Build. Orchestrate. Create.</div>
            </div>
          </div>

          {/* Search Input Bar with ⌘K Badge */}
          <div className="mt-3.5 flex items-center justify-between px-3 py-1.5 rounded-xl bg-[#090d16] border border-slate-800 text-xs text-slate-400">
            <div className="flex items-center gap-2">
              <Search size={13} className="text-slate-500" />
              <span className="text-slate-500 text-[11px]">Search...</span>
            </div>
            <kbd className="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-[9px] text-slate-400 border border-slate-700/60">
              ⌘K
            </kbd>
          </div>
        </div>

        {/* Navigation Menu Links */}
        <div className="p-3 border-b border-slate-800/80 space-y-1">
          <button
            onClick={() => setActiveNavTab('home')}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'home'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Home size={15} />
            <span>Home</span>
          </button>
          <button
            onClick={() => setActiveNavTab('projects')}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'projects'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Folder size={15} />
            <span>Projects</span>
          </button>
          <button
            onClick={() => setActiveNavTab('automations')}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'automations'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Zap size={15} />
            <span>Automations</span>
          </button>
          <button
            onClick={() => setActiveNavTab('deliverables')}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'deliverables'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Layers size={15} />
            <span>Deliverables</span>
          </button>
          <button
            onClick={() => setActiveNavTab('settings')}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'settings'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Settings size={15} />
            <span>Settings</span>
          </button>
        </div>

        {/* Projects Switcher Section */}
        <div className="p-3 border-b border-slate-800/80 flex-1 overflow-y-auto">
          <div className="flex items-center justify-between pb-2">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">
              Projects
            </span>
            <button
              className="flex h-5 w-5 items-center justify-center rounded-lg bg-slate-800/80 text-slate-400 hover:text-slate-200 hover:bg-slate-700 transition-all border border-slate-700/60"
              title="Add New Project"
            >
              <Plus size={12} />
            </button>
          </div>

          <div className="flex flex-col gap-1.5">
            {projects.map((proj) => {
              const isSelected = proj.id === selectedProjectId
              return (
                <button
                  key={proj.id}
                  onClick={() => setSelectedProjectId(proj.id)}
                  className={`group flex items-center justify-between p-2 text-left rounded-xl transition-all ${
                    isSelected
                      ? 'bg-slate-800/90 border border-slate-700/80 text-white shadow-sm'
                      : 'border border-transparent hover:bg-slate-800/40 text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <div className="flex items-center gap-2.5 min-w-0">
                    <div
                      className={`flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg ${
                        isSelected
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30'
                          : 'bg-slate-800 text-slate-400'
                      }`}
                    >
                      <Folder size={12} />
                    </div>
                    <span className="text-xs font-medium truncate">{proj.name}</span>
                  </div>
                  {isSelected && (
                    <span className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-blue-500 shadow-[0_0_8px_rgba(59,130,246,0.9)]" />
                  )}
                </button>
              )
            })}
          </div>
        </div>

        {/* 5 Modern Navy Themes Selector */}
        <div className="px-3 pt-2 pb-1 border-b border-slate-800/80">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-slate-500 pb-1.5 flex items-center justify-between">
            <span>Theme</span>
            <span className="font-mono text-[9px] text-slate-400">{theme.name}</span>
          </div>
          <div className="grid grid-cols-5 gap-1 rounded-xl bg-[#090d16] p-1 border border-slate-800">
            {ORCHESTRATE_THEME_IDS.map((tId) => {
              const t = ORCHESTRATE_THEMES[tId]
              const isActive = tId === currentThemeId
              return (
                <button
                  key={tId}
                  onClick={() => setCurrentThemeId(tId)}
                  className={`flex flex-col items-center justify-center py-1 text-[9px] transition-all rounded-lg ${
                    isActive
                      ? 'bg-slate-800 text-white font-semibold shadow-sm border border-slate-700'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title={`${t.name}: ${t.subtitle}`}
                >
                  <span
                    className={`h-1.5 w-1.5 rounded-full mb-0.5 ${isActive ? 'scale-125' : 'opacity-60'}`}
                    style={{ backgroundColor: t.accentColor }}
                  />
                  <span className="truncate max-w-[36px]">{t.name.split(' ')[0]}</span>
                </button>
              )
            })}
          </div>
        </div>

        {/* Bottom Upgrade Promo Card ("Swarm moves faster together.") */}
        <div className="p-3">
          <div className="p-3.5 rounded-2xl bg-[#0a0f1d] border border-slate-800/80 shadow-md">
            <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-blue-500/10 text-blue-400 border border-blue-500/20 mb-2">
              <Sparkles size={13} />
            </div>
            <h5 className="text-xs font-bold text-white">Swarm moves faster together.</h5>
            <p className="mt-1 text-[10px] text-slate-400 leading-relaxed">
              Turn ideas into reality with autonomous AI teams.
            </p>
            <button className="mt-3 w-full py-1.5 text-center text-xs font-semibold text-slate-200 bg-slate-800/80 hover:bg-slate-700/80 rounded-xl border border-slate-700/60 transition-all">
              Upgrade Plan
            </button>
          </div>
        </div>

        {/* Bottom User Profile HUD */}
        <div className="p-3 border-t border-slate-800/80 flex items-center justify-between bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full bg-slate-800 text-slate-200 font-bold text-[10px] border border-slate-700">
              JD
            </div>
            <div className="flex flex-col min-w-0">
              <span className="text-xs font-semibold text-slate-200 truncate">Jordan Diaz</span>
              <span className="text-[10px] text-slate-500 truncate">jordan@swarm.dev</span>
            </div>
          </div>
          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <MoreHorizontal size={14} />
          </button>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (AUTOMATION OVERVIEW & TASKS CANVAS)
         ───────────────────────────────────────────────────────────── */}
      <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* TOP: AUTOMATION OVERVIEW HEADER & ACTIONS */}
        <div className="flex items-center justify-between p-4 border-b border-slate-800/80">
          <div>
            <h1 className="text-xl font-bold tracking-tight text-white">Automation Overview</h1>
            <p className="text-xs text-slate-400 mt-0.5">
              Manage your autonomous workflows and creative production.
            </p>
          </div>

          <div className="flex items-center gap-2.5">
            <button
              onClick={() => handleSendMessage('make 3 social media videos')}
              className="flex items-center gap-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3.5 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
            >
              <Plus size={13} />
              <span>Trigger Video Swarm</span>
            </button>
            <button
              onClick={() => handleSendMessage('run full testbench')}
              className="flex items-center gap-1.5 rounded-xl bg-slate-800/80 hover:bg-slate-700/80 border border-slate-700/70 text-slate-200 text-xs px-3.5 py-1.5 transition-all active:scale-95"
            >
              <Play size={11} fill="currentColor" />
              <span>Run Testbench</span>
            </button>
          </div>
        </div>

        {/* SCROLLABLE MAIN CONTENT: ACTIVE AUTOMATIONS + TASK CARDS */}
        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {/* SECTION 1: ACTIVE AUTOMATIONS (STACKED ROWS) */}
          <section className="space-y-2.5">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <h3 className="text-xs font-bold uppercase tracking-wider text-slate-200">
                  Active Automations
                </h3>
                <span className="rounded-full bg-slate-800/90 border border-slate-700/60 px-2 py-0.5 text-[10px] font-semibold text-slate-300">
                  {automations.filter((a) => a.status === 'running').length} running
                </span>
              </div>
              <button className="flex items-center gap-1 text-xs text-slate-400 hover:text-slate-200 transition-colors">
                <span>View all</span>
                <ArrowRight size={12} />
              </button>
            </div>

            {/* Stacked Automation Items */}
            <div className="flex flex-col gap-2">
              {automations.map((auto) => (
                <div
                  key={auto.id}
                  className="flex items-center justify-between p-2.5 text-xs rounded-2xl border border-slate-800/80 bg-[#0a0f1d]/70 hover:bg-[#0e1528] transition-all shadow-sm"
                >
                  {/* Left: Icon, Name, and Run Count */}
                  <div className="flex items-center gap-3 min-w-[280px]">
                    <div
                      className={`flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full ${
                        auto.status === 'running'
                          ? 'bg-blue-500/20 text-blue-400 border border-blue-500/30'
                          : auto.status === 'scheduled'
                          ? 'bg-slate-800 text-slate-400 border border-slate-700'
                          : 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                      }`}
                    >
                      {auto.status === 'running' ? (
                        <Play size={11} fill="currentColor" />
                      ) : auto.status === 'scheduled' ? (
                        <Clock size={12} />
                      ) : (
                        <Check size={12} />
                      )}
                    </div>

                    <div className="flex flex-col min-w-0">
                      <span className="font-semibold text-white text-xs truncate">
                        {auto.name}
                      </span>
                      <span className="text-[10px] text-slate-400">
                        {auto.totalRuns} runs • Last run {auto.lastRun}
                      </span>
                    </div>
                  </div>

                  {/* Middle: Status or Progress Bar */}
                  <div className="flex-1 max-w-sm px-4">
                    {auto.status === 'running' && auto.progressPercent !== undefined ? (
                      <div className="flex items-center gap-3">
                        <span className="text-[11px] text-slate-300 min-w-[90px] truncate">
                          {auto.currentStep}
                        </span>
                        <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-slate-800">
                          <div
                            className="h-full bg-gradient-to-r from-blue-600 to-indigo-400 rounded-full transition-all duration-500"
                            style={{ width: `${auto.progressPercent}%` }}
                          />
                        </div>
                        <span className="font-mono text-[11px] text-slate-400 min-w-[28px] text-right">
                          {auto.progressPercent}%
                        </span>
                      </div>
                    ) : (
                      <span className="text-[11px] text-slate-400 font-mono">
                        {auto.outputSummary}
                      </span>
                    )}
                  </div>

                  {/* Right: More menu */}
                  <div className="flex items-center gap-2">
                    <button className="text-slate-500 hover:text-slate-300 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
                      <MoreHorizontal size={14} />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>

          {/* SECTION 2: TASK CARD 1 - MAKE 3 SOCIAL MEDIA VIDEOS WITH DELIVERABLES */}
          {tasks
            .filter((t) => t.id === 'task-101')
            .map((task) => (
              <div
                key={task.id}
                className="p-4 rounded-2xl border border-slate-800/80 bg-[#0a0f1d]/80 shadow-md space-y-4"
              >
                {/* Task Header */}
                <div className="flex items-center justify-between pb-3 border-b border-slate-800/70">
                  <div className="flex items-center gap-3">
                    <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-blue-500/10 text-blue-400 border border-blue-500/20">
                      <Inbox size={15} />
                    </div>
                    <div>
                      <h4 className="text-sm font-bold text-white">{task.title}</h4>
                      <p className="text-[11px] text-slate-400 font-medium">
                        {task.subtitle || 'Design → Generate → Polish → Deliver'}
                      </p>
                    </div>
                  </div>

                  <div className="flex items-center gap-3">
                    <span className="text-xs font-mono text-slate-400">{task.elapsed}</span>
                    <span className="flex items-center gap-1.5 rounded-full bg-blue-500/15 border border-blue-500/30 px-2.5 py-0.5 text-xs font-semibold text-blue-300">
                      <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />
                      Running
                    </span>
                  </div>
                </div>

                {/* Step Timeline (Themes -> Generate -> Edit -> Deliver) */}
                <div className="py-1">
                  <div className="flex items-center justify-between">
                    {/* Step 1: Themes */}
                    <div className="flex flex-col items-center">
                      <div className="flex h-6 w-6 items-center justify-center rounded-full bg-emerald-500 text-slate-950 font-bold text-xs shadow-sm">
                        1
                      </div>
                      <span className="mt-1.5 text-xs font-semibold text-slate-200">Themes</span>
                      <span className="text-[10px] text-slate-400">Complete</span>
                    </div>

                    <div className="h-0.5 flex-1 mx-2 bg-emerald-500/60" />

                    {/* Step 2: Generate */}
                    <div className="flex flex-col items-center">
                      <div className="flex h-6 w-6 items-center justify-center rounded-full bg-blue-600 text-white font-bold text-xs ring-4 ring-blue-500/20 shadow-sm">
                        2
                      </div>
                      <span className="mt-1.5 text-xs font-semibold text-blue-400">Generate</span>
                      <span className="text-[10px] text-blue-400 animate-pulse">Processing...</span>
                    </div>

                    <div className="h-0.5 flex-1 mx-2 bg-slate-800" />

                    {/* Step 3: Edit */}
                    <div className="flex flex-col items-center">
                      <div className="flex h-6 w-6 items-center justify-center rounded-full bg-slate-800 text-slate-500 font-bold text-xs border border-slate-700/60">
                        3
                      </div>
                      <span className="mt-1.5 text-xs font-semibold text-slate-400">Edit</span>
                      <span className="text-[10px] text-slate-500">Pending</span>
                    </div>

                    <div className="h-0.5 flex-1 mx-2 bg-slate-800" />

                    {/* Step 4: Deliver */}
                    <div className="flex flex-col items-center">
                      <div className="flex h-6 w-6 items-center justify-center rounded-full bg-slate-800 text-slate-500 font-bold text-xs border border-slate-700/60">
                        4
                      </div>
                      <span className="mt-1.5 text-xs font-semibold text-slate-400">Deliver</span>
                      <span className="text-[10px] text-slate-500">Pending</span>
                    </div>
                  </div>
                </div>

                {/* Generated Deliverables (3) Grid */}
                <div className="pt-2">
                  <div className="text-xs font-bold text-slate-200 mb-2.5">
                    Generated Deliverables ({task.deliverables?.length || 3})
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                    {task.deliverables?.map((item) => (
                      <div
                        key={item.id}
                        className="flex flex-col p-3 rounded-xl border border-slate-800/80 bg-[#0e1424] hover:border-slate-700 transition-all shadow-sm"
                      >
                        {/* Video Thumbnail */}
                        <DeliverableThumbnail
                          type={item.thumbnailType}
                          duration={item.duration}
                          onPlay={() => setActiveVideoPreview(item)}
                        />

                        {/* Title & Description */}
                        <div className="mt-2.5 flex-1">
                          <h5 className="text-xs font-bold text-white truncate">{item.title}</h5>
                          <p className="mt-1 text-[11px] text-slate-400 line-clamp-2 leading-relaxed">
                            {item.prompt}
                          </p>
                        </div>

                        {/* Bottom Actions: Download, View, Menu */}
                        <div className="mt-3 pt-2.5 border-t border-slate-800 flex items-center justify-between gap-1.5">
                          <button
                            onClick={() => handleAcceptDeliverable(task.id, item.id)}
                            className="p-1.5 rounded-lg bg-slate-800/80 hover:bg-slate-700 text-slate-300 border border-slate-700/60 transition-colors"
                            title="Download Deliverable"
                          >
                            <Download size={13} />
                          </button>

                          <button
                            onClick={() => setActiveVideoPreview(item)}
                            className="flex-1 py-1 rounded-lg bg-slate-800/80 hover:bg-slate-700 text-slate-200 border border-slate-700/60 text-xs font-medium text-center transition-colors"
                          >
                            View
                          </button>

                          <button
                            className="p-1.5 rounded-lg bg-slate-800/80 hover:bg-slate-700 text-slate-300 border border-slate-700/60 transition-colors"
                            title="More Options"
                          >
                            <MoreHorizontal size={13} />
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            ))}

          {/* SECTION 3: TASK CARD 2 - FIX COMPOSER QUICK COMMAND (DIFF PREVIEW) */}
          {tasks
            .filter((t) => t.id === 'task-102')
            .map((task) => (
              <div
                key={task.id}
                className="p-4 rounded-2xl border border-slate-800/80 bg-[#0a0f1d]/80 shadow-md space-y-3"
              >
                {/* Task Header */}
                <div className="flex items-center justify-between pb-3 border-b border-slate-800/70">
                  <div className="flex items-center gap-3">
                    <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-blue-500/10 text-blue-400 border border-blue-500/20">
                      <Code size={15} />
                    </div>
                    <div>
                      <h4 className="text-sm font-bold text-white">{task.title}</h4>
                      <p className="text-[11px] text-slate-400 font-medium">
                        {task.subtitle || 'Update keybinding logic and event handling.'}
                      </p>
                    </div>
                  </div>

                  <div className="flex items-center gap-3">
                    <span className="text-xs font-mono text-slate-400">{task.elapsed}</span>
                    <span className="rounded-full bg-amber-500/15 border border-amber-500/30 px-2.5 py-0.5 text-xs font-semibold text-amber-300">
                      Needs Review
                    </span>
                  </div>
                </div>

                {/* Code Diff Box with Line Numbers & "View changes →" button */}
                <div className="flex items-center justify-between p-3 rounded-xl bg-[#060911] border border-slate-800/80 font-mono text-[11px] leading-relaxed">
                  <div className="space-y-1">
                    <div className="flex items-center gap-3 text-red-400">
                      <span className="w-5 text-slate-600 select-none">12</span>
                      <span className="font-bold select-none">-</span>
                      <span>if (text.endsWith('/')) &#123;</span>
                    </div>
                    <div className="flex items-center gap-3 text-emerald-400">
                      <span className="w-5 text-slate-600 select-none">13</span>
                      <span className="font-bold select-none">+</span>
                      <span>if (text.endsWith('/')) &#123;</span>
                    </div>
                    <div className="flex items-center gap-3 text-slate-300">
                      <span className="w-5 text-slate-600 select-none">14</span>
                      <span className="font-bold select-none opacity-0">+</span>
                      <span className="text-slate-300 pl-4">setShowQuickCommands(true)</span>
                    </div>
                    <div className="flex items-center gap-3 text-slate-400">
                      <span className="w-5 text-slate-600 select-none">15</span>
                      <span className="font-bold select-none opacity-0">+</span>
                      <span>&#125;</span>
                    </div>
                  </div>

                  <button className="flex items-center gap-1.5 px-3.5 py-1.5 rounded-xl bg-slate-800/90 hover:bg-slate-700 text-slate-200 border border-slate-700 text-xs font-medium transition-all">
                    <span>View changes</span>
                    <ArrowRight size={12} />
                  </button>
                </div>
              </div>
            ))}
        </div>
      </main>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 3: RIGHT SIDEBAR (SWARM ORCHESTRATOR AI CHAT)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative flex w-80 flex-shrink-0 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* Header */}
        <div className="p-3.5 border-b border-slate-800/80 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30">
              <Sparkles size={14} />
            </div>
            <div>
              <div className="text-xs font-bold text-white">Swarm Orchestrator</div>
              <div className="text-[10px] text-slate-400 flex items-center gap-1">
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
                <span>Online • Ready to help</span>
              </div>
            </div>
          </div>

          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <Maximize2 size={13} />
          </button>
        </div>

        {/* Chat Messages Stream */}
        <div className="flex-1 space-y-3.5 overflow-y-auto p-3.5 text-xs">
          {messages.map((msg) => {
            const isUser = msg.sender === 'user'
            return (
              <div
                key={msg.id}
                className={`flex flex-col ${isUser ? 'items-end' : 'items-start'}`}
              >
                <div
                  className={`max-w-[95%] p-3 text-xs leading-relaxed transition-all ${
                    isUser
                      ? 'rounded-2xl rounded-tr-sm bg-slate-800 text-slate-100 border border-slate-700/60'
                      : 'rounded-2xl rounded-tl-sm bg-[#090d16] border border-slate-800/80 text-slate-200 space-y-2.5'
                  }`}
                >
                  <p className="whitespace-pre-line">{msg.text}</p>

                  {/* Embedded Video Generation Card in Assistant Message */}
                  {msg.videoProgressCard && (
                    <div className="rounded-xl overflow-hidden border border-slate-800 bg-[#060911] p-2 space-y-2">
                      {/* Video graphic preview */}
                      <DeliverableThumbnail type="orbital_data" />

                      {/* Progress bar */}
                      <div className="space-y-1 pt-1">
                        <div className="flex items-center justify-between text-[10px] text-slate-400">
                          <span className="text-slate-300 font-medium">
                            {msg.videoProgressCard.title}
                          </span>
                          <span className="font-mono text-slate-400">
                            {msg.videoProgressCard.clipsLabel}
                          </span>
                        </div>
                        <div className="h-1.5 w-full overflow-hidden rounded-full bg-slate-800">
                          <div
                            className="h-full bg-gradient-to-r from-blue-600 to-indigo-400 rounded-full"
                            style={{ width: `${msg.videoProgressCard.progressPercent}%` }}
                          />
                        </div>
                      </div>
                    </div>
                  )}

                  {/* Embedded Action Button in Assistant Message */}
                  {msg.actionButton && (
                    <button
                      onClick={() => {
                        const target = tasks[0]?.deliverables?.[0]
                        if (target) setActiveVideoPreview(target)
                      }}
                      className="w-full flex items-center justify-between p-2 rounded-xl bg-slate-800/90 hover:bg-slate-700 text-slate-200 border border-slate-700/60 text-xs font-semibold transition-colors mt-2"
                    >
                      <div className="flex items-center gap-1.5">
                        <Play size={10} fill="currentColor" />
                        <span>{msg.actionButton.label}</span>
                      </div>
                      <ArrowRight size={12} />
                    </button>
                  )}
                </div>

                <span className="mt-1 text-[9px] text-slate-500">{msg.timestamp}</span>
              </div>
            )
          })}

          {isTyping && (
            <div className="flex items-center gap-1.5 text-xs text-slate-400">
              <Sparkles size={12} className="animate-spin text-blue-400" />
              <span>Swarm Orchestrator is thinking...</span>
            </div>
          )}
        </div>

        {/* Chat Input Box Composer */}
        <div className="p-3 border-t border-slate-800/80">
          <form
            onSubmit={(e) => {
              e.preventDefault()
              handleSendMessage()
            }}
            className="flex flex-col gap-2 p-2.5 rounded-2xl bg-[#090d16] border border-slate-800"
          >
            <input
              type="text"
              value={inputText}
              onChange={(e) => setInputText(e.target.value)}
              placeholder="Message Swarm Orchestrator..."
              className="w-full bg-transparent text-xs text-white placeholder-slate-500 focus:outline-none"
            />

            <div className="flex items-center justify-between pt-1">
              <div className="flex items-center gap-2 text-slate-400">
                <button
                  type="button"
                  className="hover:text-slate-200 transition-colors p-0.5"
                  title="Attach file"
                >
                  <Paperclip size={14} />
                </button>
                <button
                  type="button"
                  className="hover:text-slate-200 transition-colors p-0.5"
                  title="Mention agent or workspace"
                >
                  <AtSign size={14} />
                </button>
                <button
                  type="button"
                  className="hover:text-slate-200 transition-colors p-0.5"
                  title="Prompt assist"
                >
                  <Sparkles size={14} />
                </button>
              </div>

              <button
                type="submit"
                disabled={!inputText.trim()}
                className="flex h-7 w-7 items-center justify-center rounded-full bg-blue-600 hover:bg-blue-500 text-white shadow-sm transition-all disabled:opacity-40"
              >
                <ArrowUp size={14} />
              </button>
            </div>
          </form>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          MODAL: VIDEO DELIVERABLE PREVIEW DIALOG (DARK NAVY GLASS)
         ───────────────────────────────────────────────────────────── */}
      {activeVideoPreview && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div className="relative flex max-w-2xl w-full flex-col p-5 rounded-3xl border border-slate-800 bg-[#0d121f] shadow-2xl">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <Film size={16} className="text-blue-400" />
                <h3 className="text-sm font-bold text-white">{activeVideoPreview.title}</h3>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => setIsFullscreen(!isFullscreen)}
                  className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
                  title="Toggle Fullscreen"
                >
                  <Maximize2 size={14} />
                </button>
                <button
                  onClick={() => {
                    setActiveVideoPreview(null)
                    setIsPlayingVideo(false)
                  }}
                  className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
                >
                  <X size={16} />
                </button>
              </div>
            </div>

            {/* Video Canvas Artwork Preview */}
            <div className="relative my-4 aspect-video w-full overflow-hidden flex items-center justify-center rounded-2xl border border-slate-800 bg-black">
              <DeliverableThumbnail type={activeVideoPreview.thumbnailType} />
              <div className="absolute inset-0 flex flex-col items-center justify-center z-30">
                <button
                  onClick={() => setIsPlayingVideo(!isPlayingVideo)}
                  className="flex h-14 w-14 items-center justify-center rounded-full bg-blue-600 text-white shadow-2xl transition-transform hover:scale-105 active:scale-95"
                >
                  {isPlayingVideo ? <Pause size={24} /> : <Play size={24} fill="currentColor" />}
                </button>
                <span className="mt-2 text-xs font-mono text-slate-200 font-semibold bg-black/60 px-2 py-0.5 rounded">
                  {isPlayingVideo ? 'Playing Video Stream...' : 'Click to Play Render Preview'}
                </span>
              </div>

              <button
                onClick={() => setIsMuted(!isMuted)}
                className="absolute bottom-2 left-2 z-40 rounded-full bg-black/70 p-1.5 text-slate-300 hover:text-white"
                title={isMuted ? 'Unmute' : 'Mute'}
              >
                <Volume2 size={13} className={isMuted ? 'opacity-40' : ''} />
              </button>
            </div>

            {/* Prompt details */}
            <div className="space-y-1.5 text-xs">
              <div>
                <span className="text-slate-400 font-semibold">Worker Prompt: </span>
                <span className="text-slate-200">{activeVideoPreview.prompt}</span>
              </div>
              <div className="flex items-center gap-4 text-slate-400 font-mono text-[11px]">
                <span>Duration: {activeVideoPreview.duration}</span>
                <span>Aspect: {activeVideoPreview.videoAspect || '16:9'}</span>
                <span>Render Time: {activeVideoPreview.metrics?.renderTime}</span>
              </div>
            </div>

            {/* Footer action buttons */}
            <div className="mt-4 flex items-center justify-end gap-2 border-t border-slate-800 pt-3">
              <button
                onClick={(e) => {
                  const parentTask = tasks.find((t) =>
                    t.deliverables?.some((d) => d.id === activeVideoPreview.id)
                  )
                  if (parentTask) {
                    handleAcceptDeliverable(parentTask.id, activeVideoPreview.id, e)
                  }
                  setActiveVideoPreview(null)
                }}
                className="flex items-center gap-2 px-5 py-2 text-xs font-bold text-white rounded-xl bg-blue-600 hover:bg-blue-500 shadow-md transition-all active:scale-95"
              >
                <CheckCircle2 size={14} />
                <span>Accept Deliverable</span>
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
