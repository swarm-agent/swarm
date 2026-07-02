import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bot, Check, ChevronDown, ExternalLink, GitBranch, Lock, RotateCcw } from 'lucide-react'
import type { AgentProfileRecord, ModelOptionRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { displayModelName, formatModelPricing } from '../services/model-options'

interface AgentModelControlProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  mode: DesktopSessionMode
  selectedModel: ModelOptionRecord | null
  modelLocked?: boolean
  modelLockNotice?: string
  onAgentSelect: (agent: string) => void
  onModeSelect: (mode: DesktopSessionMode) => void
  onOpenModelPicker: () => void
  onOpenAgentSettings?: () => void
  onUseSingleModel?: (agent: AgentProfileRecord) => void | Promise<void>
  onUseDefaultModel?: (agent: AgentProfileRecord) => void | Promise<void>
  allowModeChange?: boolean
  busy?: boolean
  dropdownAlign?: 'left' | 'right'
}

const DROPDOWN_VIEWPORT_GUTTER = 8
const MOBILE_DROPDOWN_BREAKPOINT = 700

function agentMode(profile: AgentProfileRecord): string {
  return (profile.mode || 'primary').trim().toLowerCase()
}

function agentLabel(profile: AgentProfileRecord): string {
  return profile.name === 'swarm' ? 'Swarm' : profile.name
}

function runtimeLabel(profile: AgentProfileRecord | null): string {
  const raw = profile?.exitPlanModeEnabled ? 'plan_auto' : profile?.runtimeMode || profile?.executionSetting || ''
  switch (raw) {
    case 'plan_auto': return 'starts in plan'
    case 'read': return 'read-only'
    case 'readwrite': return 'auto-capable'
    default: return 'runtime default'
  }
}

function modelBehaviorLabel(profile: AgentProfileRecord | null): string {
  if (!profile) return 'Default model'
  if (profile.modelMode === 'split') return 'Split plan/auto models'
  if (profile.provider.trim() || profile.model.trim()) return 'Single model lock'
  return 'Default model'
}

function agentSingleModelLabel(profile: AgentProfileRecord): string {
  if (profile.provider.trim() || profile.model.trim()) {
    return `${profile.provider || 'provider'}/${profile.model || 'model'}`
  }
  const provider = profile.autoProvider || profile.planProvider
  const model = profile.autoModel || profile.planModel
  return provider || model ? `${provider || 'provider'}/${model || 'model'}` : 'Use current defaults'
}

export function AgentModelControl({
  currentAgent,
  selectedPrimaryAgent,
  agents,
  mode,
  selectedModel,
  modelLocked = false,
  modelLockNotice = '',
  onAgentSelect,
  onModeSelect,
  onOpenModelPicker,
  onOpenAgentSettings,
  onUseSingleModel,
  onUseDefaultModel,
  allowModeChange = true,
  busy = false,
  dropdownAlign = 'right',
}: AgentModelControlProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left?: number; right?: number; width: number; maxHeight: number } | null>(null)
  const selectableAgents = useMemo(() => agents.filter((agent) => agent.enabled !== false), [agents])
  const selectedProfile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? null
  const primaryAgents = selectableAgents.filter((agent) => agentMode(agent) === 'primary')
  const pricingLabel = selectedModel ? formatModelPricing(selectedModel.pricing) : ''
  const modelLabel = selectedModel
    ? `${selectedModel.provider}/${displayModelName(selectedModel.provider, selectedModel.model, selectedModel.contextMode)}`
    : 'No model selected'

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }
    const rect = triggerRef.current.getBoundingClientRect()
    const viewportWidth = window.innerWidth
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight
    const mobile = viewportWidth < MOBILE_DROPDOWN_BREAKPOINT
    const width = mobile ? viewportWidth - DROPDOWN_VIEWPORT_GUTTER * 2 : Math.min(560, viewportWidth - DROPDOWN_VIEWPORT_GUTTER * 2)
    const maxHeight = mobile ? Math.max(220, viewportHeight - rect.bottom - DROPDOWN_VIEWPORT_GUTTER * 2) : Math.max(260, Math.min(520, rect.top - DROPDOWN_VIEWPORT_GUTTER * 2))
    if (mobile) {
      setPosition({ top: Math.min(rect.bottom + DROPDOWN_VIEWPORT_GUTTER, viewportHeight - 180), left: DROPDOWN_VIEWPORT_GUTTER, width, maxHeight })
      return
    }
    setPosition({
      bottom: Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportHeight - rect.top + DROPDOWN_VIEWPORT_GUTTER),
      left: dropdownAlign === 'left' ? Math.min(Math.max(DROPDOWN_VIEWPORT_GUTTER, rect.left), Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportWidth - width - DROPDOWN_VIEWPORT_GUTTER)) : undefined,
      right: dropdownAlign === 'right' ? Math.max(DROPDOWN_VIEWPORT_GUTTER, viewportWidth - rect.right) : undefined,
      width,
      maxHeight,
    })
  }, [dropdownAlign])

  useLayoutEffect(() => {
    if (!open) {
      setPosition(null)
      return
    }
    updatePosition()
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) return
    function handlePointerDownOutside(event: PointerEvent) {
      const target = event.target as Node | null
      if (!target || !target.isConnected || !document.body.contains(target)) return
      if (triggerRef.current?.contains(target) || dropdownRef.current?.contains(target)) return
      setOpen(false)
    }
    function handleEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') setOpen(false)
    }
    window.addEventListener('scroll', updatePosition, true)
    window.addEventListener('resize', updatePosition)
    document.addEventListener('pointerdown', handlePointerDownOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      window.removeEventListener('scroll', updatePosition, true)
      window.removeEventListener('resize', updatePosition)
      document.removeEventListener('pointerdown', handlePointerDownOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [open, updatePosition])

  const chooseAgent = (agent: string) => {
    onAgentSelect(agent)
  }

  const openModels = () => {
    setOpen(false)
    onOpenModelPicker()
  }

  const dropdown = open && position ? createPortal(
    <div
      ref={dropdownRef}
      style={{
        position: 'fixed',
        top: position.top === undefined ? undefined : `${position.top}px`,
        bottom: position.bottom === undefined ? undefined : `${position.bottom}px`,
        left: position.left === undefined ? undefined : `${position.left}px`,
        right: position.right === undefined ? undefined : `${position.right}px`,
        width: `${position.width}px`,
        maxHeight: `${position.maxHeight}px`,
        zIndex: 9999,
      }}
    >
      <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/40" style={{ maxHeight: `${position.maxHeight}px` }}>
        <div className="flex max-h-[inherit] min-h-0 flex-col">
          <div className="border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Chat setup</div>
                <div className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]">{currentAgent || selectedPrimaryAgent || 'Agent'}</div>
              </div>
              {onOpenAgentSettings ? (
                <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="inline-flex shrink-0 items-center gap-1 rounded-full border border-[var(--app-border)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                  Agents <ExternalLink size={12} />
                </button>
              ) : null}
            </div>
            <div className="mt-3 grid gap-2 text-[11px] text-[var(--app-text-muted)] sm:grid-cols-3">
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                <div className="font-semibold text-[var(--app-text-subtle)]">Start mode</div>
                {allowModeChange ? (
                  <div className="mt-1 flex gap-1">
                    {(['plan', 'auto'] as DesktopSessionMode[]).map((candidate) => (
                      <button key={candidate} type="button" onClick={() => onModeSelect(candidate)} className={`rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase ${candidate === mode ? 'bg-[var(--app-primary)] text-[var(--app-primary-text)]' : 'bg-[var(--app-bg-alt)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'}`}>
                        {candidate}
                      </button>
                    ))}
                  </div>
                ) : (
                  <div className="mt-1 font-semibold uppercase text-[var(--app-primary)]">{mode}</div>
                )}
              </div>
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                <div className="font-semibold text-[var(--app-text-subtle)]">Agent model mode</div>
                <div className="mt-1 text-[var(--app-text)]">{modelBehaviorLabel(selectedProfile)}</div>
              </div>
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                <div className="font-semibold text-[var(--app-text-subtle)]">Runtime</div>
                <div className="mt-1 text-[var(--app-text)]">{runtimeLabel(selectedProfile)}</div>
              </div>
            </div>
          </div>

          <div className="grid min-h-0 flex-1 gap-0 min-[701px]:grid-cols-[220px_minmax(0,1fr)]">
            <div className="min-h-0 border-b border-[var(--app-border)] min-[701px]:border-b-0 min-[701px]:border-r">
              <div className="border-b border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Primary agents</div>
              <div className="max-h-56 overflow-y-auto py-1 min-[701px]:max-h-[340px]">
                {primaryAgents.map((profile) => {
                  const selected = profile.name === selectedPrimaryAgent
                  return (
                    <button key={profile.name} type="button" onClick={() => chooseAgent(profile.name)} className={`flex w-full items-start gap-2 px-3 py-2.5 text-left text-sm transition ${selected ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}>
                      {selected ? <Check size={14} className="mt-0.5 shrink-0 text-[var(--app-primary)]" /> : <span className="mt-0.5 w-[14px] shrink-0" />}
                      <Bot size={14} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate font-medium">{agentLabel(profile)}</span>
                        <span className="mt-0.5 block truncate text-[11px] text-[var(--app-text-subtle)]">{modelBehaviorLabel(profile)}</span>
                      </span>
                    </button>
                  )
                })}
              </div>
            </div>

            <div className="min-h-0 overflow-y-auto p-4">
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Current model</div>
                    <div className="mt-1 break-words text-sm font-semibold text-[var(--app-text)]">{modelLabel}</div>
                    {pricingLabel ? <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">{pricingLabel}</div> : null}
                  </div>
                  <button type="button" onClick={openModels} disabled={modelLocked} title={modelLocked ? modelLockNotice : 'Choose provider/model'} className="rounded-full border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-60">
                    Change
                  </button>
                </div>
                {modelLocked ? (
                  <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                    <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                    <span>{modelLockNotice || 'This agent controls the model from its profile.'}</span>
                  </div>
                ) : null}
              </div>

              {selectedProfile?.modelMode === 'split' ? (
                <div className="mt-3 rounded-xl border border-[var(--app-border)] p-3">
                  <div className="flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"><GitBranch size={14} /> Split model preferences</div>
                  <div className="mt-2 grid gap-2 text-[11px] text-[var(--app-text-muted)] sm:grid-cols-2">
                    <div className="rounded-lg bg-[var(--app-bg-alt)] px-3 py-2">Plan: {selectedProfile.planProvider || 'provider'}/{selectedProfile.planModel || 'model'}</div>
                    <div className="rounded-lg bg-[var(--app-bg-alt)] px-3 py-2">Auto: {selectedProfile.autoProvider || 'provider'}/{selectedProfile.autoModel || 'model'}</div>
                  </div>
                  <div className="mt-3 flex flex-wrap gap-2">
                    {onUseSingleModel ? (
                      <button type="button" disabled={busy} onClick={() => { void onUseSingleModel(selectedProfile) }} className="inline-flex items-center gap-1 rounded-full border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-60">
                        <RotateCcw size={12} /> Use single: {agentSingleModelLabel(selectedProfile)}
                      </button>
                    ) : null}
                    {onUseDefaultModel ? (
                      <button type="button" disabled={busy} onClick={() => { void onUseDefaultModel(selectedProfile) }} className="rounded-full border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-60">
                        Inherit default model
                      </button>
                    ) : null}
                  </div>
                </div>
              ) : selectedProfile && (selectedProfile.provider.trim() || selectedProfile.model.trim()) ? (
                <div className="mt-3 rounded-xl border border-[var(--app-border)] p-3 text-sm text-[var(--app-text-muted)]">
                  <div className="font-semibold text-[var(--app-text)]">Single model lock</div>
                  <div className="mt-1 text-[11px]">This agent always uses {agentSingleModelLabel(selectedProfile)} unless changed in Agents.</div>
                  {onUseDefaultModel ? (
                    <button type="button" disabled={busy} onClick={() => { void onUseDefaultModel(selectedProfile) }} className="mt-3 rounded-full border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-60">
                      Inherit default model
                    </button>
                  ) : null}
                </div>
              ) : (
                <div className="mt-3 rounded-xl border border-[var(--app-border)] p-3 text-[11px] text-[var(--app-text-muted)]">
                  This agent inherits the default model. Use Change for this chat, or Agents for advanced defaults and split plan/auto setup.
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  ) : null

  return (
    <div className="inline-flex min-w-0 items-center">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((prev) => !prev)}
        title="Open chat agent, model, and mode setup"
        className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)]"
      >
        <Bot size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="max-w-[120px] truncate">{currentAgent || selectedPrimaryAgent || 'Agent'}</span>
        <span className="hidden rounded-full bg-[var(--app-bg-alt)] px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)] min-[1120px]:inline">{mode}</span>
        <ChevronDown size={12} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
      </button>
      {dropdown}
    </div>
  )
}
