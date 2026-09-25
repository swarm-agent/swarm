import { useState, useMemo, useEffect } from 'react'
import {
  Activity,
  ArrowRight,
  ArrowUp,
  AtSign,
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Code,
  Columns3,
  Film,
  Folder,
  FolderPlus,
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
import { requestJson } from '../../../app/api'
import { useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { sendSessionMessage } from '../chat/queries/chat-queries'
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

  const [projects, setProjects] = useState<ProjectSummary[]>(MOCK_PROJECTS)
  const [selectedProjectId, setSelectedProjectId] = useState<string>(projects[0]?.id || '')
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  // Project Onboarding Dual-Mode State
  const [isOnboardingActive, setIsOnboardingActive] = useState<boolean>(false)
  const [onboardingName, setOnboardingName] = useState('Swarm Platform')
  const [onboardingDescription, setOnboardingDescription] = useState('Core Go daemon, desktop client, and video production pipeline')
  const [onboardingWorkspaces, setOnboardingWorkspaces] = useState<Array<{ path: string; label: string; role: 'primary_code' | 'auxiliary' | 'docs'; selected: boolean }>>([
    { path: '~/swarm-go', label: 'Core Engine & Daemon', role: 'primary_code', selected: true },
    { path: '~/swarm-social', label: 'Video & Media Studio', role: 'auxiliary', selected: true },
    { path: '~/work', label: 'Testbenches & Scripts', role: 'auxiliary', selected: false },
    { path: '~/swarmcrit', label: 'Critical Operations', role: 'auxiliary', selected: false },
  ])
  const [customFolderPath, setCustomFolderPath] = useState('')
  const [isEditingContext, setIsEditingContext] = useState(false)
  const [isSynthesizing, setIsSynthesizing] = useState(false)
  const [isActivating, setIsActivating] = useState(false)
  const [onboardingContext, setOnboardingContext] = useState(`# Swarm Platform

## Overview
Core daemon, desktop client, and video production pipeline.

## Subsystems & Workspaces
- \`~/swarm-go\`: Core Engine & Daemon (\`primary_code\`)
- \`~/swarm-social\`: Video & Media Studio (\`auxiliary\`)

## Directives
- Local-first architecture; all session and project records persist to Pebble database.
- Subagents (Coder/Designer/Finder) execute inside isolated Git worktrees.
- Strict tool isolation: raw multimedia and environment tools are excluded from executive orchestrator prompt.
- Verification gate: all pull requests and deliverables require user review before promotion.`)

  // Live Pebble V3 cache state for real-time orchestrator sessions and tasks
  const sessionsById = useDesktopV3CacheSelector((s) => s.sessionsById)
  const messagesBySession = useDesktopV3CacheSelector((s) => s.messagesBySession)
  const plansBySession = useDesktopV3CacheSelector((s) => s.plansBySession)
  const [activeSessionId, setActiveSessionId] = useState<string>('')

  // Initial load from live /v3/projects if available
  useEffect(() => {
    let cancelled = false
    requestJson<{ projects: Array<{ id: string; name: string; description?: string; workspaces?: Array<{ path: string; label?: string; role?: string }>; project_context?: string; primary_session_id?: string }> }>('/v3/projects')
      .then((res) => {
        if (cancelled) return
        if (res.projects && res.projects.length > 0) {
          const loaded: ProjectSummary[] = res.projects.map((p) => ({
            id: p.id,
            name: p.name,
            slug: p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
            description: p.description || '',
            repoPath: p.workspaces?.[0]?.path || '~/workspace',
            branch: 'dev',
            gitStatus: 'clean',
            linkedWorkspaces: p.workspaces?.map((w) => w.path) || [],
            activeWorkersCount: 4,
            pendingDeliverablesCount: 3,
            runningTasksCount: 100,
            projectContext: p.project_context,
            primarySessionId: p.primary_session_id,
          }))
          setProjects(loaded)
          setSelectedProjectId(loaded[0].id)
          if (loaded[0].primarySessionId) {
            setActiveSessionId(loaded[0].primarySessionId)
          }
        }
      })
      .catch(() => {
        // Fall back gracefully to mock projects when offline
      })
    return () => {
      cancelled = true
    }
  }, [])

  const fetchProjectTasks = (projectId: string) => {
    requestJson<{ tasks: any[] }>(`/v3/projects/${projectId}/tasks`)
      .then((res) => {
        if (res.tasks && res.tasks.length > 0) {
          const backendTasks: RunningTask[] = res.tasks.map((t) => ({
            id: t.id,
            title: t.title,
            subtitle: t.description || `Autonomous execution unit for ${t.agent || 'coder'}`,
            agentType: (t.agent === 'designer' || t.agent === 'finder' || t.agent === 'video' ? t.agent : 'coder') as any,
            status: (t.status === 'in_progress' ? 'running' : t.status) as any,
            workspaceTarget: t.project_id,
            elapsed: 'Just now',
            workerName: t.worker_name || `@${t.agent || 'Coder'} Verifier`,
            priority: 'high',
            sessionId: t.session_id,
            createdAt: t.created_at,
            stageIndex: t.current_stage_index,
            totalStages: t.pipeline_stages?.length || 4,
            stepTimeline: t.pipeline_stages?.map((st: string, idx: number) => ({
              step: idx + 1,
              label: st,
              status: idx < (t.current_stage_index || 0) ? 'complete' : (idx === t.current_stage_index ? 'processing' : 'pending'),
            })),
            deliverables: t.deliverables?.map((d: any) => ({
              id: d.id,
              title: d.title,
              type: d.kind || 'video',
              status: d.status || 'ready',
              duration: d.duration || '0:15',
              thumbnailType: (d.thumbnail || 'cyber_lattice') as any,
              createdAt: 'Just now',
              author: t.worker_name || 'Orchestrator',
            })),
            subtasks: [
              { id: '1', title: 'Verify task scope', completed: true },
              { id: '2', title: 'Execute implementation', completed: t.status === 'completed' || t.status === 'needs_review' },
            ],
          }))
          setTasks((prev) => {
            const nonBackend = prev.filter((p) => !p.id.startsWith('task_'))
            return [...backendTasks, ...nonBackend]
          })
        }
      })
      .catch(() => {})
  }

  const handleToggleWorkspace = (path: string) => {
    setOnboardingWorkspaces((prev) =>
      prev.map((w) => (w.path === path ? { ...w, selected: !w.selected } : w))
    )
  }

  const handleAddCustomFolder = () => {
    const trimmed = customFolderPath.trim()
    if (!trimmed) return
    if (onboardingWorkspaces.some((w) => w.path === trimmed)) {
      setOnboardingWorkspaces((prev) =>
        prev.map((w) => (w.path === trimmed ? { ...w, selected: true } : w))
      )
    } else {
      const base = trimmed.split('/').filter(Boolean).pop() || 'Workspace'
      setOnboardingWorkspaces((prev) => [
        ...prev,
        { path: trimmed, label: base, role: 'auxiliary', selected: true },
      ])
    }
    setCustomFolderPath('')
  }

  const handleSynthesizeContext = async () => {
    setIsSynthesizing(true)
    try {
      const selectedWs = onboardingWorkspaces.filter((w) => w.selected).map((w) => w.path)
      const res = await requestJson<{ project_context: string }>('/v3/projects/synthesize-context', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: onboardingName.trim() || 'Project Architecture',
          workspaces: selectedWs,
        }),
      })
      if (res?.project_context) {
        setOnboardingContext(res.project_context)
      }
    } catch (err) {
      console.warn('Backend context synthesis failed, falling back:', err)
      const selectedWs = onboardingWorkspaces.filter((w) => w.selected)
      const synthesized = `# ${onboardingName.trim() || 'Project Architecture'}

## Overview
${onboardingDescription.trim() || 'Multi-workspace software initiative managed by Swarm Orchestrator.'}

## Subsystems & Workspaces
${selectedWs.map((w) => `- \`${w.path}\`: ${w.label} (${w.role})`).join('\n')}

## Directives
- Local-first architecture; all session and project records persist to Pebble database.
- Subagents (Coder/Designer/Finder) execute inside isolated Git worktrees.
- Strict tool isolation: raw multimedia and environment tools are excluded from executive orchestrator prompt.
- Verification gate: all pull requests and deliverables require user review before promotion.
`
      setOnboardingContext(synthesized)
    } finally {
      setIsSynthesizing(false)
    }
  }

  const handleCreateAndActivateProject = async () => {
    setIsActivating(true)
    const selectedWs = onboardingWorkspaces.filter((w) => w.selected).map((w) => ({
      path: w.path,
      role: w.role,
      label: w.label,
    }))

    const payload = {
      name: onboardingName.trim() || 'My Project',
      description: onboardingDescription.trim(),
      workspaces: selectedWs,
      project_context: onboardingContext,
    }

    let newId = `proj_${Date.now()}`
    try {
      const res = await requestJson<{ project: { id: string } }>('/v3/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (res.project?.id) {
        newId = res.project.id
      }
    } catch (err) {
      console.warn('Backend /v3/projects offline, activating project in local state:', err)
    }

    // Automatically spawn the primary orchestrator session for this newly created project
    let orchSessionId = ''
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: `Project Orchestrator: ${payload.name}`,
          workspace_path: selectedWs[0]?.path || '.',
          agent_name: 'system-orchestrator',
          metadata: {
            project_id: newId,
            role: 'project_orchestrator',
          },
        }),
      })
      if (sessRes?.session?.id) {
        orchSessionId = sessRes.session.id
        await requestJson(`/v3/projects/${newId}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ primary_session_id: orchSessionId }),
        })
      }
    } catch (e) {
      console.warn('Failed to spawn initial orchestrator session:', e)
    }

    const newProject: ProjectSummary = {
      id: newId,
      name: payload.name,
      slug: payload.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      description: payload.description,
      repoPath: selectedWs[0]?.path || '~/workspace',
      branch: 'dev',
      gitStatus: 'clean',
      linkedWorkspaces: selectedWs.map((w) => w.path),
      activeWorkersCount: 4,
      pendingDeliverablesCount: 0,
      runningTasksCount: 0,
      projectContext: payload.project_context,
      primarySessionId: orchSessionId,
    }

    setProjects((prev) => [newProject, ...prev])
    setSelectedProjectId(newProject.id)
    if (orchSessionId) {
      setActiveSessionId(orchSessionId)
    }
    setIsOnboardingActive(false)
    setIsActivating(false)

    // Append confirmation in chat
    const confirmMsg: OrchestratorMessage = {
      id: `msg-${Date.now()}`,
      sender: 'orchestrator',
      text: `🎉 Project "${newProject.name}" has been created and activated!\n\nBound Workspaces:\n${selectedWs.map((w) => `• ${w.path} (${w.label})`).join('\n')}\n\nYou can now deploy autonomous workers or dispatch your first task with the Swarm Orchestrator.`,
      timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }
    setMessages((prev) => [...prev, confirmMsg])
  }

  // Middle canvas layout variant state (5 distinct variants - Selected Canonical Default: Variant 3 Worker Fleet)
  const [middleVariant, setMiddleVariant] = useState<MiddleCanvasVariant>('fleet')

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

  // Derived live tasks with real-time status from linked V3 sessions
  const liveTasks = useMemo(() => {
    return tasks.map((task) => {
      if (!task.sessionId || !sessionsById[task.sessionId]) {
        return task
      }
      const record = sessionsById[task.sessionId]
      const sess = record?.kind === 'full' ? record.session : undefined
      const plan = plansBySession[task.sessionId] as any
      if (!sess) {
        return task
      }
      const lifecycle = sess.lifecycle as any
      let status = task.status
      if (lifecycle?.active) {
        status = 'running'
      }
      const hasWaitingReview = plan?.document?.checkpoints?.some((cp: any) => cp.status === 'needs_review')
      if (hasWaitingReview || lifecycle?.phase === 'needs_review') {
        status = 'needs_review'
      } else if (!lifecycle?.active && (sess.message_count ?? 0) > 1) {
        status = 'completed'
      }
      return {
        ...task,
        status,
        elapsed: sess.updated_at ? `${Math.max(1, Math.round((Date.now() - (sess.created_at ?? Date.now())) / 60000))}m` : task.elapsed,
      }
    })
  }, [tasks, sessionsById, plansBySession])

  // Synchronize active orchestrator session and project tasks with selected project
  useEffect(() => {
    if (!selectedProject || isOnboardingActive) return

    fetchProjectTasks(selectedProject.id)

    if (selectedProject.primarySessionId) {
      setActiveSessionId(selectedProject.primarySessionId)
    } else {
      requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: `Project Orchestrator: ${selectedProject.name}`,
          workspace_path: selectedProject.repoPath || '.',
          agent_name: 'system-orchestrator',
          metadata: {
            project_id: selectedProject.id,
            role: 'project_orchestrator',
          },
        }),
      })
        .then((sessRes) => {
          if (sessRes?.session?.id) {
            const sid = sessRes.session.id
            setActiveSessionId(sid)
            requestJson(`/v3/projects/${selectedProject.id}`, {
              method: 'PATCH',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ primary_session_id: sid }),
            }).catch(() => {})
          }
        })
        .catch((e) => {
          console.warn('Failed to ensure orchestrator session:', e)
        })
    }
  }, [selectedProject?.id, isOnboardingActive])

  const liveSessionMessages = useMemo(() => {
    if (!activeSessionId || !messagesBySession[activeSessionId]) return null
    return (messagesBySession[activeSessionId].items || []).filter(
      (m: any) => m.role === 'user' || m.role === 'assistant'
    )
  }, [activeSessionId, messagesBySession])

  const activeRecord = activeSessionId ? sessionsById[activeSessionId] : undefined
  const activeSession = activeRecord?.kind === 'full' ? activeRecord.session : undefined
  const isLiveSessionRunning = !!(activeSession && (activeSession.lifecycle as any)?.active)

  const handleDeployTask = async (
    title: string,
    agent: string = 'coder',
    workerName: string = '@Code Verifier',
    prompt?: string,
    pipelineStages?: string[]
  ) => {
    if (!selectedProject?.id) return
    try {
      const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title,
          agent,
          worker_name: workerName,
          pipeline_stages: pipelineStages || ['Inspect', 'Implement', 'Verify', 'Review'],
          deploy_session: true,
          prompt: prompt || title,
        }),
      })
      if (res?.task) {
        fetchProjectTasks(selectedProject.id)
      }
    } catch (err) {
      console.warn('Deploy task failed:', err)
    }
  }

  // Derived filtered tasks for 100-task handling
  const filteredTasks = useMemo(() => {
    return liveTasks.filter((task) => {
      const matchesSearch =
        searchQuery === '' ||
        task.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.subtitle?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.id.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesStatus = statusFilter === 'all' || task.status === statusFilter
      const matchesTag = selectedTag === 'all' || task.tags?.includes(selectedTag)

      return matchesSearch && matchesStatus && matchesTag
    })
  }, [liveTasks, searchQuery, statusFilter, selectedTag])

  const selectedTaskForSplit = useMemo(() => {
    return liveTasks.find((t) => t.id === selectedTaskId) || liveTasks[0]
  }, [liveTasks, selectedTaskId])

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

  const handleSendMessage = async (textToSend?: string) => {
    const text = (textToSend || inputText).trim()
    if (!text) return

    const userMsg: OrchestratorMessage = {
      id: `msg-${Date.now()}`,
      sender: 'user',
      text: text.trim(),
      timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }

    setMessages((prev) => [...prev, userMsg])
    if (!textToSend) setInputText('')
    setIsTyping(true)

    if (isOnboardingActive) {
      setTimeout(() => {
        const lower = text.toLowerCase()
        let reply = ''
        if (lower.includes('bundle') || lower.includes('both') || lower.includes('swarm-social')) {
          setOnboardingWorkspaces((prev) =>
            prev.map((w) => (w.path.includes('swarm') ? { ...w, selected: true } : w))
          )
          reply = "I've selected both `~/swarm-go` and `~/swarm-social` on your canvas. Shall I generate the synthesized `project.md` architecture now?"
        } else if (lower.includes('work') || lower.includes('scripts')) {
          setOnboardingWorkspaces((prev) =>
            prev.map((w) => (w.path.includes('work') ? { ...w, selected: true } : w))
          )
          reply = "I've added and selected `~/work` as an auxiliary workspace in your project list."
        } else if (lower.includes('synthesize') || lower.includes('generate') || lower.includes('context')) {
          handleSynthesizeContext()
          reply = "I've analyzed your selected repositories and generated the synthesized `project.md` context card in the middle canvas. You can review or edit it directly."
        } else if (lower.includes('create') || lower.includes('activate') || lower.includes('confirm') || lower.includes('looks good')) {
          void handleCreateAndActivateProject()
          return
        } else if (lower.startsWith('name ') || lower.startsWith('call it ')) {
          const newName = text.replace(/^(name|call it)\s+/i, '').trim()
          if (newName) {
            setOnboardingName(newName)
            reply = `Updated project name to "${newName}".`
          }
        } else {
          reply = `I'm facilitating onboarding for your new project. You can check/uncheck workspaces on the canvas, click "Generate with AI" for project.md, or click "Create & Activate Project" when ready.`
        }

        const botMsg: OrchestratorMessage = {
          id: `msg-reply-${Date.now()}`,
          sender: 'orchestrator',
          text: reply,
          timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        }
        setMessages((prev) => [...prev, botMsg])
        setIsTyping(false)
      }, 500)
      return
    }

    if (activeSessionId) {
      setIsTyping(true)
      try {
        await sendSessionMessage(activeSessionId, 'user', text)
      } catch (err) {
        console.warn('Failed to send live message to orchestrator session:', err)
        const botMsg: OrchestratorMessage = {
          id: `msg-reply-${Date.now()}`,
          sender: 'orchestrator',
          text: `Message dispatched to project orchestrator. Tracking active work units in canvas.`,
          timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        }
        setMessages((prev) => [...prev, botMsg])
      } finally {
        setIsTyping(false)
      }
      return
    }

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
  const runningCount = liveTasks.filter((t) => t.status === 'running').length
  const reviewCount = liveTasks.filter((t) => t.status === 'needs_review').length
  const queuedCount = liveTasks.filter((t) => t.status === 'queued').length
  const completedCount = liveTasks.filter((t) => t.status === 'completed').length

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
              onClick={() => setIsOnboardingActive(true)}
              className={`flex h-5 w-5 items-center justify-center rounded-lg transition-all border ${
                isOnboardingActive
                  ? 'bg-blue-600 text-white border-blue-500 shadow-[0_0_8px_rgba(59,130,246,0.5)]'
                  : 'bg-slate-800/80 text-slate-400 hover:text-slate-200 hover:bg-slate-700 border-slate-700/60'
              }`}
              title="Add New Project"
            >
              <Plus size={12} />
            </button>
          </div>

          <div className="flex flex-col gap-1.5">
            {isOnboardingActive && (
              <div className="flex items-center justify-between p-2 text-left rounded-xl bg-blue-950/40 border border-blue-500/40 text-blue-200 shadow-sm">
                <div className="flex items-center gap-2.5 min-w-0">
                  <div className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg bg-blue-600/30 text-blue-300 border border-blue-400/40">
                    <FolderPlus size={12} />
                  </div>
                  <span className="text-xs font-semibold truncate">{onboardingName || 'New Project'}</span>
                </div>
                <span className="text-[9px] uppercase tracking-wider font-mono px-1.5 py-0.5 rounded bg-blue-600/30 text-blue-300">
                  Draft
                </span>
              </div>
            )}
            {projects.map((proj) => {
              const isSelected = !isOnboardingActive && proj.id === selectedProjectId
              return (
                <button
                  key={proj.id}
                  onClick={() => {
                    setSelectedProjectId(proj.id)
                    setIsOnboardingActive(false)
                  }}
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
        {isOnboardingActive ? (
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-6">
            {/* Onboarding Header */}
            <div className="flex items-start justify-between border-b border-slate-800/80 pb-5">
              <div>
                <div className="flex items-center gap-2 text-blue-400 font-semibold text-xs uppercase tracking-wider mb-1">
                  <Sparkles size={14} className="text-blue-400 animate-pulse" />
                  <span>Facilitating Project Onboarding</span>
                </div>
                <h1 className="text-xl font-bold text-white tracking-tight">Create Your Project</h1>
                <p className="text-xs text-slate-400 mt-1 max-w-xl">
                  A Project elevates raw repository folders and background automations into a cohesive, goal-driven executive space managed by the Swarm Orchestrator.
                </p>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-[11px] text-slate-500 font-mono bg-slate-800/60 px-2.5 py-1 rounded-full border border-slate-700/50">
                  Milestone 1 Preview
                </span>
              </div>
            </div>

            {/* Project Identity Inputs */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="text-xs font-semibold text-slate-300">Project Identity</div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block">Project Name</label>
                  <input
                    type="text"
                    value={onboardingName}
                    onChange={(e) => setOnboardingName(e.target.value)}
                    placeholder="e.g. Swarm Platform"
                    className="w-full text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 transition-all font-medium"
                  />
                </div>
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block">Description</label>
                  <input
                    type="text"
                    value={onboardingDescription}
                    onChange={(e) => setOnboardingDescription(e.target.value)}
                    placeholder="e.g. Core Go daemon, desktop client, and video production pipeline"
                    className="w-full text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 transition-all font-medium"
                  />
                </div>
              </div>
            </div>

            {/* Section 1: Workspace Selection */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-xs font-semibold text-slate-300">1. Select Bound Workspaces</div>
                  <p className="text-[11px] text-slate-500">Choose existing repositories on your machine to bind into this project.</p>
                </div>
                <span className="text-[11px] text-blue-400 font-medium">
                  {onboardingWorkspaces.filter((w) => w.selected).length} selected
                </span>
              </div>

              <div className="grid grid-cols-1 gap-2 pt-1">
                {onboardingWorkspaces.map((ws) => (
                  <div
                    key={ws.path}
                    onClick={() => handleToggleWorkspace(ws.path)}
                    className={`flex items-center justify-between p-3 rounded-xl border transition-all cursor-pointer ${
                      ws.selected
                        ? 'border-blue-500/40 bg-blue-950/20 text-white shadow-sm'
                        : 'border-slate-800/80 bg-slate-950/40 text-slate-400 hover:border-slate-700/80 hover:text-slate-200'
                    }`}
                  >
                    <div className="flex items-center gap-3">
                      <div className={`h-4 w-4 rounded flex items-center justify-center border transition-all ${
                        ws.selected ? 'bg-blue-600 border-blue-500 text-white' : 'border-slate-700 bg-slate-900'
                      }`}>
                        {ws.selected && <Check size={11} strokeWidth={3} />}
                      </div>
                      <div>
                        <div className="text-xs font-semibold flex items-center gap-2">
                          <span>{ws.label}</span>
                          <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono ${
                            ws.role === 'primary_code' ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20' : 'bg-slate-800 text-slate-400'
                          }`}>
                            {ws.role}
                          </span>
                        </div>
                        <div className="text-[11px] text-slate-500 font-mono mt-0.5">{ws.path}</div>
                      </div>
                    </div>
                  </div>
                ))}
              </div>

              {/* Add folder inline */}
              <div className="pt-2 flex items-center gap-2">
                <input
                  type="text"
                  value={customFolderPath}
                  onChange={(e) => setCustomFolderPath(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      handleAddCustomFolder()
                    }
                  }}
                  placeholder="Enter path to another folder (e.g. ~/web, ~/docs)..."
                  className="flex-1 text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 transition-all font-mono"
                />
                <button
                  type="button"
                  onClick={handleAddCustomFolder}
                  className="px-3 py-2 rounded-xl bg-slate-800 text-xs font-medium text-slate-200 hover:bg-slate-700 hover:text-white border border-slate-700/80 transition-all flex items-center gap-1.5 shrink-0"
                >
                  <Plus size={13} />
                  <span>Add Folder</span>
                </button>
              </div>
            </div>

            {/* Section 2: Synthesized project.md Context Card */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-xs font-semibold text-slate-300">2. Project Context & Directives (project.md)</div>
                  <p className="text-[11px] text-slate-500">
                    High-level executive blueprint loaded into the Swarm Orchestrator context (raw AGENTS.md files are excluded to prevent prompt bloat).
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleSynthesizeContext}
                    disabled={isSynthesizing}
                    className="px-2.5 py-1 rounded-lg bg-blue-600/10 text-blue-400 hover:bg-blue-600/20 border border-blue-500/30 text-xs font-medium transition-all flex items-center gap-1.5"
                  >
                    <Sparkles size={12} className={isSynthesizing ? "animate-spin" : ""} />
                    <span>{isSynthesizing ? "Synthesizing..." : "Generate with AI"}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setIsEditingContext(!isEditingContext)}
                    className="px-2.5 py-1 rounded-lg bg-slate-800 text-slate-300 hover:text-white border border-slate-700 text-xs font-medium transition-all"
                  >
                    {isEditingContext ? "Preview" : "Edit by Hand"}
                  </button>
                </div>
              </div>

              {isEditingContext ? (
                <textarea
                  value={onboardingContext}
                  onChange={(e) => setOnboardingContext(e.target.value)}
                  rows={8}
                  className="w-full text-xs font-mono rounded-xl bg-slate-950 border border-slate-800 p-3 text-slate-300 focus:outline-none focus:border-blue-500/60 transition-all"
                />
              ) : (
                <div className="rounded-xl border border-slate-800 bg-slate-950/80 p-3 max-h-56 overflow-y-auto">
                  <pre className="text-[11px] font-mono text-slate-300 whitespace-pre-wrap leading-relaxed">
                    {onboardingContext}
                  </pre>
                </div>
              )}
            </div>

            {/* Section 3: Activation Button */}
            <div className="pt-2 flex items-center justify-between border-t border-slate-800/80 pt-4">
              <div className="text-[11px] text-slate-500">
                Clicking activate saves this project to Pebble DB and launches your executive Project Canvas.
              </div>
              <button
                type="button"
                onClick={handleCreateAndActivateProject}
                disabled={isActivating || onboardingWorkspaces.filter((w) => w.selected).length === 0}
                className="px-5 py-2.5 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 text-white font-semibold text-xs shadow-lg shadow-blue-600/20 transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <span>{isActivating ? "Activating..." : "Create & Activate Project"}</span>
                <ArrowRight size={14} />
              </button>
            </div>
          </div>
        ) : (
          <>
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
                onClick={() => handleDeployTask('Make 3 Social Media Videos for Feature Launch', 'video', '@Video Swarm Dispatcher', 'Generate 3 video teasers for the feature launch', ['Design', 'Generate', 'Polish', 'Deliver'])}
                className="flex items-center gap-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
              >
                <Plus size={13} />
                <span>+ Deploy Task</span>
              </button>
              <button
                onClick={() => handleDeployTask('Run Local Testbench Suite', 'coder', '@Code Verifier', 'Run critical test gate and inspect testbench health', ['Inspect', 'Execute', 'Analyze', 'Review'])}
                className="flex items-center gap-1.5 rounded-xl bg-slate-800/80 hover:bg-slate-700/80 border border-slate-700/70 text-slate-200 text-xs px-3 py-1.5 transition-all active:scale-95"
              >
                <Play size={11} fill="currentColor" />
                <span>Run Testbench</span>
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

              {/* Worker-Partitioned Tasks with Search, Filters & Expandable Drawers (100+ Task Scalability) */}
              <div className="pt-2 space-y-3">
                <div className="flex items-center justify-between">
                  <div>
                    <h4 className="text-xs font-bold text-white">Worker Task Assignment Queues</h4>
                    <p className="text-[10px] text-slate-400">
                      Real-time jobs dispatched and owned across your autonomous workers
                    </p>
                  </div>
                  <span className="rounded-full bg-blue-500/10 border border-blue-500/30 px-2 py-0.5 text-[9px] font-semibold text-blue-400">
                    Selected Canonical View • Variant 3
                  </span>
                </div>

                {/* Filter bar inside Worker Fleet View */}
                <div className="flex items-center justify-between gap-3">
                  <div className="flex-1 relative">
                    <Search size={13} className="absolute left-3 top-2.5 text-slate-500" />
                    <input
                      type="text"
                      value={searchQuery}
                      onChange={(e) => setSearchQuery(e.target.value)}
                      placeholder="Search tasks across workers..."
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

                {/* Task Rows with Expandable Drawers */}
                <div className="space-y-1.5">
                  {filteredTasks.map((task) => {
                    const isExpanded = expandedTaskId === task.id
                    return (
                      <div
                        key={task.id}
                        className="rounded-xl border border-slate-800/80 bg-[#0a0f1d]/70 hover:border-slate-700/80 transition-all overflow-hidden"
                      >
                        {/* Header Row */}
                        <div
                          onClick={() => setExpandedTaskId(isExpanded ? null : task.id)}
                          className="flex items-center justify-between p-2.5 cursor-pointer hover:bg-white/[0.02]"
                        >
                          <div className="flex items-center gap-2.5 min-w-0 flex-1">
                            <span
                              className={`h-2 w-2 rounded-full flex-shrink-0 ${
                                task.status === 'running'
                                  ? 'bg-blue-400 animate-pulse'
                                  : task.status === 'needs_review'
                                  ? 'bg-amber-400'
                                  : task.status === 'completed'
                                  ? 'bg-emerald-400'
                                  : 'bg-slate-600'
                              }`}
                            />
                            <div className="flex h-5 w-5 items-center justify-center rounded-md bg-slate-800 text-slate-400 flex-shrink-0">
                              {task.agentType === 'coder' ? (
                                <Code size={11} />
                              ) : task.agentType === 'designer' ? (
                                <Film size={11} />
                              ) : (
                                <Bot size={11} />
                              )}
                            </div>
                            <span className="font-semibold text-xs text-white truncate max-w-sm">
                              {task.title}
                            </span>
                            {task.workerName && (
                              <span className="rounded bg-slate-800/80 px-1.5 py-0.5 text-[9px] font-mono text-slate-400 truncate">
                                @{task.workerName}
                              </span>
                            )}
                          </div>

                          <div className="flex items-center gap-3">
                            {task.stepTimeline && (
                              <div className="flex items-center gap-1 font-mono text-[9px]">
                                {task.stepTimeline.map((step) => (
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

                            {task.deliverables && task.deliverables.length > 0 && (
                              <span className="flex items-center gap-1 rounded bg-blue-500/15 border border-blue-500/30 px-1.5 py-0.5 text-[9px] font-semibold text-blue-300">
                                <Film size={9} />
                                {task.deliverables.length} clips
                              </span>
                            )}

                            <span className="text-[10px] font-mono text-slate-500 min-w-[45px] text-right">
                              {task.elapsed}
                            </span>

                            <span className="text-slate-500">
                              {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                            </span>
                          </div>
                        </div>

                        {/* Expandable Drawer with Deliverables & Diffs */}
                        {isExpanded && (
                          <div className="border-t border-slate-800/80 bg-[#070b15] p-3 space-y-3">
                            {task.deliverables && (
                              <div>
                                <div className="text-[11px] font-bold text-slate-300 mb-2 flex items-center justify-between">
                                  <span>Attached Video Deliverables ({task.deliverables.length})</span>
                                  <span className="text-[10px] text-slate-500 font-mono">
                                    Click thumbnail to play full preview
                                  </span>
                                </div>
                                <div className="grid grid-cols-3 gap-2.5">
                                  {task.deliverables.map((item) => (
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
                                          onClick={(e) => handleAcceptDeliverable(task.id, item.id, e)}
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

                            {task.diffLines && (
                              <div>
                                <div className="text-[11px] font-bold text-slate-300 mb-1.5">
                                  Code Changes Diff
                                </div>
                                <div className="p-2.5 rounded-xl bg-[#05070e] border border-slate-800 font-mono text-[10px] space-y-0.5">
                                  {task.diffLines.map((line, idx) => (
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
        </>
        )}
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
                <span className={`h-1.5 w-1.5 rounded-full ${isOnboardingActive ? 'bg-blue-400 animate-pulse' : 'bg-emerald-400'}`} />
                <span>{isOnboardingActive ? 'Facilitating Onboarding' : 'Online • Ready to help'}</span>
              </div>
            </div>
          </div>

          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <Maximize2 size={13} />
          </button>
        </div>

        {/* Chat Messages Stream */}
        <div className="flex-1 space-y-3.5 overflow-y-auto p-3.5 text-xs">
          {liveSessionMessages && liveSessionMessages.length > 0 ? (
            liveSessionMessages.map((msg: any) => {
              const isUser = msg.role === 'user'
              return (
                <div
                  key={msg.id}
                  className={`flex flex-col ${isUser ? 'items-end' : 'items-start'}`}
                >
                  {!isUser && (
                    <div className="flex items-center gap-1.5 mb-1 text-[10px] font-medium text-slate-400">
                      <Bot size={12} className="text-blue-400" />
                      <span>Swarm Orchestrator</span>
                    </div>
                  )}
                  <div
                    className={`max-w-[95%] p-3 text-xs leading-relaxed transition-all ${
                      isUser
                        ? 'rounded-2xl rounded-tr-sm bg-gradient-to-r from-blue-600 to-indigo-600 text-white shadow-[0_2px_12px_rgba(37,99,235,0.25)]'
                        : 'rounded-2xl rounded-tl-sm bg-[#090d16] border border-slate-800/80 text-slate-200 whitespace-pre-wrap'
                    }`}
                  >
                    <p className="whitespace-pre-line">{msg.content}</p>
                  </div>
                  <span className="mt-1 text-[9px] text-slate-500">
                    {new Date(msg.createdAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                  </span>
                </div>
              )
            })
          ) : (
            messages.map((msg) => {
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
                          const target = liveTasks[0]?.deliverables?.[0]
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
            })
          )}

          {(isTyping || isLiveSessionRunning) && (
            <div className="flex items-center gap-1.5 text-xs text-slate-400">
              <Sparkles size={12} className="animate-spin text-blue-400" />
              <span>Swarm Orchestrator is executing...</span>
            </div>
          )}
        </div>

        {/* Onboarding Quick Action Chips (Non-blocking) */}
        {isOnboardingActive && (
          <div className="px-3 py-2 flex flex-wrap gap-1.5 border-t border-slate-800/80 bg-[#080c16]/70">
            <button
              type="button"
              onClick={() => handleSendMessage('Bundle ~/swarm-go & ~/swarm-social')}
              className="text-[10px] rounded-lg bg-blue-600/10 hover:bg-blue-600/20 text-blue-400 border border-blue-500/20 px-2 py-1 transition-all flex items-center gap-1 font-medium"
            >
              <span>✦ Select Both Repos</span>
            </button>
            <button
              type="button"
              onClick={() => handleSendMessage('Synthesize project.md context')}
              className="text-[10px] rounded-lg bg-blue-600/10 hover:bg-blue-600/20 text-blue-400 border border-blue-500/20 px-2 py-1 transition-all flex items-center gap-1 font-medium"
            >
              <span>✦ Synthesize project.md</span>
            </button>
            <button
              type="button"
              onClick={() => handleSendMessage('Add ~/work to project')}
              className="text-[10px] rounded-lg bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700/60 px-2 py-1 transition-all font-medium"
            >
              <span>+ Add ~/work</span>
            </button>
            <button
              type="button"
              onClick={() => void handleCreateAndActivateProject()}
              className="text-[10px] rounded-lg bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 text-white font-semibold px-2.5 py-1 transition-all shadow-sm"
            >
              <span>✓ Activate Project →</span>
            </button>
          </div>
        )}

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
