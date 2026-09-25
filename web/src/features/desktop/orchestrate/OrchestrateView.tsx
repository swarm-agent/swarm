import { useState, useMemo } from 'react'
import {
  Activity,
  ArrowRight,
  ArrowUp,
  AtSign,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Code,
  Columns3,
  Film,
  Folder,
  Home,
  Layers,
  ListFilter,
  Maximize2,
  MoreHorizontal,
  Paperclip,
  Pause,
  Play,
  Plus,
  Search,
  Settings,
  Sparkles,
  SplitSquareVertical,
  Volume2,
  X,
  Zap,
} from 'lucide-react'
import {
  MOCK_AUTOMATIONS,
  MOCK_CHAT_MESSAGES,
  MOCK_DEPLOYED_WORKERS,
  MOCK_PROJECTS,
  MOCK_100_TASKS,
} from './orchestrate-mock-data'
import { ORCHESTRATE_THEME_IDS, ORCHESTRATE_THEMES } from './orchestrate-themes'
import {
  DeployedWorker,
  MediaDeliverable,
  MiddleCanvasVariant,
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
 * Thumbnail graphic renderer for the video deliverables with modern high-craft aesthetics
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
      {type === 'cyber_lattice' && (
        <div className="absolute inset-0 bg-gradient-to-br from-[#0c162d] via-[#091024] to-[#040814] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-60" viewBox="0 0 200 112">
            <defs>
              <linearGradient id="cyber-grad" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="#3b82f6" stopOpacity="0.8" />
                <stop offset="100%" stopColor="#06b6d4" stopOpacity="0.3" />
              </linearGradient>
            </defs>
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
            <circle cx="100" cy="56" r="40" fill="#3b82f6" opacity="0.15" filter="blur(8px)" />
            <path d="M 100 32 L 128 48 L 100 64 L 72 48 Z" fill="#1e293b" stroke="#818cf8" strokeWidth="0.8" />
            <path d="M 72 48 L 100 64 L 100 94 L 72 78 Z" fill="#0f172a" stroke="#6366f1" strokeWidth="0.8" />
            <path d="M 128 48 L 100 64 L 100 94 L 128 78 Z" fill="#1e1b4b" stroke="#3b82f6" strokeWidth="0.8" />
            <line x1="100" y1="32" x2="100" y2="15" stroke="#93c5fd" strokeWidth="0.8" strokeDasharray="2 2" />
            <circle cx="100" cy="14" r="2" fill="#bfdbfe" />
          </svg>
        </div>
      )}

      {type === 'orbital_data' && (
        <div className="absolute inset-0 bg-gradient-to-b from-[#030712] via-[#081226] to-[#0d2247] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-80" viewBox="0 0 200 112">
            <path d="M -20 120 Q 100 65 220 120 Z" fill="#1e3a8a" opacity="0.8" />
            <path d="M -20 120 Q 100 64 220 120" stroke="#60a5fa" strokeWidth="1.5" fill="none" opacity="0.9" />
            <circle cx="30" cy="20" r="0.8" fill="#fff" opacity="0.8" />
            <circle cx="140" cy="25" r="0.8" fill="#fff" opacity="0.7" />
            <line x1="80" y1="40" x2="120" y2="40" stroke="#38bdf8" strokeWidth="0.8" />
            <circle cx="100" cy="40" r="3" fill="#38bdf8" />
          </svg>
        </div>
      )}

      {!['cyber_lattice', 'neural_core', 'orbital_data'].includes(type || '') && (
        <div className="absolute inset-0 bg-gradient-to-br from-slate-900 to-slate-950 flex items-center justify-center">
          <Film size={20} className="text-slate-600" />
        </div>
      )}

      <div className="relative z-10 flex h-full w-full items-center justify-center">
        <div className="flex h-9 w-9 items-center justify-center rounded-full bg-slate-900/80 text-white backdrop-blur-md border border-white/20 shadow-xl transition-transform group-hover/thumb:scale-110">
          <Play size={13} fill="currentColor" className="ml-0.5 text-white" />
        </div>
      </div>

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

  // Middle canvas layout variant state (5 distinct variants)
  const [middleVariant, setMiddleVariant] = useState<MiddleCanvasVariant>('matrix')

  // Search & Filters for 100+ tasks
  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'running' | 'needs_review' | 'queued' | 'completed'>('all')
  const [selectedTag] = useState<string>('all')

  // Matrix expandable drawer state
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>('task-101')

  // Split Studio selected task state
  const [selectedTaskId, setSelectedTaskId] = useState<string>('task-101')

  // Automations & Workers state
  const [automations] = useState<RunningAutomation[]>(MOCK_AUTOMATIONS)
  const [deployedWorkers] = useState<DeployedWorker[]>(MOCK_DEPLOYED_WORKERS)
  const [tasks, setTasks] = useState<RunningTask[]>(MOCK_100_TASKS)

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

  // Derived filtered tasks for 100-task handling
  const filteredTasks = useMemo(() => {
    return tasks.filter((task) => {
      const matchesSearch =
        searchQuery === '' ||
        task.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.subtitle?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.id.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesStatus = statusFilter === 'all' || task.status === statusFilter
      const matchesTag = selectedTag === 'all' || task.tags?.includes(selectedTag)

      return matchesSearch && matchesStatus && matchesTag
    })
  }, [tasks, searchQuery, statusFilter, selectedTag])

  const selectedTaskForSplit = useMemo(() => {
    return tasks.find((t) => t.id === selectedTaskId) || tasks[0]
  }, [tasks, selectedTaskId])

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

  // Count summaries
  const runningCount = tasks.filter((t) => t.status === 'running').length
  const reviewCount = tasks.filter((t) => t.status === 'needs_review').length
  const queuedCount = tasks.filter((t) => t.status === 'queued').length
  const completedCount = tasks.filter((t) => t.status === 'completed').length

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

        {/* 5 Themes Selector */}
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

        {/* User HUD */}
        <div className="p-3 border-t border-slate-800/80 flex items-center justify-between bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full bg-slate-800 text-slate-200 font-bold text-[10px] border border-slate-700">
              JD
            </div>
            <div className="flex flex-col min-w-0">
              <span className="text-xs font-semibold text-slate-200 truncate">Jordan Diaz</span>
              <span className="text-[10px] text-slate-500 truncate">jordan@swarmagent.dev</span>
            </div>
          </div>
          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <MoreHorizontal size={14} />
          </button>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (5 INTERACTIVE CANVAS VARIANTS)
         ───────────────────────────────────────────────────────────── */}
      <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* TOP TOOLBAR: AUTOMATION OVERVIEW & 5-VARIANT SWITCHER DOCK */}
        <div className="flex flex-col border-b border-slate-800/80 bg-[#0a0f1d]/60">
          <div className="flex items-center justify-between p-3.5 pb-2.5">
            <div className="flex items-center gap-3">
              <div>
                <div className="flex items-center gap-2">
                  <h1 className="text-base font-bold tracking-tight text-white">Automation Overview</h1>
                  <span className="rounded-full bg-blue-500/10 border border-blue-500/30 px-2 py-0.5 text-[10px] font-semibold text-blue-400">
                    100 Tasks Total
                  </span>
                </div>
                <p className="text-[11px] text-slate-400 mt-0.5">
                  Autonomous workers executing jobs across {selectedProject.name}
                </p>
              </div>
            </div>

            {/* Quick Action buttons */}
            <div className="flex items-center gap-2">
              <button
                onClick={() => handleSendMessage('deploy worker')}
                className="flex items-center gap-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
              >
                <Plus size={13} />
                <span>Deploy Worker</span>
              </button>
              <button
                onClick={() => handleSendMessage('run full testbench')}
                className="flex items-center gap-1.5 rounded-xl bg-slate-800/80 hover:bg-slate-700/80 border border-slate-700/70 text-slate-200 text-xs px-3 py-1.5 transition-all active:scale-95"
              >
                <Play size={11} fill="currentColor" />
                <span>Testbench</span>
              </button>
            </div>
          </div>

          {/* 5-VARIANT SELECTOR DOCK: Switch between 5 distinct middle layouts */}
          <div className="px-3.5 pb-2.5 flex items-center justify-between border-t border-slate-800/50 pt-2 text-xs">
            <div className="flex items-center gap-1.5">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500 mr-1">
                Canvas View:
              </span>
              <div className="flex items-center gap-1 rounded-xl bg-[#080c16] p-1 border border-slate-800">
                <button
                  onClick={() => setMiddleVariant('matrix')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                    middleVariant === 'matrix'
                      ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title="Variant 1: High-Density Compact Matrix with Drawer (Built for 100+ tasks)"
                >
                  <ListFilter size={12} />
                  <span>1. Compact Matrix</span>
                </button>

                <button
                  onClick={() => setMiddleVariant('kanban')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                    middleVariant === 'kanban'
                      ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title="Variant 2: Mission Pipeline Kanban (Multi-column stage workflow)"
                >
                  <Columns3 size={12} />
                  <span>2. Pipeline Kanban</span>
                </button>

                <button
                  onClick={() => setMiddleVariant('fleet')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                    middleVariant === 'fleet'
                      ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title="Variant 3: Autonomous Worker Fleet (Workers deploying their own jobs)"
                >
                  <Bot size={12} />
                  <span>3. Worker Fleet</span>
                </button>

                <button
                  onClick={() => setMiddleVariant('split')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                    middleVariant === 'split'
                      ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title="Variant 4: Split Studio Console (Master-Detail 2-pane live inspector)"
                >
                  <SplitSquareVertical size={12} />
                  <span>4. Split Studio</span>
                </button>

                <button
                  onClick={() => setMiddleVariant('timeline')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                    middleVariant === 'timeline'
                      ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                      : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                  }`}
                  title="Variant 5: Timeline Activity Stream (Chronological progress & attachments)"
                >
                  <Activity size={12} />
                  <span>5. Timeline Stream</span>
                </button>
              </div>
            </div>

            {/* Quick status counter summary */}
            <div className="flex items-center gap-2 font-mono text-[10px]">
              <span className="text-blue-400">● {runningCount} Running</span>
              <span className="text-amber-400">● {reviewCount} Review</span>
              <span className="text-slate-500">● {queuedCount} Queued</span>
              <span className="text-emerald-400">● {completedCount} Done</span>
            </div>
          </div>
        </div>

        {/* COMPACT ACTIVE AUTOMATION TICKER (Noiseless, tiny progress & attachments) */}
        <div className="px-4 py-2 border-b border-slate-800/80 bg-[#080d19]/80 flex items-center justify-between gap-4 text-xs">
          <div className="flex items-center gap-2.5 min-w-0">
            <span className="flex h-2 w-2 rounded-full bg-blue-500 animate-pulse flex-shrink-0" />
            <div className="flex items-center gap-2 min-w-0">
              <span className="font-semibold text-white truncate text-[11px]">
                {automations[0].name}
              </span>
              <span className="text-[10px] text-slate-400 truncate">
                • {automations[0].currentStep}
              </span>
            </div>
          </div>

          <div className="flex items-center gap-3 flex-shrink-0">
            {/* Micro stage indicators */}
            <div className="flex items-center gap-1 text-[10px] font-mono">
              <span className="px-1.5 py-0.5 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                Themes ✓
              </span>
              <span className="text-slate-600">→</span>
              <span className="px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400 border border-blue-500/30 font-bold">
                Generate 60%
              </span>
              <span className="text-slate-600">→</span>
              <span className="px-1.5 py-0.5 rounded bg-slate-800/60 text-slate-400">
                Edit
              </span>
              <span className="text-slate-600">→</span>
              <span className="px-1.5 py-0.5 rounded bg-slate-800/60 text-slate-400">
                Deliver
              </span>
            </div>

            {/* Media attachments chip */}
            <div className="flex items-center gap-1 px-2 py-0.5 rounded-lg bg-blue-500/10 text-blue-300 border border-blue-500/20 text-[10px] font-semibold">
              <Film size={11} />
              <span>3 Videos Ready</span>
            </div>
          </div>
        </div>

        {/* MAIN BODY: 5 DISTINCT VARIANTS */}
        <div className="flex-1 overflow-hidden flex flex-col">
          {/* ─────────────────────────────────────────────────────────────
              VARIANT 1: COMPACT MATRIX & DRAWER (High Density for 100+ tasks)
             ───────────────────────────────────────────────────────────── */}
          {middleVariant === 'matrix' && (
            <div className="flex-1 flex flex-col overflow-hidden p-3.5 space-y-3">
              {/* Search & Filter bar for 100+ tasks */}
              <div className="flex items-center justify-between gap-3">
                <div className="flex-1 relative">
                  <Search size={13} className="absolute left-3 top-2.5 text-slate-500" />
                  <input
                    type="text"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder="Search 100 tasks by title, worker, or tag..."
                    className="w-full bg-[#080c16] border border-slate-800 rounded-xl pl-8 pr-3 py-1.5 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500/40"
                  />
                </div>

                <div className="flex items-center gap-1.5">
                  {(['all', 'running', 'needs_review', 'queued', 'completed'] as const).map((st) => (
                    <button
                      key={st}
                      onClick={() => setStatusFilter(st)}
                      className={`px-2.5 py-1 rounded-lg text-[10px] font-semibold uppercase tracking-wider transition-all ${
                        statusFilter === st
                          ? 'bg-slate-700 text-white shadow-sm'
                          : 'bg-slate-800/60 text-slate-400 hover:text-slate-200'
                      }`}
                    >
                      {st === 'all'
                        ? `All (${tasks.length})`
                        : st === 'running'
                        ? `Running (${runningCount})`
                        : st === 'needs_review'
                        ? `Review (${reviewCount})`
                        : st === 'queued'
                        ? `Queued (${queuedCount})`
                        : `Done (${completedCount})`}
                    </button>
                  ))}
                </div>
              </div>

              {/* High-density task rows with expandable drawers */}
              <div className="flex-1 overflow-y-auto space-y-1.5 pr-1">
                {filteredTasks.map((t) => {
                  const isExpanded = expandedTaskId === t.id
                  return (
                    <div
                      key={t.id}
                      className="rounded-xl border border-slate-800/80 bg-[#0a0f1d]/70 hover:border-slate-700/80 transition-all overflow-hidden"
                    >
                      {/* Compact Task Header Row */}
                      <div
                        onClick={() => setExpandedTaskId(isExpanded ? null : t.id)}
                        className="flex items-center justify-between p-2.5 cursor-pointer hover:bg-white/[0.02]"
                      >
                        <div className="flex items-center gap-2.5 min-w-0 flex-1">
                          <span
                            className={`h-2 w-2 rounded-full flex-shrink-0 ${
                              t.status === 'running'
                                ? 'bg-blue-400 animate-pulse'
                                : t.status === 'needs_review'
                                ? 'bg-amber-400'
                                : t.status === 'completed'
                                ? 'bg-emerald-400'
                                : 'bg-slate-600'
                            }`}
                          />
                          <div className="flex h-5 w-5 items-center justify-center rounded-md bg-slate-800 text-slate-400 flex-shrink-0">
                            {t.agentType === 'coder' ? (
                              <Code size={11} />
                            ) : t.agentType === 'designer' ? (
                              <Film size={11} />
                            ) : (
                              <Bot size={11} />
                            )}
                          </div>
                          <span className="font-semibold text-xs text-white truncate max-w-sm">
                            {t.title}
                          </span>
                          {t.workerName && (
                            <span className="rounded bg-slate-800/80 px-1.5 py-0.5 text-[9px] font-mono text-slate-400 truncate">
                              @{t.workerName}
                            </span>
                          )}
                        </div>

                        {/* Middle: Micro-stages stepper & media count */}
                        <div className="flex items-center gap-3">
                          {t.stepTimeline && (
                            <div className="flex items-center gap-1 font-mono text-[9px]">
                              {t.stepTimeline.map((step) => (
                                <span
                                  key={step.step}
                                  className={`px-1 rounded ${
                                    step.status === 'complete'
                                      ? 'text-emerald-400 bg-emerald-500/10'
                                      : step.status === 'processing'
                                      ? 'text-blue-400 bg-blue-500/20 font-bold'
                                      : 'text-slate-600'
                                  }`}
                                >
                                  {step.label}
                                </span>
                              ))}
                            </div>
                          )}

                          {t.deliverables && t.deliverables.length > 0 && (
                            <span className="flex items-center gap-1 rounded bg-blue-500/15 border border-blue-500/30 px-1.5 py-0.5 text-[9px] font-semibold text-blue-300">
                              <Film size={9} />
                              {t.deliverables.length} clips
                            </span>
                          )}

                          <span className="text-[10px] font-mono text-slate-500 min-w-[45px] text-right">
                            {t.elapsed}
                          </span>

                          <span className="text-slate-500">
                            {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                          </span>
                        </div>
                      </div>

                      {/* Expandable Drawer with Deliverables, Video Player, and Diff View */}
                      {isExpanded && (
                        <div className="border-t border-slate-800/80 bg-[#070b15] p-3 space-y-3">
                          {t.deliverables && (
                            <div>
                              <div className="text-[11px] font-bold text-slate-300 mb-2 flex items-center justify-between">
                                <span>Attached Video Deliverables ({t.deliverables.length})</span>
                                <span className="text-[10px] text-slate-500 font-mono">
                                  Click thumbnail to play full preview
                                </span>
                              </div>
                              <div className="grid grid-cols-3 gap-2.5">
                                {t.deliverables.map((item) => (
                                  <div
                                    key={item.id}
                                    className="p-2 rounded-xl bg-[#0a0f1d] border border-slate-800"
                                  >
                                    <DeliverableThumbnail
                                      type={item.thumbnailType}
                                      duration={item.duration}
                                      onPlay={() => setActiveVideoPreview(item)}
                                    />
                                    <div className="mt-2 text-xs font-bold text-white truncate">
                                      {item.title}
                                    </div>
                                    <div className="mt-1 flex items-center justify-between pt-1 border-t border-slate-800/80">
                                      <button
                                        onClick={(e) => handleAcceptDeliverable(t.id, item.id, e)}
                                        className="text-[10px] font-semibold text-emerald-400 hover:underline"
                                      >
                                        Accept
                                      </button>
                                      <button
                                        onClick={() => setActiveVideoPreview(item)}
                                        className="text-[10px] text-slate-400 hover:text-white"
                                      >
                                        Inspect
                                      </button>
                                    </div>
                                  </div>
                                ))}
                              </div>
                            </div>
                          )}

                          {t.diffLines && (
                            <div>
                              <div className="text-[11px] font-bold text-slate-300 mb-1.5">
                                Code Changes Diff
                              </div>
                              <div className="p-2.5 rounded-xl bg-[#05070e] border border-slate-800 font-mono text-[10px] space-y-0.5">
                                {t.diffLines.map((line, idx) => (
                                  <div
                                    key={idx}
                                    className={`flex items-center gap-2 ${
                                      line.type === 'del'
                                        ? 'text-red-400'
                                        : line.type === 'add'
                                        ? 'text-emerald-400'
                                        : 'text-slate-400'
                                    }`}
                                  >
                                    <span className="w-4 text-slate-600 select-none">
                                      {line.lineNum}
                                    </span>
                                    <span>{line.text}</span>
                                  </div>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>
          )}

          {/* ─────────────────────────────────────────────────────────────
              VARIANT 2: MISSION PIPELINE KANBAN (Multi-column Stage Workflow)
             ───────────────────────────────────────────────────────────── */}
          {middleVariant === 'kanban' && (
            <div className="flex-1 flex overflow-x-auto p-3.5 gap-3">
              {[
                {
                  id: 'queued',
                  label: 'Queued Backlog',
                  tasks: tasks.filter((t) => t.status === 'queued'),
                  color: 'border-slate-800 text-slate-400',
                },
                {
                  id: 'running',
                  label: 'Active In-Flight',
                  tasks: tasks.filter((t) => t.status === 'running'),
                  color: 'border-blue-500/30 text-blue-400',
                },
                {
                  id: 'needs_review',
                  label: 'In Review / Deliverables',
                  tasks: tasks.filter((t) => t.status === 'needs_review'),
                  color: 'border-amber-500/30 text-amber-400',
                },
                {
                  id: 'completed',
                  label: 'Completed / Shipped',
                  tasks: tasks.filter((t) => t.status === 'completed'),
                  color: 'border-emerald-500/30 text-emerald-400',
                },
              ].map((col) => (
                <div
                  key={col.id}
                  className="flex-1 min-w-[240px] flex flex-col rounded-2xl bg-[#090d18] border border-slate-800/80 overflow-hidden"
                >
                  <div className="p-3 border-b border-slate-800 flex items-center justify-between">
                    <span className="text-xs font-bold text-white">{col.label}</span>
                    <span className={`px-2 py-0.5 rounded-full text-[10px] font-mono font-bold bg-slate-800 ${col.color}`}>
                      {col.tasks.length}
                    </span>
                  </div>

                  <div className="flex-1 overflow-y-auto p-2 space-y-2">
                    {col.tasks.map((task) => (
                      <div
                        key={task.id}
                        className="p-3 rounded-xl border border-slate-800/90 bg-[#0d1222] hover:border-slate-700 transition-all space-y-2"
                      >
                        <div className="flex items-center justify-between text-[10px] text-slate-400">
                          <span className="font-mono">#{task.id}</span>
                          <span>{task.elapsed}</span>
                        </div>
                        <div className="text-xs font-semibold text-white leading-snug">
                          {task.title}
                        </div>
                        {task.workerName && (
                          <div className="text-[10px] font-mono text-slate-400 truncate">
                            @{task.workerName}
                          </div>
                        )}
                        {task.deliverables && (
                          <div
                            onClick={() => setActiveVideoPreview(task.deliverables![0])}
                            className="flex items-center gap-1.5 p-1.5 rounded-lg bg-blue-500/10 border border-blue-500/20 text-blue-300 text-[10px] cursor-pointer hover:bg-blue-500/20"
                          >
                            <Film size={11} />
                            <span>Preview {task.deliverables.length} Media Deliverables</span>
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* ─────────────────────────────────────────────────────────────
              VARIANT 3: AUTONOMOUS WORKER FLEET (Workers Deploying Jobs)
             ───────────────────────────────────────────────────────────── */}
          {middleVariant === 'fleet' && (
            <div className="flex-1 overflow-y-auto p-4 space-y-4">
              <div className="flex items-center justify-between">
                <div>
                  <h3 className="text-sm font-bold text-white">Deployed Autonomous Workers</h3>
                  <p className="text-[11px] text-slate-400">
                    Workers running scheduled & on-demand task jobs
                  </p>
                </div>
                <button
                  onClick={() => handleSendMessage('deploy worker')}
                  className="flex items-center gap-1.5 px-3 py-1 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium"
                >
                  <Plus size={12} />
                  <span>New Worker</span>
                </button>
              </div>

              {/* 4 Deployed Worker Cards */}
              <div className="grid grid-cols-2 gap-3">
                {deployedWorkers.map((worker) => (
                  <div
                    key={worker.id}
                    className="p-3.5 rounded-2xl border border-slate-800/80 bg-[#0a0f1d] space-y-3"
                  >
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2.5">
                        <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-blue-500/10 text-blue-400 border border-blue-500/20">
                          <Bot size={16} />
                        </div>
                        <div>
                          <h4 className="text-xs font-bold text-white">{worker.name}</h4>
                          <p className="text-[10px] text-slate-400">{worker.role}</p>
                        </div>
                      </div>
                      <span className="px-2 py-0.5 rounded-full text-[9px] font-mono font-semibold uppercase bg-slate-800 text-slate-300">
                        {worker.triggerKind}
                      </span>
                    </div>

                    <div className="flex items-center justify-between text-[11px] border-t border-slate-800/80 pt-2 text-slate-400 font-mono">
                      <span>Schedule: {worker.scheduleLabel}</span>
                      <span className="text-blue-400 font-bold">{worker.activeJobsCount} active jobs</span>
                    </div>

                    {worker.currentJobTitle && (
                      <div className="p-2 rounded-xl bg-[#060912] border border-slate-800 text-[10px] text-slate-300 flex items-center justify-between">
                        <span className="truncate">Current: {worker.currentJobTitle}</span>
                        <Play size={10} className="text-blue-400 flex-shrink-0" />
                      </div>
                    )}
                  </div>
                ))}
              </div>

              {/* Worker-Partitioned Tasks */}
              <div className="pt-2">
                <h4 className="text-xs font-bold text-white mb-2">Worker Task Assignment Queues</h4>
                <div className="space-y-2">
                  {tasks.slice(0, 8).map((task) => (
                    <div
                      key={task.id}
                      className="p-2.5 rounded-xl border border-slate-800 bg-[#090d17] flex items-center justify-between text-xs"
                    >
                      <div className="flex items-center gap-3">
                        <span className="font-mono text-[10px] text-slate-400">#{task.id}</span>
                        <span className="font-semibold text-white truncate max-w-sm">{task.title}</span>
                      </div>
                      <span className="text-[10px] font-mono text-slate-400">@{task.workerName}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* ─────────────────────────────────────────────────────────────
              VARIANT 4: SPLIT STUDIO CONSOLE (Master-Detail Live Inspector)
             ───────────────────────────────────────────────────────────── */}
          {middleVariant === 'split' && (
            <div className="flex-1 flex overflow-hidden">
              {/* Left Pane: High-Density 100-Task List */}
              <div className="w-[42%] border-r border-slate-800/80 flex flex-col p-3 overflow-hidden space-y-2">
                <div className="relative">
                  <Search size={12} className="absolute left-2.5 top-2 text-slate-500" />
                  <input
                    type="text"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder="Search tasks..."
                    className="w-full bg-[#080c16] border border-slate-800 rounded-lg pl-7 pr-2 py-1 text-xs text-white placeholder-slate-500 focus:outline-none"
                  />
                </div>

                <div className="flex-1 overflow-y-auto space-y-1 pr-1">
                  {filteredTasks.map((t) => {
                    const isSelected = t.id === selectedTaskId
                    return (
                      <div
                        key={t.id}
                        onClick={() => setSelectedTaskId(t.id)}
                        className={`p-2 rounded-xl cursor-pointer transition-all border ${
                          isSelected
                            ? 'bg-blue-600/15 border-blue-500/40 text-white shadow-sm'
                            : 'bg-[#0a0f1d] border-slate-800/70 hover:border-slate-700 text-slate-300'
                        }`}
                      >
                        <div className="flex items-center justify-between text-[11px] font-bold">
                          <span className="truncate">{t.title}</span>
                          <span className="font-mono text-[9px] text-slate-400">{t.elapsed}</span>
                        </div>
                        <div className="flex items-center justify-between mt-1 text-[9px] text-slate-400">
                          <span>@{t.workerName || 'Unassigned'}</span>
                          <span className="uppercase font-semibold">{t.status}</span>
                        </div>
                      </div>
                    )
                  })}
                </div>
              </div>

              {/* Right Pane: Live Session & Deliverable Inspector */}
              <div className="flex-1 p-4 overflow-y-auto space-y-4 bg-[#090d18]/50">
                <div className="flex items-center justify-between border-b border-slate-800 pb-3">
                  <div>
                    <h3 className="text-sm font-bold text-white">{selectedTaskForSplit.title}</h3>
                    <p className="text-xs text-slate-400">{selectedTaskForSplit.subtitle}</p>
                  </div>
                  <span className="px-2.5 py-0.5 rounded-full text-xs font-semibold bg-blue-500/20 text-blue-300 border border-blue-500/30">
                    {selectedTaskForSplit.status}
                  </span>
                </div>

                {/* 4-Step Stepper */}
                <div className="p-3 rounded-2xl bg-[#0a0f1d] border border-slate-800">
                  <div className="text-xs font-bold text-slate-300 mb-2">Execution Stages</div>
                  <div className="grid grid-cols-4 gap-2 text-center text-xs">
                    {['Init Worktree', 'Agent Exec', 'Audit Diff', 'Deliver'].map((stage, idx) => (
                      <div key={idx} className="p-2 rounded-xl bg-[#070b16] border border-slate-800">
                        <span className="text-[10px] font-mono text-slate-500">Stage {idx + 1}</span>
                        <div className="font-semibold text-white text-[11px] truncate mt-0.5">{stage}</div>
                      </div>
                    ))}
                  </div>
                </div>

                {/* Attached Deliverables Gallery */}
                {selectedTaskForSplit.deliverables && (
                  <div className="space-y-2">
                    <div className="text-xs font-bold text-slate-300">Generated Media Deliverables</div>
                    <div className="grid grid-cols-2 gap-3">
                      {selectedTaskForSplit.deliverables.map((deliv) => (
                        <div key={deliv.id} className="p-3 rounded-2xl bg-[#0a0f1d] border border-slate-800">
                          <DeliverableThumbnail
                            type={deliv.thumbnailType}
                            duration={deliv.duration}
                            onPlay={() => setActiveVideoPreview(deliv)}
                          />
                          <div className="mt-2 text-xs font-bold text-white truncate">{deliv.title}</div>
                          <button
                            onClick={() => setActiveVideoPreview(deliv)}
                            className="mt-2 w-full py-1 text-center text-xs font-semibold bg-blue-600 rounded-lg text-white"
                          >
                            Inspect Video
                          </button>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* ─────────────────────────────────────────────────────────────
              VARIANT 5: TIMELINE ACTIVITY STREAM (Chronological Flow)
             ───────────────────────────────────────────────────────────── */}
          {middleVariant === 'timeline' && (
            <div className="flex-1 overflow-y-auto p-4 space-y-4">
              <div className="text-xs font-bold text-slate-400 uppercase tracking-wider">
                Active Right Now
              </div>

              <div className="relative pl-6 border-l-2 border-blue-500/40 space-y-4">
                {tasks.slice(0, 5).map((t) => (
                  <div
                    key={t.id}
                    className="relative p-3 rounded-2xl border border-slate-800/80 bg-[#0a0f1d] space-y-2"
                  >
                    <span className="absolute -left-[31px] top-3 h-3 w-3 rounded-full bg-blue-500 ring-4 ring-blue-500/20" />
                    <div className="flex items-center justify-between">
                      <span className="text-xs font-bold text-white">{t.title}</span>
                      <span className="text-[10px] font-mono text-blue-400">{t.elapsed}</span>
                    </div>
                    <p className="text-[11px] text-slate-400">{t.subtitle}</p>

                    {t.deliverables && (
                      <div className="flex gap-2 pt-1 overflow-x-auto">
                        {t.deliverables.map((d) => (
                          <div
                            key={d.id}
                            onClick={() => setActiveVideoPreview(d)}
                            className="w-36 flex-shrink-0 cursor-pointer"
                          >
                            <DeliverableThumbnail type={d.thumbnailType} duration={d.duration} />
                            <div className="text-[10px] font-medium text-white truncate mt-1">
                              {d.title}
                            </div>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
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
                      <DeliverableThumbnail type="orbital_data" />
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
