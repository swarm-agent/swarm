import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent, type KeyboardEvent } from 'react'
import { AlertTriangle, ArrowUp, FileImage, ListChecks, ListTodo, LoaderCircle, Mic, Minimize2, Square, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopV3MediaCapability, DesktopV3MediaReference } from '../../state/desktop-v3-cache-types'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand, type DesktopSlashPaletteState } from '../services/slash-commands'
import { submitDesktopComposer } from '../services/composer-submit'
import {
  chatMentionCandidates,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
} from '../services/subagent-mentions'
import { AgentModelControl, type AgentModelControlConfirmInput } from './agent-model-control'
import { ProfileAgentPicker } from './profile-agent-picker'
import { ComposerPlanModelControl } from './composer-plan-model-control'
import { DesktopMentionPanel } from './desktop-mention-panel'
import { DesktopSlashCommandPanel } from './desktop-slash-command-panel'
import { DesktopComposerActionMenu, type DesktopComposerTaskMode } from './desktop-composer-action-menu'

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
  onSubmit: (draft: string, attachments: DesktopV3MediaReference[]) => void | Promise<void>
  mediaCapability?: DesktopV3MediaCapability | null
  onUploadAttachment?: (file: File, signal: AbortSignal) => Promise<DesktopV3MediaReference>
  onStop?: () => void | Promise<void>
  mode: DesktopSessionMode
  onModeSelect?: (mode: DesktopSessionMode) => void
  showModePicker?: boolean
  executionLabel?: string
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  modelProfiles?: ModelProfileRecord[]
  activeModelProfile?: ActiveModelProfileState
  onModelProfileSelect?: (profileId: string) => void | Promise<void>
  onModelProfileSetDefault?: (profileId: string) => void | Promise<void>
  onModelProfileDelete?: (profileId: string) => void | Promise<void>
  onModelProfileReorder?: (profileIds: string[]) => void | Promise<void>
  modelProfilesLoading?: boolean
  modelProfilesError?: string | null
  onUseAgentModelDefault?: () => void | Promise<void>
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
  onCompact?: (draft: string) => void | Promise<void>
  compactDisabled?: boolean
  subagents?: string[]
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  onMentionSelect?: (agent: string) => void
  onDropTodo?: (event: ReactDragEvent<HTMLTextAreaElement>) => void
}

export const DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME = "mx-auto grid w-full min-w-0 max-w-[70rem] gap-3 px-4 pb-[calc(0.75rem+var(--app-safe-area-bottom))] pt-4 sm:px-6 sm:pb-[calc(1.25rem+var(--app-safe-area-bottom))] sm:pt-5";

export function DesktopV3CompactButton({
  contextLabel,
  contextTooltip,
  disabled = false,
  onClick,
}: {
  contextLabel: string
  contextTooltip?: string
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={contextTooltip || 'Compact conversation'}
      aria-label={contextTooltip || 'Compact conversation'}
      className="inline-flex min-h-7 min-w-0 shrink-0 items-center gap-1 rounded-lg border-0 bg-transparent px-2 text-[11px] font-medium tabular-nums text-[var(--app-text)] transition-all hover:-translate-y-0.5 hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none"
    >
      <span>{contextLabel}</span>
      <Minimize2 size={12} className="text-[var(--app-text-subtle)]" />
    </button>
  )
}

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
  mediaCapability = null,
  onUploadAttachment,
  onStop,
  mode,
  onModeSelect,
  showModePicker = true,
  executionLabel,
  currentAgent,
  selectedPrimaryAgent,
  agents,
  modelProfiles = [],
  activeModelProfile,
  onModelProfileSelect,
  onModelProfileSetDefault,
  onModelProfileDelete,
  onModelProfileReorder,
  modelProfilesLoading = false,
  modelProfilesError = null,
  onUseAgentModelDefault: _onUseAgentModelDefault,
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
  onCompact,
  compactDisabled = false,
  subagents = [],
  onSlashCommand,
  onMentionSelect,
  onDropTodo,
}: DesktopV3AgenticComposerProps) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const uploadAbortRef = useRef<AbortController | null>(null)
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
  const [dismissedComposerError, setDismissedComposerError] = useState<string | null>(null)
  const [slashSelectionIndex, setSlashSelectionIndex] = useState(0)
  const [mentionSelectionIndex, setMentionSelectionIndex] = useState(0)
  const [agentSetupOpenSignal, setAgentSetupOpenSignal] = useState(0)
  const [agentSetupInitialAgent, setAgentSetupInitialAgent] = useState('')
  const [agentSetupProfileId, setAgentSetupProfileId] = useState<string | null | undefined>(undefined)
  const [createProfileSignal, setCreateProfileSignal] = useState(0)
  const [primedTaskMode, setPrimedTaskMode] = useState<DesktopComposerTaskMode | null>(null)
  const [attachments, setAttachments] = useState<DesktopV3MediaReference[]>([])
  const [uploadingAttachment, setUploadingAttachment] = useState(false)
  const [attachmentError, setAttachmentError] = useState<string | null>(null)

  const effectiveMediaCapability = mediaCapability?.status === 'available' && mediaCapability.contract_token && mediaCapability.capabilities.length > 0
    ? mediaCapability
    : null
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
    setAgentSetupProfileId(undefined)
    setAgentSetupInitialAgent(agent?.trim() || currentAgent)
    setAgentSetupOpenSignal((current) => current + 1)
  }, [currentAgent])
  const addModelProfile = useCallback(() => {
    setAgentSetupProfileId('')
    setAgentSetupInitialAgent(currentAgent)
    setCreateProfileSignal((current) => current + 1)
  }, [currentAgent])
  const editModelProfile = useCallback((profileId: string) => {
    setAgentSetupProfileId(profileId)
    setAgentSetupInitialAgent(currentAgent)
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
    setPrimedTaskMode(null)
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
    setAttachments([])
    setAttachmentError(null)
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
    if (!error) setDismissedComposerError(null)
  }, [error])

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

  const handlePrimeTask = useCallback((taskMode: DesktopComposerTaskMode) => {
    if (dictationEnabledRef.current) stopDictation(false)
    setPrimedTaskMode(taskMode)
    if (typeof window === 'undefined') return
    window.requestAnimationFrame(() => {
      const textarea = textareaRef.current
      if (!textarea) return
      textarea.focus()
      const cursorPosition = textarea.value.length
      textarea.setSelectionRange(cursorPosition, cursorPosition)
      resizeTextareaElement(textarea)
    })
  }, [resizeTextareaElement, stopDictation])

  const handleSubmitClick = useCallback(async () => {
    const visibleDraft = textareaRef.current?.value ?? dictationComposer
    const submittedDraft = primedTaskMode === 'plan'
      ? `/task plan ${visibleDraft}`
      : primedTaskMode === 'action'
        ? `/task ${visibleDraft}`
        : visibleDraft
    await submitDesktopComposer({
      draft: submittedDraft,
      canStop,
      clear: clearComposerForSubmit,
      attachments,
      onSubmit,
      onStop,
      onSlashCommand,
    })
  }, [attachments, canStop, clearComposerForSubmit, dictationComposer, onSlashCommand, onStop, onSubmit, primedTaskMode])

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
    if (command.action.kind === 'queue-ai-task') {
      void Promise.resolve(onSlashCommand?.(command, draft))
        .then(() => onDraftChange(''))
        .catch(() => {
          // The owning pane surfaces the error and the task request stays editable.
        })
      return
    }
    void onSlashCommand?.(command, draft)
    if (command.action.kind === 'open-model-picker') {
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
      if (event.key === 'Enter' && !event.shiftKey && (!slashPalette.hasArguments || slashPalette.exactMatch?.action.kind === 'queue-ai-task')) {
        event.preventDefault()
        if (slashPalette.exactMatch?.action.kind === 'queue-ai-task') {
          void handleSubmitClick()
          return
        }
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

  const handleAttachmentFiles = useCallback(async (files: File[]) => {
    if (files.length === 0) return
    if (!effectiveMediaCapability || !onUploadAttachment) {
      const reasons = mediaCapability?.denial_reasons?.filter(Boolean) ?? []
      setAttachmentError(reasons.length > 0
        ? `Attachments are unavailable: ${reasons.join('; ')}.`
        : 'Attachments are unavailable for the current model, credential, agent, or session mode.')
      return
    }
    const maxCount = Math.min(...effectiveMediaCapability.capabilities.map((capability) => capability.max_count || 1))
    if (attachments.length + files.length > maxCount) {
      setAttachmentError(`This model allows at most ${maxCount} attachments per message.`)
      return
    }
    setAttachmentError(null)
    setUploadingAttachment(true)
    const controller = new AbortController()
    uploadAbortRef.current = controller
    try {
      const uploaded: DesktopV3MediaReference[] = []
      for (const file of files) uploaded.push(await onUploadAttachment(file, controller.signal))
      setAttachments((current) => [...current, ...uploaded])
    } catch (error) {
      setAttachmentError(error instanceof Error ? error.message : 'Attachment upload failed.')
    } finally {
      if (uploadAbortRef.current === controller) uploadAbortRef.current = null
      setUploadingAttachment(false)
    }
  }, [attachments.length, effectiveMediaCapability, onUploadAttachment])

  useEffect(() => {
    if (!effectiveMediaCapability && attachments.length > 0) {
      setAttachmentError('Attachment capability changed. Remove the existing attachments or restore the supported model and credential.')
    }
  }, [attachments.length, effectiveMediaCapability])

  const handleDragOver = useCallback((event: ReactDragEvent<HTMLTextAreaElement>) => {
    const hasTodo = Array.from(event.dataTransfer.types).includes(TODO_DRAG_MIME) || Array.from(event.dataTransfer.types).includes('text/plain')
    const hasFiles = Boolean(effectiveMediaCapability) && Array.from(event.dataTransfer.types).includes('Files')
    if (!hasTodo && !hasFiles) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [effectiveMediaCapability])

  const dictationButton = () => showDictationButton ? (
    <button
      type="button"
      onClick={handleDictationToggle}
      disabled={dictationButtonDisabled}
      aria-pressed={dictationEnabled}
      aria-label={dictationEnabled ? 'Stop microphone dictation' : 'Start microphone dictation'}
      title={dictationSupported ? (dictationEnabled ? 'Stop dictation' : 'Start dictation') : 'Speech recognition is not available in this browser'}
      className={dictationEnabled
        ? 'inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border-0 bg-transparent text-[var(--app-primary)] transition-colors hover:text-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50'
        : 'inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border-0 bg-transparent text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50'}
    >
      <Mic size={15} className={dictationListening ? 'animate-pulse' : undefined} />
    </button>
  ) : null

  const planToggle = () => onModeSelect?.(mode === 'plan' ? 'auto' : 'plan')
  const renderComposerControl = (openPicker: () => void, open: boolean) => <ComposerPlanModelControl
    mode={mode}
    provider={selectedModel?.provider}
    model={selectedModel?.model}
    thinking={selectedThinking}
    serviceTier={selectedServiceTier}
    planDisabled={composerDisabled || agentModelControlBusy || !onModeSelect}
    pickerDisabled={composerDisabled || agentModelControlBusy}
    open={open}
    onPlanToggle={planToggle}
    onPickerOpen={openPicker}
  />

  const visibleComposerError = error && error !== dismissedComposerError ? error : null
  const dismissDictationWarning = () => {
    setDictationError(null)
    if (dictationEnabledRef.current) stopDictation(false)
  }

  const taskModeIndicator = () => primedTaskMode ? (
    <div
      className="inline-flex min-w-0 items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] py-1 pl-2.5 pr-1 text-[11px] text-[var(--app-text)] shadow-sm"
      data-testid="desktop-composer-task-mode-indicator"
    >
      {primedTaskMode === 'plan'
        ? <ListChecks size={14} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
        : <ListTodo size={14} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />}
      <span className="truncate font-semibold">
        {primedTaskMode === 'plan' ? 'Background planning task' : 'Background action task'}
      </span>
      <button
        type="button"
        onClick={() => setPrimedTaskMode(null)}
        disabled={composerDisabled}
        aria-label="Clear background task mode"
        title="Clear background task mode"
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50"
      >
        <X size={13} aria-hidden="true" />
      </button>
    </div>
  ) : null

  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="desktop-v3-agentic-composer">
      <div className={DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME}>
        {visibleComposerError ? (
          <div className="flex min-w-0 items-center gap-2 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] py-1 pl-3 pr-1 text-sm text-[var(--app-danger)]" role="alert">
            <span className="min-w-0 flex-1">{visibleComposerError}</span>
            <button
              type="button"
              onClick={() => setDismissedComposerError(visibleComposerError)}
              aria-label="Dismiss composer error"
              title="Dismiss error"
              className="grid min-h-11 min-w-11 shrink-0 touch-manipulation place-items-center rounded-lg transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-danger)]"
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
        ) : null}
        {dictationError ? (
          <div className="flex min-w-0 items-center gap-2 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] py-1 pl-3 pr-1 text-sm text-[var(--app-warning)]" role="alert">
            <span className="min-w-0 flex-1">{dictationError}</span>
            <button
              type="button"
              onClick={dismissDictationWarning}
              aria-label="Dismiss dictation warning"
              title="Dismiss warning"
              className="grid min-h-11 min-w-11 shrink-0 touch-manipulation place-items-center rounded-lg transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-warning)]"
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
        ) : null}
        {mentionPaletteIsActive ? (
          <DesktopMentionPanel matches={mentionPaletteMatches} selectedIndex={mentionSelectionIndex} onHover={setMentionSelectionIndex} onSelect={handleMentionInsert} />
        ) : slashPalette.active ? (
          <DesktopSlashCommandPanel palette={slashPalette as DesktopSlashPaletteState} selectedIndex={slashSelectionIndex} onHover={setSlashSelectionIndex} onSelect={handleSlashSelect} />
        ) : null}
        <div className="relative min-w-0 overflow-visible rounded-2xl border border-[var(--app-border)]/40 bg-[var(--app-bg-alt)] shadow-[0_4px_16px_rgba(0,0,0,0.04)] transition-all duration-300 ease-out focus-within:border-transparent focus-within:ring-2 focus-within:ring-[var(--app-border-accent)]/60 focus-within:shadow-[0_8px_24px_rgba(0,0,0,0.06),0_0_12px_rgba(59,130,246,0.1)]">
          <div className="flex min-w-0 items-end gap-3 px-4 py-2 sm:py-3 lg:py-2.5" data-composer-input-row>
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
                onDrop={(event) => {
                  if (effectiveMediaCapability && event.dataTransfer.files.length > 0) {
                    event.preventDefault()
                    void handleAttachmentFiles(Array.from(event.dataTransfer.files))
                    return
                  }
                  onDropTodo?.(event)
                }}
                onPaste={(event) => {
                  if (!effectiveMediaCapability) return
                  const files = Array.from(event.clipboardData.files)
                  if (files.length > 0) {
                    event.preventDefault()
                    void handleAttachmentFiles(files)
                  }
                }}
                placeholder={placeholder}
                aria-label={inputLabel}
                className="max-h-[50vh] !min-h-[32px] resize-none overflow-y-hidden !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]"
                rows={1}
                disabled={composerDisabled}
              />
            </div>
          </div>
          {attachments.length > 0 ? (
            <div className="flex flex-wrap gap-2 border-t border-[var(--app-border)] px-4 py-2" data-testid="desktop-media-attachments">
              {attachments.map((attachment, index) => (
                <span key={`${attachment.asset_id}:${index}`} className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]">
                  <FileImage size={13} aria-hidden="true" />
                  <span>{attachment.file_type?.toUpperCase() || attachment.mime_type}</span>
                  <span className="text-[var(--app-text-muted)]">{Math.ceil(attachment.size / 1024)} KB</span>
                  <button type="button" aria-label="Remove attachment" onClick={() => setAttachments((current) => current.filter((_, itemIndex) => itemIndex !== index))}><X size={13} /></button>
                </span>
              ))}
            </div>
          ) : null}
          {attachmentError ? <div className="border-t border-[var(--app-warning-border)] px-4 py-2 text-xs text-[var(--app-warning)]" role="alert">{attachmentError}</div> : null}
          {mentionPaletteIsActive ? (
            <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2 text-[11px] text-[var(--app-text-muted)]">
              Use ↑/↓ to choose a subagent, Tab or Enter to insert, then continue typing your task.
            </div>
          ) : null}
          <div className="flex min-w-0 items-center gap-2 overflow-visible bg-transparent px-4 py-3 text-[11px]" data-composer-bottom-row>
            {effectiveMediaCapability ? (
              <input ref={fileInputRef} type="file" hidden multiple accept={effectiveMediaCapability.capabilities.flatMap((capability) => capability.mime_types ?? []).join(',')} onChange={(event) => { void handleAttachmentFiles(Array.from(event.target.files ?? [])); event.target.value = '' }} />
            ) : null}
            <DesktopComposerActionMenu
              disabled={composerDisabled}
              onPrimeTask={handlePrimeTask}
              onAttach={effectiveMediaCapability ? () => fileInputRef.current?.click() : undefined}
              attachDisabled={composerDisabled || uploadingAttachment}
              attaching={uploadingAttachment}
              contextLabel={contextLabel}
              contextTooltip={contextTooltip}
              onCompact={onCompact ? () => { void onCompact(draft) } : undefined}
              compactDisabled={compactDisabled || !onCompact}
            />
            {uploadingAttachment ? <button type="button" className="text-xs text-[var(--app-warning)]" onClick={() => uploadAbortRef.current?.abort()}>Cancel upload</button> : null}
            <div className="hidden min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:flex">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : showModePicker ? (
                  <ProfileAgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} profiles={modelProfiles} activeProfile={activeModelProfile} mode={mode} loading={modelProfilesLoading} error={modelProfilesError} busy={agentModelControlBusy} disabled={composerDisabled || agentModelControlBusy} modelDetail={modelControlDetail} renderTrigger={({ openPicker, open }) => renderComposerControl(openPicker, open)} onAgentSelect={onAgentSelect} onProfileSelect={onModelProfileSelect} onAddProfile={addModelProfile} onOpenAgentSetup={openAgentSetup} onEditProfile={editModelProfile} onReorderProfiles={onModelProfileReorder} onSetDefault={async (profileId) => { if (!onModelProfileSetDefault) throw new Error('Default profile management is unavailable'); await onModelProfileSetDefault(profileId) }} onDeleteProfile={async (profileId) => { if (!onModelProfileDelete) throw new Error('Profile deletion is unavailable'); await onModelProfileDelete(profileId) }} />
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
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {dictationButton()}
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <ArrowUp size={22} strokeWidth={2.25} className="shrink-0" />}
                </Button>
              </div>
            </div>
            <div className="flex min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:hidden">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : showModePicker ? (
                  <ProfileAgentPicker currentAgent={currentAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} profiles={modelProfiles} activeProfile={activeModelProfile} mode={mode} loading={modelProfilesLoading} error={modelProfilesError} busy={agentModelControlBusy} disabled={composerDisabled || agentModelControlBusy} compact modelDetail={modelControlDetail} renderTrigger={({ openPicker, open }) => renderComposerControl(openPicker, open)} onAgentSelect={onAgentSelect} onProfileSelect={onModelProfileSelect} onAddProfile={addModelProfile} onOpenAgentSetup={openAgentSetup} onEditProfile={editModelProfile} onReorderProfiles={onModelProfileReorder} onSetDefault={async (profileId) => { if (!onModelProfileSetDefault) throw new Error('Default profile management is unavailable'); await onModelProfileSetDefault(profileId) }} onDeleteProfile={async (profileId) => { if (!onModelProfileDelete) throw new Error('Profile deletion is unavailable'); await onModelProfileDelete(profileId) }} />
                ) : (
                  <span className="min-w-0 truncate font-medium text-[var(--app-text-muted)]">{executionLabel || (currentAgent === 'swarm' ? 'Swarm' : currentAgent)}</span>
                )}

              </div>
              <div className="flex shrink-0 items-center gap-2">
                {dictationButton()}
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (!canSubmit || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <ArrowUp size={22} strokeWidth={2.25} className="shrink-0" />}
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
        thinkingTagsEnabled={thinkingTagsEnabled}
        onThinkingTagsToggle={onThinkingTagsToggle}
        thinkingTagsBusy={thinkingTagsBusy}
        modelLocked={modelPickerDisabled || Boolean(modelLockNotice.trim())}
        modelLockNotice={modelPickerDisabledReason || modelLockNotice}
        triggerDetail={modelControlDetail}
        openSignal={effectiveAgentSetupOpenSignal}
        initialAgentName={effectiveAgentSetupInitialAgent}
        onOpenAgentSettings={onOpenAgentSettings ? () => onOpenAgentSettings(agentSetupInitialAgent || currentAgent) : undefined}
        onConfirmAgentSettings={onConfirmAgentSettings}
        onSetDefaultModelProfile={onModelProfileSetDefault}
        modelProfiles={modelProfiles}
        activeModelProfile={activeModelProfile}
        initialModelProfileId={agentSetupProfileId}
        createModelProfileSignal={createProfileSignal}
        busy={agentModelControlBusy}
        showTrigger={false}
      />
    </div>
  )
}
