import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent, type KeyboardEvent } from 'react'
import { LoaderCircle, Mic, Minimize2, Send, Settings2, Square } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import type { AgentProfileRecord, ModelOptionRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopChatRoute } from '../services/chat-routing'
import { supportsCodexFastMode } from '../services/model-options'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand, type DesktopSlashPaletteState } from '../services/slash-commands'
import {
  chatMentionCandidates,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
} from '../services/subagent-mentions'
import { AgentPicker } from './agent-picker'
import { DesktopMentionPanel } from './desktop-mention-panel'
import { DesktopSlashCommandPanel } from './desktop-slash-command-panel'
import { ModePicker } from './mode-picker'
import { ModelPicker } from './model-picker'
import { ThinkingPicker } from './thinking-picker'

const THINKING_OPTIONS = ['off', 'low', 'medium', 'high', 'xhigh']
const FAST_ON_OFF_OPTIONS = ['off', 'on']
const DICTATION_RESTART_DELAY_MS = 180
const DICTATION_FINAL_FLUSH_MS = 450
const TODO_DRAG_MIME = 'application/x-swarm-workspace-todo'

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike

type SpeechRecognitionWindow = Window & typeof globalThis & {
  SpeechRecognition?: SpeechRecognitionConstructor
  webkitSpeechRecognition?: SpeechRecognitionConstructor
}

type SpeechRecognitionAlternativeLike = {
  transcript: string
  confidence?: number
}

type SpeechRecognitionResultLike = {
  readonly isFinal: boolean
  readonly length: number
  [index: number]: SpeechRecognitionAlternativeLike | undefined
}

type SpeechRecognitionResultListLike = {
  readonly length: number
  [index: number]: SpeechRecognitionResultLike | undefined
}

type SpeechRecognitionResultEventLike = Event & {
  readonly resultIndex: number
  readonly results: SpeechRecognitionResultListLike
}

type SpeechRecognitionErrorEventLike = Event & {
  readonly error?: string
  readonly message?: string
}

type SpeechRecognitionLike = {
  continuous: boolean
  interimResults: boolean
  lang: string
  maxAlternatives: number
  onstart: ((event: Event) => void) | null
  onend: ((event: Event) => void) | null
  onerror: ((event: SpeechRecognitionErrorEventLike) => void) | null
  onresult: ((event: SpeechRecognitionResultEventLike) => void) | null
  start: () => void
  stop: () => void
  abort: () => void
}

function getSpeechRecognitionConstructor(): SpeechRecognitionConstructor | null {
  if (typeof window === 'undefined') return null
  const speechWindow = window as SpeechRecognitionWindow
  return speechWindow.SpeechRecognition ?? speechWindow.webkitSpeechRecognition ?? null
}

function appendDictationText(base: string, addition: string): string {
  const normalizedAddition = addition.replace(/\s+/g, ' ').trim()
  if (!normalizedAddition) return base
  const trimmedBaseEnd = base.replace(/[ \t]+$/g, '')
  if (!trimmedBaseEnd) return normalizedAddition
  const needsSpace = !/[\s\n]$/.test(trimmedBaseEnd) && !/^[,.;:!?]/.test(normalizedAddition)
  return `${trimmedBaseEnd}${needsSpace ? ' ' : ''}${normalizedAddition}`
}

function speechRecognitionErrorMessage(error: string, message = ''): string {
  switch (error) {
    case 'not-allowed':
      return 'Microphone permission was denied.'
    case 'service-not-allowed':
      return 'Browser speech recognition is blocked in this context. Try Safari/Chrome over HTTPS.'
    case 'audio-capture':
      return 'No microphone was found for browser dictation.'
    case 'network':
      return 'Browser speech recognition hit a network error.'
    case 'no-speech':
      return 'No speech detected yet; still listening.'
    case 'language-not-supported':
      return 'This browser does not support speech recognition for the selected language.'
    default:
      return message.trim() || 'Browser speech recognition failed.'
  }
}

function normalizeThinking(value: string): string {
  return value.trim() || 'off'
}

function normalizeFastToggle(value: string): 'on' | 'off' {
  return value.trim().toLowerCase() === 'on' ? 'on' : 'off'
}

export interface DesktopV3AgenticComposerProps {
  draft: string
  onDraftChange: (draft: string) => void
  placeholder: string
  inputLabel: string
  disabled?: boolean
  locked?: boolean
  busy?: boolean
  canSubmit: boolean
  canStop?: boolean
  submitLabel?: string
  error?: string | null
  retainedNotice?: string | null
  onSubmit: () => void | Promise<void>
  onStop?: () => void | Promise<void>
  onAbandonRetained?: () => void
  mode: DesktopSessionMode
  onModeChange: (mode: DesktopSessionMode) => void
  showModePicker?: boolean
  executionLabel?: string
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  onAgentSelect: (agent: string) => void
  modelOptions: ModelOptionRecord[]
  selectedModelKey: string
  selectedModelAvailable: boolean
  onModelSelect: (key: string) => void
  modelPickerOpenSignal?: number
  modelPickerDisabled?: boolean
  modelPickerDisabledReason?: string
  modelLockNotice?: string
  onOpenAgentSettings?: () => void
  thinking: string
  onThinkingChange: (value: string) => void
  thinkingTagsEnabled?: boolean
  onThinkingTagsToggle?: (enabled: boolean) => void
  thinkingTagsBusy?: boolean
  fast: 'on' | 'off'
  onFastChange: (value: 'on' | 'off') => void
  route?: DesktopChatRoute | null
  routeOptions?: DesktopChatRoute[]
  onRouteSelect?: (routeId: string) => void
  routeTitle?: string
  contextLabel?: string
  contextTooltip?: string
  onCompact?: (draft: string) => void | Promise<void>
  compactDisabled?: boolean
  subagents?: string[]
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  onMentionSelect?: (agent: string) => void
  onDropTodo?: (event: ReactDragEvent<HTMLTextAreaElement>) => void
}

export function DesktopV3AgenticComposer({
  draft,
  onDraftChange,
  placeholder,
  inputLabel,
  disabled = false,
  locked = false,
  busy = false,
  canSubmit,
  canStop = false,
  submitLabel: _submitLabel,
  error,
  retainedNotice,
  onSubmit,
  onStop,
  onAbandonRetained,
  mode,
  onModeChange,
  showModePicker = true,
  executionLabel,
  currentAgent,
  selectedPrimaryAgent,
  agents,
  onAgentSelect,
  modelOptions,
  selectedModelKey,
  selectedModelAvailable,
  onModelSelect,
  modelPickerOpenSignal = 0,
  modelPickerDisabled = false,
  modelPickerDisabledReason = '',
  modelLockNotice = '',
  onOpenAgentSettings,
  thinking,
  onThinkingChange,
  thinkingTagsEnabled,
  onThinkingTagsToggle,
  thinkingTagsBusy = false,
  fast,
  onFastChange,
  contextLabel,
  contextTooltip,
  onCompact,
  compactDisabled = false,
  subagents = [],
  onSlashCommand,
  onMentionSelect,
  onDropTodo,
}: DesktopV3AgenticComposerProps) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const dictationEnabledRef = useRef(false)
  const dictationCanRunRef = useRef(false)
  const dictationRestartTimerRef = useRef<number | null>(null)
  const dictationBaseDraftRef = useRef('')
  const dictationFinalTranscriptRef = useRef('')
  const dictationInterimTranscriptRef = useRef('')
  const dictationManualStopRef = useRef(false)
  const dictationAcceptLateResultRef = useRef(false)
  const dictationStartingRef = useRef(false)
  const finalFlushTimerRef = useRef<number | null>(null)
  const [dictationSupported, setDictationSupported] = useState(false)
  const [dictationEnabled, setDictationEnabled] = useState(false)
  const [dictationListening, setDictationListening] = useState(false)
  const [dictationError, setDictationError] = useState<string | null>(null)
  const [mobileSettingsOpen, setMobileSettingsOpen] = useState(false)
  const [intermediateSettingsOpen, setIntermediateSettingsOpen] = useState(false)
  const [slashSelectionIndex, setSlashSelectionIndex] = useState(0)
  const [mentionSelectionIndex, setMentionSelectionIndex] = useState(0)
  const [internalModelPickerSignal, setInternalModelPickerSignal] = useState(0)
  const mobileSettingsRef = useRef<HTMLDivElement | null>(null)
  const mobileSettingsTriggerRef = useRef<HTMLButtonElement | null>(null)
  const intermediateSettingsRef = useRef<HTMLDivElement | null>(null)
  const intermediateSettingsTriggerRef = useRef<HTMLButtonElement | null>(null)

  const composerDisabled = disabled || locked
  const showDictationButton = true
  const dictationButtonDisabled = composerDisabled || !dictationSupported
  const selectableAgents = useMemo(() => agents.filter((agent) => agent.enabled !== false), [agents])
  const mentionSubagents = useMemo(
    () => normalizeMentionSubagents(subagents.length > 0 ? subagents : selectableAgents.filter((agent) => (agent.mode || '').toLowerCase() === 'subagent').map((agent) => agent.name)),
    [selectableAgents, subagents],
  )
  const slashPalette = useMemo(() => buildDesktopSlashPaletteState(draft), [draft])
  const mentionPaletteIsActive = useMemo(() => mentionPaletteActive(draft, mentionSubagents), [draft, mentionSubagents])
  const mentionPaletteMatches = useMemo(() => chatMentionCandidates(mentionPaletteQuery(draft), mentionSubagents), [draft, mentionSubagents])
  const selectedModel = useMemo(() => modelOptions.find((option) => option.key === selectedModelKey) ?? null, [modelOptions, selectedModelKey])
  const modelPickerLocked = modelPickerDisabled || Boolean(modelLockNotice.trim())
  const modelPickerReason = modelPickerDisabledReason || modelLockNotice
  const normalizedThinking = normalizeThinking(thinking)
  const fastSupported = selectedModel ? supportsCodexFastMode(selectedModel.provider, selectedModel.model) : false
  const effectiveModelPickerSignal = modelPickerOpenSignal + internalModelPickerSignal
  const dictationComposer = dictationEnabled
    ? appendDictationText(appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current), dictationInterimTranscriptRef.current)
    : draft

  const clearDictationRestartTimer = useCallback(() => {
    if (dictationRestartTimerRef.current === null || typeof window === 'undefined') return
    window.clearTimeout(dictationRestartTimerRef.current)
    dictationRestartTimerRef.current = null
  }, [])

  const clearFinalFlushTimer = useCallback(() => {
    if (finalFlushTimerRef.current === null || typeof window === 'undefined') return
    window.clearTimeout(finalFlushTimerRef.current)
    finalFlushTimerRef.current = null
  }, [])

  const commitDictationDraft = useCallback((includeInterim = false) => {
    const nextDraft = appendDictationText(
      appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current),
      includeInterim ? dictationInterimTranscriptRef.current : '',
    )
    dictationBaseDraftRef.current = nextDraft
    dictationFinalTranscriptRef.current = ''
    dictationInterimTranscriptRef.current = ''
    onDraftChange(nextDraft)
  }, [onDraftChange])

  const stopDictation = useCallback((acceptLateResults = false) => {
    dictationEnabledRef.current = false
    dictationAcceptLateResultRef.current = acceptLateResults
    dictationManualStopRef.current = true
    dictationStartingRef.current = false
    clearDictationRestartTimer()
    setDictationEnabled(false)
    setDictationListening(false)
    const recognition = recognitionRef.current
    if (recognition) {
      try {
        recognition.stop()
      } catch {
        try {
          recognition.abort()
        } catch {
          // ignore browser recognition shutdown races
        }
      }
    }
    if (acceptLateResults && typeof window !== 'undefined') {
      clearFinalFlushTimer()
      finalFlushTimerRef.current = window.setTimeout(() => {
        finalFlushTimerRef.current = null
        commitDictationDraft(false)
        dictationAcceptLateResultRef.current = false
      }, DICTATION_FINAL_FLUSH_MS)
    } else {
      commitDictationDraft(false)
      dictationAcceptLateResultRef.current = false
    }
  }, [clearDictationRestartTimer, clearFinalFlushTimer, commitDictationDraft])

  const startRecognition = useCallback(() => {
    if (!dictationEnabledRef.current || !dictationCanRunRef.current || dictationStartingRef.current) return
    const recognition = recognitionRef.current
    if (!recognition) return
    dictationStartingRef.current = true
    dictationManualStopRef.current = false
    try {
      recognition.start()
    } catch (error) {
      dictationStartingRef.current = false
      setDictationError(error instanceof Error ? error.message : 'Browser speech recognition failed to start.')
      stopDictation(false)
    }
  }, [stopDictation])

  useEffect(() => {
    const Recognition = getSpeechRecognitionConstructor()
    setDictationSupported(Boolean(Recognition))
    if (!Recognition) return
    const recognition = new Recognition()
    recognition.continuous = true
    recognition.interimResults = true
    recognition.lang = typeof navigator !== 'undefined' ? navigator.language || 'en-US' : 'en-US'
    recognition.maxAlternatives = 1
    recognition.onstart = () => {
      dictationStartingRef.current = false
      setDictationListening(true)
      setDictationError(null)
    }
    recognition.onresult = (event) => {
      if (!dictationEnabledRef.current && !dictationAcceptLateResultRef.current) return
      let finalTranscript = ''
      let interimTranscript = ''
      for (let index = event.resultIndex; index < event.results.length; index += 1) {
        const result = event.results[index]
        const transcript = result?.[0]?.transcript ?? ''
        if (!transcript) continue
        if (result?.isFinal) finalTranscript = appendDictationText(finalTranscript, transcript)
        else interimTranscript = appendDictationText(interimTranscript, transcript)
      }
      if (finalTranscript) {
        dictationFinalTranscriptRef.current = appendDictationText(dictationFinalTranscriptRef.current, finalTranscript)
      }
      dictationInterimTranscriptRef.current = interimTranscript
      onDraftChange(appendDictationText(appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current), dictationInterimTranscriptRef.current))
    }
    recognition.onerror = (event) => {
      const error = event.error ?? ''
      dictationStartingRef.current = false
      if (error === 'no-speech' && dictationEnabledRef.current) {
        setDictationError(speechRecognitionErrorMessage(error, event.message))
        return
      }
      setDictationError(speechRecognitionErrorMessage(error, event.message))
      stopDictation(false)
    }
    recognition.onend = () => {
      setDictationListening(false)
      dictationStartingRef.current = false
      if (!dictationEnabledRef.current || dictationManualStopRef.current || !dictationCanRunRef.current || typeof window === 'undefined') return
      clearDictationRestartTimer()
      dictationRestartTimerRef.current = window.setTimeout(() => {
        dictationRestartTimerRef.current = null
        if (recognitionRef.current && dictationEnabledRef.current && dictationCanRunRef.current) startRecognition()
      }, DICTATION_RESTART_DELAY_MS)
    }
    recognitionRef.current = recognition
    return () => {
      clearDictationRestartTimer()
      clearFinalFlushTimer()
      dictationEnabledRef.current = false
      try {
        recognition.abort()
      } catch {
        // ignore browser recognition teardown races
      }
      recognitionRef.current = null
    }
  }, [clearDictationRestartTimer, clearFinalFlushTimer, onDraftChange, startRecognition, stopDictation])

  useEffect(() => {
    dictationCanRunRef.current = showDictationButton && !composerDisabled
    if (!dictationCanRunRef.current && dictationEnabledRef.current) stopDictation(true)
  }, [composerDisabled, showDictationButton, stopDictation])

  useEffect(() => {
    if (!mobileSettingsOpen && !intermediateSettingsOpen) return
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as Node
      if (
        mobileSettingsRef.current?.contains(target) ||
        mobileSettingsTriggerRef.current?.contains(target) ||
        intermediateSettingsRef.current?.contains(target) ||
        intermediateSettingsTriggerRef.current?.contains(target) ||
        !document.getElementById('root')?.contains(target)
      ) return
      setMobileSettingsOpen(false)
      setIntermediateSettingsOpen(false)
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [mobileSettingsOpen, intermediateSettingsOpen])

  const handleDictationToggle = useCallback(() => {
    if (dictationButtonDisabled) return
    if (dictationEnabledRef.current) {
      stopDictation(true)
      return
    }
    dictationCanRunRef.current = true
    dictationAcceptLateResultRef.current = false
    dictationBaseDraftRef.current = draft
    dictationFinalTranscriptRef.current = ''
    dictationInterimTranscriptRef.current = ''
    dictationEnabledRef.current = true
    setDictationEnabled(true)
    setDictationError(null)
    startRecognition()
  }, [dictationButtonDisabled, draft, startRecognition, stopDictation])

  const handleSubmitClick = useCallback(() => {
    if (canStop) {
      void onStop?.()
      return
    }
    if (dictationEnabledRef.current) stopDictation(true)
    void onSubmit()
  }, [canStop, onStop, onSubmit, stopDictation])

  const handleMentionInsert = useCallback((agent: string) => {
    const trimmedStartLength = draft.length - draft.replace(/^[\s\t\r\n]+/, '').length
    const prefix = draft.slice(0, trimmedStartLength)
    const body = draft.slice(trimmedStartLength)
    const withoutAt = body.startsWith('@') ? body.slice(1) : body
    const firstWhitespace = withoutAt.search(/\s/)
    const rest = firstWhitespace >= 0 ? withoutAt.slice(firstWhitespace).trimStart() : ''
    const next = `${prefix}@${agent}${rest ? ` ${rest}` : ' '}`
    onDraftChange(next)
    onMentionSelect?.(agent)
  }, [draft, onDraftChange, onMentionSelect])

  const handleSlashSelect = useCallback((command: DesktopSlashCommand) => {
    if (command.state !== 'ready') return
    void onSlashCommand?.(command, draft)
    if (command.action.kind === 'open-model-picker') {
      setInternalModelPickerSignal((current) => current + 1)
      onDraftChange('')
      return
    }
    if (command.action.kind === 'toggle-fast') {
      if (fastSupported) onFastChange(normalizeFastToggle(fast) === 'on' ? 'off' : 'on')
      onDraftChange('')
      return
    }
    if (command.action.kind === 'compact-session') {
      void onCompact?.(draft)
      onDraftChange('')
      return
    }
    if (!slashPalette.hasArguments) onDraftChange('')
  }, [draft, fast, fastSupported, onCompact, onDraftChange, onFastChange, onSlashCommand, slashPalette.hasArguments])

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (mentionPaletteIsActive && mentionPaletteMatches.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setMentionSelectionIndex((current) => Math.min(mentionPaletteMatches.length - 1, current + 1))
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setMentionSelectionIndex((current) => Math.max(0, current - 1))
        return
      }
      if (event.key === 'Tab' || event.key === 'Enter') {
        event.preventDefault()
        handleMentionInsert(mentionPaletteMatches[mentionSelectionIndex] ?? mentionPaletteMatches[0])
        return
      }
    }
    if (slashPalette.active && slashPalette.matches.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setSlashSelectionIndex((current) => Math.min(slashPalette.matches.length - 1, current + 1))
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setSlashSelectionIndex((current) => Math.max(0, current - 1))
        return
      }
      if (event.key === 'Tab') {
        event.preventDefault()
        const command = slashPalette.matches[slashSelectionIndex] ?? slashPalette.matches[0]
        if (command) onDraftChange(command.command + ' ')
        return
      }
      if (event.key === 'Enter' && !event.shiftKey && !slashPalette.hasArguments) {
        event.preventDefault()
        const command = slashPalette.matches[slashSelectionIndex] ?? slashPalette.matches[0]
        if (command) handleSlashSelect(command)
        return
      }
    }
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      if (canSubmit || canStop) handleSubmitClick()
    }
  }, [canStop, canSubmit, handleMentionInsert, handleSlashSelect, handleSubmitClick, mentionPaletteIsActive, mentionPaletteMatches, mentionSelectionIndex, onDraftChange, slashPalette, slashSelectionIndex])

  const handleDragOver = useCallback((event: ReactDragEvent<HTMLTextAreaElement>) => {
    const hasTodo = Array.from(event.dataTransfer.types).includes(TODO_DRAG_MIME) || Array.from(event.dataTransfer.types).includes('text/plain')
    if (!hasTodo) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const compactButton = (mobile = false) => (
    <button
      type="button"
      onClick={() => { void onCompact?.(draft) }}
      disabled={compactDisabled || !onCompact}
      title={contextTooltip || 'Compact conversation'}
      className={mobile
        ? 'inline-flex h-10 min-w-0 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-1.5 text-[10px] font-medium tabular-nums text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50 sm:px-2.5 sm:text-[11px]'
        : 'inline-flex min-h-6 items-center gap-1 rounded-full bg-[var(--app-bg-alt)] px-2 py-0.5 font-medium tabular-nums text-[var(--app-text)] transition hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50'}
    >
      <span className={mobile ? 'min-w-0 w-full truncate text-center' : undefined}>{contextLabel || 'ctx'}</span>
      {mobile ? null : <Minimize2 size={12} className="text-[var(--app-text-subtle)]" />}
    </button>
  )

  const selectedAgentRuntimeMode = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent)?.runtimeMode || ''
  const runtimeSummary = selectedAgentRuntimeMode ? ` · ${selectedAgentRuntimeMode.replace('_', ' ')}` : ''
  const settingsSummary = `${mode}${runtimeSummary} · ${selectedModel?.label || 'Model'} · ${normalizedThinking}${fastSupported ? ` · fast ${fast}` : ''}`

  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="desktop-v3-agentic-composer">
      <div className="grid gap-3 px-4 pb-[calc(0.75rem+var(--app-safe-area-bottom))] pt-4 focus-within:pb-[calc(1rem+var(--app-safe-area-bottom))] sm:px-6 sm:pb-[calc(1.25rem+var(--app-safe-area-bottom))] sm:pt-5">
        {error ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]" role="alert">{error}</div> : null}
        {dictationError ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning-text)]">{dictationError}</div> : null}
        {retainedNotice ? (
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>{retainedNotice}</span>
              {onAbandonRetained ? <Button type="button" variant="ghost" onClick={onAbandonRetained} disabled={busy}>Abandon retained operation</Button> : null}
            </div>
          </div>
        ) : null}
        {modelLockNotice ? (
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>{modelLockNotice}</span>
              {onOpenAgentSettings ? <Button type="button" variant="ghost" onClick={onOpenAgentSettings}>Settings → Agents</Button> : null}
            </div>
          </div>
        ) : null}
        {mentionPaletteIsActive ? (
          <DesktopMentionPanel matches={mentionPaletteMatches} selectedIndex={mentionSelectionIndex} onHover={setMentionSelectionIndex} onSelect={handleMentionInsert} />
        ) : slashPalette.active ? (
          <DesktopSlashCommandPanel palette={slashPalette as DesktopSlashPaletteState} selectedIndex={slashSelectionIndex} onHover={setSlashSelectionIndex} onSelect={handleSlashSelect} />
        ) : null}
        <div className="relative rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] transition-colors focus-within:border-[var(--app-border-accent)]">
          <div className="flex items-end gap-3 px-4 py-3 lg:py-2.5">
            <div className="min-w-0 flex-1">
              <Textarea
                value={dictationComposer}
                onChange={(event) => {
                  if (dictationEnabledRef.current) {
                    dictationBaseDraftRef.current = event.target.value
                    dictationFinalTranscriptRef.current = ''
                    dictationInterimTranscriptRef.current = ''
                  }
                  onDraftChange(event.target.value)
                }}
                onKeyDown={handleKeyDown}
                onDragOver={handleDragOver}
                onDrop={onDropTodo}
                placeholder={placeholder}
                aria-label={inputLabel}
                className={showDictationButton ? 'min-h-[56px] resize-none !rounded-none !border-0 !border-none bg-transparent px-0 py-0 pr-12 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent lg:min-h-[52px]' : 'min-h-[56px] resize-none !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent lg:min-h-[52px]'}
                rows={2}
                disabled={composerDisabled}
              />
              {showDictationButton ? (
                <button
                  type="button"
                  onClick={handleDictationToggle}
                  disabled={dictationButtonDisabled}
                  aria-pressed={dictationEnabled}
                  aria-label={dictationEnabled ? 'Stop microphone dictation' : 'Start microphone dictation'}
                  title={dictationSupported ? (dictationEnabled ? 'Stop dictation' : 'Start dictation') : 'Speech recognition is not available in this browser'}
                  className={dictationEnabled
                    ? 'absolute right-3 top-3 inline-flex h-9 w-9 items-center justify-center rounded-full border border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-primary-text)] shadow-sm transition hover:bg-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50'
                    : 'absolute right-3 top-3 inline-flex h-9 w-9 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] shadow-sm transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50'}
                >
                  <Mic size={17} className={dictationListening ? 'animate-pulse' : undefined} />
                </button>
              ) : null}
            </div>
          </div>
          {mentionPaletteIsActive ? (
            <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2 text-[11px] text-[var(--app-text-muted)]">
              Use ↑/↓ to choose a subagent, Tab or Enter to insert, then continue typing your task.
            </div>
          ) : null}
          <div className="border-t border-[var(--app-border)] px-4 py-2 text-[11px]">
            <div className="hidden min-w-0 items-center justify-between gap-2 min-[1000px]:flex">
              <div className="flex min-w-0 flex-1 items-center gap-3 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {showModePicker ? <ModePicker mode={mode} onSelect={onModeChange} /> : executionLabel ? (
                  <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-[var(--app-text-muted)]">
                    <span className="text-[var(--app-text-subtle)]">Execution:</span>
                    <span className="font-semibold uppercase tracking-wider text-[var(--app-primary)]">{executionLabel}</span>
                  </span>
                ) : null}
                <AgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} onSelect={onAgentSelect} />
                <ModelPicker options={modelOptions} selectedKey={selectedModelAvailable ? selectedModelKey : ''} onSelect={onModelSelect} openSignal={effectiveModelPickerSignal} disabled={modelPickerLocked} disabledReason={modelPickerReason} />
                <div className="hidden min-[1100px]:contents">
                  <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={onThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={onThinkingTagsToggle} tagsBusy={thinkingTagsBusy} disabled={modelPickerLocked} disabledReason={modelPickerReason} />
                  {fastSupported ? <ThinkingPicker value={fast} options={FAST_ON_OFF_OPTIONS} onSelect={(value) => onFastChange(normalizeFastToggle(value))} label="Fast" /> : null}
                </div>
                <div className="relative hidden min-[1000px]:block min-[1100px]:hidden">
                  {intermediateSettingsOpen ? (
                    <div ref={intermediateSettingsRef} className="absolute bottom-[100%] left-0 z-50 mb-2 flex w-[260px] flex-col gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-[var(--shadow-panel)]">
                      <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={onThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={onThinkingTagsToggle} tagsBusy={thinkingTagsBusy} disabled={modelPickerLocked} disabledReason={modelPickerReason} />
                      {fastSupported ? <ThinkingPicker value={fast} options={FAST_ON_OFF_OPTIONS} onSelect={(value) => onFastChange(normalizeFastToggle(value))} label="Fast" /> : null}
                    </div>
                  ) : null}
                  <button ref={intermediateSettingsTriggerRef} type="button" onClick={() => setIntermediateSettingsOpen(!intermediateSettingsOpen)} title="Thinking and speed settings" aria-haspopup="menu" aria-expanded={intermediateSettingsOpen} className="inline-flex h-10 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                    <span className="truncate">{normalizedThinking}</span>
                  </button>
                </div>
                {compactButton(false)}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-xl border border-transparent bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] active:bg-[var(--app-primary-active)]" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <Send size={20} />}
                </Button>
              </div>
            </div>
            <div className="relative flex w-full min-w-0 min-[1000px]:hidden">
              {mobileSettingsOpen ? (
                <div ref={mobileSettingsRef} className="absolute bottom-[100%] left-0 z-50 mb-2 flex w-[max(260px,100%)] flex-col gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-[var(--shadow-panel)]">
                  {showModePicker ? <ModePicker mode={mode} onSelect={onModeChange} /> : null}
                  <AgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} onSelect={onAgentSelect} dropdownAlign="left" />
                  <ModelPicker options={modelOptions} selectedKey={selectedModelAvailable ? selectedModelKey : ''} onSelect={onModelSelect} openSignal={effectiveModelPickerSignal} disabled={modelPickerLocked} disabledReason={modelPickerReason} />
                  <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={onThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={onThinkingTagsToggle} tagsBusy={thinkingTagsBusy} disabled={modelPickerLocked} disabledReason={modelPickerReason} />
                  {fastSupported ? <ThinkingPicker value={fast} options={FAST_ON_OFF_OPTIONS} onSelect={(value) => onFastChange(normalizeFastToggle(value))} label="Fast" /> : null}
                </div>
              ) : null}
              <div className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_48px_40px] items-center gap-1.5 sm:grid-cols-[minmax(0,1fr)_56px_40px] sm:gap-2">
                <button ref={mobileSettingsTriggerRef} type="button" onClick={() => setMobileSettingsOpen(!mobileSettingsOpen)} className="flex h-10 min-w-0 items-center gap-1.5 overflow-hidden rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] px-2 text-left shadow-sm transition hover:bg-[var(--app-surface-hover)]" title="Open mode, agent, model, thinking, and speed settings">
                  <Settings2 size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="flex min-w-0 flex-col leading-tight">
                    <span className="truncate text-[11px] font-medium text-[var(--app-text)] sm:text-[12px]">{currentAgent}</span>
                    <span className="truncate text-[10px] text-[var(--app-text-muted)]">{settingsSummary}</span>
                  </span>
                </button>
                {compactButton(true)}
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-xl border border-transparent bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] active:bg-[var(--app-primary-active)]" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <Send size={20} />}
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
