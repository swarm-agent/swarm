import { useState, useMemo, useEffect, useCallback, useRef } from 'react'
import {
  Activity,
  ArrowRight,
  ArrowUp,
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  Code,
  Columns3,
  Edit3,
  Film,
  Folder,
  FolderPlus,
  Home,
  Layers,
  ListFilter,
  Maximize2,
  MoreHorizontal,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Search,
  Settings,
  Sparkles,
  Trash2,
  Volume2,
  X,
  Zap,
} from 'lucide-react'
import { requestJson } from '../../../app/api'
import { useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { fetchSessionMessages, sendSessionMessage } from '../chat/queries/chat-queries'
import { ORCHESTRATE_THEME_IDS, ORCHESTRATE_THEMES } from './orchestrate-themes'
import {
  DeployedWorker,
  MediaDeliverable,
  MiddleCanvasVariant,
  OrchestrateThemeId,
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
 * Thumbnail graphic renderer for the video and media deliverables
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

  // Real user profile from /v1/auth/desktop/session
  const [userProfile, setUserProfile] = useState<{ id: string; name: string; email: string }>({
    id: '',
    name: 'Operator',
    email: 'local operator',
  })

  // Projects State - empty initially until loaded from Pebble
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string>('')
  const [_isLoadingProjects, setIsLoadingProjects] = useState<boolean>(true)
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  // Project Onboarding & Creation State
  const [isOnboardingActive, setIsOnboardingActive] = useState<boolean>(false)
  const [onboardingName, setOnboardingName] = useState('Swarm Platform')
  const [onboardingDescription, setOnboardingDescription] = useState('Core daemon, desktop client, and multi-workspace initiative')
  const [onboardingWorkspaces, setOnboardingWorkspaces] = useState<Array<{ path: string; label: string; role: 'primary_code' | 'auxiliary'; selected: boolean }>>([])
  const [customFolderPath, setCustomFolderPath] = useState('')
  const [isEditingContext, setIsEditingContext] = useState(false)
  const [isSynthesizing, setIsSynthesizing] = useState(false)
  const [isActivating, setIsActivating] = useState(false)
  const [onboardingContext, setOnboardingContext] = useState('')

  // Live Pebble V3 cache state for real-time orchestrator sessions and tasks
  const sessionsById = useDesktopV3CacheSelector((s) => s.sessionsById)
  const messagesBySession = useDesktopV3CacheSelector((s) => s.messagesBySession)
  const plansBySession = useDesktopV3CacheSelector((s) => s.plansBySession)
  const [activeSessionId, setActiveSessionId] = useState<string>('')

  // Tasks State - empty initially until loaded from /v3/projects/{id}/tasks
  const [tasks, setTasks] = useState<RunningTask[]>([])
  const [automations, setAutomations] = useState<RunningAutomation[]>([])

  // Middle canvas layout variant state
  const [middleVariant, setMiddleVariant] = useState<MiddleCanvasVariant>('matrix')

  // Search & Filters
  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'running' | 'needs_review' | 'queued' | 'completed'>('all')
  const [selectedTag] = useState<string>('all')

  // Matrix expandable drawer state
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null)

  // Split Studio selected task state
  const [selectedTaskId, setSelectedTaskId] = useState<string>('')

  // Video preview modal
  const [activeVideoPreview, setActiveVideoPreview] = useState<MediaDeliverable | null>(null)
  const [isPlayingVideo, setIsPlayingVideo] = useState(false)
  const [isMuted, setIsMuted] = useState(false)
  const [isFullscreen, setIsFullscreen] = useState(false)

  // Chat state
  const [inputText, setInputText] = useState('')
  const [isTyping, setIsTyping] = useState(false)
  const [activeNavTab, setActiveNavTab] = useState<'home' | 'projects' | 'automations' | 'deliverables' | 'settings'>('home')

  // Deploy Task Modal State
  const [isDeployModalOpen, setIsDeployModalOpen] = useState(false)
  const [newTaskTitle, setNewTaskTitle] = useState('')
  const [newTaskPrompt, setNewTaskPrompt] = useState('')
  const [newTaskAgent, setNewTaskAgent] = useState<'coder' | 'finder' | 'designer' | 'video'>('coder')
  const [newTaskWorkspace, setNewTaskWorkspace] = useState('')
  const [isDeployingTask, setIsDeployingTask] = useState(false)

  const chatBottomRef = useRef<HTMLDivElement>(null)

  // 1. Fetch User Auth, Workspaces, Automations, and Projects on mount
  useEffect(() => {
    let cancelled = false

    async function bootstrap() {
      // 1. User Session
      try {
        const auth = await requestJson<{ ok: boolean; user_id?: string; account_scope_id?: string }>('/v1/auth/desktop/session')
        if (auth?.user_id && !cancelled) {
          const rawId = auth.user_id
          setUserProfile({
            id: rawId,
            name: rawId.startsWith('user_') ? `Operator (${rawId.slice(5, 11)})` : rawId,
            email: auth.account_scope_id || 'authenticated session',
          })
        }
      } catch {}

      // 2. Discover Registered Workspaces
      let detectedWorkspaces: Array<{ path: string; label: string; role: 'primary_code' | 'auxiliary'; selected: boolean }> = []
      try {
        const wsRes = await requestJson<{ workspaces?: Array<{ path: string; name?: string; id?: string }> }>('/v1/workspace/list?limit=200')
        if (wsRes?.workspaces && wsRes.workspaces.length > 0) {
          detectedWorkspaces = wsRes.workspaces.map((w, idx) => ({
            path: w.path,
            label: w.name || w.path.split('/').filter(Boolean).pop() || 'Workspace',
            role: (idx === 0 ? 'primary_code' : 'auxiliary') as 'primary_code' | 'auxiliary',
            selected: true,
          }))
          if (!cancelled) {
            setOnboardingWorkspaces(detectedWorkspaces)
          }
        }
      } catch {}

      // 3. Automations
      try {
        const autoRes = await requestJson<{ records?: any[] }>('/v3/automations/v2?action=list')
        if (autoRes?.records && !cancelled) {
          const mapped: RunningAutomation[] = autoRes.records.map((r: any) => ({
            id: r.id || 'automation',
            name: r.worker_v2?.name || r.name || 'Worker Automation',
            kind: (r.worker_v2?.kind || r.schedule?.kind || 'trigger') as any,
            status: (r.status || (r.paused ? 'idle' : 'running')) as any,
            nextRun: r.schedule?.cron || r.schedule?.interval || 'On demand',
            lastRun: r.last_run_at ? new Date(r.last_run_at).toLocaleTimeString() : 'Never',
            outputSummary: r.description || r.worker_v2?.definition?.goal || 'Automated background task',
            totalRuns: r.run_count || 0,
          }))
          setAutomations(mapped)
        }
      } catch {}

      // 4. Projects from Pebble
      try {
        const res = await requestJson<{ projects?: any[] }>('/v3/projects')
        if (cancelled) return

        if (res?.projects && res.projects.length > 0) {
          const loaded: ProjectSummary[] = res.projects.map((p) => ({
            id: p.id,
            name: p.name,
            slug: p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
            description: p.description || '',
            repoPath: p.workspaces?.[0]?.path || '.',
            branch: 'dev',
            gitStatus: 'clean',
            linkedWorkspaces: p.workspaces?.map((w: any) => w.path) || [],
            activeWorkersCount: 0,
            pendingDeliverablesCount: 0,
            runningTasksCount: 0,
            projectContext: p.project_context,
            primarySessionId: p.primary_session_id,
          }))
          setProjects(loaded)
          setSelectedProjectId(loaded[0].id)
          if (loaded[0].primarySessionId) {
            setActiveSessionId(loaded[0].primarySessionId)
          }
          fetchProjectTasks(loaded[0].id)
        } else {
          // No projects in Pebble. Do not auto-create projects without user action.
          if (!cancelled) {
            setProjects([])
            setSelectedProjectId('')
            setActiveSessionId('')
            setIsOnboardingActive(true)
          }
        }
      } catch (err) {
        console.warn('Failed to load projects from Pebble:', err)
      } finally {
        if (!cancelled) {
          setIsLoadingProjects(false)
        }
      }
    }

    void bootstrap()

    return () => {
      cancelled = true
    }
  }, [])

  // Fetch project tasks for a given project from Pebble
  const fetchProjectTasks = useCallback((projectId: string) => {
    if (!projectId) return
    requestJson<{ tasks?: any[] }>(`/v3/projects/${projectId}/tasks`)
      .then((res) => {
        const backendTasks: RunningTask[] = (res.tasks || []).map((t) => ({
          id: t.id,
          title: t.title,
          subtitle: t.description || `Autonomous execution unit for ${t.agent || 'coder'}`,
          agentType: (t.agent === 'designer' || t.agent === 'finder' || t.agent === 'video' ? t.agent : 'coder') as any,
          status: (t.status === 'in_progress' ? 'running' : t.status) || 'queued',
          workspaceTarget: t.workspace_path || t.project_id,
          elapsed: t.created_at ? `${Math.max(1, Math.round((Date.now() - t.created_at) / 60000))}m` : 'Just now',
          workerName: t.worker_name || `@${t.agent || 'Coder'} Worker`,
          priority: 'high',
          sessionId: t.session_id,
          createdAt: t.created_at,
          stageIndex: t.current_stage_index,
          totalStages: t.pipeline_stages?.length || 4,
          stepTimeline: (t.pipeline_stages || ['Inspect', 'Implement', 'Verify', 'Review']).map((st: string, idx: number) => ({
            step: idx + 1,
            label: st,
            status: idx < (t.current_stage_index || 0) ? 'complete' : (idx === t.current_stage_index ? 'processing' : 'pending'),
          })),
          deliverables: (t.deliverables || []).map((d: any) => ({
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
        setTasks(backendTasks)
        if (backendTasks.length > 0 && !selectedTaskId) {
          setSelectedTaskId(backendTasks[0].id)
        }
      })
      .catch(() => {
        setTasks([])
      })
  }, [selectedTaskId])

  // Helper to ensure an active orchestrator session exists for a project
  const ensureOrchestratorSession = useCallback(async (project: ProjectSummary): Promise<string | null> => {
    if (project.primarySessionId) {
      setActiveSessionId(project.primarySessionId)
      return project.primarySessionId
    }
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Idempotency-Key': clientRequestId,
        },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          title: `Project Orchestrator: ${project.name}`,
          workspace_path: project.repoPath || '.',
          agent_name: 'system-orchestrator',
          preference: {
            provider: 'google',
            model: 'gemini-3.8-flash',
            thinking: 'low',
          },
          metadata: {
            project_id: project.id,
            role: 'project_orchestrator',
          },
        }),
      })
      if (sessRes?.session?.id) {
        const sid = sessRes.session.id
        setActiveSessionId(sid)
        await requestJson(`/v3/projects/${project.id}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ primary_session_id: sid }),
        }).catch(() => {})
        setProjects((prev) =>
          prev.map((p) => (p.id === project.id ? { ...p, primarySessionId: sid } : p))
        )
        return sid
      }
    } catch (err) {
      console.warn('Failed to spawn orchestrator session:', err)
    }
    return null
  }, [])

  // Synchronize active orchestrator session and project tasks with selected project
  useEffect(() => {
    if (!selectedProject || isOnboardingActive) return

    fetchProjectTasks(selectedProject.id)

    if (selectedProject.primarySessionId) {
      setActiveSessionId(selectedProject.primarySessionId)
      void fetchSessionMessages(selectedProject.primarySessionId, undefined, 0, { sessionApi: 'v3', tail: true, limit: 100 }).catch(() => {})
    } else {
      void ensureOrchestratorSession(selectedProject)
    }
  }, [selectedProject?.id, isOnboardingActive, fetchProjectTasks, ensureOrchestratorSession])

  // Real-time messages for active session from V3 cache
  const liveSessionMessages = useMemo(() => {
    if (!activeSessionId || !messagesBySession[activeSessionId]) return null
    return (messagesBySession[activeSessionId].items || []).filter(
      (m: any) => m.role === 'user' || m.role === 'assistant' || m.role === 'system'
    )
  }, [activeSessionId, messagesBySession])

  const activeRecord = activeSessionId ? sessionsById[activeSessionId] : undefined
  const activeSession = activeRecord?.kind === 'full' ? activeRecord.session : undefined
  const isLiveSessionRunning = !!(activeSession && (activeSession.lifecycle as any)?.active)

  // Auto scroll chat to bottom when new messages arrive
  useEffect(() => {
    chatBottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [liveSessionMessages, isTyping])

  // Derive real active workers from V3 sessions
  const deployedWorkers = useMemo<DeployedWorker[]>(() => {
    const list: DeployedWorker[] = []
    const allRecords = Object.values(sessionsById)
    for (const rec of allRecords) {
      if (rec.kind !== 'full' || !rec.session) continue
      const sess = rec.session
      const isActive = !!(sess.lifecycle as any)?.active
      const agent = (sess as any).agent_name || (sess as any).agent?.name || 'swarm'
      if (isActive || agent !== 'swarm' || (sess.message_count ?? 0) > 1) {
        list.push({
          id: sess.id,
          name: sess.title || `@${agent}`,
          role: `${agent.toUpperCase()} Agent • ${sess.workspace_name || 'Workspace'}`,
          triggerKind: 'trigger',
          scheduleLabel: isActive ? 'Executing Live Run' : 'Idle / Ready',
          activeJobsCount: isActive ? 1 : 0,
          completedJobsCount: (sess.message_count ?? 0) > 2 ? 1 : 0,
          status: isActive ? 'active' : 'idle',
          currentJobTitle: sess.title,
          assignedTaskIds: [],
        })
      }
    }
    return list.slice(0, 12)
  }, [sessionsById])

  // Deploy task to project & spawn worker session
  const handleDeployTask = async (
    title: string,
    agent: string = 'coder',
    workerName: string = '@Coder Worker',
    prompt?: string,
    pipelineStages?: string[],
    workspacePath?: string
  ) => {
    if (!selectedProject?.id) return
    try {
      const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: title.trim(),
          agent,
          worker_name: workerName,
          workspace_path: workspacePath || selectedProject.repoPath || '.',
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

  const handleDeployModalSubmit = async () => {
    if (!newTaskTitle.trim() || !selectedProject?.id) return
    setIsDeployingTask(true)
    try {
      await handleDeployTask(
        newTaskTitle.trim(),
        newTaskAgent,
        `@${newTaskAgent.charAt(0).toUpperCase() + newTaskAgent.slice(1)} Worker`,
        newTaskPrompt.trim() || newTaskTitle.trim(),
        ['Inspect', 'Implement', 'Verify', 'Review'],
        newTaskWorkspace || selectedProject.repoPath
      )
      setIsDeployModalOpen(false)
      setNewTaskTitle('')
      setNewTaskPrompt('')
    } finally {
      setIsDeployingTask(false)
    }
  }

  // Real-time status transitions linked to V3 session lifecycles
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

  // Filtered tasks
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

  // Real message sending to Swarm Orchestrator session
  const handleSendMessage = async (textToSend?: string) => {
    const text = (textToSend || inputText).trim()
    if (!text) return

    if (!textToSend) setInputText('')
    setIsTyping(true)

    let targetSid = activeSessionId
    if (!targetSid && selectedProject) {
      targetSid = (await ensureOrchestratorSession(selectedProject)) || ''
    }

    if (targetSid) {
      try {
        await sendSessionMessage(targetSid, 'user', text)
      } catch (err) {
        console.warn('Failed to send message to orchestrator session:', err)
      } finally {
        setIsTyping(false)
      }
    } else {
      setIsTyping(false)
    }
  }

  const handleDeleteProject = async (projectId: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
    try {
      await requestJson(`/v3/projects/${projectId}`, { method: 'DELETE' })
      setProjects((prev) => prev.filter((p) => p.id !== projectId))
      if (selectedProjectId === projectId) {
        const remaining = projects.filter((p) => p.id !== projectId)
        if (remaining.length > 0) {
          setSelectedProjectId(remaining[0].id)
        } else {
          setSelectedProjectId('')
          setActiveSessionId('')
          setIsOnboardingActive(true)
        }
      }
    } catch (err) {
      console.warn('Failed to delete project:', err)
    }
  }

  // Workspaces toggling for project onboarding
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
      console.warn('Context synthesis failed, generating local fallback:', err)
      const selectedWs = onboardingWorkspaces.filter((w) => w.selected)
      const synthesized = `# ${onboardingName.trim() || 'Project Architecture'}

## Overview
${onboardingDescription.trim() || 'Multi-workspace software initiative managed by Swarm Orchestrator.'}

## Subsystems & Workspaces
${selectedWs.map((w) => `- \`${w.path}\`: ${w.label} (${w.role})`).join('\n')}

## Operational Directives
- Local-first operation; session records persist to Pebble database.
- Subagents (Coder/Designer/Finder) execute inside isolated Git worktrees.
- Strict tool isolation: raw multimedia and environment tools excluded from executive orchestrator prompt.
- Verification gate: all pull requests and deliverables require review before promotion.
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
      if (res?.project?.id) {
        newId = res.project.id
      }
    } catch (err) {
      console.warn('Backend /v3/projects save failed:', err)
    }

    // Spawn primary orchestrator session
    let orchSessionId = ''
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Idempotency-Key': clientRequestId,
        },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          title: `Project Orchestrator: ${payload.name}`,
          workspace_path: selectedWs[0]?.path || '.',
          agent_name: 'system-orchestrator',
          preference: {
            provider: 'google',
            model: 'gemini-3.8-flash',
            thinking: 'low',
          },
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
        }).catch(() => {})
      }
    } catch (e) {
      console.warn('Failed to spawn initial orchestrator session:', e)
    }

    const newProject: ProjectSummary = {
      id: newId,
      name: payload.name,
      slug: payload.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      description: payload.description,
      repoPath: selectedWs[0]?.path || '.',
      branch: 'dev',
      gitStatus: 'clean',
      linkedWorkspaces: selectedWs.map((w) => w.path),
      activeWorkersCount: 0,
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
    fetchProjectTasks(newId)
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
        {/* App Branding & Header */}
        <div className="p-3.5 border-b border-slate-800/80">
          <div className="flex items-center justify-between pb-2">
            <div className="flex items-center gap-2.5">
              <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30 shadow-[0_2px_8px_rgba(37,99,235,0.3)]">
                <Sparkles size={14} />
              </div>
              <div>
                <div className="text-xs font-bold tracking-tight text-white">Swarm Orchestrate</div>
                <div className="text-[10px] text-slate-400">Autonomous Multi-Agent System</div>
              </div>
            </div>

            {onNavigateHome && (
              <button
                onClick={onNavigateHome}
                className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-800 hover:text-slate-200 transition-all border border-slate-700/60"
                title="Back to Sessions"
              >
                <X size={13} />
              </button>
            )}
          </div>

          {/* Search Bar */}
          <div className="mt-2.5 flex items-center justify-between px-3 py-1.5 rounded-xl bg-[#090d16] border border-slate-800 text-xs text-slate-400">
            <div className="flex items-center gap-2">
              <Search size={13} className="text-slate-500" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search tasks & projects..."
                className="bg-transparent border-none text-[11px] text-slate-200 placeholder-slate-500 focus:outline-none w-full"
              />
            </div>
            {searchQuery && (
              <button onClick={() => setSearchQuery('')} className="text-slate-500 hover:text-slate-300">
                <X size={11} />
              </button>
            )}
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
            <span>Tasks & Canvas</span>
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
            <span>Projects ({projects.length})</span>
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
            <span>Automations ({automations.length})</span>
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
            <span>Deliverables ({liveTasks.flatMap((t) => t.deliverables || []).length})</span>
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
            <span>Project Charter</span>
          </button>
        </div>

        {/* Projects Switcher Section */}
        <div className="p-3 border-b border-slate-800/80 flex-1 overflow-y-auto">
          <div className="flex items-center justify-between pb-2">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">
              Projects
            </span>
            <button
              onClick={() => {
                setIsOnboardingActive(true)
                setActiveNavTab('home')
              }}
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
                <div
                  key={proj.id}
                  onClick={() => {
                    setSelectedProjectId(proj.id)
                    setIsOnboardingActive(false)
                  }}
                  className={`group flex items-center justify-between p-2 text-left rounded-xl transition-all cursor-pointer ${
                    isSelected
                      ? 'bg-slate-800/90 border border-slate-700/80 text-white shadow-sm'
                      : 'border border-transparent hover:bg-slate-800/40 text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <div className="flex items-center gap-2.5 min-w-0 flex-1">
                    <div
                      className={`flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg border transition-all ${
                        isSelected
                          ? 'bg-blue-600/20 text-blue-400 border-blue-500/30 shadow-[0_1px_6px_rgba(37,99,235,0.2)]'
                          : 'bg-slate-800/60 text-slate-500 border-slate-700/50 group-hover:text-slate-300'
                      }`}
                    >
                      <Folder size={12} />
                    </div>
                    <div className="flex flex-col min-w-0">
                      <span className="text-xs font-semibold truncate">{proj.name}</span>
                      <span className="text-[10px] text-slate-500 truncate font-mono">
                        {proj.linkedWorkspaces.length} workspace{proj.linkedWorkspaces.length === 1 ? '' : 's'}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1 flex-shrink-0">
                    {isSelected && (
                      <span className="h-2 w-2 rounded-full bg-blue-500 shadow-[0_0_6px_rgba(59,130,246,0.6)]" />
                    )}
                    <button
                      type="button"
                      onClick={(e) => handleDeleteProject(proj.id, e)}
                      className="opacity-0 group-hover:opacity-100 p-1 text-slate-400 hover:text-rose-400 rounded-md transition-all hover:bg-rose-500/10"
                      title="Delete Project"
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
              )
            })}
            {projects.length === 0 && !isOnboardingActive && (
              <div className="p-3 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-xl">
                No projects saved.
              </div>
            )}
          </div>
        </div>

        {/* Theme Picker */}
        <div className="p-3 border-b border-slate-800/80">
          <div className="flex items-center justify-between pb-1.5">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">
              Palette Theme
            </span>
            <span className="text-[10px] font-mono text-slate-400">{theme.name}</span>
          </div>
          <div className="grid grid-cols-5 gap-1.5">
            {ORCHESTRATE_THEME_IDS.slice(0, 5).map((tId) => {
              const t = ORCHESTRATE_THEMES[tId]
              const isActive = currentThemeId === tId
              return (
                <button
                  key={tId}
                  onClick={() => setCurrentThemeId(tId)}
                  className={`flex flex-col items-center justify-center p-1 rounded-lg border text-[9px] transition-all ${
                    isActive
                      ? 'border-blue-500 bg-blue-500/10 text-white font-bold'
                      : 'border-slate-800 bg-[#090d16] text-slate-400 hover:text-slate-200'
                  }`}
                  title={t.name}
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

        {/* Real User HUD */}
        <div className="p-3 border-t border-slate-800/80 flex items-center justify-between bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full bg-blue-600/20 text-blue-400 font-bold text-[10px] border border-blue-500/30">
              {userProfile.name.slice(0, 2).toUpperCase()}
            </div>
            <div className="flex flex-col min-w-0">
              <span className="text-xs font-semibold text-slate-200 truncate">{userProfile.name}</span>
              <span className="text-[10px] text-slate-500 truncate">{userProfile.email}</span>
            </div>
          </div>
          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <MoreHorizontal size={14} />
          </button>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (CANVAS / TASKS / VIEWS)
         ───────────────────────────────────────────────────────────── */}
      <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {isOnboardingActive ? (
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-6">
            {/* Onboarding Header */}
            <div className="flex items-start justify-between border-b border-slate-800/80 pb-5">
              <div>
                <div className="flex items-center gap-2 text-blue-400 font-semibold text-xs uppercase tracking-wider mb-1">
                  <Sparkles size={14} className="text-blue-400 animate-pulse" />
                  <span>Project Setup & Architecture Synthesis</span>
                </div>
                <h1 className="text-xl font-bold text-white tracking-tight">Create Your Project</h1>
                <p className="text-xs text-slate-400 mt-1 max-w-xl">
                  A Project elevates your local workspaces into an executive space coordinated by the Swarm Orchestrator.
                </p>
              </div>
              {projects.length > 0 && (
                <button
                  onClick={() => setIsOnboardingActive(false)}
                  className="text-xs text-slate-400 hover:text-slate-200 px-3 py-1.5 rounded-xl border border-slate-800 hover:bg-slate-800 transition-colors"
                >
                  Cancel
                </button>
              )}
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
                  onKeyDown={(e) => e.key === 'Enter' && handleAddCustomFolder()}
                  placeholder="Enter path to another workspace folder on this machine..."
                  className="flex-1 text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 font-mono"
                />
                <button
                  type="button"
                  onClick={handleAddCustomFolder}
                  className="px-3 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-200 text-xs font-medium transition-all"
                >
                  Add Folder
                </button>
              </div>
            </div>

            {/* Section 2: AI Context Synthesis */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-xs font-semibold text-slate-300">2. Authoritative Project Context (project.md)</div>
                  <p className="text-[11px] text-slate-500">
                    Synthesizes architectural boundaries from selected workspaces to inject into Swarm Orchestrator.
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleSynthesizeContext}
                    disabled={isSynthesizing || onboardingWorkspaces.filter((w) => w.selected).length === 0}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600/20 hover:bg-blue-600/30 text-blue-400 border border-blue-500/30 text-xs font-medium transition-all disabled:opacity-50"
                  >
                    <Sparkles size={12} className={isSynthesizing ? 'animate-spin' : ''} />
                    <span>{isSynthesizing ? 'Synthesizing...' : 'Generate with AI'}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setIsEditingContext(!isEditingContext)}
                    className="flex items-center gap-1 px-2.5 py-1.5 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs transition-all"
                  >
                    <Edit3 size={12} />
                    <span>{isEditingContext ? 'Preview' : 'Edit'}</span>
                  </button>
                </div>
              </div>

              {isEditingContext ? (
                <textarea
                  value={onboardingContext}
                  onChange={(e) => setOnboardingContext(e.target.value)}
                  rows={10}
                  className="w-full text-xs font-mono rounded-xl bg-slate-950/80 border border-slate-800 p-3 text-slate-200 focus:outline-none focus:border-blue-500/60 transition-all leading-relaxed"
                />
              ) : (
                <div className="rounded-xl bg-slate-950/80 border border-slate-800 p-3 max-h-56 overflow-y-auto font-mono text-[11px] text-slate-300 whitespace-pre-wrap leading-relaxed">
                  {onboardingContext || 'Click "Generate with AI" to synthesize architecture from selected workspaces.'}
                </div>
              )}
            </div>

            {/* Submit Action */}
            <div className="flex items-center justify-between pt-2">
              <div className="text-[11px] text-slate-500">
                Local-first • Saved securely in Swarm Pebble database
              </div>
              <button
                type="button"
                onClick={handleCreateAndActivateProject}
                disabled={isActivating || onboardingWorkspaces.filter((w) => w.selected).length === 0}
                className="px-5 py-2.5 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 text-white font-semibold text-xs shadow-lg shadow-blue-600/20 transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <span>{isActivating ? 'Activating...' : 'Create & Activate Project'}</span>
                <ArrowRight size={14} />
              </button>
            </div>
          </div>
        ) : activeNavTab === 'projects' ? (
          /* PROJECTS OVERVIEW TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Registered Projects</h2>
                <p className="text-xs text-slate-400 mt-0.5">Projects managed by Swarm Orchestrator with Pebble persistence</p>
              </div>
              <button
                onClick={() => setIsOnboardingActive(true)}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold"
              >
                <Plus size={13} />
                <span>+ New Project</span>
              </button>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {projects.map((p) => (
                <div
                  key={p.id}
                  onClick={() => {
                    setSelectedProjectId(p.id)
                    setActiveNavTab('home')
                  }}
                  className={`p-4 rounded-2xl border transition-all cursor-pointer ${
                    p.id === selectedProjectId
                      ? 'bg-slate-800/80 border-blue-500/50 shadow-md'
                      : 'bg-[#0a0f1d] border-slate-800 hover:border-slate-700'
                  }`}
                >
                  <div className="flex items-center justify-between mb-2">
                    <h3 className="text-sm font-bold text-white flex items-center gap-2">
                      <Folder size={15} className="text-blue-400" />
                      <span>{p.name}</span>
                    </h3>
                    <div className="flex items-center gap-2">
                      <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                        {p.branch || 'dev'}
                      </span>
                      <button
                        type="button"
                        onClick={(e) => handleDeleteProject(p.id, e)}
                        className="opacity-0 group-hover:opacity-100 p-1 text-slate-400 hover:text-rose-400 rounded-md hover:bg-rose-500/10 transition-all"
                        title="Delete Project"
                      >
                        <Trash2 size={13} />
                      </button>
                    </div>
                  </div>
                  <p className="text-xs text-slate-400 mb-3">{p.description || 'No description'}</p>
                  <div className="space-y-1">
                    <span className="text-[10px] text-slate-500 font-semibold uppercase tracking-wider block">
                      Bound Workspaces:
                    </span>
                    <div className="flex flex-wrap gap-1">
                      {p.linkedWorkspaces.map((ws) => (
                        <span key={ws} className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-900 border border-slate-800 text-slate-300">
                          {ws}
                        </span>
                      ))}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        ) : activeNavTab === 'automations' ? (
          /* AUTOMATIONS OVERVIEW TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Registered Automations</h2>
                <p className="text-xs text-slate-400 mt-0.5">Worker V2 schedules and background automations</p>
              </div>
            </div>
            {automations.length > 0 ? (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                {automations.map((a) => (
                  <div key={a.id} className="p-4 rounded-2xl border border-slate-800 bg-[#0a0f1d] space-y-2">
                    <div className="flex items-center justify-between">
                      <h4 className="text-xs font-bold text-white flex items-center gap-2">
                        <Zap size={14} className="text-amber-400" />
                        <span>{a.name}</span>
                      </h4>
                      <span className="text-[9px] uppercase font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                        {a.kind}
                      </span>
                    </div>
                    <p className="text-xs text-slate-400">{a.outputSummary}</p>
                    <div className="flex items-center justify-between text-[11px] font-mono text-slate-500 pt-2 border-t border-slate-800/80">
                      <span>Runs: {a.totalRuns}</span>
                      <span>Next: {a.nextRun || 'On demand'}</span>
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="p-8 text-center border border-dashed border-slate-800 rounded-2xl">
                <Zap size={24} className="mx-auto text-slate-600 mb-2" />
                <h4 className="text-xs font-bold text-slate-300">No automations registered</h4>
                <p className="text-[11px] text-slate-500 mt-1">Use Worker V2 or the Automations tab to create scheduled triggers.</p>
              </div>
            )}
          </div>
        ) : activeNavTab === 'deliverables' ? (
          /* DELIVERABLES TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Project Deliverables</h2>
                <p className="text-xs text-slate-400 mt-0.5">Media assets, reports, and code deliverables produced by autonomous workers</p>
              </div>
            </div>
            {liveTasks.flatMap((t) => t.deliverables || []).length > 0 ? (
              <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
                {liveTasks.flatMap((t) => t.deliverables || []).map((d) => (
                  <div key={d.id} className="rounded-2xl border border-slate-800 bg-[#0a0f1d] p-3 space-y-2">
                    <DeliverableThumbnail
                      type={d.thumbnailType}
                      duration={d.duration}
                      onPlay={() => setActiveVideoPreview(d)}
                    />
                    <div className="text-xs font-semibold text-white truncate">{d.title}</div>
                    <div className="flex items-center justify-between text-[10px] text-slate-400 font-mono">
                      <span>{d.type}</span>
                      <span className="text-blue-400">{d.status}</span>
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="p-8 text-center border border-dashed border-slate-800 rounded-2xl">
                <Layers size={24} className="mx-auto text-slate-600 mb-2" />
                <h4 className="text-xs font-bold text-slate-300">No deliverables yet</h4>
                <p className="text-[11px] text-slate-500 mt-1">Deploy tasks generating video or designs to view deliverables here.</p>
              </div>
            )}
          </div>
        ) : activeNavTab === 'settings' ? (
          /* PROJECT CHARTER / SETTINGS TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Project Charter: {selectedProject?.name}</h2>
                <p className="text-xs text-slate-400 mt-0.5">
                  Authoritative project.md context injected into Swarm Orchestrator prompt
                </p>
              </div>
              <div className="flex items-center gap-2">
                {selectedProject && (
                  <button
                    type="button"
                    onClick={() => handleDeleteProject(selectedProject.id)}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-rose-600/10 hover:bg-rose-600/20 text-rose-400 border border-rose-500/30 text-xs font-semibold transition-all"
                  >
                    <Trash2 size={12} />
                    <span>Delete Project</span>
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => {
                  if (selectedProject?.linkedWorkspaces) {
                    setIsSynthesizing(true)
                    requestJson<{ project_context: string }>('/v3/projects/synthesize-context', {
                      method: 'POST',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({
                        name: selectedProject.name,
                        workspaces: selectedProject.linkedWorkspaces,
                      }),
                    })
                      .then((res) => {
                        if (res?.project_context) {
                          requestJson(`/v3/projects/${selectedProject.id}`, {
                            method: 'PATCH',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ project_context: res.project_context }),
                          }).then(() => {
                            setProjects((prev) =>
                              prev.map((p) => (p.id === selectedProject.id ? { ...p, projectContext: res.project_context } : p))
                            )
                          })
                        }
                      })
                      .finally(() => setIsSynthesizing(false))
                  }
                }}
                disabled={isSynthesizing}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600/20 hover:bg-blue-600/30 text-blue-400 border border-blue-500/30 text-xs font-semibold transition-all disabled:opacity-50"
              >
                <RefreshCw size={12} className={isSynthesizing ? 'animate-spin' : ''} />
                <span>{isSynthesizing ? 'Synthesizing...' : 'Re-synthesize Context'}</span>
              </button>
            </div>
          </div>
          <div className="rounded-2xl border border-slate-800 bg-slate-950 p-4">
              <pre className="font-mono text-xs text-slate-300 whitespace-pre-wrap leading-relaxed overflow-x-auto">
                {selectedProject?.projectContext || 'No synthesized context available. Click "Re-synthesize Context" to scan bound repositories.'}
              </pre>
            </div>
          </div>
        ) : (
          /* HOME: 5-VARIANT TASK MANAGEMENT CANVAS */
          <>
            {/* TOP TOOLBAR: AUTOMATION OVERVIEW & 5-VARIANT SWITCHER DOCK */}
            <div className="flex flex-col border-b border-slate-800/80 bg-[#0a0f1d]/60">
              <div className="flex items-center justify-between p-3.5 pb-2.5">
                <div className="flex items-center gap-3">
                  <div>
                    <div className="flex items-center gap-2">
                      <h1 className="text-base font-bold tracking-tight text-white">
                        {selectedProject?.name || 'Automation Overview'}
                      </h1>
                      <span className="rounded-full bg-blue-500/10 border border-blue-500/30 px-2 py-0.5 text-[10px] font-semibold text-blue-400">
                        {liveTasks.length} {liveTasks.length === 1 ? 'Task' : 'Tasks'}
                      </span>
                    </div>
                    <p className="text-[11px] text-slate-400 mt-0.5">
                      Autonomous workers executing jobs across {selectedProject?.name || 'workspace'}
                    </p>
                  </div>
                </div>

                {/* Quick Action buttons */}
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setIsDeployModalOpen(true)}
                    className="flex items-center gap-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
                  >
                    <Plus size={13} />
                    <span>+ Deploy Task</span>
                  </button>
                  <button
                    onClick={() =>
                      handleDeployTask(
                        'Run Local Testbench Suite',
                        'coder',
                        '@Code Verifier',
                        'Run critical test gate and inspect testbench health',
                        ['Inspect', 'Execute', 'Analyze', 'Review']
                      )
                    }
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
                      title="Compact Matrix: Dense table view with expandable drawers"
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
                      title="Pipeline Kanban: Stage workflow by task status"
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
                      title="Worker Fleet: Active agent sessions and their jobs"
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
                      title="Split Studio: Master list on left, live inspector on right"
                    >
                      <Layers size={12} />
                      <span>4. Split Studio</span>
                    </button>

                    <button
                      onClick={() => setMiddleVariant('timeline')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all ${
                        middleVariant === 'timeline'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
                      title="Timeline Stream: Chronological progress & history"
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

            {/* STATUS / AUTOMATION TICKER */}
            <div className="px-4 py-2 border-b border-slate-800/80 bg-[#080d19]/80 flex items-center justify-between gap-4 text-xs">
              <div className="flex items-center gap-2.5 min-w-0">
                <span className="flex h-2 w-2 rounded-full bg-emerald-400 flex-shrink-0" />
                <div className="flex items-center gap-2 min-w-0">
                  <span className="font-semibold text-white truncate text-[11px]">
                    {selectedProject?.name || 'Project Orchestrator'}
                  </span>
                  <span className="text-[10px] text-slate-400 truncate">
                    • System Orchestrator Active • {selectedProject?.linkedWorkspaces?.length || 0} Bound Workspace{selectedProject?.linkedWorkspaces?.length === 1 ? '' : 's'}
                  </span>
                </div>
              </div>
              <div className="flex items-center gap-2 text-[10px] font-mono text-slate-400">
                <span>Branch: {selectedProject?.branch || 'dev'}</span>
                <span>•</span>
                <span className="text-emerald-400">Pebble DB Synced</span>
              </div>
            </div>

            {/* MAIN BODY: 5 DISTINCT VARIANTS */}
            <div className="flex-1 overflow-hidden flex flex-col">
              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 1: COMPACT MATRIX & DRAWER
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'matrix' && (
                <div className="flex-1 flex flex-col overflow-hidden p-3.5 space-y-3">
                  {/* Search & Filter bar */}
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex-1 relative">
                      <Search size={13} className="absolute left-3 top-2.5 text-slate-500" />
                      <input
                        type="text"
                        value={searchQuery}
                        onChange={(e) => setSearchQuery(e.target.value)}
                        placeholder="Search tasks by title, worker, or tag..."
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
                            ? `All (${liveTasks.length})`
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

                  {/* Task rows */}
                  <div className="flex-1 overflow-y-auto space-y-1.5 pr-1">
                    {filteredTasks.length > 0 ? (
                      filteredTasks.map((t) => {
                        const isExpanded = expandedTaskId === t.id
                        return (
                          <div
                            key={t.id}
                            className="rounded-xl border border-slate-800/80 bg-[#0a0f1d]/70 hover:border-slate-700/80 transition-all overflow-hidden"
                          >
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
                                <span className="font-mono text-[10px] text-slate-500 w-16">{t.id.slice(0, 8)}</span>
                                <span className="text-xs font-semibold text-slate-200 truncate">{t.title}</span>
                                <span className="text-[10px] text-slate-400 truncate hidden md:inline">
                                  {t.subtitle}
                                </span>
                              </div>

                              <div className="flex items-center gap-3 flex-shrink-0">
                                <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                                  {t.workerName}
                                </span>
                                <span className="text-[10px] text-slate-500 font-mono">{t.elapsed}</span>
                                <ChevronDown
                                  size={13}
                                  className={`text-slate-400 transition-transform ${isExpanded ? 'rotate-180' : ''}`}
                                />
                              </div>
                            </div>

                            {/* Drawer Content */}
                            {isExpanded && (
                              <div className="p-3 border-t border-slate-800/60 bg-[#070b14] space-y-3 text-xs">
                                <div className="flex items-center justify-between">
                                  <div className="text-slate-400 font-medium">Pipeline Execution Progress</div>
                                  {t.sessionId && (
                                    <span className="font-mono text-[10px] text-blue-400">
                                      Session: {t.sessionId.slice(0, 12)}...
                                    </span>
                                  )}
                                </div>
                                <div className="grid grid-cols-4 gap-2">
                                  {t.stepTimeline?.map((st) => (
                                    <div
                                      key={st.step}
                                      className={`p-2 rounded-xl border text-[11px] ${
                                        st.status === 'complete'
                                          ? 'border-emerald-500/30 bg-emerald-950/20 text-emerald-300'
                                          : st.status === 'processing'
                                          ? 'border-blue-500/40 bg-blue-950/30 text-blue-300'
                                          : 'border-slate-800 bg-slate-900/40 text-slate-500'
                                      }`}
                                    >
                                      <div className="font-mono text-[9px] uppercase">Stage {st.step}</div>
                                      <div className="font-semibold">{st.label}</div>
                                    </div>
                                  ))}
                                </div>

                                {t.deliverables && t.deliverables.length > 0 && (
                                  <div>
                                    <div className="text-slate-400 font-medium mb-1.5">Deliverables</div>
                                    <div className="grid grid-cols-3 gap-2">
                                      {t.deliverables.map((d) => (
                                        <div
                                          key={d.id}
                                          onClick={() => setActiveVideoPreview(d)}
                                          className="p-2 rounded-xl border border-slate-800 bg-[#0a0f1d] hover:border-blue-500/40 cursor-pointer space-y-1"
                                        >
                                          <div className="text-xs font-semibold text-white truncate">{d.title}</div>
                                          <div className="text-[10px] text-blue-400 font-mono flex items-center justify-between">
                                            <span>{d.type}</span>
                                            <span>{d.status}</span>
                                          </div>
                                        </div>
                                      ))}
                                    </div>
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                        )
                      })
                    ) : (
                      <div className="flex-1 flex flex-col items-center justify-center p-8 text-center border border-dashed border-slate-800 rounded-2xl bg-[#080c16]/50">
                        <Code size={20} className="text-slate-600 mb-2" />
                        <h4 className="text-xs font-bold text-slate-300">No tasks active</h4>
                        <p className="text-[11px] text-slate-500 mt-1 mb-4">
                          Deploy an autonomous task or prompt the orchestrator to begin work across your workspaces.
                        </p>
                        <button
                          onClick={() => setIsDeployModalOpen(true)}
                          className="px-4 py-2 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold shadow-md shadow-blue-600/20 transition-all flex items-center gap-1.5"
                        >
                          <Plus size={13} />
                          <span>Deploy First Task</span>
                        </button>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 2: MISSION PIPELINE KANBAN
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'kanban' && (
                <div className="flex-1 flex overflow-x-auto p-4 gap-3">
                  {(
                    [
                      { key: 'queued', label: 'Queued', color: 'slate' },
                      { key: 'running', label: 'In Progress', color: 'blue' },
                      { key: 'needs_review', label: 'Needs Review', color: 'amber' },
                      { key: 'completed', label: 'Completed', color: 'emerald' },
                    ] as const
                  ).map((col) => {
                    const colTasks = liveTasks.filter((t) => t.status === col.key)
                    return (
                      <div
                        key={col.key}
                        className="w-72 flex-shrink-0 flex flex-col rounded-2xl border border-slate-800/80 bg-[#090d16]/70 p-3 space-y-2.5 overflow-hidden"
                      >
                        <div className="flex items-center justify-between pb-1.5 border-b border-slate-800/60">
                          <span className="text-xs font-bold text-white flex items-center gap-1.5">
                            <span
                              className={`h-2 w-2 rounded-full ${
                                col.color === 'blue'
                                  ? 'bg-blue-400'
                                  : col.color === 'amber'
                                  ? 'bg-amber-400'
                                  : col.color === 'emerald'
                                  ? 'bg-emerald-400'
                                  : 'bg-slate-500'
                              }`}
                            />
                            <span>{col.label}</span>
                          </span>
                          <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-400">
                            {colTasks.length}
                          </span>
                        </div>

                        <div className="flex-1 overflow-y-auto space-y-2 pr-0.5">
                          {colTasks.map((t) => (
                            <div
                              key={t.id}
                              onClick={() => {
                                setSelectedTaskId(t.id)
                                setMiddleVariant('split')
                              }}
                              className="p-3 rounded-xl border border-slate-800 bg-[#0d121f] hover:border-slate-700 cursor-pointer space-y-2 shadow-sm transition-all"
                            >
                              <div className="text-xs font-semibold text-white leading-snug">{t.title}</div>
                              <p className="text-[10px] text-slate-400 line-clamp-2">{t.subtitle}</p>
                              <div className="flex items-center justify-between text-[10px] font-mono text-slate-500 pt-1.5 border-t border-slate-800/80">
                                <span>{t.workerName}</span>
                                <span>{t.elapsed}</span>
                              </div>
                            </div>
                          ))}
                          {colTasks.length === 0 && (
                            <div className="p-4 text-center text-[10px] text-slate-600 border border-dashed border-slate-850 rounded-xl">
                              No {col.label.toLowerCase()} tasks
                            </div>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 3: AUTONOMOUS WORKER FLEET
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'fleet' && (
                <div className="flex-1 overflow-y-auto p-4 space-y-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <h3 className="text-sm font-bold text-white">Active Worker Sessions</h3>
                      <p className="text-[11px] text-slate-400">
                        Autonomous worker sessions executing across your repositories
                      </p>
                    </div>
                    <button
                      onClick={() => setIsDeployModalOpen(true)}
                      className="flex items-center gap-1.5 px-3 py-1 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium"
                    >
                      <Plus size={12} />
                      <span>Deploy Worker</span>
                    </button>
                  </div>

                  {deployedWorkers.length > 0 ? (
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
                                <h4 className="text-xs font-bold text-white truncate max-w-[180px]">{worker.name}</h4>
                                <p className="text-[10px] text-slate-400 truncate max-w-[180px]">{worker.role}</p>
                              </div>
                            </div>
                            <span
                              className={`px-2 py-0.5 rounded-full text-[9px] font-mono font-semibold uppercase ${
                                worker.status === 'active'
                                  ? 'bg-blue-500/20 text-blue-400 border border-blue-500/30'
                                  : 'bg-slate-800 text-slate-400'
                              }`}
                            >
                              {worker.status}
                            </span>
                          </div>

                          <div className="flex items-center justify-between text-[11px] border-t border-slate-800/80 pt-2 text-slate-400 font-mono">
                            <span>{worker.scheduleLabel}</span>
                            <span className="text-blue-400 font-bold">{worker.activeJobsCount} active</span>
                          </div>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <div className="p-8 text-center border border-dashed border-slate-800 rounded-2xl">
                      <Bot size={24} className="mx-auto text-slate-600 mb-2" />
                      <h4 className="text-xs font-bold text-slate-300">No active worker sessions</h4>
                      <p className="text-[11px] text-slate-500 mt-1">Deploy a task to launch an autonomous worker session.</p>
                    </div>
                  )}

                  {/* Tasks List in Fleet */}
                  <div className="pt-2 space-y-3">
                    <h4 className="text-xs font-bold text-white">Project Tasks ({liveTasks.length})</h4>
                    <div className="space-y-1.5">
                      {liveTasks.map((task) => (
                        <div
                          key={task.id}
                          className="flex items-center justify-between p-2.5 rounded-xl border border-slate-800/80 bg-[#0a0f1d]/70 text-xs"
                        >
                          <div className="flex items-center gap-2">
                            <span
                              className={`h-2 w-2 rounded-full ${
                                task.status === 'running'
                                  ? 'bg-blue-400 animate-pulse'
                                  : task.status === 'needs_review'
                                  ? 'bg-amber-400'
                                  : task.status === 'completed'
                                  ? 'bg-emerald-400'
                                  : 'bg-slate-600'
                              }`}
                            />
                            <span className="font-semibold text-slate-200">{task.title}</span>
                          </div>
                          <span className="text-[10px] font-mono text-slate-500">{task.elapsed}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 4: SPLIT STUDIO CONSOLE
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'split' && (
                <div className="flex-1 flex overflow-hidden">
                  {/* Left Column: Tasks List */}
                  <div className="w-1/2 border-r border-slate-800/80 flex flex-col p-3 overflow-y-auto space-y-2">
                    <div className="text-xs font-bold text-white pb-1">Tasks ({liveTasks.length})</div>
                    {liveTasks.map((t) => {
                      const isSel = selectedTaskId === t.id
                      return (
                        <div
                          key={t.id}
                          onClick={() => setSelectedTaskId(t.id)}
                          className={`p-3 rounded-xl border transition-all cursor-pointer ${
                            isSel
                              ? 'bg-blue-950/20 border-blue-500/50 text-white shadow-sm'
                              : 'bg-[#0a0f1d] border-slate-800/80 hover:border-slate-700 text-slate-300'
                          }`}
                        >
                          <div className="text-xs font-semibold leading-snug">{t.title}</div>
                          <div className="flex items-center justify-between text-[10px] font-mono text-slate-500 mt-2">
                            <span>{t.workerName}</span>
                            <span className="text-blue-400 font-bold">{t.status}</span>
                          </div>
                        </div>
                      )
                    })}
                    {liveTasks.length === 0 && (
                      <div className="p-6 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-xl">
                        No tasks found.
                      </div>
                    )}
                  </div>

                  {/* Right Column: Live Inspector */}
                  <div className="w-1/2 flex flex-col p-4 overflow-y-auto space-y-4">
                    {selectedTaskForSplit ? (
                      <>
                        <div>
                          <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20">
                            {selectedTaskForSplit.agentType}
                          </span>
                          <h3 className="text-sm font-bold text-white mt-1.5">{selectedTaskForSplit.title}</h3>
                          <p className="text-xs text-slate-400 mt-1">{selectedTaskForSplit.subtitle}</p>
                        </div>

                        <div className="p-3 rounded-xl border border-slate-800 bg-[#090d16] space-y-2 text-xs">
                          <div className="text-slate-400 font-medium">Pipeline Stages</div>
                          <div className="space-y-1.5">
                            {selectedTaskForSplit.stepTimeline?.map((st) => (
                              <div key={st.step} className="flex items-center justify-between text-[11px] font-mono">
                                <span className="text-slate-300">
                                  {st.step}. {st.label}
                                </span>
                                <span
                                  className={`px-1.5 py-0.2 rounded text-[9px] ${
                                    st.status === 'complete'
                                      ? 'text-emerald-400'
                                      : st.status === 'processing'
                                      ? 'text-blue-400'
                                      : 'text-slate-500'
                                  }`}
                                >
                                  {st.status}
                                </span>
                              </div>
                            ))}
                          </div>
                        </div>

                        {selectedTaskForSplit.deliverables && selectedTaskForSplit.deliverables.length > 0 && (
                          <div className="space-y-2">
                            <div className="text-xs font-bold text-white">Attached Deliverables</div>
                            {selectedTaskForSplit.deliverables.map((d) => (
                              <div
                                key={d.id}
                                onClick={() => setActiveVideoPreview(d)}
                                className="p-3 rounded-xl border border-slate-800 bg-[#090d16] hover:border-blue-500/40 cursor-pointer space-y-1.5"
                              >
                                <div className="text-xs font-semibold text-white">{d.title}</div>
                                <div className="flex items-center justify-between text-[10px] text-blue-400 font-mono">
                                  <span>{d.type}</span>
                                  <span>{d.duration}</span>
                                </div>
                              </div>
                            ))}
                          </div>
                        )}
                      </>
                    ) : (
                      <div className="flex-1 flex items-center justify-center text-xs text-slate-500">
                        Select a task to inspect details
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 5: TIMELINE ACTIVITY STREAM
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'timeline' && (
                <div className="flex-1 overflow-y-auto p-4 space-y-3">
                  <div className="text-xs font-bold text-white pb-1">Activity Stream</div>
                  {liveTasks.map((t, idx) => (
                    <div key={t.id} className="flex items-start gap-3 p-3 rounded-xl border border-slate-800/80 bg-[#0a0f1d] text-xs">
                      <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 font-mono text-[10px] font-bold">
                        {idx + 1}
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center justify-between">
                          <span className="font-semibold text-white truncate">{t.title}</span>
                          <span className="text-[10px] font-mono text-slate-500">{t.elapsed}</span>
                        </div>
                        <p className="text-[11px] text-slate-400 mt-0.5">{t.subtitle}</p>
                        <div className="flex items-center gap-2 mt-2">
                          <span className="text-[9px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                            {t.workerName}
                          </span>
                          <span className="text-[9px] font-mono text-blue-400">{t.status}</span>
                        </div>
                      </div>
                    </div>
                  ))}
                  {liveTasks.length === 0 && (
                    <div className="p-8 text-center border border-dashed border-slate-800 rounded-2xl text-xs text-slate-500">
                      No task activity recorded yet.
                    </div>
                  )}
                </div>
              )}
            </div>
          </>
        )}
      </main>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 3: RIGHT PANEL (SWARM ORCHESTRATOR CHAT & DISPATCH)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative flex w-80 flex-shrink-0 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* Chat Header */}
        <div className="flex items-center justify-between p-3.5 border-b border-slate-800/80 bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5">
            <div className="relative flex h-7 w-7 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30">
              <Bot size={15} />
              <span
                className={`absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full border border-slate-900 ${
                  isLiveSessionRunning ? 'bg-blue-400 animate-pulse' : activeSessionId ? 'bg-emerald-400' : 'bg-amber-400'
                }`}
              />
            </div>
            <div>
              <div className="text-xs font-bold text-white">Swarm Orchestrator</div>
              <div className="text-[10px] text-slate-400 truncate max-w-[150px]">
                {selectedProject?.name || 'Project Executive'}
              </div>
            </div>
          </div>
          <div className="flex items-center gap-1.5">
            <span
              className={`text-[9px] font-mono px-2 py-0.5 rounded-full border ${
                isLiveSessionRunning
                  ? 'border-blue-500/30 bg-blue-500/10 text-blue-400'
                  : 'border-slate-700 bg-slate-800 text-slate-300'
              }`}
            >
              {isLiveSessionRunning ? 'Thinking...' : activeSessionId ? 'Connected' : 'Initializing'}
            </span>
          </div>
        </div>

        {/* Chat Messages Stream */}
        <div className="flex-1 space-y-3.5 overflow-y-auto p-3.5 text-xs">
          {liveSessionMessages && liveSessionMessages.length > 0 ? (
            liveSessionMessages.map((msg: any) => {
              const isUser = msg.role === 'user'
              const isSystem = msg.role === 'system'
              return (
                <div
                  key={msg.id}
                  className={`flex flex-col ${isUser ? 'items-end' : 'items-start'}`}
                >
                  {!isUser && !isSystem && (
                    <div className="flex items-center gap-1.5 mb-1 text-[10px] font-medium text-slate-400">
                      <Bot size={12} className="text-blue-400" />
                      <span>Swarm Orchestrator</span>
                    </div>
                  )}
                  {isSystem && (
                    <div className="flex items-center gap-1.5 mb-1 text-[10px] font-medium text-amber-400">
                      <Zap size={12} className="text-amber-400" />
                      <span>System Event</span>
                    </div>
                  )}
                  <div
                    className={`max-w-[95%] p-3 text-xs leading-relaxed transition-all ${
                      isUser
                        ? 'rounded-2xl rounded-tr-sm bg-gradient-to-r from-blue-600 to-indigo-600 text-white shadow-[0_2px_12px_rgba(37,99,235,0.25)]'
                        : isSystem
                        ? 'rounded-2xl rounded-tl-sm bg-amber-950/20 border border-amber-500/30 text-amber-200 whitespace-pre-wrap font-mono text-[11px]'
                        : 'rounded-2xl rounded-tl-sm bg-[#090d16] border border-slate-800/80 text-slate-200 whitespace-pre-wrap'
                    }`}
                  >
                    <p className="whitespace-pre-line">{msg.content}</p>
                  </div>
                  <span className="mt-1 text-[9px] text-slate-500">
                    {msg.created_at ? new Date(msg.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''}
                  </span>
                </div>
              )
            })
          ) : (
            <div className="flex flex-col items-center justify-center text-center p-4 my-auto space-y-3">
              <div className="h-10 w-10 rounded-2xl bg-blue-600/20 border border-blue-500/30 flex items-center justify-center text-blue-400 shadow-lg shadow-blue-600/10">
                <Sparkles size={18} />
              </div>
              <div>
                <h4 className="text-xs font-bold text-white">Swarm Project Orchestrator</h4>
                <p className="text-[11px] text-slate-400 mt-1 max-w-xs leading-relaxed">
                  Active for <strong className="text-slate-200">{selectedProject?.name || 'Project'}</strong>.
                  Ask for cross-workspace coordination, architecture reviews, or dispatching tasks to autonomous workers.
                </p>
              </div>
              <div className="w-full space-y-1.5 pt-2">
                {[
                  'What is the architecture and charter of this project?',
                  'Deploy a verification task to check repository health',
                  'What tasks are currently queued or running?',
                ].map((prompt) => (
                  <button
                    key={prompt}
                    onClick={() => handleSendMessage(prompt)}
                    className="w-full text-left p-2 rounded-xl bg-slate-900/80 hover:bg-slate-800/80 border border-slate-800 hover:border-slate-700 text-[11px] text-slate-300 transition-colors"
                  >
                    → {prompt}
                  </button>
                ))}
              </div>
            </div>
          )}

          {(isTyping || isLiveSessionRunning) && (
            <div className="flex items-center gap-1.5 text-xs text-slate-400">
              <Sparkles size={12} className="animate-spin text-blue-400" />
              <span>Swarm Orchestrator is executing...</span>
            </div>
          )}
          <div ref={chatBottomRef} />
        </div>

        {/* Chat Input Bar */}
        <div className="p-3 border-t border-slate-800/80 bg-[#0a0f1d]/70">
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void handleSendMessage()
            }}
            className="flex flex-col gap-2 rounded-2xl border border-slate-800 bg-[#070b14] p-2 focus-within:border-blue-500/50 transition-all"
          >
            <input
              type="text"
              value={inputText}
              onChange={(e) => setInputText(e.target.value)}
              placeholder="Instruct orchestrator or dispatch tasks..."
              className="w-full bg-transparent px-2 py-1 text-xs text-slate-200 placeholder-slate-500 focus:outline-none"
            />
            <div className="flex items-center justify-between border-t border-slate-800/60 pt-1.5 text-slate-400">
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setIsDeployModalOpen(true)}
                  className="hover:text-slate-200 transition-colors p-0.5"
                  title="Deploy new task"
                >
                  <Plus size={14} />
                </button>
                <button
                  type="button"
                  onClick={() => handleSendMessage('Review project architecture')}
                  className="hover:text-slate-200 transition-colors p-0.5"
                  title="Prompt assist"
                >
                  <Sparkles size={14} />
                </button>
              </div>

              <button
                type="submit"
                disabled={!inputText.trim() || isTyping}
                className="flex h-7 w-7 items-center justify-center rounded-full bg-blue-600 hover:bg-blue-500 text-white shadow-sm transition-all disabled:opacity-40"
              >
                <ArrowUp size={14} />
              </button>
            </div>
          </form>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          MODAL: DEPLOY AUTONOMOUS TASK MODAL
         ───────────────────────────────────────────────────────────── */}
      {isDeployModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div className="relative flex max-w-lg w-full flex-col p-6 rounded-3xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <Code size={16} className="text-blue-400" />
                <h3 className="text-sm font-bold text-white">Deploy Autonomous Task</h3>
              </div>
              <button
                onClick={() => setIsDeployModalOpen(false)}
                className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
              >
                <X size={16} />
              </button>
            </div>

            <div className="space-y-3 text-xs">
              <div>
                <label className="text-[11px] text-slate-400 mb-1 block font-medium">Task Title</label>
                <input
                  type="text"
                  value={newTaskTitle}
                  onChange={(e) => setNewTaskTitle(e.target.value)}
                  placeholder="e.g. Run testbench suite & verify stability"
                  className="w-full rounded-xl bg-slate-950 border border-slate-800 px-3 py-2 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500/60 font-medium"
                />
              </div>

              <div>
                <label className="text-[11px] text-slate-400 mb-1 block font-medium">Agent Type</label>
                <div className="grid grid-cols-4 gap-2">
                  {(['coder', 'finder', 'designer', 'video'] as const).map((ag) => (
                    <button
                      key={ag}
                      type="button"
                      onClick={() => setNewTaskAgent(ag)}
                      className={`py-2 px-3 rounded-xl border text-xs font-semibold capitalize transition-all ${
                        newTaskAgent === ag
                          ? 'bg-blue-600 border-blue-500 text-white shadow-sm'
                          : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-slate-200'
                      }`}
                    >
                      {ag}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <label className="text-[11px] text-slate-400 mb-1 block font-medium">Prompt / Instructions</label>
                <textarea
                  value={newTaskPrompt}
                  onChange={(e) => setNewTaskPrompt(e.target.value)}
                  rows={3}
                  placeholder="Describe what the worker should accomplish..."
                  className="w-full rounded-xl bg-slate-950 border border-slate-800 px-3 py-2 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500/60 resize-none"
                />
              </div>

              {selectedProject?.linkedWorkspaces && selectedProject.linkedWorkspaces.length > 0 && (
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block font-medium">Target Workspace</label>
                  <select
                    value={newTaskWorkspace || selectedProject.repoPath}
                    onChange={(e) => setNewTaskWorkspace(e.target.value)}
                    className="w-full rounded-xl bg-slate-950 border border-slate-800 px-3 py-2 text-white focus:outline-none focus:border-blue-500/60"
                  >
                    {selectedProject.linkedWorkspaces.map((ws) => (
                      <option key={ws} value={ws}>
                        {ws}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </div>

            <div className="flex items-center justify-end gap-2 pt-3 border-t border-slate-800">
              <button
                type="button"
                onClick={() => setIsDeployModalOpen(false)}
                className="px-4 py-2 rounded-xl text-xs font-medium text-slate-400 hover:text-slate-200 hover:bg-slate-800 transition-colors"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={!newTaskTitle.trim() || isDeployingTask}
                onClick={handleDeployModalSubmit}
                className="flex items-center gap-1.5 px-5 py-2 rounded-xl text-xs font-bold text-white bg-blue-600 hover:bg-blue-500 shadow-md transition-all disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <Plus size={13} />
                <span>{isDeployingTask ? 'Deploying...' : 'Deploy Task'}</span>
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ─────────────────────────────────────────────────────────────
          MODAL: VIDEO DELIVERABLE PREVIEW DIALOG
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
