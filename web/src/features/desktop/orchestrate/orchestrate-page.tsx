import React, { useState } from 'react'
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
  Pause,
  Play,
  Plus,
  Send,
  Sparkles,
  Terminal,
  Zap,
} from 'lucide-react'
import { ORCHESTRATE_THEMES } from './orchestrate-themes'
import {
  MOCK_AUTOMATIONS,
  MOCK_CHAT_MESSAGES,
  MOCK_DELIVERABLES,
  MOCK_PROJECTS,
  MOCK_RUNNING_TASKS,
} from './orchestrate-mock-data'
import {
  MediaDeliverable,
  OrchestrateThemeId,
  OrchestratorMessage,
  ProjectSummary,
  RunningAutomation,
} from './orchestrate-types'

export function OrchestratePage({
  workspaceSlug: _workspaceSlug,
  onNavigateHome,
}: {
  workspaceSlug?: string
  onNavigateHome?: () => void
}) {
  const [currentThemeId, setCurrentThemeId] = useState<OrchestrateThemeId>('obsidian')
  const theme = ORCHESTRATE_THEMES[currentThemeId]

  const [projects] = useState<ProjectSummary[]>(MOCK_PROJECTS)
  const [selectedProjectId, setSelectedProjectId] = useState<string>(projects[0].id)
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  const [automations] = useState<RunningAutomation[]>(MOCK_AUTOMATIONS)
  const [deliverables, setDeliverables] = useState<MediaDeliverable[]>(MOCK_DELIVERABLES)
  const [mediaFilter, setMediaFilter] = useState<'all' | 'video' | 'code' | 'report'>('all')

  const [activeVideoPreview, setActiveVideoPreview] = useState<MediaDeliverable | null>(null)
  const [isPlayingVideo, setIsPlayingVideo] = useState(false)

  // Chat state
  const [messages, setMessages] = useState<OrchestratorMessage[]>(MOCK_CHAT_MESSAGES)
  const [inputText, setInputText] = useState('')
  const [isTyping, setIsTyping] = useState(false)

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

    // Simulate Orchestrator intelligent response
    setTimeout(() => {
      let replyText = `Understood. Analyzing project context for "${selectedProject.name}"...`
      const newDelivs: MediaDeliverable[] = []

      if (text.toLowerCase().includes('video')) {
        replyText = `Dispatched to Video Swarm Worker! Generating requested social media variations with localized project themes. I've added the new job to your top automations bar and deliverable canvas.`
        
        // Add a new mock deliverable to middle canvas
        const newVideo: MediaDeliverable = {
          id: `deliv-vid-${Date.now()}`,
          title: `Project Launch Promo (Variation ${deliverables.length + 1})`,
          type: 'video',
          videoAspect: '9:16',
          duration: '0:15',
          status: 'ready',
          createdAt: 'Just now',
          author: 'Video Swarm Worker',
          prompt: 'Dynamic split-second sequence of high-tech interface components aligning with kinetic typography',
          metrics: { renderTime: '24s', tokens: '980', views: 'Preview ready' },
        }
        newDelivs.push(newVideo)
        setDeliverables((prev) => [newVideo, ...prev])
      } else if (text.toLowerCase().includes('test') || text.toLowerCase().includes('testbench')) {
        replyText = `Triggered isolated systemd-nspawn testbench lease on slot-1 for "${selectedProject.repoPath}". Verifying pre-push hermetic invariants.`
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
    }, 900)
  }

  const handleAcceptDeliverable = (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    setDeliverables((prev) =>
      prev.map((d) => (d.id === id ? { ...d, status: 'accepted' } : d))
    )
  }

  return (
    <div
      className={`flex h-screen w-screen flex-col overflow-hidden font-sans ${theme.bgClass} ${theme.textPrimaryClass}`}
      style={theme.customVars as React.CSSProperties}
    >
      {/* ─────────────────────────────────────────────────────────────
          TOP CONTROL BAR & THEME SWITCHER
         ───────────────────────────────────────────────────────────── */}
      <header
        className={`flex h-12 flex-shrink-0 items-center justify-between border-b px-4 ${theme.panelBgClass} ${theme.borderClass}`}
      >
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <div
              className={`flex h-7 w-7 items-center justify-center rounded-lg ${theme.accentBgClass} ${theme.accentTextClass} ${theme.glowClass}`}
            >
              <Cpu size={16} />
            </div>
            <div className="flex flex-col">
              <span className="text-xs font-bold tracking-wider uppercase">
                Swarm Orchestrate
              </span>
              <span className={`text-[10px] ${theme.textSecondaryClass}`}>
                Project Hub & Lean Mission Control
              </span>
            </div>
          </div>

          <div className="mx-2 h-4 w-[1px] bg-slate-700/50" />

          {/* Active Project Breadcrumb */}
          <div className="flex items-center gap-1.5 rounded-md bg-black/20 px-2.5 py-1 text-xs">
            <FolderGit2 size={13} className={theme.accentTextClass} />
            <span className="font-semibold">{selectedProject.name}</span>
            <span className={`text-[10px] ${theme.textSecondaryClass}`}>
              ({selectedProject.branch})
            </span>
          </div>
        </div>

        {/* 5 Switchable Themes */}
        <div className="flex items-center gap-2">
          <span className={`text-[11px] font-medium ${theme.textSecondaryClass}`}>
            Theme Switcher:
          </span>
          <div className="flex items-center gap-1 rounded-lg bg-black/30 p-1">
            {(Object.keys(ORCHESTRATE_THEMES) as OrchestrateThemeId[]).map((tId) => {
              const t = ORCHESTRATE_THEMES[tId]
              const isActive = tId === currentThemeId
              return (
                <button
                  key={tId}
                  onClick={() => setCurrentThemeId(tId)}
                  className={`flex items-center gap-1.5 rounded px-2.5 py-1 text-[11px] font-medium transition-all ${
                    isActive
                      ? `${theme.accentBgClass} ${theme.accentTextClass} shadow-sm ring-1 ring-white/20`
                      : 'text-slate-400 hover:bg-white/5 hover:text-slate-200'
                  }`}
                  title={t.subtitle}
                >
                  <span
                    className="h-2 w-2 rounded-full"
                    style={{ backgroundColor: t.accentColor }}
                  />
                  <span>{t.name.split(' ')[0]}</span>
                </button>
              )
            })}
          </div>

          {onNavigateHome && (
            <button
              onClick={onNavigateHome}
              className="ml-2 rounded px-2 py-1 text-xs text-slate-400 hover:bg-white/10 hover:text-slate-200"
            >
              Exit Mockup
            </button>
          )}
        </div>
      </header>

      {/* ─────────────────────────────────────────────────────────────
          MAIN 3-PANEL LAYOUT
         ───────────────────────────────────────────────────────────── */}
      <div className="flex flex-1 overflow-hidden">
        {/* =========================================================
            PANEL 1: LEFT SIDEBAR (PROJECTS & MISSION CONTROL HUD)
           ========================================================= */}
        <aside
          className={`flex w-72 flex-shrink-0 flex-col border-r ${theme.panelBgClass} ${theme.borderClass}`}
        >
          {/* Projects Switcher */}
          <div className={`border-b p-3 ${theme.borderClass}`}>
            <div className="flex items-center justify-between pb-2">
              <span
                className={`text-[11px] font-semibold uppercase tracking-wider ${theme.textSecondaryClass}`}
              >
                Projects
              </span>
              <button
                className={`flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] ${theme.accentBgClass} ${theme.accentTextClass}`}
                title="Create New Project"
              >
                <Plus size={11} /> New
              </button>
            </div>

            <div className="flex flex-col gap-1">
              {projects.map((proj) => {
                const isSelected = proj.id === selectedProjectId
                return (
                  <button
                    key={proj.id}
                    onClick={() => setSelectedProjectId(proj.id)}
                    className={`flex items-center justify-between rounded-lg p-2 text-left transition-all ${
                      isSelected
                        ? `${theme.cardBgClass} ring-1 ${theme.borderClass} ${theme.glowClass}`
                        : 'hover:bg-white/5 opacity-80 hover:opacity-100'
                    }`}
                  >
                    <div className="flex items-center gap-2">
                      <div
                        className={`flex h-7 w-7 items-center justify-center rounded-md ${
                          isSelected ? theme.accentBgClass : 'bg-slate-800'
                        }`}
                      >
                        <FolderGit2
                          size={14}
                          className={isSelected ? theme.accentTextClass : 'text-slate-400'}
                        />
                      </div>
                      <div className="flex flex-col">
                        <span className="text-xs font-semibold">{proj.name}</span>
                        <span className={`text-[10px] ${theme.textSecondaryClass}`}>
                          {proj.repoPath}
                        </span>
                      </div>
                    </div>
                    {isSelected && (
                      <span className={`h-1.5 w-1.5 rounded-full ${theme.accentBgClass}`} />
                    )}
                  </button>
                )
              })}
            </div>
          </div>

          {/* Mission Control HUD for Selected Project */}
          <div className="flex-1 space-y-4 overflow-y-auto p-3 text-xs">
            {/* Git & Worktree Status Block */}
            <div className={`rounded-lg border p-2.5 ${theme.cardBgClass} ${theme.borderClass}`}>
              <div className="flex items-center justify-between pb-1.5">
                <span className="flex items-center gap-1.5 font-semibold text-[11px]">
                  <GitBranch size={13} className={theme.accentTextClass} /> Repository HUD
                </span>
                <span className="flex items-center gap-1 text-[10px] text-emerald-400">
                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                  Clean
                </span>
              </div>
              <div className={`space-y-1 text-[11px] ${theme.textSecondaryClass}`}>
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

            {/* Automation Health HUD */}
            <div className={`rounded-lg border p-2.5 ${theme.cardBgClass} ${theme.borderClass}`}>
              <div className="flex items-center justify-between pb-1.5">
                <span className="flex items-center gap-1.5 font-semibold text-[11px]">
                  <Activity size={13} className={theme.accentTextClass} /> Automation Health
                </span>
                <span className="text-[10px] font-mono text-cyan-400">Pebble V2</span>
              </div>
              <div className="space-y-1.5 text-[11px]">
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
                  <span className={`rounded px-1.5 py-0.2 font-bold ${theme.accentBgClass} ${theme.accentTextClass}`}>
                    {deliverables.filter((d) => d.status === 'ready').length} pending
                  </span>
                </div>
              </div>
            </div>

            {/* Lean Memory (projects.md) HUD */}
            <div className={`rounded-lg border p-2.5 ${theme.cardBgClass} ${theme.borderClass}`}>
              <div className="flex items-center justify-between pb-1.5">
                <span className="flex items-center gap-1.5 font-semibold text-[11px]">
                  <Brain size={13} className={theme.accentTextClass} /> Project Memory
                </span>
                <span className="rounded bg-emerald-950 px-1 text-[9px] font-mono text-emerald-400 border border-emerald-800">
                  LEAN
                </span>
              </div>
              <p className={`text-[10px] leading-relaxed ${theme.textSecondaryClass}`}>
                <strong>Zero AGENTS.md Bloat:</strong> The primary orchestrator reads only this
                project’s lean memory (420 tokens). Repositories retain full rules loaded only by
                isolated subagents upon worktree dispatch.
              </p>
              <div className="mt-2 rounded bg-black/40 p-1.5 font-mono text-[10px] text-slate-300">
                # Project: {selectedProject.name}
                <br />
                - Go daemon + Vite desktop
                <br />
                - Video studio subagent bound
              </div>
            </div>
          </div>
        </aside>

        {/* =========================================================
            PANEL 2: MIDDLE SECTION (PROJECT CANVAS & DELIVERABLES)
           ========================================================= */}
        <main className="flex flex-1 flex-col overflow-hidden bg-black/10">
          {/* Top: Running Automations & Live Progress Bar */}
          <div
            className={`border-b p-4 ${theme.panelBgClass} ${theme.borderClass} flex flex-col gap-3 flex-shrink-0`}
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Zap size={15} className={theme.accentTextClass} />
                <span className="text-xs font-bold uppercase tracking-wider">
                  Active Project Automations & Workers
                </span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => handleSendMessage('make 5 social media videos')}
                  className={`flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-semibold ${theme.accentBgClass} ${theme.accentTextClass} transition-all hover:brightness-110`}
                >
                  <Film size={12} /> + Trigger Video Swarm
                </button>
                <button
                  onClick={() => handleSendMessage('run full testbench')}
                  className="flex items-center gap-1.5 rounded-md bg-slate-800 px-2.5 py-1 text-xs font-semibold text-slate-300 hover:bg-slate-700"
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
                  className={`rounded-lg border p-2.5 text-xs transition-all ${theme.cardBgClass} ${theme.borderClass}`}
                >
                  <div className="flex items-center justify-between pb-1">
                    <span className="font-semibold truncate max-w-[140px]">{auto.name}</span>
                    <span
                      className={`text-[9px] uppercase px-1.5 py-0.5 rounded font-bold ${
                        auto.status === 'running'
                          ? 'bg-cyan-950 text-cyan-400 border border-cyan-800'
                          : auto.status === 'scheduled'
                          ? 'bg-amber-950 text-amber-400 border border-amber-800'
                          : 'bg-slate-800 text-slate-400'
                      }`}
                    >
                      {auto.status}
                    </span>
                  </div>

                  {auto.status === 'running' && auto.progressPercent !== undefined ? (
                    <div className="mt-1 space-y-1">
                      <div className="h-1.5 w-full rounded-full bg-slate-800 overflow-hidden">
                        <div
                          className={`h-full rounded-full transition-all duration-500 ${theme.accentBgClass}`}
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
              <div className="flex items-center gap-1 rounded-lg bg-black/30 p-1">
                {(['all', 'video', 'code', 'report'] as const).map((filter) => (
                  <button
                    key={filter}
                    onClick={() => setMediaFilter(filter)}
                    className={`rounded px-3 py-1 text-xs font-semibold capitalize transition-all ${
                      mediaFilter === filter
                        ? `${theme.accentBgClass} ${theme.accentTextClass}`
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
                  className={`group relative cursor-pointer flex flex-col rounded-xl border p-3.5 transition-all hover:scale-[1.01] ${theme.cardBgClass} ${theme.borderClass} ${theme.glowClass}`}
                >
                  {/* Top Bar of Deliverable */}
                  <div className="flex items-center justify-between pb-2">
                    <div className="flex items-center gap-1.5">
                      {item.type === 'video' ? (
                        <Film size={14} className="text-cyan-400" />
                      ) : item.type === 'code' ? (
                        <FileCode size={14} className="text-emerald-400" />
                      ) : (
                        <FileCheck size={14} className="text-amber-400" />
                      )}
                      <span className="text-[10px] font-mono text-slate-400">{item.author}</span>
                    </div>

                    <span
                      className={`text-[9px] uppercase px-1.5 py-0.5 rounded font-bold ${
                        item.status === 'ready'
                          ? 'bg-emerald-950 text-emerald-400 border border-emerald-800'
                          : item.status === 'generating'
                          ? 'bg-cyan-950 text-cyan-400 border border-cyan-800 animate-pulse'
                          : 'bg-slate-800 text-slate-300'
                      }`}
                    >
                      {item.status}
                    </span>
                  </div>

                  {/* Thumbnail / Visual Box */}
                  {item.type === 'video' && (
                    <div className="relative mb-2.5 aspect-video w-full rounded-lg bg-black/60 overflow-hidden flex items-center justify-center border border-white/5 group-hover:border-cyan-500/40">
                      {/* Abstract Animated Glow to represent video */}
                      <div
                        className="absolute inset-0 opacity-40 bg-gradient-to-tr from-cyan-900/60 via-purple-900/40 to-slate-900"
                        style={{
                          backgroundSize: '200% 200%',
                          animation: 'pulse 3s ease-in-out infinite',
                        }}
                      />

                      <div className="relative z-10 flex flex-col items-center gap-1">
                        <div className="flex h-9 w-9 items-center justify-center rounded-full bg-cyan-500/80 text-black shadow-lg transition-transform group-hover:scale-110">
                          <Play size={16} fill="black" />
                        </div>
                        <span className="text-[10px] font-mono text-cyan-200">
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
                          className="flex items-center gap-1 rounded bg-emerald-600/30 px-2 py-0.5 font-semibold text-emerald-300 hover:bg-emerald-600/50"
                        >
                          <CheckCircle2 size={11} /> Accept
                        </button>
                      )}
                      <button
                        className="rounded bg-white/5 px-2 py-0.5 text-slate-300 hover:bg-white/10"
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
                  <Layers size={14} className={theme.accentTextClass} /> Active Project Tasks &
                  Subagent Reviews
                </span>
                <span className={`text-[11px] ${theme.textSecondaryClass}`}>
                  Sessions are encapsulated inside tasks
                </span>
              </div>

              <div className="space-y-2.5">
                {MOCK_RUNNING_TASKS.map((task) => (
                  <div
                    key={task.id}
                    className={`rounded-lg border p-3 ${theme.cardBgClass} ${theme.borderClass}`}
                  >
                    <div className="flex items-center justify-between pb-2">
                      <div className="flex items-center gap-2">
                        <span
                          className={`rounded px-1.5 py-0.5 text-[9px] uppercase font-bold ${
                            task.agentType === 'coder'
                              ? 'bg-blue-950 text-blue-300 border border-blue-800'
                              : 'bg-purple-950 text-purple-300 border border-purple-800'
                          }`}
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
                          className={`text-[9px] uppercase font-bold px-1.5 py-0.5 rounded ${
                            task.status === 'running'
                              ? 'bg-cyan-950 text-cyan-300'
                              : 'bg-amber-950 text-amber-300'
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
                      <div className="mt-2 rounded bg-black/60 p-2 font-mono text-[10px] text-emerald-400 border border-emerald-950">
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
          className={`flex w-80 flex-shrink-0 flex-col border-l ${theme.panelBgClass} ${theme.borderClass}`}
        >
          {/* Header */}
          <div className={`border-b p-3 ${theme.borderClass}`}>
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <div
                  className={`flex h-7 w-7 items-center justify-center rounded-lg ${theme.accentBgClass} ${theme.accentTextClass}`}
                >
                  <Bot size={15} />
                </div>
                <div className="flex flex-col">
                  <span className="text-xs font-bold">Swarm Orchestrator</span>
                  <span className="text-[10px] text-emerald-400 flex items-center gap-1">
                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                    Bound to {selectedProject.name}
                  </span>
                </div>
              </div>

              <span className="rounded bg-black/30 px-1.5 py-0.5 text-[9px] font-mono text-slate-400">
                Lean
              </span>
            </div>

            {/* Quick Action Pills */}
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <button
                onClick={() => handleSendMessage('Make 10 social media videos')}
                className={`rounded border px-2 py-0.5 text-[10px] transition-all hover:bg-white/10 ${theme.borderClass}`}
              >
                🎥 Make 10 videos
              </button>
              <button
                onClick={() => handleSendMessage('Run testbench on dev branch')}
                className={`rounded border px-2 py-0.5 text-[10px] transition-all hover:bg-white/10 ${theme.borderClass}`}
              >
                🧪 Run tests
              </button>
              <button
                onClick={() => handleSendMessage('Audit project token velocity')}
                className={`rounded border px-2 py-0.5 text-[10px] transition-all hover:bg-white/10 ${theme.borderClass}`}
              >
                ⚡ Token audit
              </button>
            </div>
          </div>

          {/* Chat Messages Stream */}
          <div className="flex-1 space-y-3 overflow-y-auto p-3 text-xs">
            {messages.map((msg) => {
              const isUser = msg.sender === 'user'
              return (
                <div
                  key={msg.id}
                  className={`flex flex-col ${isUser ? 'items-end' : 'items-start'}`}
                >
                  <div
                    className={`max-w-[90%] rounded-xl px-3 py-2 text-xs leading-relaxed ${
                      isUser
                        ? `${theme.accentBgClass} ${theme.accentTextClass}`
                        : `${theme.cardBgClass} border ${theme.borderClass}`
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
                            className="flex items-center justify-between rounded bg-black/40 px-2 py-1 text-[10px] text-cyan-300 hover:bg-black/60"
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
          <div className={`border-t p-2.5 ${theme.borderClass}`}>
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
                className={`flex-1 rounded-lg border bg-black/40 px-3 py-1.5 text-xs text-slate-100 placeholder-slate-500 focus:outline-none focus:ring-1 ${theme.borderClass}`}
              />
              <button
                type="submit"
                disabled={!inputText.trim()}
                className={`flex h-7 w-7 items-center justify-center rounded-lg ${theme.accentBgClass} ${theme.accentTextClass} disabled:opacity-40`}
              >
                <Send size={13} />
              </button>
            </form>
          </div>
        </aside>
      </div>

      {/* ─────────────────────────────────────────────────────────────
          MODAL: VIDEO DELIVERABLE PREVIEW DIALOG
         ───────────────────────────────────────────────────────────── */}
      {activeVideoPreview && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-sm">
          <div
            className={`flex max-w-2xl w-full flex-col rounded-2xl border p-5 ${theme.panelBgClass} ${theme.borderClass} ${theme.glowClass}`}
          >
            <div className="flex items-center justify-between pb-3 border-b border-white/10">
              <div className="flex items-center gap-2">
                <Film size={16} className="text-cyan-400" />
                <h3 className="text-sm font-bold">{activeVideoPreview.title}</h3>
              </div>
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

            {/* Simulated Video Canvas */}
            <div className="relative my-4 aspect-video w-full rounded-xl bg-black overflow-hidden flex items-center justify-center border border-white/10">
              <div
                className="absolute inset-0 opacity-60 bg-gradient-to-tr from-cyan-900 via-indigo-900 to-black"
                style={{
                  animation: isPlayingVideo ? 'pulse 1.5s ease-in-out infinite' : 'none',
                }}
              />
              <div className="relative z-10 flex flex-col items-center gap-2">
                <button
                  onClick={() => setIsPlayingVideo(!isPlayingVideo)}
                  className="flex h-14 w-14 items-center justify-center rounded-full bg-cyan-400 text-black shadow-2xl transition-transform hover:scale-110"
                >
                  {isPlayingVideo ? <Pause size={24} /> : <Play size={24} fill="black" />}
                </button>
                <span className="text-xs font-mono text-cyan-200">
                  {isPlayingVideo ? 'Playing Simulated Stream...' : 'Click to Play Render Preview'}
                </span>
              </div>
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
                  handleAcceptDeliverable(activeVideoPreview.id, { stopPropagation: () => {} } as any)
                  setActiveVideoPreview(null)
                }}
                className="flex items-center gap-1.5 rounded-lg bg-emerald-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-emerald-500"
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
