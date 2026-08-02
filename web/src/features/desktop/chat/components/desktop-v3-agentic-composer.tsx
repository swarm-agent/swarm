import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, ArrowUp, FileCode2, FileImage, ListChecks, ListTodo, LoaderCircle, Mic, Minimize2, Sparkles, Square, UploadCloud, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopV3MediaCapability, DesktopV3MediaReference } from '../../state/desktop-v3-cache-types'
import type { DesktopV3RoutedComposerSnapshot, DesktopV3RoutedNewSessionState } from '../../session-v3/new-session-flow'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand, type DesktopSlashPaletteState } from '../services/slash-commands'
import { desktopComposerBackgroundRouterCommand, submitDesktopComposer } from '../services/composer-submit'
import {
  DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT,
  DESKTOP_COMPOSER_TEXT_TOTAL_MAX_BYTES,
  admitComposerFile,
  appendComposerTextFile,
  composerFileType,
  desktopComposerStagedMediaInput,
  isComposerTextFile,
  type DesktopComposerStagedAttachment,
} from '../services/composer-attachments'
import {
  chatMentionCandidates,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
} from '../services/subagent-mentions'
import { AgentModelControl, type AgentModelControlConfirmInput } from './agent-model-control'
import { ComposerPlanModelControl } from './composer-plan-model-control'
import { DesktopMentionPanel } from './desktop-mention-panel'
import { DesktopSlashCommandPanel } from './desktop-slash-command-panel'
import { DesktopComposerActionMenu, type DesktopComposerTaskMode } from './desktop-composer-action-menu'
import { DesktopWorkspaceActionPanel } from './desktop-workspace-action-panel'
import type { WorkspaceAction } from '../../../workspaces/actions/types'
import type { WorkspaceSkill } from '../services/workspace-skills'
import { DesktopRoutedWorktreePrime } from './desktop-routed-worktree-prime'
import { DesktopComposerPlanToggle } from './desktop-composer-plan-toggle'

const DICTATION_RESTART_DELAY_MS = 180
const DICTATION_FINAL_FLUSH_MS = 450
const TODO_DRAG_MIME = 'application/x-swarm-workspace-todo'

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike

type DesktopComposerTextAttachment = {
  id: number
  name: string
  fileType?: string
  size: number
  content: string
}

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
  onRoutedSubmit?: (snapshot: DesktopV3RoutedComposerSnapshot) => Promise<DesktopV3RoutedNewSessionState>
  routedStagedAttachments?: readonly DesktopComposerStagedAttachment[]
  onRoutedStageAttachments?: (files: File[], signal: AbortSignal) => Promise<void>
  onRoutedRemoveStagedAttachment?: (stagingId: string) => void
  routedComposerSnapshot?: DesktopV3RoutedComposerSnapshot | null
  routedWorktreeRequested?: boolean
  onRoutedWorktreeRequestedChange?: (requested: boolean) => void
  modelStatusLabel?: string
  mediaCapability?: DesktopV3MediaCapability | null
  onUploadAttachment?: (file: File, signal: AbortSignal) => Promise<DesktopV3MediaReference>
  onStop?: () => void | Promise<void>
  mode?: DesktopSessionMode
  onModeSelect?: (mode: DesktopSessionMode) => void
  showModePicker?: boolean
  resolvedSessionControls?: boolean
  executionLabel?: string
  currentAgent?: string
  selectedPrimaryAgent?: string
  agents?: AgentProfileRecord[]
  modelProfiles?: ModelProfileRecord[]
  activeModelProfile?: ActiveModelProfileState
  onUseAgentModelDefault?: () => void | Promise<void>
  modelOptions?: ModelOptionRecord[]
  selectedModelKey?: string
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
  thinking?: string
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
  focusSignal?: number
  workspacePath?: string
  /** Pre-route composer state; agent/model controls remain visible before the first send. */
  routedNewSession?: boolean
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
  onRoutedSubmit,
  routedStagedAttachments = [],
  onRoutedStageAttachments,
  onRoutedRemoveStagedAttachment,
  routedComposerSnapshot = null,
  routedWorktreeRequested = false,
  onRoutedWorktreeRequestedChange,
  modelStatusLabel = '',
  mediaCapability = null,
  onUploadAttachment,
  onStop,
  mode = 'auto',
  onModeSelect,
  showModePicker = true,
  resolvedSessionControls = false,
  executionLabel,
  currentAgent = '',
  selectedPrimaryAgent = '',
  agents = [],
  modelProfiles = [],
  activeModelProfile,
  onUseAgentModelDefault: _onUseAgentModelDefault,
  modelOptions = [],
  selectedModelKey = '',
  selectedServiceTier = '',
  agentSettingsOpenSignal = 0,
  agentSettingsInitialAgent = '',
  modelPickerDisabled = false,
  modelPickerDisabledReason = '',
  modelLockNotice = '',
  modelControlDetail = '',
  onOpenAgentSettings,
  onAgentSelect: _onAgentSelect,
  needsAuth = false,
  onOpenAuthSettings,
  onConfirmAgentSettings,
  agentModelControlBusy = false,
  thinking = '',
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
  focusSignal = 0,
  workspacePath = '',
  routedNewSession = false,
}: DesktopV3AgenticComposerProps) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const composerRootRef = useRef<HTMLDivElement | null>(null)
  const fileDragDepthRef = useRef(0)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const uploadAbortRef = useRef<AbortController | null>(null)
  const textAttachmentSequenceRef = useRef(0)
  const routedSubmissionRef = useRef(false)
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
  const [primedTaskMode, setPrimedTaskMode] = useState<DesktopComposerTaskMode | null>(null)
  const [attachments, setAttachments] = useState<DesktopV3MediaReference[]>([])
  const [textAttachments, setTextAttachments] = useState<DesktopComposerTextAttachment[]>([])
  const [uploadingAttachment, setUploadingAttachment] = useState(false)
  const [attachmentError, setAttachmentError] = useState<string | null>(null)
  const [fileDropZone, setFileDropZone] = useState<HTMLElement | null>(null)
  const [filesDraggingOverChat, setFilesDraggingOverChat] = useState(false)
  const [selectedWorkspaceAction, setSelectedWorkspaceAction] = useState<WorkspaceAction | null>(null)
  const [selectedWorkspaceSkill, setSelectedWorkspaceSkill] = useState<WorkspaceSkill | null>(null)

  const effectiveMediaCapability = mediaCapability?.status === 'available' && mediaCapability.capabilities.length > 0
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
  const slashCommands = useMemo(
    () => slashPalette.matches.filter((command) => command.state === 'ready' && (command.action.kind !== 'prime-worktree' || routedNewSession)),
    [routedNewSession, slashPalette.matches],
  )
  const mentionPaletteIsActive = useMemo(() => mentionPaletteActive(draft, mentionSubagents), [draft, mentionSubagents])
  const mentionPaletteMatches = useMemo(() => chatMentionCandidates(mentionPaletteQuery(draft), mentionSubagents), [draft, mentionSubagents])
  const selectedModel = useMemo(() => modelOptions.find((option) => option.key === selectedModelKey) ?? null, [modelOptions, selectedModelKey])
  const selectedThinking = thinking.trim() || 'off'
  void _onAgentSelect
  const openAgentSetup = useCallback(() => {
    setAgentSetupOpenSignal((current) => current + 1)
  }, [])
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
    setTextAttachments([])
    setSelectedWorkspaceSkill(null)
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
    if (focusSignal <= 0 || composerDisabled || typeof window === 'undefined') return
    const frame = window.requestAnimationFrame(() => {
      const textarea = textareaRef.current
      if (!textarea) return
      textarea.focus()
      const cursorPosition = textarea.value.length
      textarea.setSelectionRange(cursorPosition, cursorPosition)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [composerDisabled, focusSignal])

  useEffect(() => {
    if (!error) setDismissedComposerError(null)
    if (routedNewSession && error) routedSubmissionRef.current = false
  }, [error, routedNewSession])

  useEffect(() => {
    if (!routedNewSession || !routedComposerSnapshot) return
    routedSubmissionRef.current = false
    setSelectedWorkspaceAction((routedComposerSnapshot.selectedAction as WorkspaceAction | null) ?? null)
    setSelectedWorkspaceSkill((routedComposerSnapshot.selectedSkill as WorkspaceSkill | null) ?? null)
  }, [routedComposerSnapshot, routedNewSession])

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
    if (routedNewSession) {
      onRoutedWorktreeRequestedChange?.(true)
      if (typeof window !== 'undefined') {
        window.requestAnimationFrame(() => textareaRef.current?.focus())
      }
      return
    }
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
  }, [onRoutedWorktreeRequestedChange, resizeTextareaElement, routedNewSession, stopDictation])

  const handleSubmitClick = useCallback(async () => {
    if (uploadingAttachment) {
      setAttachmentError('Wait for all attachments to finish uploading before sending the message.')
      return
    }
    if (routedNewSession && routedSubmissionRef.current) return
    const rawDraft = textareaRef.current?.value ?? dictationComposer
    const textAttachmentDraft = textAttachments.reduce(
      (nextDraft, attachment) => appendComposerTextFile(nextDraft, attachment.name, attachment.fileType, attachment.content),
      rawDraft,
    )
    const attachmentDraft = textAttachmentDraft.trim() || (attachments.length > 0 || routedStagedAttachments.length > 0 ? 'Please review the attached file(s).' : textAttachmentDraft)
    const skillInstruction = selectedWorkspaceSkill
      ? `Use the skill-use tool to load "${selectedWorkspaceSkill.canonicalName}" before executing this request.`
      : ''
    const visibleDraft = skillInstruction
      ? `${skillInstruction}${attachmentDraft.trim() ? `\n\n${attachmentDraft}` : ''}`
      : attachmentDraft
    const submittedDraft = primedTaskMode === 'plan'
      ? `/task plan ${visibleDraft}`
      : primedTaskMode === 'action'
        ? `/task ${visibleDraft}`
        : visibleDraft
    const submittedBackgroundRouterCommand = desktopComposerBackgroundRouterCommand(submittedDraft)
    if (routedNewSession && submittedBackgroundRouterCommand) {
      void submitDesktopComposer({
        draft: submittedDraft,
        canStop,
        clear: clearComposerForSubmit,
        attachments,
        onSubmit,
        onStop,
        onSlashCommand,
      })
      return
    }
    if (routedNewSession && onRoutedSubmit) {
      routedSubmissionRef.current = true
      const routedSnapshot = {
        prompt: submittedDraft,
        attachments: desktopComposerStagedMediaInput(routedStagedAttachments),
        selectedAction: selectedWorkspaceAction,
        selectedSkill: selectedWorkspaceSkill,
        worktreePrimed: routedWorktreeRequested,
        planModeRequested: mode === 'plan',
      }
      let routedSubmit: Promise<DesktopV3RoutedNewSessionState>
      try {
        routedSubmit = onRoutedSubmit(routedSnapshot)
      } catch (cause) {
        routedSubmissionRef.current = false
        setAttachmentError(cause instanceof Error ? cause.message : 'Routed session start failed.')
        return
      }
      clearComposerForSubmit()
      setSelectedWorkspaceAction(null)
      void routedSubmit.then((state) => {
        if (state.phase === 'failed') routedSubmissionRef.current = false
      }).catch((cause) => {
        routedSubmissionRef.current = false
        setAttachmentError(cause instanceof Error ? cause.message : 'Routed session start failed.')
      })
      return
    }
    void submitDesktopComposer({
      draft: submittedDraft,
      canStop,
      clear: clearComposerForSubmit,
      attachments,
      onSubmit,
      onStop,
      onSlashCommand,
    })
  }, [attachments, canStop, clearComposerForSubmit, dictationComposer, mode, onRoutedSubmit, onSlashCommand, onStop, onSubmit, primedTaskMode, routedNewSession, routedStagedAttachments, routedWorktreeRequested, selectedWorkspaceAction, selectedWorkspaceSkill, textAttachments, uploadingAttachment])

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
    if (command.action.kind === 'start-background-router-session') {
      void handleSubmitClick()
      return
    }
    if (command.action.kind === 'toggle-thinking') {
      if (thinkingTagsEnabled !== undefined && onThinkingTagsToggle && !thinkingTagsBusy) {
        onThinkingTagsToggle(!thinkingTagsEnabled)
      }
      onDraftChange('')
      return
    }
    if (command.action.kind === 'prime-worktree') {
      if (routedNewSession) onRoutedWorktreeRequestedChange?.(true)
      if (!slashPalette.hasArguments) onDraftChange('')
      return
    }
    void onSlashCommand?.(command, draft)
    if (command.action.kind === 'open-model-picker') {
      if (!routedNewSession) openAgentSetup()
      onDraftChange('')
      return
    }
    if (command.action.kind === 'compact-session') {
      void onCompact?.(draft)
      onDraftChange('')
      return
    }
    if (!slashPalette.hasArguments) onDraftChange('')
  }, [currentAgent, draft, handleSubmitClick, onCompact, onDraftChange, onSlashCommand, onThinkingTagsToggle, openAgentSetup, routedNewSession, slashPalette.hasArguments, thinkingTagsBusy, thinkingTagsEnabled])

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
      if (event.key === 'Enter' && !event.shiftKey && (!slashPalette.hasArguments || slashPalette.exactMatch?.action.kind === 'start-background-router-session')) {
        event.preventDefault()
        if (slashPalette.exactMatch?.action.kind === 'start-background-router-session') {
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
      if (canSubmit || attachments.length > 0 || textAttachments.length > 0 || selectedWorkspaceSkill || canStop) handleSubmitClick()
    }
  }, [attachments.length, canStop, canSubmit, handleMentionInsert, handleSlashSelect, handleSubmitClick, mentionPaletteIsActive, mentionPaletteMatches, mentionSelectionIndex, onDraftChange, selectedWorkspaceSkill, slashCommands, slashPalette.active, slashPalette.hasArguments, slashSelectionIndex, textAttachments.length])

  const handleAttachmentFiles = useCallback(async (files: File[]) => {
    if (files.length === 0) return
    if (uploadingAttachment) {
      setAttachmentError('Wait for the current attachment batch to finish before adding more files.')
      return
    }
    if (routedNewSession) {
      const routedTextFiles = files.filter(isComposerTextFile)
      const routedMediaFiles = files.filter((file) => !isComposerTextFile(file))
      if (textAttachments.length + routedTextFiles.length > DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT) {
        setAttachmentError(`Add at most ${DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT} text or code files per message.`)
        return
      }
      const existingTextBytes = textAttachments.reduce((total, item) => total + item.size, 0)
      const routedTextBytes = routedTextFiles.reduce((total, file) => total + file.size, 0)
      const draftBytes = new TextEncoder().encode(draft).byteLength
      if (draftBytes + existingTextBytes + routedTextBytes > DESKTOP_COMPOSER_TEXT_TOTAL_MAX_BYTES) {
        setAttachmentError('The message and selected text/code files exceed the 4 MB combined limit.')
        return
      }
      if (routedMediaFiles.length > 0 && !onRoutedStageAttachments) {
        setAttachmentError('Pre-session attachment staging is unavailable.')
        return
      }
      setAttachmentError(null)
      setUploadingAttachment(true)
      const controller = new AbortController()
      uploadAbortRef.current = controller
      try {
        const pendingTextAttachments = await Promise.all(routedTextFiles.map(async (file) => {
          textAttachmentSequenceRef.current += 1
          return {
            id: textAttachmentSequenceRef.current,
            name: file.name.trim() || 'attachment.txt',
            fileType: composerFileType(file),
            size: file.size,
            content: await file.text(),
          } satisfies DesktopComposerTextAttachment
        }))
        if (routedMediaFiles.length > 0) {
          await onRoutedStageAttachments!(routedMediaFiles, controller.signal)
        }
        if (pendingTextAttachments.length > 0) {
          setTextAttachments((current) => [...current, ...pendingTextAttachments])
        }
      } catch (error) {
        setAttachmentError(error instanceof Error ? error.message : 'Attachment staging failed.')
      } finally {
        if (uploadAbortRef.current === controller) uploadAbortRef.current = null
        setUploadingAttachment(false)
      }
      return
    }
    const admissions = files.map((file) => ({ file, admission: admitComposerFile(file, effectiveMediaCapability) }))
    const rejected = admissions.find((item) => item.admission.kind === 'rejected')
    if (rejected?.admission.kind === 'rejected') {
      setAttachmentError(rejected.admission.reason)
      return
    }
    const mediaFiles = admissions.filter((item) => item.admission.kind === 'media')
    const textFiles = admissions.filter((item) => item.admission.kind === 'text')
    if (textAttachments.length + textFiles.length > DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT) {
      setAttachmentError(`Add at most ${DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT} text or code files per message.`)
      return
    }
    const existingTextBytes = textAttachments.reduce((total, item) => total + item.size, 0)
    const textBytes = textFiles.reduce((total, item) => total + item.file.size, 0)
    const draftBytes = new TextEncoder().encode(draft).byteLength
    if (draftBytes + existingTextBytes + textBytes > DESKTOP_COMPOSER_TEXT_TOTAL_MAX_BYTES) {
      setAttachmentError('The message and selected text/code files exceed the 4 MB combined limit.')
      return
    }
    if (mediaFiles.length > 0 && !onUploadAttachment) {
      const reasons = mediaCapability?.denial_reasons?.filter(Boolean) ?? []
      setAttachmentError(reasons.length > 0
        ? `Attachments are unavailable: ${reasons.join('; ')}.`
        : 'Media uploads are unavailable for the current model, credential, agent, or session mode.')
      return
    }
    const maxCount = effectiveMediaCapability?.capabilities.length
      ? Math.min(...effectiveMediaCapability.capabilities.map((capability) => capability.max_count || 1))
      : 0
    if (mediaFiles.length > 0 && attachments.length + mediaFiles.length > maxCount) {
      setAttachmentError(`This model allows at most ${maxCount} media attachments per message.`)
      return
    }
    setAttachmentError(null)
    setUploadingAttachment(true)
    const controller = new AbortController()
    uploadAbortRef.current = controller
    try {
      for (const item of textFiles) {
        if (item.admission.kind !== 'text') continue
        const content = await item.file.text()
        textAttachmentSequenceRef.current += 1
        const pendingAttachment: DesktopComposerTextAttachment = {
          id: textAttachmentSequenceRef.current,
          name: item.file.name.trim() || 'attachment.txt',
          fileType: item.admission.fileType,
          size: item.file.size,
          content,
        }
        setTextAttachments((current) => [...current, pendingAttachment])
      }
      for (const item of mediaFiles) {
        const uploaded = await onUploadAttachment!(item.file, controller.signal)
        setAttachments((current) => [...current, uploaded])
      }
    } catch (error) {
      setAttachmentError(error instanceof Error ? error.message : 'Attachment upload failed.')
    } finally {
      if (uploadAbortRef.current === controller) uploadAbortRef.current = null
      setUploadingAttachment(false)
    }
  }, [attachments.length, draft, effectiveMediaCapability, mediaCapability?.denial_reasons, onRoutedStageAttachments, onUploadAttachment, routedNewSession, textAttachments, uploadingAttachment])

  useEffect(() => {
    const dropZone = composerRootRef.current?.closest<HTMLElement>('[data-desktop-chat-drop-zone]') ?? null
    setFileDropZone(dropZone)
    if (!dropZone) return

    const hasFiles = (event: globalThis.DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes('Files')
    const handleDragEnter = (event: globalThis.DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      fileDragDepthRef.current += 1
      setFilesDraggingOverChat(true)
    }
    const handleDragOverChat = (event: globalThis.DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
      setFilesDraggingOverChat(true)
    }
    const handleDragLeave = () => {
      fileDragDepthRef.current = Math.max(0, fileDragDepthRef.current - 1)
      if (fileDragDepthRef.current === 0) setFilesDraggingOverChat(false)
    }
    const handleDrop = (event: globalThis.DragEvent) => {
      if (!hasFiles(event)) return
      event.preventDefault()
      fileDragDepthRef.current = 0
      setFilesDraggingOverChat(false)
      const files = Array.from(event.dataTransfer?.files ?? [])
      if (files.length > 0) void handleAttachmentFiles(files)
    }
    dropZone.addEventListener('dragenter', handleDragEnter)
    dropZone.addEventListener('dragover', handleDragOverChat)
    dropZone.addEventListener('dragleave', handleDragLeave)
    dropZone.addEventListener('drop', handleDrop)
    return () => {
      fileDragDepthRef.current = 0
      setFilesDraggingOverChat(false)
      dropZone.removeEventListener('dragenter', handleDragEnter)
      dropZone.removeEventListener('dragover', handleDragOverChat)
      dropZone.removeEventListener('dragleave', handleDragLeave)
      dropZone.removeEventListener('drop', handleDrop)
    }
  }, [handleAttachmentFiles])

  useEffect(() => {
    if (!effectiveMediaCapability && attachments.length > 0) {
      setAttachmentError('Attachment capability changed. Remove the existing attachments or restore the supported model and credential.')
    }
  }, [attachments.length, effectiveMediaCapability])

  const handleDragOver = useCallback((event: ReactDragEvent<HTMLTextAreaElement>) => {
    const hasTodo = Array.from(event.dataTransfer.types).includes(TODO_DRAG_MIME) || Array.from(event.dataTransfer.types).includes('text/plain')
    const hasFiles = Array.from(event.dataTransfer.types).includes('Files')
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

  const renderComposerControl = (openPicker: () => void, open: boolean) => <ComposerPlanModelControl
    provider={selectedModel?.provider}
    model={selectedModel?.model}
    thinking={selectedThinking}
    serviceTier={selectedServiceTier}
    statusLabel={modelStatusLabel}
    disabled={composerDisabled || agentModelControlBusy}
    open={open}
    onOpen={openPicker}
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
    <div ref={composerRootRef} className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="desktop-v3-agentic-composer">
      {fileDropZone && filesDraggingOverChat ? createPortal(
        <div className="pointer-events-none absolute inset-3 z-50 grid place-items-center rounded-2xl border-2 border-dashed border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-bg))] p-6 shadow-xl" data-testid="desktop-chat-file-drop-overlay" role="status" aria-live="polite">
          <div className="flex max-w-md flex-col items-center gap-3 rounded-2xl bg-[var(--app-surface-elevated)] px-8 py-6 text-center shadow-lg">
            <UploadCloud size={34} className="text-[var(--app-primary)]" aria-hidden="true" />
            <div>
              <div className="text-base font-semibold text-[var(--app-text)]">Drop files to attach</div>
              <div className="mt-1 text-sm text-[var(--app-text-muted)]">Images, Markdown, and supported code or text files will be added to this message.</div>
            </div>
          </div>
        </div>,
        fileDropZone,
      ) : null}
      <div className={DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME}>
        {selectedWorkspaceAction && workspacePath.trim() ? (
          <DesktopWorkspaceActionPanel key={selectedWorkspaceAction.id} workspacePath={workspacePath} action={selectedWorkspaceAction} onClose={() => setSelectedWorkspaceAction(null)} />
        ) : null}
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
                  if (event.dataTransfer.files.length > 0) {
                    event.preventDefault()
                    return
                  }
                  onDropTodo?.(event)
                }}
                onPaste={(event) => {
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
          {attachments.length > 0 || routedStagedAttachments.length > 0 || textAttachments.length > 0 || selectedWorkspaceSkill ? (
            <div className="flex flex-wrap gap-2 border-t border-[var(--app-border)] px-4 py-2" data-testid="desktop-media-attachments">
              {selectedWorkspaceSkill ? (
                <span className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]" data-testid="desktop-composer-selected-skill">
                  <Sparkles size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={selectedWorkspaceSkill.description || selectedWorkspaceSkill.name}>{selectedWorkspaceSkill.name}</span>
                  <span className="rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[10px] uppercase text-[var(--app-text-muted)]">Skill</span>
                  <button type="button" aria-label={`Remove ${selectedWorkspaceSkill.name} skill`} onClick={() => setSelectedWorkspaceSkill(null)}><X size={13} /></button>
                </span>
              ) : null}
              {routedStagedAttachments.map((attachment) => (
                <span key={attachment.stagingId} className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]">
                  <FileImage size={13} aria-hidden="true" />
                  <span className="max-w-48 truncate">{attachment.name}</span>
                  <span className="text-[var(--app-text-muted)]">{Math.ceil(attachment.size / 1024)} KB</span>
                  {onRoutedRemoveStagedAttachment ? <button type="button" aria-label={`Remove ${attachment.name}`} onClick={() => onRoutedRemoveStagedAttachment(attachment.stagingId)}><X size={13} /></button> : null}
                </span>
              ))}
              {attachments.map((attachment, index) => (
                <span key={`${attachment.asset_id}:${index}`} className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]">
                  <FileImage size={13} aria-hidden="true" />
                  <span>{attachment.file_type?.toUpperCase() || attachment.mime_type}</span>
                  <span className="text-[var(--app-text-muted)]">{Math.ceil(attachment.size / 1024)} KB</span>
                  <button type="button" aria-label="Remove attachment" onClick={() => setAttachments((current) => current.filter((_, itemIndex) => itemIndex !== index))}><X size={13} /></button>
                </span>
              ))}
              {textAttachments.map((attachment) => (
                <span key={`text:${attachment.id}`} className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]">
                  <FileCode2 size={13} className="shrink-0" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={attachment.name}>{attachment.name}</span>
                  <span className="rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[10px] uppercase text-[var(--app-text-muted)]">{attachment.fileType || 'text'}</span>
                  <span className="text-[var(--app-text-muted)]">{Math.max(1, Math.ceil(attachment.size / 1024))} KB</span>
                  <button type="button" aria-label={`Remove ${attachment.name}`} onClick={() => setTextAttachments((current) => current.filter((item) => item.id !== attachment.id))}><X size={13} /></button>
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
            {effectiveMediaCapability || (routedNewSession && onRoutedStageAttachments) ? (
              <input
                ref={fileInputRef}
                type="file"
                hidden
                multiple
                accept={effectiveMediaCapability?.capabilities.flatMap((capability) => [
                  ...(capability.mime_types ?? []),
                  ...(capability.file_types ?? []).map((fileType) => `.${fileType.replace(/^\./, '')}`),
                ]).join(',')}
                onChange={(event) => { void handleAttachmentFiles(Array.from(event.target.files ?? [])); event.target.value = '' }}
              />
            ) : null}
            <DesktopComposerActionMenu
              disabled={composerDisabled}
              onPrimeTask={handlePrimeTask}
              onAttach={routedNewSession ? (onRoutedStageAttachments ? () => fileInputRef.current?.click() : undefined) : effectiveMediaCapability ? () => fileInputRef.current?.click() : undefined}
              attachDisabled={(routedNewSession ? !onRoutedStageAttachments : !effectiveMediaCapability) || composerDisabled || uploadingAttachment}
              attaching={uploadingAttachment}
              contextLabel={contextLabel}
              contextTooltip={contextTooltip}
              onCompact={onCompact ? () => { void onCompact(draft) } : undefined}
              compactDisabled={compactDisabled || !onCompact}
              workspacePath={workspacePath}
              onActionSelect={setSelectedWorkspaceAction}
              onSkillSelect={setSelectedWorkspaceSkill}
            />
            {uploadingAttachment ? <button type="button" className="text-xs text-[var(--app-warning)]" onClick={() => uploadAbortRef.current?.abort()}>Cancel upload</button> : null}
            {routedNewSession && onRoutedWorktreeRequestedChange ? (
              <DesktopRoutedWorktreePrime requested={routedWorktreeRequested} onRequestedChange={onRoutedWorktreeRequestedChange} disabled={composerDisabled || uploadingAttachment} />
            ) : null}
            {routedNewSession && showModePicker ? (
              <DesktopComposerPlanToggle
                active={mode === 'plan'}
                onActiveChange={mode === 'plan' ? undefined : () => onModeSelect?.('plan')}
                disabled={composerDisabled || agentModelControlBusy || !onModeSelect}
                readOnly={mode === 'plan'}
              />
            ) : resolvedSessionControls && showModePicker && mode === 'plan' ? (
              <DesktopComposerPlanToggle active readOnly />
            ) : null}
            <div className="hidden min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:flex">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : (resolvedSessionControls || routedNewSession) ? (
                  renderComposerControl(openAgentSetup, false)
                ) : executionLabel && !routedNewSession ? (
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
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (uploadingAttachment || (!canSubmit && attachments.length === 0 && textAttachments.length === 0 && !selectedWorkspaceSkill) || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <ArrowUp size={22} strokeWidth={2.25} className="shrink-0" />}
                </Button>
              </div>
            </div>
            <div className="flex min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:hidden">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : (resolvedSessionControls || routedNewSession) ? (
                  renderComposerControl(openAgentSetup, false)
                ) : !routedNewSession ? (
                  <span className="min-w-0 truncate font-medium text-[var(--app-text-muted)]">{executionLabel || (currentAgent === 'swarm' ? 'Swarm' : currentAgent)}</span>
                ) : null}

              </div>
              <div className="flex shrink-0 items-center gap-2">
                {dictationButton()}
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (uploadingAttachment || (!canSubmit && attachments.length === 0 && textAttachments.length === 0 && !selectedWorkspaceSkill) || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
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
        modelLocked={modelPickerDisabled || Boolean(modelLockNotice.trim())}
        modelLockNotice={modelPickerDisabledReason || modelLockNotice}
        triggerDetail={modelControlDetail}
        openSignal={agentSettingsOpenSignal + agentSetupOpenSignal}
        initialAgentName={agentSettingsInitialAgent}
        onOpenAgentSettings={onOpenAgentSettings ? () => onOpenAgentSettings(agentSettingsInitialAgent || currentAgent) : undefined}
        onConfirmAgentSettings={onConfirmAgentSettings}
        modelProfiles={modelProfiles}
        activeModelProfile={activeModelProfile}
        busy={agentModelControlBusy}
        showTrigger={false}
      />
    </div>
  )
}
