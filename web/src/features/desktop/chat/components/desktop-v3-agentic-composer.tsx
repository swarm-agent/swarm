import { useCallback, useEffect, useId, useMemo, useRef, useState, type DragEvent as ReactDragEvent, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, ArrowLeft, ArrowUp, FileCode2, FileImage, Folder, GalleryHorizontal, ListChecks, ListTodo, LoaderCircle, Mic, Minimize2, Sparkles, Square, UploadCloud, Video, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopV3MediaCapability, DesktopV3MediaReference } from '../../state/desktop-v3-cache-types'
import type { DesktopV3RoutedComposerSnapshot, DesktopV3RoutedNewSessionState } from '../../session-v3/new-session-flow'
import { buildDesktopFlagTaskPrompt, buildDesktopSlashPaletteState, parseDesktopNewSessionCommand, type DesktopSlashCommand, type DesktopSlashPaletteState } from '../services/slash-commands'
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
import { DesktopWorkspaceActionChooser } from './desktop-workspace-action-chooser'
import type { WorkspaceAction } from '../../../workspaces/actions/types'
import type { WorkspaceSkill } from '../services/workspace-skills'
import { addSourceMediaDirectory, getSourceMediaDirectories } from '../../settings/media/queries/get-media-settings'
import { browseDesktopVideoSource, DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT, type DesktopVideoSourceAttachment, type DesktopVideoSourceBrowseResult } from '../services/video-source-attachments'
import { DesktopComposerPlanToggle } from './desktop-composer-plan-toggle'
import { DesktopV3ArtifactCatalogGallery } from './desktop-v3-artifact-gallery'
import {
  appendDesktopV3ArtifactMessageSelections,
  removeDesktopV3ArtifactMessageSelection,
  type DesktopV3ArtifactMessageSelection,
} from '../../session-v3/artifact-api'

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
  initialArtifactSelections?: readonly DesktopV3ArtifactMessageSelection[]
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | readonly DesktopV3ArtifactMessageSelection[] | null
  contextChip?: { id: string; label: string; kind: string; description?: string } | null
  onContextChipRemove?: () => void
  onArtifactSelectionRequestHandled?: () => void
  onSubmit: (draft: string, attachments: DesktopV3MediaReference[], artifactSelections: DesktopV3ArtifactMessageSelection[], videoAttachments: DesktopVideoSourceAttachment[]) => void | Promise<void>
  onRoutedSubmit?: (snapshot: DesktopV3RoutedComposerSnapshot) => Promise<DesktopV3RoutedNewSessionState | void>
  routedStagedAttachments?: readonly DesktopComposerStagedAttachment[]
  onRoutedStageAttachments?: (files: File[], signal: AbortSignal) => Promise<void>
  onRoutedRemoveStagedAttachment?: (stagingId: string) => void
  routedComposerSnapshot?: DesktopV3RoutedComposerSnapshot | null
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
  onApplyModelFavorite?: (profile: ModelProfileRecord) => void | Promise<void>
  onApplyModelFavoriteChatOnly?: (profile: ModelProfileRecord) => void | Promise<void>
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
  sessionId?: string
  developerMode?: boolean
  onOpenActionSettings?: () => void
  /** Pre-route composer state; agent/model controls remain visible before the first send. */
  routedNewSession?: boolean
  slashCommandContext?: 'existing-session' | 'new-session'
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
  initialArtifactSelections = [],
  artifactSelectionRequest = null,
  contextChip = null,
  onContextChipRemove,
  onArtifactSelectionRequestHandled,
  onSubmit,
  onRoutedSubmit,
  routedStagedAttachments = [],
  onRoutedStageAttachments,
  onRoutedRemoveStagedAttachment,
  routedComposerSnapshot = null,
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
  onApplyModelFavorite,
  onApplyModelFavoriteChatOnly,
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
  sessionId = '',
  developerMode = false,
  onOpenActionSettings,
  routedNewSession = false,
  slashCommandContext = 'existing-session',
}: DesktopV3AgenticComposerProps) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const composerRootRef = useRef<HTMLDivElement | null>(null)
  const fileDragDepthRef = useRef(0)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const uploadAbortRef = useRef<AbortController | null>(null)
  const textAttachmentSequenceRef = useRef(0)
  const routedSubmissionRef = useRef(false)
  const handledArtifactSelectionRequestRef = useRef('')
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
  const [modelFavoritesOpenSignal, setModelFavoritesOpenSignal] = useState(0)
  const [agentSetupOpenSignal, setAgentSetupOpenSignal] = useState(0)
  const modelFavoritesAnchorId = useId()
  const [primedTaskMode, setPrimedTaskMode] = useState<DesktopComposerTaskMode | null>(null)
  const [attachments, setAttachments] = useState<DesktopV3MediaReference[]>([])
  const [videoAttachments, setVideoAttachments] = useState<DesktopVideoSourceAttachment[]>([])
  const [videoPickerOpen, setVideoPickerOpen] = useState(false)
  const [videoRoots, setVideoRoots] = useState<string[]>([])
  const [videoRoot, setVideoRoot] = useState('')
  const [videoBrowse, setVideoBrowse] = useState<DesktopVideoSourceBrowseResult | null>(null)
  const [videoBrowseLoading, setVideoBrowseLoading] = useState(false)
  const [videoPickerError, setVideoPickerError] = useState<string | null>(null)
  const [videoFolderDraft, setVideoFolderDraft] = useState('')
  const [artifactSelections, setArtifactSelections] = useState<DesktopV3ArtifactMessageSelection[]>(() => [...(routedComposerSnapshot?.artifactSelections ?? initialArtifactSelections)])
  const [textAttachments, setTextAttachments] = useState<DesktopComposerTextAttachment[]>([])
  const [uploadingAttachment, setUploadingAttachment] = useState(false)
  const [attachmentError, setAttachmentError] = useState<string | null>(null)
  const [fileDropZone, setFileDropZone] = useState<HTMLElement | null>(null)
  const [filesDraggingOverChat, setFilesDraggingOverChat] = useState(false)
  const [selectedWorkspaceAction, setSelectedWorkspaceAction] = useState<WorkspaceAction | null>(null)
  const [workspaceActionChooserOpen, setWorkspaceActionChooserOpen] = useState(false)
  const [artifactViewerOpen, setArtifactViewerOpen] = useState(false)
  const [workspaceActionAutoLaunch, setWorkspaceActionAutoLaunch] = useState(false)
  const [workspaceActionLaunchToken, setWorkspaceActionLaunchToken] = useState(0)
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
  const slashPalette = useMemo(() => buildDesktopSlashPaletteState(draft, { developerMode: developerMode && Boolean(sessionId.trim()) }), [developerMode, draft, sessionId])
  const slashCommands = useMemo(
    () => slashPalette.matches.filter((command) => command.state === 'ready'
      && (slashCommandContext !== 'new-session' || command.action.kind !== 'new-session')),
    [slashCommandContext, slashPalette.matches],
  )
  const mentionPaletteIsActive = useMemo(() => mentionPaletteActive(draft, mentionSubagents), [draft, mentionSubagents])
  const mentionPaletteMatches = useMemo(() => chatMentionCandidates(mentionPaletteQuery(draft), mentionSubagents), [draft, mentionSubagents])
  const selectedModel = useMemo(() => modelOptions.find((option) => option.key === selectedModelKey) ?? null, [modelOptions, selectedModelKey])
  const selectedThinking = thinking.trim() || 'off'
  void _onAgentSelect
  const openModelFavorites = useCallback(() => {
    setModelFavoritesOpenSignal((current) => current + 1)
  }, [])
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
    setVideoAttachments([])
    setArtifactSelections([])
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
    if (!artifactSelectionRequest) {
      handledArtifactSelectionRequestRef.current = ''
      return
    }
    const artifactSelectionRequests: readonly DesktopV3ArtifactMessageSelection[] = Array.isArray(artifactSelectionRequest)
      ? artifactSelectionRequest
      : [artifactSelectionRequest as DesktopV3ArtifactMessageSelection]
    const artifactSelectionRequestKey = JSON.stringify(artifactSelectionRequests)
    if (handledArtifactSelectionRequestRef.current === artifactSelectionRequestKey) return
    handledArtifactSelectionRequestRef.current = artifactSelectionRequestKey
    try {
      setArtifactSelections((current) => appendDesktopV3ArtifactMessageSelections(current, artifactSelectionRequests))
      setAttachmentError(null)
    } catch (cause) {
      setAttachmentError(cause instanceof Error ? cause.message : 'Artifact selection failed.')
    } finally {
      onArtifactSelectionRequestHandled?.()
    }
  }, [artifactSelectionRequest, onArtifactSelectionRequestHandled])

  useEffect(() => {
    if (!routedNewSession || !routedComposerSnapshot) return
    routedSubmissionRef.current = false
    setSelectedWorkspaceAction((routedComposerSnapshot.selectedAction as WorkspaceAction | null) ?? null)
    setWorkspaceActionAutoLaunch(false)
    setSelectedWorkspaceSkill((routedComposerSnapshot.selectedSkill as WorkspaceSkill | null) ?? null)
    setArtifactSelections(routedComposerSnapshot.artifactSelections ?? [])
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

  const loadVideoRoot = useCallback(async (rootPath: string, relativePath = '.') => {
    if (!workspacePath.trim() || !rootPath.trim()) return
    setVideoBrowseLoading(true)
    setVideoPickerError(null)
    try {
      const result = await browseDesktopVideoSource(workspacePath, rootPath, relativePath)
      setVideoRoot(result.rootPath)
      setVideoBrowse(result)
    } catch (cause) {
      setVideoPickerError(cause instanceof Error ? cause.message : 'Video source is unavailable.')
    } finally {
      setVideoBrowseLoading(false)
    }
  }, [workspacePath])

  const openVideoPicker = useCallback(async () => {
    if (!workspacePath.trim()) return
    setVideoPickerOpen(true)
    setVideoPickerError(null)
    try {
      const roots = await getSourceMediaDirectories(workspacePath)
      setVideoRoots(roots)
      const root = roots.includes(videoRoot) ? videoRoot : roots[0] ?? ''
      if (root) await loadVideoRoot(root)
      else setVideoBrowse(null)
    } catch (cause) {
      setVideoPickerError(cause instanceof Error ? cause.message : 'Registered media folders are unavailable.')
    }
  }, [loadVideoRoot, videoRoot, workspacePath])

  const registerVideoFolder = useCallback(async () => {
    const path = videoFolderDraft.trim()
    if (!path || !workspacePath.trim()) return
    setVideoBrowseLoading(true)
    setVideoPickerError(null)
    try {
      const roots = await addSourceMediaDirectory(workspacePath, path)
      setVideoRoots(roots)
      setVideoFolderDraft('')
      await loadVideoRoot(path)
    } catch (cause) {
      setVideoPickerError(cause instanceof Error ? cause.message : 'Media folder registration failed.')
      setVideoBrowseLoading(false)
    }
  }, [loadVideoRoot, videoFolderDraft, workspacePath])

  const toggleVideoAttachment = useCallback((clip: DesktopVideoSourceAttachment) => {
    setVideoAttachments((current) => {
      if (current.some((item) => item.ref === clip.ref)) return current.filter((item) => item.ref !== clip.ref)
      if (current.length >= DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT) {
        setVideoPickerError(`Attach at most ${DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT} videos per message.`)
        return current
      }
      setVideoPickerError(null)
      return [...current, clip]
    })
  }, [])

  const handlePrimeTask = useCallback((taskMode: DesktopComposerTaskMode) => {
    if (dictationEnabledRef.current) stopDictation(false)
    if (routedNewSession) {
      if (typeof window !== 'undefined') window.requestAnimationFrame(() => textareaRef.current?.focus())
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
  }, [resizeTextareaElement, routedNewSession, stopDictation])

  const handleWorkspaceActionSelect = useCallback((action: WorkspaceAction, confirmedLaunch: boolean) => {
    setSelectedWorkspaceAction(action)
    setWorkspaceActionAutoLaunch(confirmedLaunch)
    setWorkspaceActionLaunchToken((current) => current + 1)
  }, [])

  const closeWorkspaceActionPanel = useCallback(() => {
    setSelectedWorkspaceAction(null)
    setWorkspaceActionAutoLaunch(false)
  }, [])

  const handleSubmitClick = useCallback(async () => {
    if (uploadingAttachment) {
      setAttachmentError('Wait for all attachments to finish uploading before sending the message.')
      return
    }
    if (routedNewSession && routedSubmissionRef.current) return
    const rawDraft = textareaRef.current?.value ?? dictationComposer
    if (slashPalette.exactMatch?.action.kind === 'open-artifact-viewer' && !slashPalette.hasArguments) {
      setArtifactViewerOpen(true)
      onDraftChange('')
      return
    }
    const flagCommandSelected = slashPalette.exactMatch?.id === 'flag'
    const flagTaskPrompt = flagCommandSelected ? buildDesktopFlagTaskPrompt(rawDraft, sessionId) : null
    if (flagCommandSelected && !flagTaskPrompt) {
      setAttachmentError(sessionId.trim() ? 'Enter a problem after /flag.' : '/flag requires an existing session to investigate.')
      return
    }
    const newSessionCommand = routedNewSession ? parseDesktopNewSessionCommand(rawDraft) : null
    if (newSessionCommand && !newSessionCommand.prompt) {
      onModeSelect?.(newSessionCommand.planModeRequested ? 'plan' : 'auto')
      onDraftChange('')
      const textarea = textareaRef.current
      if (textarea) {
        textarea.value = ''
        resizeTextareaElement(textarea)
      }
      return
    }
    const commandDraft = flagTaskPrompt ? `/task ${flagTaskPrompt}` : newSessionCommand?.prompt ?? rawDraft
    const textAttachmentDraft = textAttachments.reduce(
      (nextDraft, attachment) => appendComposerTextFile(nextDraft, attachment.name, attachment.fileType, attachment.content),
      commandDraft,
    )
    const attachmentDraft = textAttachmentDraft.trim()
      || (attachments.length > 0 || routedStagedAttachments.length > 0 ? 'Please review the attached file(s).' : '')
      || (videoAttachments.length > 0 ? 'Please review the attached video(s).' : '')
      || (artifactSelections.length > 0 ? 'Please review the selected artifact(s).' : textAttachmentDraft)
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
        selections: artifactSelections,
        videoAttachments,
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
        artifactSelections,
        videoAttachments,
        selectedAction: selectedWorkspaceAction,
        selectedSkill: selectedWorkspaceSkill,
        planModeRequested: newSessionCommand?.planModeRequested ?? mode === 'plan',
      }
      let routedSubmit: Promise<DesktopV3RoutedNewSessionState | void>
      try {
        routedSubmit = onRoutedSubmit(routedSnapshot)
      } catch (cause) {
        routedSubmissionRef.current = false
        setAttachmentError(cause instanceof Error ? cause.message : 'Routed session start failed.')
        return
      }
      void routedSubmit.then((state) => {
        if (state?.phase === 'failed') {
          routedSubmissionRef.current = false
          return
        }
        clearComposerForSubmit()
        setSelectedWorkspaceAction(null)
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
      selections: artifactSelections,
      videoAttachments,
      onSubmit,
      onStop,
      onSlashCommand,
    })
  }, [artifactSelections, attachments, canStop, clearComposerForSubmit, dictationComposer, mode, onDraftChange, onModeSelect, onRoutedSubmit, onSlashCommand, onStop, onSubmit, primedTaskMode, resizeTextareaElement, routedNewSession, routedStagedAttachments, selectedWorkspaceAction, selectedWorkspaceSkill, sessionId, slashPalette.exactMatch?.action.kind, slashPalette.exactMatch?.id, slashPalette.hasArguments, textAttachments, uploadingAttachment, videoAttachments])

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

  const openWorkspaceActionChooser = useCallback(() => {
    setWorkspaceActionChooserOpen(true)
    setSelectedWorkspaceAction(null)
  }, [])

  const handleSlashSelect = useCallback((command: DesktopSlashCommand) => {
    if (command.state !== 'ready') return
    if (command.action.kind === 'open-artifact-viewer') {
      setArtifactViewerOpen(true)
      onDraftChange('')
      return
    }
    if (command.action.kind === 'open-action-chooser') {
      openWorkspaceActionChooser()
      onDraftChange('')
      return
    }
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
    void onSlashCommand?.(command, draft)
    if (command.action.kind === 'open-settings' && command.action.tab === 'agents') {
      openAgentSetup()
      onDraftChange('')
      return
    }
    if (command.action.kind === 'toggle-tips') {
      onDraftChange('')
      return
    }
    if (command.action.kind === 'open-model-picker') {
      if (!routedNewSession) openModelFavorites()
      onDraftChange('')
      return
    }
    if (command.action.kind === 'compact-session') {
      void onCompact?.(draft)
      onDraftChange('')
      return
    }
    if (!slashPalette.hasArguments) onDraftChange('')
  }, [currentAgent, draft, handleSubmitClick, onCompact, onDraftChange, onSlashCommand, onThinkingTagsToggle, openAgentSetup, openModelFavorites, openWorkspaceActionChooser, routedNewSession, slashPalette.hasArguments, thinkingTagsBusy, thinkingTagsEnabled])

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (routedNewSession && event.key === 'Tab' && event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault()
      onModeSelect?.('plan')
      return
    }
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
      if (event.key === 'Enter' && !event.shiftKey && (!slashPalette.hasArguments || slashPalette.exactMatch?.action.kind === 'start-background-router-session' || slashPalette.exactMatch?.action.kind === 'new-session' || slashPalette.exactMatch?.action.kind === 'toggle-tips' || slashPalette.exactMatch?.action.kind === 'open-action-chooser')) {
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
      if (canSubmit || attachments.length > 0 || artifactSelections.length > 0 || textAttachments.length > 0 || selectedWorkspaceSkill || canStop) handleSubmitClick()
    }
  }, [artifactSelections.length, attachments.length, canStop, canSubmit, handleMentionInsert, handleSlashSelect, handleSubmitClick, mentionPaletteIsActive, mentionPaletteMatches, mentionSelectionIndex, onDraftChange, onModeSelect, routedNewSession, selectedWorkspaceSkill, slashCommands, slashPalette.active, slashPalette.exactMatch?.action.kind, slashPalette.hasArguments, slashSelectionIndex, textAttachments.length])

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
    popoverAnchorId={modelFavoritesAnchorId}
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
      <DesktopV3ArtifactCatalogGallery
        open={artifactViewerOpen}
        onOpenChange={setArtifactViewerOpen}
        onAddToChat={(artifacts) => {
          setArtifactSelections((current) => appendDesktopV3ArtifactMessageSelections(current, artifacts.map(({ label, description, selection }) => ({ ...selection, label, description, action: 'select' }))))
          setArtifactViewerOpen(false)
        }}
        onUseThisDesign={({ label, description, selection }) => {
          setArtifactSelections((current) => appendDesktopV3ArtifactMessageSelections(current, [{ ...selection, label, description, action: 'use' }]))
          setArtifactViewerOpen(false)
        }}
        onExportVideoStills={({ label, description, selection }, prompt) => {
          setArtifactSelections((current) => appendDesktopV3ArtifactMessageSelections(current, [{ ...selection, label, description, action: 'select' }]))
          onDraftChange(prompt)
          setArtifactViewerOpen(false)
        }}
      />
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
        {workspaceActionChooserOpen && workspacePath.trim() ? (
          <DesktopWorkspaceActionChooser
            workspacePath={workspacePath}
            sessionId={sessionId}
            onSelect={(action) => {
              setWorkspaceActionChooserOpen(false)
              handleWorkspaceActionSelect(action, false)
            }}
            onOpenSettings={onOpenActionSettings}
            onClose={() => setWorkspaceActionChooserOpen(false)}
          />
        ) : selectedWorkspaceAction && workspacePath.trim() ? (
          <DesktopWorkspaceActionPanel key={`${selectedWorkspaceAction.id}:${workspaceActionLaunchToken}`} workspacePath={workspacePath} sessionId={sessionId} action={selectedWorkspaceAction} autoLaunch={workspaceActionAutoLaunch} onClose={closeWorkspaceActionPanel} />
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
          <DesktopSlashCommandPanel palette={{ ...slashPalette, matches: slashCommands } as DesktopSlashPaletteState} selectedIndex={slashSelectionIndex} onHover={setSlashSelectionIndex} onSelect={handleSlashSelect} />
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
                placeholder={!draft.trim() && artifactSelections.some((selection) => Boolean(selection.pending_request?.trim()))
                  ? 'Let Swarm know your next step requests'
                  : placeholder}
                aria-label={inputLabel}
                aria-keyshortcuts={routedNewSession ? 'Shift+Tab' : undefined}
                title={routedNewSession ? 'Shift+Tab enables Plan for this new session' : undefined}
                className="max-h-[50vh] !min-h-[32px] resize-none overflow-y-hidden !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]"
                rows={1}
                disabled={composerDisabled}
              />
            </div>
          </div>
          {attachments.length > 0 || videoAttachments.length > 0 || artifactSelections.length > 0 || routedStagedAttachments.length > 0 || textAttachments.length > 0 || selectedWorkspaceSkill || contextChip ? (
            <div className="flex flex-wrap gap-2 border-t border-[var(--app-border)] px-4 py-2" data-testid="desktop-media-attachments">
              {selectedWorkspaceSkill ? (
                <span className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]" data-testid="desktop-composer-selected-skill">
                  <Sparkles size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={selectedWorkspaceSkill.description || selectedWorkspaceSkill.name}>{selectedWorkspaceSkill.name}</span>
                  <span className="rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[10px] uppercase text-[var(--app-text-muted)]">Skill</span>
                  <button type="button" aria-label={`Remove ${selectedWorkspaceSkill.name} skill`} onClick={() => setSelectedWorkspaceSkill(null)}><X size={13} /></button>
                </span>
              ) : null}
              {contextChip ? (
                <span className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]" data-testid="desktop-composer-context-chip">
                  <Video size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={contextChip.description || contextChip.label}>{contextChip.label}</span>
                  <span className="rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[10px] uppercase text-[var(--app-text-muted)]">{contextChip.kind}</span>
                  <button type="button" aria-label={`Remove ${contextChip.label} context`} onClick={onContextChipRemove}><X size={13} /></button>
                </span>
              ) : null}
              {artifactSelections.map((selection) => (
                <span key={JSON.stringify([selection.session_id, selection.artifact_id, selection.revision_ref, selection.target_part_ids, selection.collection_id, selection.variant_id, selection.part_id])} className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]" data-testid="desktop-composer-artifact-chip">
                  <GalleryHorizontal size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={selection.description || selection.label}>{selection.label}</span>
                  <span className="rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[10px] uppercase text-[var(--app-text-muted)]">{selection.artifact_id || selection.pending_request?.trim() ? 'Pending update' : selection.part_id ? 'Part target' : selection.action === 'use' ? 'Use design' : 'Artifact'}</span>
                  <button type="button" aria-label={`Remove ${selection.label} artifact`} onClick={() => setArtifactSelections((current) => removeDesktopV3ArtifactMessageSelection(current, selection))}><X size={13} /></button>
                </span>
              ))}
              {videoAttachments.map((attachment) => (
                <span key={`video:${attachment.ref}`} className="inline-flex max-w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-xs text-[var(--app-text)]" data-testid="desktop-composer-video-chip">
                  <Video size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="max-w-48 truncate font-medium" title={attachment.name}>{attachment.name}</span>
                  <span className="text-[var(--app-text-muted)]">{Math.max(1, Math.ceil(attachment.size_bytes / (1024 * 1024)))} MB</span>
                  <button type="button" aria-label={`Remove ${attachment.name} video`} onClick={() => setVideoAttachments((current) => current.filter((item) => item.ref !== attachment.ref))}><X size={13} /></button>
                </span>
              ))}
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
              onAddMediaFolder={workspacePath.trim() ? () => { void openVideoPicker() } : undefined}
              addMediaFolderDisabled={composerDisabled || videoBrowseLoading}
              onAttach={routedNewSession ? (onRoutedStageAttachments ? () => fileInputRef.current?.click() : undefined) : effectiveMediaCapability ? () => fileInputRef.current?.click() : undefined}
              attachDisabled={(routedNewSession ? !onRoutedStageAttachments : !effectiveMediaCapability) || composerDisabled || uploadingAttachment}
              attaching={uploadingAttachment}
              contextLabel={contextLabel}
              contextTooltip={contextTooltip}
              onCompact={onCompact ? () => { void onCompact(draft) } : undefined}
              compactDisabled={compactDisabled || !onCompact}
              workspacePath={workspacePath}
              sessionId={sessionId}
              onActionSelect={handleWorkspaceActionSelect}
              onOpenActionSettings={onOpenActionSettings}
              onSkillSelect={setSelectedWorkspaceSkill}
            />
            {uploadingAttachment ? <button type="button" className="text-xs text-[var(--app-warning)]" onClick={() => uploadAbortRef.current?.abort()}>Cancel upload</button> : null}
            {routedNewSession && showModePicker ? (
              <DesktopComposerPlanToggle
                active={mode === 'plan'}
                onActiveChange={(active) => onModeSelect?.(active ? 'plan' : 'auto')}
                disabled={composerDisabled || agentModelControlBusy || !onModeSelect}
                allowDisable
              />
            ) : resolvedSessionControls && showModePicker && mode === 'plan' ? (
              <DesktopComposerPlanToggle active readOnly />
            ) : null}
            <div className="hidden min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:flex">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : (resolvedSessionControls || routedNewSession) ? (
                  renderComposerControl(openModelFavorites, false)
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
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (uploadingAttachment || (!canSubmit && attachments.length === 0 && videoAttachments.length === 0 && artifactSelections.length === 0 && textAttachments.length === 0 && !selectedWorkspaceSkill) || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <ArrowUp size={22} strokeWidth={2.25} className="shrink-0" />}
                </Button>
              </div>
            </div>
            <div className="flex min-w-0 flex-1 items-center justify-between gap-2 min-[1000px]:hidden">
              <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                {primedTaskMode ? taskModeIndicator() : (resolvedSessionControls || routedNewSession) ? (
                  renderComposerControl(openModelFavorites, false)
                ) : !routedNewSession ? (
                  <span className="min-w-0 truncate font-medium text-[var(--app-text-muted)]">{executionLabel || (currentAgent === 'swarm' ? 'Swarm' : currentAgent)}</span>
                ) : null}

              </div>
              <div className="flex shrink-0 items-center gap-2">
                {dictationButton()}
                <Button size="sm" className="h-10 w-10 shrink-0 rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] transition-all hover:-translate-y-0.5 hover:bg-[var(--app-primary-hover)] hover:shadow-md active:bg-[var(--app-primary-active)] disabled:hover:translate-y-0" onClick={handleSubmitClick} disabled={!canStop && (uploadingAttachment || (!canSubmit && attachments.length === 0 && videoAttachments.length === 0 && artifactSelections.length === 0 && textAttachments.length === 0 && !selectedWorkspaceSkill) || busy)} aria-label={canStop ? 'Stop run' : 'Send message'}>
                  {canStop ? <Square size={18} /> : busy ? <LoaderCircle size={18} className="animate-spin" /> : <ArrowUp size={22} strokeWidth={2.25} className="shrink-0" />}
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>
      {videoPickerOpen ? <Dialog role="dialog" aria-modal="true" aria-label="Attach videos">
        <DialogBackdrop />
        <DialogPanel className="w-[min(48rem,calc(100vw-2rem))] overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
          <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] px-5 py-4">
            <div><h2 className="text-lg font-semibold text-[var(--app-text)]">Attach videos</h2><p className="text-sm text-[var(--app-text-muted)]">Register a read-only folder, browse supported videos, then choose individual files.</p></div>
            <ModalCloseButton onClick={() => setVideoPickerOpen(false)} />
          </div>
          <div className="grid max-h-[70vh] gap-4 overflow-y-auto p-5">
            <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); void registerVideoFolder() }}>
              <input value={videoFolderDraft} onChange={(event) => setVideoFolderDraft(event.target.value)} placeholder="/path/to/media-folder" aria-label="Media folder path" className="min-w-0 flex-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm text-[var(--app-text)]" />
              <Button type="submit" variant="outline" disabled={!videoFolderDraft.trim() || videoBrowseLoading}>Add media folder</Button>
            </form>
            {videoRoots.length > 0 ? <div className="flex flex-wrap gap-2">{videoRoots.map((root) => <button type="button" key={root} onClick={() => { void loadVideoRoot(root) }} className={`rounded-lg border px-3 py-2 text-left text-xs ${root === videoRoot ? 'border-[var(--app-border-accent)] bg-[var(--app-primary-muted)] text-[var(--app-text)]' : 'border-[var(--app-border)] text-[var(--app-text-muted)]'}`} title={root}>{root.split(/[\\/]/).filter(Boolean).pop() || root}</button>)}</div> : <p className="text-sm text-[var(--app-text-muted)]">No media folders registered yet.</p>}
            {videoBrowse ? <div className="grid gap-2">
              <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
                {videoBrowse.relativePath !== '.' ? <button type="button" onClick={() => { const parts = videoBrowse.relativePath.split(/[\\/]/); parts.pop(); void loadVideoRoot(videoRoot, parts.join('/') || '.') }} className="inline-flex items-center gap-1 font-semibold text-[var(--app-primary)]"><ArrowLeft size={13} />Up</button> : null}
                <span className="truncate font-mono">{videoBrowse.relativePath}</span>
              </div>
              {videoBrowse.directories.map((directory) => <button type="button" key={directory.relative_path} onClick={() => { void loadVideoRoot(videoRoot, directory.relative_path) }} className="flex items-center gap-2 rounded-lg border border-[var(--app-border)] px-3 py-2 text-left text-sm text-[var(--app-text)]"><Folder size={15} />{directory.name}</button>)}
              {videoBrowse.clips.map((clip) => { const selected = videoAttachments.some((item) => item.ref === clip.ref); return <button type="button" key={clip.ref} onClick={() => toggleVideoAttachment(clip)} className={`flex items-center gap-3 rounded-lg border px-3 py-2 text-left ${selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary-muted)]' : 'border-[var(--app-border)]'}`}><Video size={16} className="text-[var(--app-primary)]" /><span className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</span><span className="text-xs text-[var(--app-text-muted)]">{Math.max(1, Math.ceil(clip.size_bytes / (1024 * 1024)))} MB</span><span className="text-xs font-semibold text-[var(--app-primary)]">{selected ? 'Selected' : 'Select'}</span></button> })}
              {!videoBrowseLoading && videoBrowse.directories.length === 0 && videoBrowse.clips.length === 0 ? <p className="py-6 text-center text-sm text-[var(--app-text-muted)]">No supported videos in this folder.</p> : null}
            </div> : null}
            {videoBrowseLoading ? <p className="inline-flex items-center gap-2 text-sm text-[var(--app-text-muted)]"><LoaderCircle size={15} className="animate-spin" />Loading videos…</p> : null}
            {videoPickerError ? <p role="alert" className="text-sm text-[var(--app-danger)]">{videoPickerError}</p> : null}
          </div>
          <div className="flex items-center justify-between border-t border-[var(--app-border)] px-5 py-3"><span className="text-xs text-[var(--app-text-muted)]">{videoAttachments.length} of {DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT} selected</span><Button type="button" onClick={() => setVideoPickerOpen(false)}>Done</Button></div>
        </DialogPanel>
      </Dialog> : null}
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
        openSignal={modelFavoritesOpenSignal}
        setupOpenSignal={agentSettingsOpenSignal + agentSetupOpenSignal}
        initialAgentName={agentSettingsInitialAgent}
        onOpenAgentSettings={onOpenAgentSettings ? () => onOpenAgentSettings(agentSettingsInitialAgent || currentAgent) : undefined}
        onConfirmAgentSettings={onConfirmAgentSettings}
        onApplyModelFavorite={onApplyModelFavorite}
        onApplyModelFavoriteChatOnly={onApplyModelFavoriteChatOnly}
        popoverAnchorId={modelFavoritesAnchorId}
        modelProfiles={modelProfiles}
        activeModelProfile={activeModelProfile}
        busy={agentModelControlBusy}
        showTrigger={false}
      />
    </div>
  )
}
