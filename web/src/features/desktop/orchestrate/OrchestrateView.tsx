import { useState, useMemo, useEffect, useCallback } from 'react'
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Code,
  Columns3,
  Edit3,
  FileText,
  Film,
  Folder,
  FolderGit2,
  FolderPlus,
  GitBranch,
  Home,
  Layers,
  ListFilter,
  Maximize2,
  MessageSquare,
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
import { DesktopV3ExistingConversationPane } from '../chat/components/desktop-v3-existing-conversation-pane'
import { isDesktopV3SessionTailReady, selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { selectAndHydrateDesktopV3Session } from '../state/desktop-v3-session-hydrator'
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
 * Thumbnail graphic renderer for video and media deliverables
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
      className="group/thumb relative aspect-video w-full cursor-pointer overflow-hidden rounded-lg border border-slate-800/80 bg-[#090d16] transition-all hover:border-blue-500/40"
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
          </svg>
        </div>
      )}

      {type !== 'cyber_lattice' && type !== 'neural_core' && (
        <div className="absolute inset-0 bg-gradient-to-br from-slate-900 to-slate-950 flex items-center justify-center">
          <Film size={18} className="text-slate-600" />
        </div>
      )}

      <div className="relative z-10 flex h-full w-full items-center justify-center">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-slate-900/80 text-white backdrop-blur-md border border-white/20 shadow-xl transition-transform group-hover/thumb:scale-105">
          <Play size={12} fill="currentColor" className="ml-0.5 text-white" />
        </div>
      </div>

      {duration && (
        <span className="absolute bottom-1 right-1 z-20 rounded bg-black/80 px-1 py-0.5 font-mono text-[9px] font-semibold text-slate-300 backdrop-blur-sm border border-white/10">
          {duration}
        </span>
      )}
    </div>
  )
}

/**
 * Minimal Task Card: Clean, technical, outcome-focused task card
 * Free of highlight gradients and pill badges. Displays "Action Needed",
 * worktree/unmerged git status, and "What did it do?" vs "What's not done yet?".
 */
function MinimalTaskCard({
  task,
  isSelected,
  onSelect,
  onOpenChat,
  onApprove,
  onIntegrate,
  onDelete,
  onRefine,
  onPreviewDeliverable,
}: {
  task: RunningTask
  isSelected?: boolean
  onSelect?: () => void
  onOpenChat?: () => void
  onApprove?: () => void
  onIntegrate?: () => void
  onDelete?: () => void
  onRefine?: (feedback?: string, errorSummary?: string) => void
  onPreviewDeliverable?: (d: MediaDeliverable) => void
}) {
  const [isFullPlanOpen, setIsFullPlanOpen] = useState(false)
  const [isRefineOpen, setIsRefineOpen] = useState(false)
  const [refineFeedback, setRefineFeedback] = useState('')

  const isPendingApproval = task.status === 'pending_approval' || task.status === 'queued'
  const isRunning = task.status === 'running' || task.status === 'in_progress'
  const isNeedsReview = task.status === 'needs_review'
  const isCompleted = task.status === 'completed'
  const hasUnintegrated = (task.unintegratedCommits ?? 0) > 0

  return (
    <div
      onClick={onSelect}
      className={`relative flex flex-col rounded-xl border transition-all p-3.5 space-y-3 cursor-pointer ${
        isSelected
          ? 'bg-[#0f1526] border-blue-500/60 shadow-[0_4px_20px_rgba(0,0,0,0.5)]'
          : 'bg-[#0a0f1d] border-slate-800/80 hover:border-slate-700/80 hover:bg-[#0c1222]'
      }`}
    >
      {/* 1. Header: Agent Tag + Title + Status + Actions */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-1 min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span
              className={`font-mono text-[9px] uppercase font-bold px-2 py-0.5 rounded border ${
                task.agentType === 'designer' || task.agentType === 'video'
                  ? 'bg-blue-500/10 text-blue-400 border-blue-500/25'
                  : 'bg-indigo-500/10 text-indigo-300 border-indigo-500/25'
              }`}
            >
              {task.agentType.toUpperCase()} • {task.workspacePath ? task.workspacePath.split('/').filter(Boolean).pop() : 'WORKSPACE'}
            </span>
            {task.outcomeType && (
              <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-slate-800 text-slate-400 border border-slate-700/50">
                {task.outcomeType.replace('_', ' ')}
              </span>
            )}
          </div>
          <h3 className="text-xs font-bold text-white tracking-tight leading-snug truncate">
            {task.title}
          </h3>
          {task.workspacesInvolved && task.workspacesInvolved.length > 0 && (
            <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
              <span className="font-mono text-[9px] uppercase tracking-wider text-slate-500 font-semibold">Workspaces:</span>
              {task.workspacesInvolved.map((ws, i) => {
                const wsLabel = ws.split('/').filter(Boolean).pop() || ws
                return (
                  <span
                    key={i}
                    className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-blue-950/60 text-blue-300 border border-blue-500/30 flex items-center gap-1"
                    title={ws}
                  >
                    <FolderGit2 size={9} />
                    <span>{wsLabel}</span>
                  </span>
                )
              })}
            </div>
          )}
        </div>

        <div className="flex items-center gap-2 flex-shrink-0">
          <div
            className={`font-mono text-[10px] font-bold uppercase px-2 py-0.5 rounded border flex items-center gap-1.5 ${
              isPendingApproval
                ? 'bg-amber-950/40 text-amber-300 border-amber-500/40'
                : isRunning
                ? 'bg-blue-950/40 text-blue-400 border-blue-500/40'
                : isNeedsReview
                ? 'bg-amber-950/40 text-amber-300 border-amber-500/40'
                : isCompleted
                ? 'bg-emerald-950/40 text-emerald-300 border-emerald-500/40'
                : 'bg-slate-800 text-slate-400 border-slate-700'
            }`}
          >
            <span
              className={`h-1.5 w-1.5 rounded-sm ${
                isPendingApproval
                  ? 'bg-amber-400'
                  : isRunning
                  ? 'bg-blue-400 animate-pulse'
                  : isNeedsReview
                  ? 'bg-amber-400'
                  : isCompleted
                  ? 'bg-emerald-400'
                  : 'bg-slate-500'
              }`}
            />
            <span>{task.status.replace('_', ' ')}</span>
          </div>

          <span className="font-mono text-[10px] text-slate-500">{task.elapsed}</span>

          {task.sessionId && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onOpenChat?.()
              }}
              className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700/60"
              title="Open session chat with this worker"
            >
              <MessageSquare size={11} />
              <span>Chat</span>
            </button>
          )}
        </div>
      </div>

      {/* 2. PENDING APPROVAL MISSION PROPOSAL BANNER */}
      {isPendingApproval && (
        <div className="flex flex-col p-3 rounded-lg bg-blue-950/20 border border-blue-500/40 space-y-2.5 text-xs">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <span className="font-bold text-blue-400 flex items-center gap-1.5 font-mono text-[10px] uppercase">
                <Sparkles size={12} />
                <span>AI Mission Proposal</span>
              </span>
              <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-blue-900/40 text-blue-300 border border-blue-500/30">
                {task.tier === 'discovery' ? 'Tier 2: Discovery' : task.tier === 'complex' ? 'Tier 3: Complex Plan' : 'Tier 1: Direct'}
              </span>
              {task.revision && task.revision > 1 && (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-900/50 text-indigo-300 border border-indigo-500/30 font-bold">
                  Rev {task.revision}
                </span>
              )}
            </div>
            <span className="text-[10px] font-mono text-slate-400">
              Branch: <span className="text-indigo-300 font-semibold">{task.worktreeBranch || 'agent/worktree'}</span>
            </span>
          </div>

          <p className="text-slate-200 text-xs leading-relaxed">
            {task.subtitle || task.title}
          </p>

          {/* Plan Summary */}
          {task.planSummary && (
            <div className="p-2.5 rounded bg-slate-900/80 border border-slate-800 text-[11px] font-mono text-slate-300 whitespace-pre-line leading-relaxed">
              <div className="text-[9px] uppercase tracking-wider text-blue-400 font-bold mb-1">
                Execution Overview
              </div>
              {task.planSummary}
            </div>
          )}

          {/* Expandable Full Plan */}
          {task.fullPlanMarkdown && (
            <div className="border border-slate-800/80 rounded bg-[#070b14]/90 overflow-hidden">
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  setIsFullPlanOpen(!isFullPlanOpen)
                }}
                className="w-full flex items-center justify-between px-2.5 py-1.5 text-[10px] font-mono text-slate-400 hover:text-slate-200 transition-colors"
              >
                <span className="flex items-center gap-1.5 font-semibold">
                  <FileText size={11} className="text-blue-400" />
                  <span>{isFullPlanOpen ? 'Hide Full Plan Spec' : 'Read Full Plan Spec & Criteria'}</span>
                </span>
                {isFullPlanOpen ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
              </button>
              {isFullPlanOpen && (
                <div className="p-3 border-t border-slate-800 text-[11px] text-slate-300 font-mono whitespace-pre-wrap leading-relaxed max-h-60 overflow-y-auto bg-slate-950/60">
                  {task.fullPlanMarkdown}
                </div>
              )}
            </div>
          )}

          {/* Refine / Actions Bar */}
          <div className="flex flex-col gap-2 pt-1 border-t border-blue-500/20">
            <div className="flex items-center justify-between text-[11px] gap-2 flex-wrap">
              <div className="flex items-center gap-2">
                <span className="text-slate-400 font-mono text-[10px]">
                  Expected: <strong className="text-white">{task.outcomeType === 'media_bundle' ? '3 Videos (1080p MP4)' : task.outcomeType === 'bug_patch' ? 'Regression Test & Fix Diff' : task.outcomeType === 'audit_report' ? 'Findings Ledger Report' : '1 Branch PR + Test Suite'}</strong>
                </span>
                {onRefine && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      setIsRefineOpen(!isRefineOpen)
                    }}
                    className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700/60"
                    title="Send instructions back to the Router / Plan Agent to adjust workspaces or plan"
                  >
                    <Sparkles size={10} className="text-indigo-400" />
                    <span>{isRefineOpen ? 'Cancel' : 'Refine Plan'}</span>
                  </button>
                )}
              </div>

              <div className="flex items-center gap-2">
                {onDelete && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onDelete()
                    }}
                    className="px-2.5 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-400 hover:text-rose-400 text-[10px] font-medium transition-colors"
                  >
                    Discard
                  </button>
                )}
                {onApprove && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onApprove()
                    }}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded bg-blue-600 hover:bg-blue-500 text-white font-bold text-xs shadow-md transition-all active:scale-95"
                  >
                    <Play size={11} fill="currentColor" />
                    <span>Approve & Start Session</span>
                  </button>
                )}
              </div>
            </div>

            {/* Inline Refine Input */}
            {isRefineOpen && onRefine && (
              <div className="flex items-center gap-2 p-2 rounded bg-slate-900 border border-slate-800 animate-in fade-in duration-200">
                <input
                  type="text"
                  value={refineFeedback}
                  onClick={(e) => e.stopPropagation()}
                  onChange={(e) => setRefineFeedback(e.target.value)}
                  placeholder="e.g. Keep in web workspace only, don't touch daemon API..."
                  className="flex-1 bg-slate-950 border border-slate-800 rounded px-2.5 py-1 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500 font-mono"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && refineFeedback.trim()) {
                      e.stopPropagation()
                      onRefine(refineFeedback)
                      setRefineFeedback('')
                      setIsRefineOpen(false)
                    }
                  }}
                />
                <button
                  type="button"
                  disabled={!refineFeedback.trim()}
                  onClick={(e) => {
                    e.stopPropagation()
                    if (refineFeedback.trim()) {
                      onRefine(refineFeedback)
                      setRefineFeedback('')
                      setIsRefineOpen(false)
                    }
                  }}
                  className="px-3 py-1 rounded bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 text-white font-bold text-[10px] transition-colors flex-shrink-0"
                >
                  Send to Router
                </button>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Execution Error Recovery Banner */}
      {task.lastError && (
        <div className="flex items-start justify-between p-2.5 rounded-lg bg-rose-950/30 border border-rose-500/40 text-[11px] gap-2">
          <div className="flex items-start gap-2 min-w-0">
            <AlertTriangle size={13} className="text-rose-400 flex-shrink-0 mt-0.5" />
            <div className="space-y-0.5 min-w-0">
              <span className="font-bold text-rose-300 block">Execution Error / Test Failure:</span>
              <span className="font-mono text-slate-300 text-[10px] break-all block">{task.lastError}</span>
            </div>
          </div>
          {onRefine && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onRefine(undefined, task.lastError)
              }}
              className="flex-shrink-0 px-2.5 py-1 rounded bg-rose-600 hover:bg-rose-500 text-white font-bold text-[10px] transition-colors flex items-center gap-1 shadow"
              title="Send this failure log to Plan Agent to generate an error recovery fix strategy"
            >
              <Sparkles size={10} />
              <span>Re-Plan with Plan Agent</span>
            </button>
          )}
        </div>
      )}

      {/* 3. Action Needed Banner (if unintegrated commits or action needed) */}
      {!isPendingApproval && (task.actionNeeded || hasUnintegrated) && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-amber-950/20 border border-amber-500/30 text-amber-200 text-[11px] gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <span className="font-bold text-amber-400 flex-shrink-0">Action:</span>
            <span className="truncate">
              {task.actionNeeded || `${task.unintegratedCommits} unintegrated commit(s) ready to land.`}
            </span>
          </div>
          {hasUnintegrated && onIntegrate && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onIntegrate()
              }}
              className="flex-shrink-0 px-2.5 py-0.5 rounded bg-amber-500 hover:bg-amber-400 text-slate-950 font-bold text-[10px] transition-colors"
            >
              Integrate into dev
            </button>
          )}
        </div>
      )}

      {/* 4. Worktree & Git Status Bar */}
      {(task.worktreeBranch || task.diffSummary || hasUnintegrated) && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-[#070b14] border border-slate-800/80 text-[10px] font-mono text-slate-400">
          <div className="flex items-center gap-2 truncate">
            <span className="text-indigo-400 flex items-center gap-1">
              <GitBranch size={10} />
              <span>{task.worktreeBranch || 'agent/worktree'}</span>
            </span>
            {task.diffSummary && <span>• {task.diffSummary}</span>}
          </div>
          <div className="flex items-center gap-2 flex-shrink-0">
            <span className={hasUnintegrated ? 'text-amber-400 font-semibold' : 'text-slate-500'}>
              {hasUnintegrated ? `${task.unintegratedCommits} unmerged commit(s)` : 'up-to-date'}
            </span>
            <span>•</span>
            <span className={task.isDirty ? 'text-amber-400' : 'text-emerald-400'}>
              {task.isDirty ? 'modified' : 'clean'}
            </span>
          </div>
        </div>
      )}

      {/* 5. Two-Column Ledger: "What did it do?" vs "What's not done yet?" */}
      {!isPendingApproval && ((task.whatDidDo && task.whatDidDo.length > 0) || (task.whatNotDone && task.whatNotDone.length > 0)) && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-[11px] p-2 rounded-lg bg-[#070b14]/70 border border-slate-800/60">
          <div className="space-y-1">
            <span className="font-mono text-[9px] uppercase font-bold text-slate-500 block">
              What did it do?
            </span>
            {(task.whatDidDo && task.whatDidDo.length > 0
              ? task.whatDidDo
              : ['Verified scope and authored changes']
            ).map((item, idx) => (
              <div key={idx} className="flex items-start gap-1.5 text-slate-300 leading-tight">
                <span className="text-emerald-400 font-bold">✓</span>
                <span className="truncate">{item}</span>
              </div>
            ))}
          </div>
          <div className="space-y-1">
            <span className="font-mono text-[9px] uppercase font-bold text-slate-500 block">
              What's not done yet?
            </span>
            {(task.whatNotDone && task.whatNotDone.length > 0
              ? task.whatNotDone
              : ['Awaiting code review and merge']
            ).map((item, idx) => (
              <div key={idx} className="flex items-start gap-1.5 text-slate-400 leading-tight">
                <span className="text-amber-400">⋯</span>
                <span className="truncate">{item}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 6. Expected Deliverables Slot Rendering */}
      {task.deliverables && task.deliverables.length > 0 && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between text-[10px] font-semibold text-slate-500">
            <span className="uppercase font-mono tracking-wider">
              Deliverables ({task.deliverables.length})
            </span>
            <span className="font-mono text-[9px]">Contract: {task.outcomeType || 'media'}</span>
          </div>
          <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
            {task.deliverables.map((d) => (
              <div
                key={d.id}
                onClick={(e) => {
                  e.stopPropagation()
                  onPreviewDeliverable?.(d)
                }}
                className="p-2 rounded-lg border border-slate-800 bg-[#070b14] hover:border-blue-500/40 cursor-pointer space-y-1 transition-colors"
              >
                <div className="text-[11px] font-semibold text-white truncate">{d.title}</div>
                <div className="flex items-center justify-between text-[9px] text-blue-400 font-mono">
                  <span>{d.type}</span>
                  <span className="text-slate-400">{d.status}</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 7. Pipeline Stepper & Footer */}
      <div className="flex items-center justify-between pt-1 border-t border-slate-800/60 text-[10px] font-mono text-slate-500">
        <div className="flex items-center gap-1.5 truncate">
          {task.stepTimeline?.map((st, idx) => (
            <span
              key={st.step}
              className={`flex items-center gap-1 ${
                st.status === 'complete'
                  ? 'text-emerald-400'
                  : st.status === 'processing'
                  ? 'text-blue-400 font-bold'
                  : 'text-slate-600'
              }`}
            >
              {idx > 0 && <span className="text-slate-700">→</span>}
              <span>{st.label}</span>
            </span>
          ))}
        </div>

        {task.sessionId && (
          <span
            onClick={(e) => {
              e.stopPropagation()
              onOpenChat?.()
            }}
            className="text-blue-400 hover:text-blue-300 hover:underline cursor-pointer flex-shrink-0"
          >
            sess_{task.sessionId.slice(0, 8)}
          </span>
        )}
      </div>
    </div>
  )
}

/**
 * Right AI Chat Panel: Supports switching between Executive Project Orchestrator
 * and individual Task Worker sessions with a back-button navigation bar.
 */
function OrchestratorChatSidebar({
  sessionId,
  project,
  activeTask,
  onBackToOrchestrator,
}: {
  sessionId: string
  project?: ProjectSummary
  activeTask?: RunningTask
  onBackToOrchestrator?: () => void
}) {
  const [error, setError] = useState(false)
  const [attempt, setAttempt] = useState(0)

  const messages = useDesktopV3CacheSelector(
    useCallback((state) => selectRenderedSessionMessages(state, sessionId), [sessionId]),
    (left, right) =>
      left.committed === right.committed &&
      left.pendingUser === right.pendingUser &&
      left.liveRuns === right.liveRuns &&
      left.runIntents === right.runIntents &&
      left.currentRunIntent === right.currentRunIntent &&
      left.latestRunIntent === right.latestRunIntent
  )
  const ready = useDesktopV3CacheSelector(
    useCallback((state) => isDesktopV3SessionTailReady(state, sessionId), [sessionId])
  )
  const count = useDesktopV3CacheSelector(
    useCallback((state) => state.messagesBySession[sessionId]?.items.length ?? 0, [sessionId])
  )
  const hydrating = useDesktopV3CacheSelector(
    useCallback((state) => (state.hydrateInFlightBySession[sessionId] ?? 0) > 0, [sessionId])
  )

  useEffect(() => {
    let active = true
    setError(false)
    void selectAndHydrateDesktopV3Session(sessionId).catch(() => {
      if (active) setError(true)
    })
    return () => {
      active = false
    }
  }, [sessionId, attempt])

  return (
    <aside
      aria-label="Swarm Orchestrator AI Chat"
      className="relative flex w-[440px] flex-shrink-0 flex-col overflow-hidden rounded-3xl border border-slate-800/80 bg-[#0d121f] shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]"
      style={{
        '--app-bg': '#0d121f',
        '--app-bg-alt': '#090d16',
        '--app-surface': '#111728',
        '--app-surface-subtle': '#0a0f1d',
        '--app-border': 'rgba(255, 255, 255, 0.08)',
        '--app-border-muted': 'rgba(255, 255, 255, 0.05)',
      } as React.CSSProperties}
    >
      {/* Top Header: Task Navigation vs Orchestrator Header */}
      {activeTask ? (
        <div className="flex items-center justify-between p-3 border-b border-slate-800 bg-[#0a0f1d] text-xs">
          <div className="flex items-center gap-2.5 min-w-0">
            <button
              onClick={onBackToOrchestrator}
              className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-slate-800 hover:bg-slate-700 text-slate-200 font-semibold transition-colors border border-slate-700"
              title="Return to Executive Project Orchestrator"
            >
              <ArrowLeft size={12} />
              <span>Orchestrator</span>
            </button>
            <div className="flex flex-col min-w-0">
              <span className="font-bold text-white truncate">{activeTask.title}</span>
              <span className="text-[10px] text-slate-400 font-mono">
                {activeTask.workerName || '@Worker'} • {activeTask.status} • {activeTask.elapsed}
              </span>
            </div>
          </div>
          <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 font-bold">
            {activeTask.agentType}
          </span>
        </div>
      ) : (
        <div className="flex items-center justify-between p-3 border-b border-slate-800 bg-[#0a0f1d] text-xs">
          <div className="flex items-center gap-2">
            <div className="h-2 w-2 rounded-full bg-emerald-400" />
            <span className="font-bold text-white">Project Orchestrator</span>
            <span className="text-[10px] text-slate-400 font-mono">({project?.name})</span>
          </div>
          <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-slate-800 text-slate-300 font-bold">
            Executive
          </span>
        </div>
      )}

      {error && (
        <div className="flex items-center justify-between p-3 bg-red-950/40 border-b border-red-500/30 text-xs text-red-200">
          <span>Failed to load session.</span>
          <button
            onClick={() => setAttempt((a) => a + 1)}
            className="px-2 py-0.5 rounded bg-red-800 text-white font-medium hover:bg-red-700"
          >
            Retry
          </button>
        </div>
      )}

      <DesktopV3ExistingConversationPane
        presentation="sidebar"
        sessionId={sessionId}
        initialHydrateStatus={error ? 'error' : hydrating ? 'loading' : ready ? 'ready' : 'loading'}
        renderedMessages={messages}
        messagesLoaded={ready}
        loadedMessageCount={count}
        contextChip={
          activeTask
            ? {
                id: activeTask.id,
                label: activeTask.title,
                kind: 'task',
                description: `Task Session (${activeTask.agentType}) for ${activeTask.title}`,
              }
            : project
            ? {
                id: project.id,
                label: project.name,
                kind: 'project',
                description: `Executive Orchestrator for ${project.name} (${project.linkedWorkspaces.length} workspace(s) bound)`,
              }
            : null
        }
        metadata={{
          orchestrate_view: true,
          ...(project ? { project_id: project.id } : {}),
          ...(activeTask ? { task_id: activeTask.id } : {}),
        }}
      />
    </aside>
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

  // Projects State
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string>('')
  const [, setIsLoadingProjects] = useState<boolean>(true)
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

  // Live Pebble V3 cache state
  const sessionsById = useDesktopV3CacheSelector((s) => s.sessionsById)
  const plansBySession = useDesktopV3CacheSelector((s) => s.plansBySession)

  // Active Chat Session state (can be executive orchestrator OR a task session)
  const [activeSessionId, setActiveSessionId] = useState<string>('')
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null)

  // Tasks State
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

  // Navigation tab state
  const [activeNavTab, setActiveNavTab] = useState<'home' | 'projects' | 'automations' | 'deliverables' | 'settings'>('home')

  // Deploy Task Modal State (Plain English Prompt)
  const [isDeployModalOpen, setIsDeployModalOpen] = useState(false)
  const [newTaskPrompt, setNewTaskPrompt] = useState('')
  const [newTaskWorkspace, setNewTaskWorkspace] = useState('')
  const [isDeployingTask, setIsDeployingTask] = useState(false)

  // Active task object derived from activeTaskId
  const activeTask = useMemo(() => tasks.find((t) => t.id === activeTaskId), [tasks, activeTaskId])

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
      try {
        const wsRes = await requestJson<{ workspaces?: Array<{ path: string; name?: string; id?: string }> }>('/v1/workspace/list?limit=200')
        if (wsRes?.workspaces && wsRes.workspaces.length > 0 && !cancelled) {
          const detected = wsRes.workspaces.map((w, idx) => ({
            path: w.path,
            label: w.name || w.path.split('/').filter(Boolean).pop() || 'Workspace',
            role: (idx === 0 ? ('primary_code' as const) : ('auxiliary' as const)),
            selected: true,
          }))
          setOnboardingWorkspaces(detected)
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
          outcomeType: t.outcome_type,
          workspaceTarget: t.workspace_path || t.project_id,
          workspacePath: t.workspace_path,
          worktreeBranch: t.worktree_branch,
          unintegratedCommits: t.unintegrated_commits ?? 0,
          diffSummary: t.diff_summary ?? '',
          isDirty: !!t.is_dirty,
          actionNeeded: t.action_needed,
          whatDidDo: t.what_did_do,
          whatNotDone: t.what_not_done,
          workspacesInvolved: t.workspaces_involved || (t.workspace_path ? [t.workspace_path] : []),
          planSummary: t.plan_summary,
          fullPlanMarkdown: t.full_plan_markdown,
          tier: t.tier || 'direct',
          revision: t.revision || 1,
          lastError: t.last_error,
          feedbackHistory: t.feedback_history,
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
        headers: { 'Content-Type': 'application/json' },
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
    } catch (e) {
      console.warn('Failed to ensure orchestrator session:', e)
    }
    return null
  }, [])

  // Synchronize active orchestrator session and project tasks with selected project
  useEffect(() => {
    if (!selectedProject || isOnboardingActive) return
    fetchProjectTasks(selectedProject.id)
    void ensureOrchestratorSession(selectedProject)
  }, [selectedProject?.id, isOnboardingActive, fetchProjectTasks, ensureOrchestratorSession])

  // Task selection & Per-Task Session Switching
  const handleSelectTask = (task: RunningTask) => {
    setSelectedTaskId(task.id)
    if (task.sessionId) {
      setActiveSessionId(task.sessionId)
      setActiveTaskId(task.id)
    }
  }

  const handleBackToOrchestrator = () => {
    setActiveTaskId(null)
    if (selectedProject?.primarySessionId) {
      setActiveSessionId(selectedProject.primarySessionId)
    }
  }

  // Derive real active workers from V3 sessions
  const deployedWorkers = useMemo<DeployedWorker[]>(() => {
    const list: DeployedWorker[] = []
    const allRecords = Object.values(sessionsById)
    for (const rec of allRecords) {
      if (rec.kind !== 'full' || !rec.session) continue
      const sess = rec.session
      const isActive = !!(sess.lifecycle as any)?.active
      const agent = (sess as any).agent_name || 'swarm'
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

  // Submit Plain English Task Proposal
  const handleDeployModalSubmit = async () => {
    const prompt = newTaskPrompt.trim()
    if (!prompt || !selectedProject?.id) return
    setIsDeployingTask(true)
    try {
      const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt,
          workspace_path: newTaskWorkspace || selectedProject.repoPath || '.',
          deploy_session: true,
        }),
      })
      if (res?.task) {
        fetchProjectTasks(selectedProject.id)
        if (res.task.session_id) {
          setActiveSessionId(res.task.session_id)
          setActiveTaskId(res.task.id)
        }
      }
      setIsDeployModalOpen(false)
      setNewTaskPrompt('')
    } finally {
      setIsDeployingTask(false)
    }
  }

  // Approve pending task and start execution run
  const handleApproveTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/approve`, {
        method: 'POST',
      })
      fetchProjectTasks(selectedProject.id)
      const approvedTask = tasks.find((t) => t.id === taskId)
      if (approvedTask?.sessionId) {
        setActiveSessionId(approvedTask.sessionId)
        setActiveTaskId(approvedTask.id)
      }
    } catch (err) {
      console.warn('Approve task failed:', err)
    }
  }

  // Delete/discard task
  const handleDeleteTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}`, {
        method: 'DELETE',
      })
      fetchProjectTasks(selectedProject.id)
      if (activeTaskId === taskId) {
        handleBackToOrchestrator()
      }
    } catch (err) {
      console.warn('Delete task failed:', err)
    }
  }

  // Integrate / Promote task commits into target branch
  const handleIntegrateTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/integrate`, {
        method: 'POST',
      })
      fetchProjectTasks(selectedProject.id)
    } catch (err) {
      console.warn('Integrate task failed:', err)
    }
  }

  // Refine task with router or re-plan error
  const handleRefineTask = async (taskId: string, feedback?: string, errorSummary?: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/refine`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          feedback: feedback?.trim() || undefined,
          error_summary: errorSummary?.trim() || undefined,
        }),
      })
      fetchProjectTasks(selectedProject.id)
    } catch (err) {
      console.warn('Refine task failed:', err)
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
      if (task.status === 'pending_approval') {
        // Keep pending approval until explicitly approved
        status = 'pending_approval'
      } else if (lifecycle?.active) {
        status = 'running'
      } else {
        const hasWaitingReview = plan?.document?.checkpoints?.some((cp: any) => cp.status === 'needs_review')
        if (hasWaitingReview || lifecycle?.phase === 'needs_review') {
          status = 'needs_review'
        } else if (!lifecycle?.active && (sess.message_count ?? 0) > 1) {
          status = 'completed'
        }
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

  const handleDeleteProject = async (projectId: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
    try {
      await requestJson(`/v3/projects/${projectId}`, { method: 'DELETE' })
      setProjects((prev) => {
        const next = prev.filter((p) => p.id !== projectId)
        if (selectedProjectId === projectId) {
          if (next.length > 0) {
            setSelectedProjectId(next[0].id)
            if (next[0].primarySessionId) {
              setActiveSessionId(next[0].primarySessionId)
            }
            fetchProjectTasks(next[0].id)
          } else {
            setSelectedProjectId('')
            setActiveSessionId('')
            setIsOnboardingActive(true)
          }
        }
        return next
      })
    } catch (err) {
      console.warn('Delete project failed:', err)
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
- Local-first architecture; session records persist to Pebble database.
- Subagents execute inside isolated Git worktrees.
- Autonomous project tasks deliver verified outcomes (code_pr, media_bundle, bug_patch, audit_report).
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

    // Spawn primary orchestrator session with valid client_request_id and model preference
    let orchSessionId = ''
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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
      setActiveTaskId(null)
    }
    setIsOnboardingActive(false)
    setIsActivating(false)
    fetchProjectTasks(newId)
  }

  // Count summaries
  const runningCount = liveTasks.filter((t) => t.status === 'running').length
  const reviewCount = liveTasks.filter((t) => t.status === 'needs_review').length
  const queuedCount = liveTasks.filter((t) => t.status === 'queued' || t.status === 'pending_approval').length
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
                <div className="text-[10px] text-slate-400">Autonomous Project Coordination</div>
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
                    setActiveTaskId(null)
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
                    setActiveTaskId(null)
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
                        onClick={(e) => handleDeleteProject(p.id, e)}
                        className="text-slate-500 hover:text-rose-400 p-1 rounded transition-colors"
                        title="Delete project"
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
                    onClick={(e) => handleDeleteProject(selectedProject.id, e)}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-rose-600/20 hover:bg-rose-600/30 text-rose-400 border border-rose-500/30 text-xs font-semibold transition-all"
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
                        {selectedProject?.name || 'Project Overview'}
                      </h1>
                      <span className="rounded bg-blue-500/10 border border-blue-500/30 px-2 py-0.5 text-[10px] font-semibold text-blue-400 font-mono">
                        {liveTasks.length} {liveTasks.length === 1 ? 'Task' : 'Tasks'}
                      </span>
                    </div>
                    <p className="text-[11px] text-slate-400 mt-0.5">
                      Autonomous worker sessions executing across {selectedProject?.name || 'workspace'}
                    </p>
                  </div>
                </div>

                {/* Quick Action buttons */}
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setIsDeployModalOpen(true)}
                    className="flex items-center gap-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
                  >
                    <Plus size={13} />
                    <span>+ New Task</span>
                  </button>
                  <button
                    onClick={() => {
                      setNewTaskPrompt('Run local testbench suite: verify critical test gates and check repository stability')
                      setIsDeployModalOpen(true)
                    }}
                    className="flex items-center gap-1.5 rounded-lg bg-slate-800 hover:bg-slate-700 border border-slate-700 text-slate-200 text-xs px-3 py-1.5 transition-all active:scale-95"
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
                  <div className="flex items-center gap-1 rounded-lg bg-[#080c16] p-1 border border-slate-800">
                    <button
                      onClick={() => setMiddleVariant('matrix')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium transition-all ${
                        middleVariant === 'matrix'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
                    >
                      <ListFilter size={12} />
                      <span>1. Compact Matrix</span>
                    </button>

                    <button
                      onClick={() => setMiddleVariant('kanban')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium transition-all ${
                        middleVariant === 'kanban'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
                    >
                      <Columns3 size={12} />
                      <span>2. Pipeline Kanban</span>
                    </button>

                    <button
                      onClick={() => setMiddleVariant('fleet')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium transition-all ${
                        middleVariant === 'fleet'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
                    >
                      <Bot size={12} />
                      <span>3. Worker Fleet</span>
                    </button>

                    <button
                      onClick={() => setMiddleVariant('split')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium transition-all ${
                        middleVariant === 'split'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
                    >
                      <Layers size={12} />
                      <span>4. Split Studio</span>
                    </button>

                    <button
                      onClick={() => setMiddleVariant('timeline')}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium transition-all ${
                        middleVariant === 'timeline'
                          ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30 font-semibold shadow-sm'
                          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/40'
                      }`}
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
                <span className="flex h-2 w-2 rounded-sm bg-emerald-400 flex-shrink-0" />
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
                  VARIANT 1: COMPACT MATRIX & EXPANDABLE DRAWER
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
                        className="w-full bg-[#080c16] border border-slate-800 rounded-lg pl-8 pr-3 py-1.5 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500/40"
                      />
                    </div>

                    <div className="flex items-center gap-1.5">
                      {(['all', 'running', 'needs_review', 'queued', 'completed'] as const).map((st) => (
                        <button
                          key={st}
                          onClick={() => setStatusFilter(st)}
                          className={`px-2.5 py-1 rounded text-[10px] font-semibold uppercase tracking-wider transition-all ${
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
                  <div className="flex-1 overflow-y-auto space-y-2 pr-1">
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
                              className="flex items-center justify-between p-3 cursor-pointer hover:bg-white/[0.02]"
                            >
                              <div className="flex items-center gap-2.5 min-w-0 flex-1">
                                <span
                                  className={`h-2 w-2 rounded-sm flex-shrink-0 ${
                                    t.status === 'pending_approval'
                                      ? 'bg-amber-400'
                                      : t.status === 'running'
                                      ? 'bg-blue-400 animate-pulse'
                                      : t.status === 'needs_review'
                                      ? 'bg-amber-400'
                                      : t.status === 'completed'
                                      ? 'bg-emerald-400'
                                      : 'bg-slate-600'
                                  }`}
                                />
                                <span className="font-mono text-[10px] text-slate-500 w-16">{t.id.slice(0, 8)}</span>
                                <span className="text-xs font-bold text-slate-200 truncate">{t.title}</span>
                                {t.outcomeType && (
                                  <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-slate-800 text-slate-400">
                                    {t.outcomeType.replace('_', ' ')}
                                  </span>
                                )}
                              </div>

                              <div className="flex items-center gap-3 flex-shrink-0">
                                {(t.unintegratedCommits ?? 0) > 0 && (
                                  <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-amber-950/40 text-amber-300 border border-amber-500/30">
                                    {t.unintegratedCommits} unmerged
                                  </span>
                                )}
                                <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                                  {t.workerName}
                                </span>
                                <span className="text-[10px] text-slate-500 font-mono">{t.elapsed}</span>
                                <button
                                  type="button"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    handleSelectTask(t)
                                  }}
                                  className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium"
                                  title="Open Chat"
                                >
                                  <MessageSquare size={11} />
                                  <span>Chat</span>
                                </button>
                                <ChevronDown
                                  size={13}
                                  className={`text-slate-400 transition-transform ${isExpanded ? 'rotate-180' : ''}`}
                                />
                              </div>
                            </div>

                            {/* Drawer Content */}
                            {isExpanded && (
                              <div className="p-3 border-t border-slate-800/60 bg-[#070b14]">
                                <MinimalTaskCard
                                  task={t}
                                  isSelected={selectedTaskId === t.id}
                                  onSelect={() => handleSelectTask(t)}
                                  onOpenChat={() => handleSelectTask(t)}
                                  onApprove={() => handleApproveTask(t.id)}
                                  onIntegrate={() => handleIntegrateTask(t.id)}
                                  onDelete={() => handleDeleteTask(t.id)}
                                  onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                                  onPreviewDeliverable={(d) => setActiveVideoPreview(d)}
                                />
                              </div>
                            )}
                          </div>
                        )
                      })
                    ) : (
                      <div className="flex-1 flex flex-col items-center justify-center p-8 text-center border border-dashed border-slate-800 rounded-xl bg-[#080c16]/50">
                        <Code size={20} className="text-slate-600 mb-2" />
                        <h4 className="text-xs font-bold text-slate-300">No tasks active</h4>
                        <p className="text-[11px] text-slate-500 mt-1 mb-4">
                          Propose a task in plain English to have the AI organize the mission and branch for approval.
                        </p>
                        <button
                          onClick={() => setIsDeployModalOpen(true)}
                          className="px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold shadow-md shadow-blue-600/20 transition-all flex items-center gap-1.5"
                        >
                          <Plus size={13} />
                          <span>+ New Task</span>
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
                      { key: 'queued', label: 'Pending Approval', color: 'amber' },
                      { key: 'running', label: 'In Progress', color: 'blue' },
                      { key: 'needs_review', label: 'Needs Review', color: 'amber' },
                      { key: 'completed', label: 'Completed', color: 'emerald' },
                    ] as const
                  ).map((col) => {
                    const colTasks = liveTasks.filter((t) => {
                      if (col.key === 'queued') return t.status === 'queued' || t.status === 'pending_approval'
                      if (col.key === 'running') return t.status === 'running' || t.status === 'in_progress'
                      return t.status === col.key
                    })
                    return (
                      <div
                        key={col.key}
                        className="w-80 flex-shrink-0 flex flex-col rounded-xl border border-slate-800/80 bg-[#090d16]/70 p-3 space-y-2.5 overflow-hidden"
                      >
                        <div className="flex items-center justify-between pb-1.5 border-b border-slate-800/60">
                          <span className="text-xs font-bold text-white flex items-center gap-1.5">
                            <span
                              className={`h-2 w-2 rounded-sm ${
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

                        <div className="flex-1 overflow-y-auto space-y-2.5 pr-0.5">
                          {colTasks.map((t) => (
                            <MinimalTaskCard
                              key={t.id}
                              task={t}
                              isSelected={selectedTaskId === t.id}
                              onSelect={() => handleSelectTask(t)}
                              onOpenChat={() => handleSelectTask(t)}
                              onApprove={() => handleApproveTask(t.id)}
                              onIntegrate={() => handleIntegrateTask(t.id)}
                              onDelete={() => handleDeleteTask(t.id)}
                              onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                              onPreviewDeliverable={(d) => setActiveVideoPreview(d)}
                            />
                          ))}
                          {colTasks.length === 0 && (
                            <div className="p-4 text-center text-[10px] text-slate-600 border border-dashed border-slate-850 rounded-lg">
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
                      className="flex items-center gap-1.5 px-3 py-1 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium"
                    >
                      <Plus size={12} />
                      <span>+ New Task</span>
                    </button>
                  </div>

                  {deployedWorkers.length > 0 ? (
                    <div className="grid grid-cols-2 gap-3">
                      {deployedWorkers.map((worker) => (
                        <div
                          key={worker.id}
                          className="p-3 rounded-xl border border-slate-800/80 bg-[#0a0f1d] space-y-2.5"
                        >
                          <div className="flex items-center justify-between">
                            <div className="flex items-center gap-2.5">
                              <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-blue-500/10 text-blue-400 border border-blue-500/20">
                                <Bot size={15} />
                              </div>
                              <div>
                                <h4 className="text-xs font-bold text-white truncate max-w-[180px]">{worker.name}</h4>
                                <p className="text-[10px] text-slate-400 truncate max-w-[180px]">{worker.role}</p>
                              </div>
                            </div>
                            <span
                              className={`px-2 py-0.5 rounded text-[9px] font-mono font-semibold uppercase ${
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
                    <div className="p-6 text-center border border-dashed border-slate-800 rounded-xl">
                      <Bot size={22} className="mx-auto text-slate-600 mb-2" />
                      <h4 className="text-xs font-bold text-slate-300">No active worker sessions</h4>
                      <p className="text-[11px] text-slate-500 mt-1">Propose a task to launch an autonomous worker session.</p>
                    </div>
                  )}

                  {/* Tasks List in Fleet */}
                  <div className="pt-2 space-y-3">
                    <h4 className="text-xs font-bold text-white">Project Tasks ({liveTasks.length})</h4>
                    <div className="space-y-2.5">
                      {liveTasks.map((task) => (
                        <MinimalTaskCard
                          key={task.id}
                          task={task}
                          isSelected={selectedTaskId === task.id}
                          onSelect={() => handleSelectTask(task)}
                          onOpenChat={() => handleSelectTask(task)}
                          onApprove={() => handleApproveTask(task.id)}
                          onIntegrate={() => handleIntegrateTask(task.id)}
                          onDelete={() => handleDeleteTask(task.id)}
                          onRefine={(fb, err) => handleRefineTask(task.id, fb, err)}
                          onPreviewDeliverable={(d) => setActiveVideoPreview(d)}
                        />
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
                          onClick={() => handleSelectTask(t)}
                          className={`p-3 rounded-lg border transition-all cursor-pointer ${
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
                      <div className="p-6 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-lg">
                        No tasks found.
                      </div>
                    )}
                  </div>

                  {/* Right Column: Live Inspector */}
                  <div className="w-1/2 flex flex-col p-4 overflow-y-auto space-y-3">
                    {selectedTaskForSplit ? (
                      <MinimalTaskCard
                        task={selectedTaskForSplit}
                        isSelected={true}
                        onSelect={() => handleSelectTask(selectedTaskForSplit)}
                        onOpenChat={() => handleSelectTask(selectedTaskForSplit)}
                        onApprove={() => handleApproveTask(selectedTaskForSplit.id)}
                        onIntegrate={() => handleIntegrateTask(selectedTaskForSplit.id)}
                        onDelete={() => handleDeleteTask(selectedTaskForSplit.id)}
                        onRefine={(fb, err) => handleRefineTask(selectedTaskForSplit.id, fb, err)}
                        onPreviewDeliverable={(d) => setActiveVideoPreview(d)}
                      />
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
                  {liveTasks.map((t) => (
                    <MinimalTaskCard
                      key={t.id}
                      task={t}
                      isSelected={selectedTaskId === t.id}
                      onSelect={() => handleSelectTask(t)}
                      onOpenChat={() => handleSelectTask(t)}
                      onApprove={() => handleApproveTask(t.id)}
                      onIntegrate={() => handleIntegrateTask(t.id)}
                      onDelete={() => handleDeleteTask(t.id)}
                      onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                      onPreviewDeliverable={(d) => setActiveVideoPreview(d)}
                    />
                  ))}
                  {liveTasks.length === 0 && (
                    <div className="p-8 text-center border border-dashed border-slate-800 rounded-xl text-xs text-slate-500">
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
          PANEL 3: RIGHT PANEL (CANONICAL DESKTOP V3 AI CHAT SIDEBAR)
         ───────────────────────────────────────────────────────────── */}
      {activeSessionId ? (
        <OrchestratorChatSidebar
          key={activeSessionId}
          sessionId={activeSessionId}
          project={selectedProject}
          activeTask={activeTask}
          onBackToOrchestrator={handleBackToOrchestrator}
        />
      ) : (
        <aside className="relative flex w-[440px] flex-shrink-0 flex-col items-center justify-center p-6 text-center rounded-3xl border border-slate-800/80 bg-[#0d121f] text-xs text-slate-400 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
          <div className="h-12 w-12 rounded-2xl bg-blue-600/20 border border-blue-500/30 flex items-center justify-center text-blue-400 mb-3 shadow-lg shadow-blue-600/10">
            <Bot size={22} />
          </div>
          <h3 className="font-bold text-sm text-white mb-1">Swarm Project Orchestrator</h3>
          <p className="text-slate-400 text-xs max-w-xs leading-relaxed">
            {selectedProject ? 'Connecting executive orchestrator session...' : 'Select or create a project to activate the executive AI orchestrator.'}
          </p>
        </aside>
      )}

      {/* ─────────────────────────────────────────────────────────────
          MODAL: NEW TASK (PLAIN ENGLISH PROPOSAL)
         ───────────────────────────────────────────────────────────── */}
      {isDeployModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div className="relative flex max-w-lg w-full flex-col p-6 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <Sparkles size={16} className="text-blue-400" />
                <h3 className="text-sm font-bold text-white">New Task</h3>
              </div>
              <button
                onClick={() => setIsDeployModalOpen(false)}
                className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
              >
                <X size={16} />
              </button>
            </div>

            <div className="space-y-3 text-xs">
              <p className="text-[11px] text-slate-400 leading-relaxed">
                Describe what you want to achieve in plain English. The AI will inspect your request, synthesize the mission, and present it on the task card for your approval.
              </p>

              <div>
                <textarea
                  value={newTaskPrompt}
                  onChange={(e) => setNewTaskPrompt(e.target.value)}
                  rows={4}
                  autoFocus
                  placeholder="e.g. Add GitHub OAuth login and make sure it has unit test coverage, or generate 3 promotional video cuts for the product launch..."
                  className="w-full rounded-lg bg-slate-950 border border-slate-800 p-3 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500/60 resize-none text-xs leading-relaxed"
                />
              </div>

              {selectedProject?.linkedWorkspaces && selectedProject.linkedWorkspaces.length > 1 && (
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block font-medium">Target Workspace</label>
                  <select
                    value={newTaskWorkspace || selectedProject.repoPath}
                    onChange={(e) => setNewTaskWorkspace(e.target.value)}
                    className="w-full rounded-lg bg-slate-950 border border-slate-800 px-3 py-2 text-white focus:outline-none focus:border-blue-500/60"
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
                className="px-4 py-2 rounded-lg text-xs font-medium text-slate-400 hover:text-slate-200 hover:bg-slate-800 transition-colors"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={!newTaskPrompt.trim() || isDeployingTask}
                onClick={handleDeployModalSubmit}
                className="flex items-center gap-1.5 px-5 py-2 rounded-lg text-xs font-bold text-white bg-blue-600 hover:bg-blue-500 shadow-md transition-all disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <Plus size={13} />
                <span>{isDeployingTask ? 'Organizing Mission...' : 'Create Task'}</span>
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
          <div className="relative flex max-w-2xl w-full flex-col p-5 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl">
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

            <div className="relative my-4 aspect-video w-full overflow-hidden flex items-center justify-center rounded-xl border border-slate-800 bg-black">
              <DeliverableThumbnail type={activeVideoPreview.thumbnailType} />
              <div className="absolute inset-0 flex flex-col items-center justify-center z-30">
                <button
                  onClick={() => setIsPlayingVideo(!isPlayingVideo)}
                  className="flex h-12 w-12 items-center justify-center rounded-full bg-blue-600 text-white shadow-2xl transition-transform hover:scale-105 active:scale-95"
                >
                  {isPlayingVideo ? <Pause size={20} /> : <Play size={20} fill="currentColor" />}
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
                <span className="text-slate-200">{activeVideoPreview.prompt || 'Generated by autonomous video worker'}</span>
              </div>
              <div className="flex items-center gap-4 text-slate-400 font-mono text-[11px]">
                <span>Duration: {activeVideoPreview.duration || '0:15'}</span>
                <span>Aspect: {activeVideoPreview.videoAspect || '16:9'}</span>
                <span>Render Time: {activeVideoPreview.metrics?.renderTime || '14.2s'}</span>
              </div>
            </div>

            <div className="mt-4 flex items-center justify-end gap-2 border-t border-slate-800 pt-3">
              <button
                onClick={() => {
                  const parentTask = tasks.find((t) =>
                    t.deliverables?.some((d) => d.id === activeVideoPreview.id)
                  )
                  if (parentTask) {
                    setTasks((prev) =>
                      prev.map((task) =>
                        task.id === parentTask.id
                          ? {
                              ...task,
                              deliverables: task.deliverables?.map((d) =>
                                d.id === activeVideoPreview.id ? { ...d, status: 'accepted' as const } : d
                              ),
                            }
                          : task
                      )
                    )
                  }
                  setActiveVideoPreview(null)
                }}
                className="flex items-center gap-2 px-5 py-2 text-xs font-bold text-white rounded-lg bg-blue-600 hover:bg-blue-500 shadow-md transition-all active:scale-95"
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
