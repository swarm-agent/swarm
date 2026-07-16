import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type DragEvent as ReactDragEvent, type KeyboardEvent } from 'react'
import { AlertTriangle, LoaderCircle, Mic, Minimize2, Send, Square } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import type { AgentProfileRecord, ModelOptionRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand, type DesktopSlashPaletteState } from '../services/slash-commands'
import {
  chatMentionCandidates,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
} from '../services/subagent-mentions'
import { AgentModelControl, type AgentModelControlConfirmInput } from './agent-model-control'
import { AgentPicker } from './agent-picker'
import { ModePicker } from './mode-picker'
import { DesktopMentionPanel } from './desktop-mention-panel'
import { DesktopSlashCommandPanel } from './desktop-slash-command-panel'

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

function clampContextUsagePercent(value?: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 0
  return Math.max(0, Math.min(100, value))
}

export interface DesktopV3AgenticComposerProps {
  draft: string
  onDraftChange: (draft: string) => void
  placeholder: string
  inputLabel: string
  disabled?: boolean
  busy?: boolean
  canSubmit: boolean
  canStop?: boolean
  submitLabel?: string
  error?: string | null
  onSubmit: (draft: string) => void | Promise<void>
  onStop?: () => void | Promise<void>
  mode: DesktopSessionMode
  onModeSelect?: (mode: DesktopSessionMode) => void
  showModePicker?: boolean
  executionLabel?: string
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  modelOptions: ModelOptionRecord[]
  selectedModelKey: string
  selectedServiceTier?: string
  agentSettingsOpenSignal?: number
  agentSettingsInitialAgent?: string
  modelPickerDisabled?: boolean
  modelPickerDisabledReason?: string
  modelLockNotice?: string
  modelControlDetail?: string
  onOpenAgentSettings?: (agent?: string) => void
  onAgentSelect?: (agent: string) => void | Promise<void>
  needsAuth?: boolean
  onOpenAuthSettings?: () => void
  onConfirmAgentSettings?: (input: AgentModelControlConfirmInput) => void | Promise<void>
  agentModelControlBusy?: boolean
  thinking: string
  thinkingTagsEnabled?: boolean
  onThinkingTagsToggle?: (enabled: boolean) => void
  thinkingTagsBusy?: boolean
  contextLabel?: string
  contextTooltip?: string
  contextUsagePercent?: number
  onCompact?: (draft: string) => void | Promise<void>
  compactDisabled?: boolean
  subagents?: string[]
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  onMentionSelect?: (agent: string) => void
  onDropTodo?: (event: ReactDragEvent<HTMLTextAreaElement>) => void
}

export const DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME = "mx-auto grid w-full min-w-0 max-w-[70rem] gap-3 px-4 pb-[calc(0.75rem+var(--app-safe-area-bottom))] pt-4 sm:px-6 sm:pb-[calc(1.25rem+var(--app-safe-area-bottom))] sm:pt-5";

export function DesktopV3AgenticComposer({
  draft,
  onDraftChange,
  placeholder,
  inputLabel,
  disabled = false,
  busy = false,
  canSubmit,
  canStop = false,
  submitLabel: _submitLabel,
  error,
  onSubmit,
  onStop,
  mode,
  onModeSelect,
  showModePicker = true,
  executionLabel,
  currentAgent,
  selectedPrimaryAgent,
  agents,
  modelOptions,
  selectedModelKey,
  selectedServiceTier = '',
  agentSettingsOpenSignal = 0,
  agentSettingsInitialAgent = '',
  modelPickerDisabled = false,
  modelPickerDisabledReason = '',
  modelLockNotice = '',
  modelControlDetail = '',
  onOpenAgentSettings,
  onAgentSelect,
  needsAuth = false,
  onOpenAuthSettings,
  onConfirmAgentSettings,
  agentModelControlBusy = false,
  thinking,
  thinkingTagsEnabled,
  onThinkingTagsToggle,
  thinkingTagsBusy = false,
  contextLabel,
  contextTooltip,
  contextUsagePercent,
  onCompact,
  compactDisabled = false,
  subagents = [],
  onSlashCommand,
  onMentionSelect,
  onDropTodo,
}: DesktopV3AgenticComposerProps) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
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
  const [slashSelectionIndex, setSlashSelectionIndex] = useState(0)
  const [mentionSelectionIndex, setMentionSelectionIndex] = useState(0)
  const [agentSetupOpenSignal, setAgentSetupOpenSignal] = useState(0)
  const [agentSetupInitialAgent, setAgentSetupInitialAgent] = useState('')

  const composerDisabled = disabled
  const showDictationButton = true
  const dictationButtonDisabled = composerDisabled || !dictationSupported
  const selectableAgents = useMemo(() => agents.filter((agent) => agent.enabled !== false), [agents])
  const mentionSubagents = useMemo(
    () => normalizeMentionSubagents(subagents.length > 0 ? subagents : selectableAgents.filter((agent) => (agent.mode || '').toLowerCase() === 'subagent').map((agent) => agent.name)),
    [selectableAgents, subagents],
  )
  const slashPalette = useMemo(() => buildDesktopSlashPaletteState(draft), [draft])
  const slashCommands = useMemo(() => slashPalette.matches.filter((command) => command.state === 'ready'), [slashPalette.matches])
  const mentionPaletteIsActive = useMemo(() => mentionPaletteActive(draft, mentionSubagents), [draft, mentionSubagents])
  const mentionPaletteMatches = useMemo(() => chatMentionCandidates(mentionPaletteQuery(draft), mentionSubagents), [draft, mentionSubagents])
  const selectedModel = useMemo(() => modelOptions.find((option) => option.key === selectedModelKey) ?? null, [modelOptions, selectedModelKey])
  const selectedThinking = thinking.trim() || 'off'
  const effectiveAgentSetupOpenSignal = agentSettingsOpenSignal + agentSetupOpenSignal
  const effectiveAgentSetupInitialAgent = agentSetupInitialAgent || agentSettingsInitialAgent
  const openAgentSetup = useCallback((agent?: string) => {
    setAgentSetupInitialAgent(agent?.trim() || currentAgent)
    setAgentSetupOpenSignal((current) => current + 1)
  }, [currentAgent])
  const dictationComposer = dictationEnabled
    ? appendDictationText(appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current), dictationInterimTranscriptRef.current)
    : draft

  const resizeTextareaElement = useCallback((textarea: HTMLTextAreaElement | null) => {
    if (!textarea) return
    textarea.style.height = 'auto'
    const viewportMaxHeight = typeof window === 'undefined' ? 360 : Math.max(120, Math.floor(window.innerHeight * 0.5))
    const nextHeight = Math.min(textarea.scrollHeight, viewportMaxHeight)
    textarea.style.height = `${nextHeight}px`
    textarea.style.overflowY = textarea.scrollHeight > viewportMaxHeight ? 'auto' : 'hidden'
  }, [])

  const resizeComposerTextarea = useCallback(() => {
    resizeTextareaElement(textareaRef.current)
  }, [resizeTextareaElement])

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

  const clearComposerForSubmit = useCallback(() => {
    dictationEnabledRef.current = false
    dictationCanRunRef.current = false
    dictationAcceptLateResultRef.current = false
    dictationManualStopRef.current = true
    dictationStartingRef.current = false
    dictationBaseDraftRef.current = ''
    dictationFinalTranscriptRef.current = ''
    dictationInterimTranscriptRef.current = ''
    clearDictationRestartTimer()
    clearFinalFlushTimer()
    setDictationEnabled(false)
    setDictationListening(false)
    const recognition = recognitionRef.current
    if (recognition) {
      try {
        recognition.abort()
      } catch {
        // ignore browser recognition shutdown races
      }
    }
    const textarea = textareaRef.current
    if (textarea) {
      textarea.value = ''
      resizeTextareaElement(textarea)
    }
    onDraftChange('')
  }, [clearDictationRestartTimer, clearFinalFlushTimer, onDraftChange, resizeTextareaElement])

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
    resizeComposerTextarea()
  }, [dictationComposer, resizeComposerTextarea])

  useEffect(() => {
    if (typeof window === 'undefined') return
    window.addEventListener('resize', resizeComposerTextarea)
    return () => window.removeEventListener('resize', resizeComposerTextarea)
  }, [resizeComposerTextarea])

  useEffect(() => {
    dictationCanRunRef.current = showDictationButton && !composerDisabled
    if (!dictationCanRunRef.current && dictationEnabledRef.current) stopDictation(true)
  }, [composerDisabled, showDictationButton, stopDictation])

  useEffect(() => {
    setSlashSelectionIndex((current) => Math.min(Math.max(0, current), Math.max(0, slashCommands.length - 1)))
  }, [slashCommands.length])

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
    const submittedDraft = textareaRef.current?.value ?? dictationComposer
    clearComposerForSubmit()
    void onSubmit(submittedDraft)
  }, [canStop, clearComposerForSubmit, dictationComposer, onStop, onSubmit])

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
    if (command.action.kind === 'open-model-picker' || command.action.kind === 'toggle-fast') {
      openAgentSetup(currentAgent)
      onDraftChange('')
      return
    }
    if (command.action.kind === 'compact-session') {
      void onCompact?.(draft)
      onDraftChange('')
      return
    }
    if (!slashPalette.hasArguments) onDraftChange('')
  }, [currentAgent, draft, onCompact, onDraftChange, onSlashCommand, openAgentSetup, slashPalette.hasArguments])

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
    if (slashPalette.active && slashCommands.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setSlashSelectionIndex((current) => Math.min(slashCommands.length - 1, current + 1))
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setSlashSelectionIndex((current) => Math.max(0, current - 1))
        return
      }
      if (event.key === 'Tab') {
        event.preventDefault()
        const command = slashCommands[slashSelectionIndex] ?? slashCommands[0]
        if (command) onDraftChange(command.command + ' ')
        return
      }
      if (event.key === 'Enter' && !event.shiftKey && !slashPalette.hasArguments) {
        event.preventDefault()
        const command = slashCommands[slashSelectionIndex] ?? slashCommands[0]
        if (command) handleSlashSelect(command)
        return
      }
    }
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      if (canSubmit || canStop) handleSubmitClick()
    }
  }, [canStop, canSubmit, handleMentionInsert, handleSlashSelect, handleSubmitClick, mentionPaletteIsActive, mentionPaletteMatches, mentionSelectionIndex, onDraftChange, slashCommands, slashPalette.active, slashPalette.hasArguments, slashSelectionIndex])

  const handleDragOver = useCallback((event: ReactDragEvent<HTMLTextAreaElement>) => {
    const hasTodo = Array.from(event.dataTransfer.types).includes(TODO_DRAG_MIME) || Array.from(event.dataTransfer.types).includes('text/plain')
    if (!hasTodo) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const hasContextUsagePercent = typeof contextUsagePercent === 'number' && Number.isFinite(contextUsagePercent)
  const normalizedContextUsagePercent = clampContextUsagePercent(contextUsagePercent)
  const mobileContextUsageLabel = hasContextUsagePercent ? `${Math.round(normalizedContextUsagePercent)}%` : 'ctx'
  const mobileContextProgressStyle: CSSProperties = {
    background: `conic-gradient(var(--app-primary) ${normalizedContextUsagePercent * 3.6}deg, var(--app-border) 0deg)`,
  }
  const dictationButton = () => showDictationButton ? (
    <button
      type="button"
      onClick={handleDictationToggle}
      disabled={dictationButtonDisabled}
      aria-pressed={dictationEnabled}
      aria-label={dictationEnabled ? 'Stop microphone dictation' : 'Start microphone dictation'}
      title={dictationSupported ? (dictationEnabled ? 'Stop dictation' : 'Start dictation') : 'Speech recognition is not available in this browser'}
      className={dictationEnabled
        ? 'inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-primary-text)] shadow-sm transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0'
        : 'inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-surface)] text-[var(--app-text-muted)] shadow-sm transition-all hover:-translate-y-0.5 hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] hover:shadow-md disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0'}
    >
      <Mic size={15} className={dictationListening ? 'animate-pulse' : undefined} />
    </button>
  ) : null

  const compactButton = (mobile = false) => (
    <button
      type="button"
      onClick={() => { void onCompact?.(draft) }}
      disabled={compactDisabled || !onCompact}
      title={contextTooltip || 'Compact conversation'}
      aria-label={contextTooltip || 'Compact conversation'}
      style={mobile ? mobileContextProgressStyle : undefined}
      className={mobile
        ? 'inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border-0 p-[2px] text-[9px] font-semibold uppercase tracking-wide text-[var(--app-primary)] shadow-sm transition-all hover:-translate-y-0.5 hover:shadow-md disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0'
        : 'inline-flex min-h-7 min-w-0 items-center gap-1 rounded-lg border-0 bg-transparent px-2 text-[11px] font-medium tabular-nums text-[var(--app-text)] transition-all hover:-translate-y-0.5 hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none'}
    >
      {mobile ? (
        <span className="flex h-full w-full items-center justify-center rounded-full bg-[var(--app-bg-alt)]">{mobileContextUsageLabel}</span>
      ) : (
        <>
          <span>{contextLabel || 'ctx'}</span>
          <Minimize2 size={12} className="text-[var(--app-text-subtle)]" />
        </>
      )}
    </button>
  )

  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="desktop-v3-agentic-composer">
      <div className={DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME}>
        {error ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]" role="alert">{error}</div> : null}
        {dictationError ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning)]">{dictationError}</div> : null}
        {mentionPaletteIsActive ? (
          <DesktopMentionPanel matches={mentionPaletteMatches} selectedIndex={mentionSelectionIndex} onHover={setMentionSelectionIndex} onSelect={handleMentionInsert} />
        ) : slashPalette.active ? (
          <DesktopSlashCommandPanel palette={slashPalette as DesktopSlashPaletteState} selectedIndex={slashSelectionIndex} onHover={setSlashSelectionIndex} onSelect={handleSlashSelect} />
        ) : null}
        <div className="relative min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] transition-colors focus-within:border-[var(--app-border-accent)]">
          <div className="flex min-w-0 items-end gap-3 px-4 py-2 sm:py-3 lg:py-2.5">
            <div className="min-w-0 flex-1">
              <Textarea
                ref={textareaRef}
                value={dictationComposer}
                onChange={(event) => {
                  if (dictationEnabledRef.current) {
                    dictationBaseDraftRef.current = event.target.value
                    dictationFinalTranscriptRef.current = ''
                    dictationInterimTranscriptRef.current = ''
                  }
                  onDraftChange(event.target.value)
                  resizeTextareaElement(event.target)
                }}
                onKeyDown={handleKeyDown}
                onDragOver={handleDragOver}
                onDrop={onDropTodo}
                placeholder={placeholder}
                aria-label={inputLabel}
                className="max-h-[50vh] !min-h-[32px] resize-none overflow-y-hidden !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]"
                rows={1}
                disabled={composerDisabled}
              />
            </div>
          </div>
          {mentionPaletteIsActive ? (
            <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2 text-[11px] text-[var(--app-text-muted)]">
              Use ↑/↓ to choose a subagent, Tab or Enter to insert, then continue typing your task.
            </div>
          ) : null}
          <div className="min-w-0 overflow-hidden border-y border-[var(--app-border-strong)] bg-transparent px-4 py-2 text-[11px]">
            <div className="hidden min-w-0 items-center justify-between gap-2 min-[1000px]:flex">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {showModePicker ? (
                  <>
                    <ModePicker mode={mode} onSelect={(nextMode) => onModeSelect?.(nextMode)} disabled={!onModeSelect || composerDisabled} />
                    <AgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} mode={mode} onSelect={(agent) => onAgentSelect?.(agent)} onOpenSettings={openAgentSetup} disabled={composerDisabled || agentModelControlBusy} />
                  </>
                ) : executionLabel ? (
                  <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-[var(--app-text-muted)]">
                    <span className="text-[var(--app-text-subtle)]">Execution:</span>
                    <span className="font-semibold uppercase tracking-wider text-[var(--app-primary)]">{executionLabel}</span>
                  </span>
                ) : null}
                {needsAuth ? (
                  <button type="button" onClick={onOpenAuthSettings} disabled={!onOpenAuthSettings} className="inline-flex shrink-0 items-center gap-1 rounded-full border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-2 py-1 text-[11px] font-semibold text-[var(--app-warning)] transition-all hover:-translate-y-0.5 hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:translate-y-0 disabled:hover:shadow-none" title="Open auth settings to add a provider credential">
                    <AlertTriangle size={13} className="shrink-0" />
                    Needs auth!
                  </button>
                ) : null}
                {compactButton(false)}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {dictationButton()}
                <Button size="sm" className="h-9 w-9 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={16} /> : busy ? <LoaderCircle size={16} className="animate-spin" /> : <Send size={17} />}
                </Button>
              </div>
            </div>
            <div className="flex w-full min-w-0 min-[1000px]:hidden">
              <div className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_48px_36px_36px] items-center gap-1.5 sm:grid-cols-[minmax(0,1fr)_56px_36px_36px] sm:gap-2">
                <div className="flex h-10 min-w-0 items-center overflow-hidden rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] px-1.5 shadow-sm">
                  {showModePicker ? (
                    <>
                      <ModePicker mode={mode} onSelect={(nextMode) => onModeSelect?.(nextMode)} disabled={!onModeSelect || composerDisabled} triggerClassName="h-full shrink-0 px-2" />
                      <AgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} mode={mode} onSelect={(agent) => onAgentSelect?.(agent)} onOpenSettings={openAgentSetup} disabled={composerDisabled || agentModelControlBusy} triggerClassName="w-full justify-between px-1.5 py-1.5" />
                    </>
                  ) : (
                    <span className="min-w-0 truncate px-2 text-[11px] font-semibold text-[var(--app-text)]">{executionLabel || (currentAgent === 'swarm' ? 'Swarm' : currentAgent)}</span>
                  )}
                </div>
                {compactButton(true)}
                {dictationButton()}
                <Button size="sm" className="h-9 w-9 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={16} /> : busy ? <LoaderCircle size={16} className="animate-spin" /> : <Send size={17} />}
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>
      <AgentModelControl
        currentAgent={currentAgent}
        selectedPrimaryAgent={selectedPrimaryAgent}
        agents={selectableAgents}
        mode={mode}
        selectedModel={selectedModel}
        selectedServiceTier={selectedServiceTier}
        selectedThinking={selectedThinking}
        modelOptions={modelOptions}
        modelLocked={modelPickerDisabled || Boolean(modelLockNotice.trim())}
        modelLockNotice={modelPickerDisabledReason || modelLockNotice}
        triggerDetail={modelControlDetail}
        openSignal={effectiveAgentSetupOpenSignal}
        initialAgentName={effectiveAgentSetupInitialAgent}
        onOpenAgentSettings={onOpenAgentSettings ? () => onOpenAgentSettings(agentSetupInitialAgent || currentAgent) : undefined}
        onConfirmAgentSettings={onConfirmAgentSettings}
        thinkingTagsEnabled={thinkingTagsEnabled}
        onThinkingTagsToggle={onThinkingTagsToggle}
        thinkingTagsBusy={thinkingTagsBusy}
        busy={agentModelControlBusy}
        showTrigger={false}
      />
    </div>
  )
}
