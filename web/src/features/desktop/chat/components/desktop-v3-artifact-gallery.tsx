import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from 'react'
import { createPortal } from 'react-dom'
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock3,
  Download,
  FileText,
  FolderOpen,
  GalleryHorizontal,
  Loader2,
  Maximize2,
  MessageSquarePlus,
  Minimize2,
  Pause,
  Play,
  Search,
  Sparkles,
  X,
} from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import {
  DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT,
  DESKTOP_V3_HTML_STILL_EXPORT_PROMPT,
  desktopV3ArtifactCanExportHTMLStills,
  desktopV3ArtifactCatalogEntryForKey,
  desktopV3ArtifactCatalogEntryKey,
  desktopV3ArtifactDownloadName,
  desktopV3ArtifactMessageSelection,
  desktopV3ArtifactPartIterationMessageSelection,
  desktopV3ArtifactPartMessageSelection,
  desktopV3ArtifactRevisionHasPart,
  desktopV3ArtifactRequiresBundle,
  desktopV3ArtifactSelection,
  fetchDesktopV3ArtifactCollectionBundle,
  fetchDesktopV3ArtifactDownload,
  fetchDesktopV3ArtifactCatalogResult,
  fetchDesktopV3ArtifactPreviewAccess,
  fetchDesktopV3ArtifactTextPreview,
  preflightDesktopV3ArtifactDirectContent,
  revealDesktopV3Artifact,
  revealDesktopV3ArtifactCollection,
  formatDesktopV3ArtifactAnimationProfile,
  formatDesktopV3ArtifactOutputRequirements,
  selectDesktopV3ArtifactPartRevisions,
  useDesktopV3Artifact,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactCollectionProgress,
  type DesktopV3ArtifactPartRevisionChoice,
  type DesktopV3ArtifactSelection,
} from '../../session-v3/artifact-api'
import { refreshOpenDesktopV3ArtifactCatalogs } from '../../session-v3/artifact-catalog-refresh'
import { useDesktopV3OpenArtifactCatalogRefresh } from '../../session-v3/use-artifact-catalog-refresh'
import {
  desktopV3ArtifactStudioEntries,
  desktopV3ArtifactStudioHead,
  desktopV3ArtifactStudioSamePartRevision,
  desktopV3ArtifactStudioRounds,
  desktopV3ArtifactStudioParent,
  desktopV3ArtifactStudioPresentationGroupKey,
  desktopV3ArtifactStudioSectionAlternatives,
  desktopV3ArtifactStudioSectionLineage,
  desktopV3ArtifactStudioTurns,
} from '../../session-v3/artifact-studio-model'
import { useDesktopV3ArtifactPreviewVisibility } from './desktop-v3-artifact-preview-thumbnail'
import {
  DesktopV3ArtifactSeekAcknowledger,
  resolveDesktopV3ArtifactAutoplaySection,
} from './desktop-v3-artifact-playback'
import {
  DESKTOP_V3_ARTIFACT_PLAYER_PROTOCOL,
  desktopV3ArtifactIterationChangeDescription,
  desktopV3ArtifactIterationMessage,
  desktopV3ArtifactIterationNextSectionDescription,
  formatDesktopV3ArtifactIterationTime,
  normalizeDesktopV3ArtifactIterationDescriptor,
  type DesktopV3ArtifactIterationDescriptor,
  type DesktopV3ArtifactIterationSection,
} from '../../session-v3/artifact-iteration-protocol'

export type DesktopV3ArtifactGalleryEntry = DesktopV3ArtifactCatalogEntry

/** A visible label paired with the opaque authority reference sent to chat. */
export interface DesktopV3ArtifactChatSelection {
  label: string
  description?: string
  selection: DesktopV3ArtifactSelection & { action?: 'select' | 'use' }
}

export interface DesktopV3ArtifactGalleryProps {
  artifacts: DesktopV3ArtifactGalleryEntry[]
  open?: boolean
  onOpenChange?: (open: boolean) => void
  onAddToChat?: (artifacts: DesktopV3ArtifactChatSelection[]) => void | Promise<void>
  onUseThisDesign?: (artifact: DesktopV3ArtifactChatSelection) => void | Promise<void>
  onIterateSection?: (artifact: DesktopV3ArtifactChatSelection, prompt: string, mode: 'alternatives' | 'next-section') => void | Promise<void>
  onActiveBranchChange?: (artifact: DesktopV3ArtifactChatSelection) => void | Promise<void>
  onExportVideoStills?: (artifact: DesktopV3ArtifactChatSelection, prompt: string) => void | Promise<void>
  onSelectionPersisted?: () => void | Promise<void>
  showTrigger?: boolean
  loading?: boolean
  error?: string
  title?: string
  initialArtifactKey?: string
  initialCollectionId?: string
  initialPartId?: string
  artifactHref?: (artifact: DesktopV3ArtifactGalleryEntry) => string
  collectionHref?: (artifact: DesktopV3ArtifactGalleryEntry) => string
  onArtifactNavigate?: (artifact: DesktopV3ArtifactGalleryEntry) => void
  onCollectionNavigate?: (artifact: DesktopV3ArtifactGalleryEntry) => void
  onTriggerOpen?: (artifact: DesktopV3ArtifactGalleryEntry) => void
  presentation?: 'fullscreen' | 'embedded'
  embeddedPortalTarget?: HTMLElement | null
  backLabel?: string
}

type ArtifactCollectionGroup = {
  key: string
  entries: DesktopV3ArtifactGalleryEntry[]
  progress: DesktopV3ArtifactCollectionProgress
  sessionLabel: string
  workspaceLabel: string
}

function artifactSelectionKey(artifact: DesktopV3ArtifactGalleryEntry): string {
  return desktopV3ArtifactCatalogEntryKey(artifact)
}

function artifactCollectionKey(entries: readonly DesktopV3ArtifactGalleryEntry[], artifact: DesktopV3ArtifactGalleryEntry): string {
  return desktopV3ArtifactStudioPresentationGroupKey(entries, artifact)
}

function artifactTypeLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.kind.trim() || artifact.mediaType.trim() || 'artifact'
}

function artifactWorkspaceLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.workspaceName.trim() || artifact.workspacePath.trim() || 'Workspace'
}

function collectionProgress(entries: DesktopV3ArtifactGalleryEntry[]): DesktopV3ArtifactCollectionProgress {
  const reported = entries.find((entry) => entry.progress)?.progress
  if (reported) return reported
  return entries.reduce<DesktopV3ArtifactCollectionProgress>((progress, entry) => {
    progress.total += 1
    if (entry.status === 'staging') progress.staging += 1
    else if (entry.status === 'failed') progress.failed += 1
    else if (entry.status === 'unavailable') progress.unavailable += 1
    else progress.ready += 1
    return progress
  }, { total: 0, staging: 0, ready: 0, failed: 0, unavailable: 0 })
}

function collectionLandingArtifact(group: ArtifactCollectionGroup): DesktopV3ArtifactGalleryEntry | undefined {
  const projected = group.entries.find((entry) => entry.graphState === 'git_projection' && entry.chain?.head)
  if (!projected?.chain?.head) return group.entries[0]
  const head = projected.chain.head
  return group.entries.find((entry) => entry.sessionId === head.sessionId
    && entry.collectionId === head.collectionId
    && entry.artifactId === head.variantId
    && entry.eventSeq === head.eventSeq)
}

function collectionDisplayLabel(group: ArtifactCollectionGroup): string {
  const first = group.entries[0]
  if (!first?.collectionId) return first?.label || 'Artifact'
  if (first.collectionName) return first.collectionName
  if (first.lineage?.iterationGroup) return `${first.lineage.iterationGroup} iterations`
  if (first.lineage?.programId) return first.lineage.programId.replace(/[-_]+/g, ' ')
  if (first.lineage?.iterationGroupId || first.lineage?.taskCallId) return 'Designer iteration group'
  return first.sessionTitle ? `${first.sessionTitle} designs` : 'Design collection'
}

function variantDisplayLabel(artifact: DesktopV3ArtifactGalleryEntry, index: number): string {
  const specialized = artifact.lineage?.iterationLabel || artifact.lineage?.iterationTheme
  if (specialized) return specialized
  const artifactLabel = artifact.label.trim()
  const collectionLabel = artifact.collectionName.trim()
  if (artifactLabel && artifactLabel !== collectionLabel) return artifactLabel
  return `Variant ${artifact.lineage?.iterationIndex || index + 1}`
}

function iterationDisplayLabel(artifact: DesktopV3ArtifactGalleryEntry, index: number): string {
  return variantDisplayLabel(artifact, index)
}

function artifactStatusLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  if (artifact.status === 'staging') return 'Generating'
  if (artifact.status === 'failed') return 'Failed'
  if (artifact.status === 'unavailable') return 'Unavailable'
  if (artifact.status === 'ready') return 'Ready'
  return ''
}

function artifactCanReveal(artifact: DesktopV3ArtifactGalleryEntry | undefined): artifact is DesktopV3ArtifactGalleryEntry {
  return Boolean(artifact
    && artifact.localRevealAvailable === true
    && artifact.status !== 'staging'
    && artifact.status !== 'failed'
    && artifact.status !== 'unavailable'
    && artifact.content === undefined)
}

function collectionGroups(entries: DesktopV3ArtifactGalleryEntry[]): ArtifactCollectionGroup[] {
  const grouped = new Map<string, DesktopV3ArtifactGalleryEntry[]>()
  for (const entry of entries) {
    const key = artifactCollectionKey(entries, entry)
    grouped.set(key, [...(grouped.get(key) ?? []), entry])
  }
  return [...grouped.entries()].map(([key, collectionEntries]) => ({
    key,
    entries: [...collectionEntries].sort((left, right) =>
      (left.step?.revisionNumber ?? left.revisionNumber ?? 0) - (right.step?.revisionNumber ?? right.revisionNumber ?? 0)
        || (left.candidateIndex || left.lineage?.iterationIndex || 0) - (right.candidateIndex || right.lineage?.iterationIndex || 0)
        || left.updatedAt - right.updatedAt),
    progress: collectionProgress(collectionEntries),
    sessionLabel: collectionEntries[0]?.sessionTitle || 'Session artifacts',
    workspaceLabel: collectionEntries[0] ? artifactWorkspaceLabel(collectionEntries[0]) : 'Workspace',
  }))
}

export function DesktopV3ArtifactGallery({
  artifacts,
  open: controlledOpen,
  onOpenChange,
  onAddToChat,
  onUseThisDesign,
  onIterateSection,
  onActiveBranchChange,
  onExportVideoStills,
  onSelectionPersisted,
  showTrigger = true,
  loading: catalogLoading = false,
  error: catalogError = '',
  title = 'Artifact Studio',
  initialArtifactKey = '',
  initialCollectionId = '',
  initialPartId = '',
  artifactHref,
  collectionHref,
  onArtifactNavigate,
  onCollectionNavigate,
  onTriggerOpen,
  presentation = 'fullscreen',
  embeddedPortalTarget: providedEmbeddedPortalTarget = null,
  backLabel = 'Back to conversation',
}: DesktopV3ArtifactGalleryProps) {
  const [internalOpen, setInternalOpen] = useState(false)
  const [selectedId, setSelectedId] = useState(artifacts[0] ? artifactSelectionKey(artifacts[0]) : '')
  const [overviewCollectionKey, setOverviewCollectionKey] = useState('')
  const [chatSelectedIds, setChatSelectedIds] = useState<string[]>([])
  const [durableSelectedId, setDurableSelectedId] = useState('')
  const [studioActiveBranchId, setStudioActiveBranchId] = useState('')
  const [previewURL, setPreviewURL] = useState('')
  const [previewText, setPreviewText] = useState('')
  const [previewError, setPreviewError] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewRetry, setPreviewRetry] = useState(0)
  const [iterationDescriptor, setIterationDescriptor] = useState<DesktopV3ArtifactIterationDescriptor | null>(null)
  const [iterationSectionId, setIterationSectionId] = useState('')
  const [selectedPartIds, setSelectedPartIds] = useState<string[]>([])
  const [stagedPartChoices, setStagedPartChoices] = useState<Record<string, DesktopV3ArtifactPartRevisionChoice>>({})
  const [iterationTimeMs, setIterationTimeMs] = useState(0)
  const [iterationPlaying, setIterationPlaying] = useState(false)
  const [iterationPlayerReadyVersion, setIterationPlayerReadyVersion] = useState(0)
  const [actionPending, setActionPending] = useState<'add' | 'use' | 'ask-part' | 'apply-parts' | 'iterate-part' | 'iterate-section' | 'next-section' | 'export-video-stills' | 'download-collection' | 'reveal-artifact' | 'reveal-collection' | ''>('')
  const [actionError, setActionError] = useState('')
  const [actionConfirmation, setActionConfirmation] = useState('')
  const [query, setQuery] = useState('')
  const [organization, setOrganization] = useState<'collection' | 'workspace'>('collection')
  const galleryButtonRef = useRef<HTMLButtonElement>(null)
  const backButtonRef = useRef<HTMLButtonElement>(null)
  const previewSurfaceRef = useRef<HTMLDivElement>(null)
  const animationFrameRef = useRef<HTMLIFrameElement>(null)
  const iterationDescribeRequestRef = useRef('')
  const iterationDescribeArtifactRef = useRef('')
  const iterationPlayerArtifactRef = useRef('')
  const iterationSectionIdRef = useRef('')
  const selectedArtifactKeyRef = useRef('')
  const iterationDescriptorRef = useRef<DesktopV3ArtifactIterationDescriptor | null>(null)
  const iterationPlaybackFrameRef = useRef<number | null>(null)
  const iterationPlaybackStartedAtRef = useRef(0)
  const iterationPlaybackStartMsRef = useRef(0)
  const iterationTimeMsRef = useRef(0)
  const iterationLastUIUpdateRef = useRef(0)
  const iterationSeekAcknowledgerRef = useRef<DesktopV3ArtifactSeekAcknowledger | null>(null)
  if (!iterationSeekAcknowledgerRef.current) {
    iterationSeekAcknowledgerRef.current = new DesktopV3ArtifactSeekAcknowledger((timeMs) => sendAnimationMessage('seek', timeMs))
  }
  const iterationAutoplayArtifactRef = useRef('')
  const iterationAutoplaySectionRef = useRef('')
  const initialPlaybackArtifactRef = useRef('')
  const initialPartRequestRef = useRef('')
  const initialPartPlaybackRef = useRef('')
  const open = controlledOpen ?? internalOpen
  const { previewRef: animationPreviewRef, previewVisible: animationPreviewVisible } = useDesktopV3ArtifactPreviewVisibility(open)
  const [previewFullscreen, setPreviewFullscreen] = useState(false)
  const setOpen = (next: boolean) => {
    if (controlledOpen === undefined) setInternalOpen(next)
    onOpenChange?.(next)
  }

  const visibleArtifacts = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    if (!normalizedQuery) return artifacts
    return artifacts.filter((artifact) => [
      artifact.label,
      artifact.description,
      artifact.filename,
      artifact.mediaType,
      artifact.kind,
      formatDesktopV3ArtifactOutputRequirements(artifact.outputRequirements),
      formatDesktopV3ArtifactAnimationProfile(artifact.animationProfile),
      artifact.sessionTitle,
      artifact.workspaceName,
      artifact.workspacePath,
      artifact.collectionId ?? '',
      artifact.lineage?.programId ?? '',
      artifact.lineage?.taskCallId ?? '',
    ].some((value) => value.toLowerCase().includes(normalizedQuery)))
  }, [artifacts, query])

  const groups = useMemo(() => collectionGroups(visibleArtifacts), [visibleArtifacts])
  const overviewGroup = groups.find((group) => group.key === overviewCollectionKey)
  const selected = overviewGroup
    ? undefined
    : visibleArtifacts.find((artifact) => artifactSelectionKey(artifact) === selectedId) ?? visibleArtifacts[0]
  selectedArtifactKeyRef.current = selected ? artifactSelectionKey(selected) : ''
  iterationDescriptorRef.current = iterationDescriptor
  const selectedGroupKey = overviewGroup?.key ?? (selected ? artifactCollectionKey(visibleArtifacts, selected) : '')
  const selectedGroup = overviewGroup ?? groups.find((group) => group.key === selectedGroupKey)
  const selectedIsWaveGroup = selectedGroupKey.startsWith('turn:') || selectedGroupKey.startsWith('collection:')
  const selectedChainEntries = selected ? desktopV3ArtifactStudioEntries(artifacts, selected) : []
  const selectedHead = selected ? desktopV3ArtifactStudioHead(artifacts, selected) : undefined
  const selectedRounds = selected ? desktopV3ArtifactStudioRounds(artifacts, selected) : []
  const selectedTurns = selected ? desktopV3ArtifactStudioTurns(artifacts, selected) : []
  const latestSelectedTurnId = selectedTurns[selectedTurns.length - 1]?.id ?? ''
  const selectedRound = selected
    ? selectedRounds.find((round) => round.candidates.some((candidate) => artifactSelectionKey(candidate) === artifactSelectionKey(selected)))
    : undefined
  const selectedVariants = selectedIsWaveGroup
    ? selectedGroup?.entries ?? []
    : selectedRound?.candidates ?? selectedGroup?.entries ?? selectedChainEntries
  const selectedVariantIndex = selected
    ? selectedVariants.findIndex((artifact) => artifactSelectionKey(artifact) === artifactSelectionKey(selected))
    : -1
  const durableSelectedArtifact = artifacts.find((artifact) => artifactSelectionKey(artifact) === durableSelectedId)
  const durableSelectionMatchesGroup = durableSelectedArtifact && artifactCollectionKey(visibleArtifacts, durableSelectedArtifact) === selectedGroupKey
  const canonicalSelectedId = durableSelectionMatchesGroup ? durableSelectedId : selectedHead ? artifactSelectionKey(selectedHead) : ''
  const selectedIsCanonical = Boolean(selected && artifactSelectionKey(selected) === canonicalSelectedId)
  const selectedCanAttach = selected?.status === 'ready' && Boolean(selected.collectionId) && (selected.eventSeq ?? 0) > 0
  const selectedCanExportVideoStills = Boolean(selected && desktopV3ArtifactCanExportHTMLStills(selected))
  const selectedIsQueuedForChat = Boolean(selected && chatSelectedIds.includes(artifactSelectionKey(selected)))
  const selectedRequirementLabel = formatDesktopV3ArtifactOutputRequirements(selected?.outputRequirements)
  const selectedAnimationLabel = formatDesktopV3ArtifactAnimationProfile(selected?.animationProfile)
  const selectedAnimationActive = animationPreviewVisible
  const selectedVideoProfileCompatible = !selected?.animationProfile || selected.animationProfile.profileId === 'final_render'
  const iterationSection = iterationDescriptor?.sections.find((section) => section.id === iterationSectionId) ?? iterationDescriptor?.sections[0]
  const selectedPartDefinitions = selected?.partGraphState === 'git_projection'
    ? selected.partDefinitions ?? []
    : selected?.partGraphState === 'legacy_unproven'
      ? (selected.parts ?? []).map((part) => ({ id: part.id, label: part.label, description: part.description, locator: part }))
      : []
  const selectedParts = selectedPartDefinitions.filter((part) => selectedPartIds.includes(part.id))
  const selectedAcceptedPartHeads = selected?.acceptedPartHeads ?? []
  const selectedAcceptedPartCount = selected?.composition?.parts.filter((part) => selectedAcceptedPartHeads.some((accepted) => desktopV3ArtifactStudioSamePartRevision(part, accepted))).length ?? 0
  const studioSectionLineage = selected ? desktopV3ArtifactStudioSectionLineage(artifacts, selected) : undefined
  const studioSectionId = iterationSection?.id ?? studioSectionLineage?.iterationSectionId ?? ''
  const studioModeActive = Boolean(iterationDescriptor || studioSectionId || selectedTurns.length)
  const iterationAlternativesForSection = (sectionId: string) => selected
    ? desktopV3ArtifactStudioSectionAlternatives(artifacts, selected, sectionId)
    : []
  const iterationSectionAlternatives = studioSectionId ? iterationAlternativesForSection(studioSectionId) : []
  const selectedIterationAlternative = selected && iterationSectionAlternatives.some((artifact) => artifactSelectionKey(artifact) === artifactSelectionKey(selected))
    ? selected
    : undefined
  const activeIterationAlternative = iterationSectionAlternatives.find((artifact) => artifactSelectionKey(artifact) === studioActiveBranchId)
    ?? selectedIterationAlternative
    ?? iterationSectionAlternatives[iterationSectionAlternatives.length - 1]
  const iterationRoundSourceArtifact = activeIterationAlternative
    ? (desktopV3ArtifactStudioParent(artifacts, activeIterationAlternative) ?? activeIterationAlternative)
    : selected
  const nextIterationSection = iterationDescriptor && iterationSection
    ? iterationDescriptor.sections[iterationDescriptor.sections.findIndex((section) => section.id === iterationSection.id) + 1]
    : undefined
  const iterationNarration = iterationSection?.narration.find((line) => iterationTimeMs >= line.startMs && iterationTimeMs < line.endMs)
  const attachableSelectedArtifacts = artifacts.filter((artifact) => chatSelectedIds.includes(artifactSelectionKey(artifact))
    && artifact.status === 'ready'
    && Boolean(artifact.collectionId)
    && (artifact.eventSeq ?? 0) > 0)
  const pendingChatArtifacts = attachableSelectedArtifacts.length > 0
    ? attachableSelectedArtifacts
    : selectedCanAttach && selected ? [selected] : []
  const selectedChoiceCount = chatSelectedIds.length || (selectedCanAttach && selected ? 1 : 0)

  useEffect(() => {
    setDurableSelectedId((current) => {
      if (current && artifacts.some((artifact) => artifactSelectionKey(artifact) === current && artifact.selected)) return current
      const persisted = artifacts.find((artifact) => artifact.graphState === 'git_projection'
        && artifact.chain?.head
        && artifact.sessionId === artifact.chain.head.sessionId
        && artifact.collectionId === artifact.chain.head.collectionId
        && artifact.artifactId === artifact.chain.head.variantId
        && artifact.eventSeq === artifact.chain.head.eventSeq)
      return persisted ? artifactSelectionKey(persisted) : ''
    })
    const availableKeys = new Set(artifacts.map(artifactSelectionKey))
    setChatSelectedIds((current) => current.filter((id) => availableKeys.has(id)))
    setStudioActiveBranchId((current) => availableKeys.has(current) ? current : '')
  }, [artifacts])

  useEffect(() => {
    setStagedPartChoices({})
  }, [selectedGroupKey])

  useEffect(() => {
    if (open) return
    setChatSelectedIds([])
    setStagedPartChoices({})
    setOverviewCollectionKey('')
    setActionError('')
    setActionConfirmation('')
    if (iterationPlaybackFrameRef.current !== null) cancelAnimationFrame(iterationPlaybackFrameRef.current)
    iterationPlaybackFrameRef.current = null
    iterationSeekAcknowledgerRef.current?.setOnSettled(null)
    setIterationPlaying(false)
  }, [open])

  useEffect(() => {
    if (iterationPlaybackFrameRef.current !== null) cancelAnimationFrame(iterationPlaybackFrameRef.current)
    iterationPlaybackFrameRef.current = null
    setIterationPlaying(false)
    iterationDescribeRequestRef.current = ''
    iterationDescribeArtifactRef.current = ''
    iterationPlayerArtifactRef.current = ''
    iterationSeekAcknowledgerRef.current?.reset()
    const selectedKey = selected ? artifactSelectionKey(selected) : ''
    const requestedPartId = iterationAutoplayArtifactRef.current === selectedKey ? iterationAutoplaySectionRef.current : ''
    const targetPartId = requestedPartId || studioSectionLineage?.iterationSectionId || ''
    iterationSectionIdRef.current = targetPartId
    iterationTimeMsRef.current = 0
    setIterationDescriptor(null)
    setIterationSectionId(targetPartId)
    setIterationTimeMs(0)
    setIterationPlayerReadyVersion(0)
    setSelectedPartIds(targetPartId && selected && desktopV3ArtifactRevisionHasPart(selected, targetPartId) ? [targetPartId] : [])
  }, [selected?.artifactId, selected?.eventSeq, selected?.sessionId, studioSectionLineage?.iterationSectionId])

  useEffect(() => {
    if (!selected || !iterationDescriptor || iterationPlayerReadyVersion === 0) return
    const selectedKey = artifactSelectionKey(selected)
    if (iterationAutoplayArtifactRef.current === selectedKey && iterationAutoplaySectionRef.current) return
    const targetSectionId = studioSectionLineage?.iterationSectionId ?? ''
    if (!targetSectionId || initialPlaybackArtifactRef.current === selectedKey) return
    const targetSection = iterationDescriptor.sections.find((section) => section.id === targetSectionId)
    if (!targetSection) return
    initialPlaybackArtifactRef.current = selectedKey
    iterationSectionIdRef.current = targetSection.id
    setIterationSectionId(targetSection.id)
    setSelectedPartIds([targetSection.id])
    startIterationSectionPlayback(targetSection, true)
  }, [iterationDescriptor, iterationPlayerReadyVersion, selected?.artifactId, selected?.eventSeq, studioSectionLineage?.iterationSectionId])

  useEffect(() => {
    const receiveIterationDescription = (event: MessageEvent) => {
      if (event.source !== animationFrameRef.current?.contentWindow) return
      const data = event.data && typeof event.data === 'object' && !Array.isArray(event.data) ? event.data as Record<string, unknown> : null
      if (!data || data.protocol !== DESKTOP_V3_ARTIFACT_PLAYER_PROTOCOL || data.ok !== true) return
      if (iterationSeekAcknowledgerRef.current?.acknowledge(data.id)) return
      if (data.id !== iterationDescribeRequestRef.current) return
      const descriptor = normalizeDesktopV3ArtifactIterationDescriptor(data.result)
      iterationDescribeRequestRef.current = ''
      const describedArtifactKey = iterationDescribeArtifactRef.current
      iterationDescribeArtifactRef.current = ''
      if (!descriptor || !describedArtifactKey) return
      iterationPlayerArtifactRef.current = describedArtifactKey
      setIterationDescriptor(descriptor)
      const selectedSection = descriptor.sections.find((section) => section.id === iterationSectionIdRef.current) ?? descriptor.sections[0]
      iterationSectionIdRef.current = selectedSection?.id ?? ''
      setIterationSectionId(iterationSectionIdRef.current)
      iterationTimeMsRef.current = selectedSection?.startMs ?? 0
      setIterationTimeMs(iterationTimeMsRef.current)
      setIterationPlayerReadyVersion((current) => current + 1)
    }
    window.addEventListener('message', receiveIterationDescription)
    return () => window.removeEventListener('message', receiveIterationDescription)
  }, [])

  useEffect(() => () => {
    if (iterationPlaybackFrameRef.current !== null) cancelAnimationFrame(iterationPlaybackFrameRef.current)
  }, [])

  useEffect(() => {
    if (!open || !initialArtifactKey) return
    const requested = desktopV3ArtifactCatalogEntryForKey(artifacts, initialArtifactKey)
    if (requested) {
      const artifactKey = artifactSelectionKey(requested)
      setOverviewCollectionKey('')
      setSelectedId(artifactKey)
      if (initialPartId) {
        const requestKey = `${artifactKey}:${initialPartId}`
        if (initialPartRequestRef.current !== requestKey) {
          initialPartRequestRef.current = requestKey
          initialPartPlaybackRef.current = ''
          iterationAutoplayArtifactRef.current = artifactKey
          iterationAutoplaySectionRef.current = initialPartId
          iterationSectionIdRef.current = initialPartId
          setIterationSectionId(initialPartId)
          setSelectedPartIds([initialPartId])
        }
        const descriptor = iterationDescriptorRef.current
        if (initialPartPlaybackRef.current !== requestKey && selectedArtifactKeyRef.current === artifactKey && descriptor) {
          const targetSection = descriptor.sections.find((section) => section.id === initialPartId)
          if (targetSection) {
            initialPartPlaybackRef.current = requestKey
            iterationAutoplayArtifactRef.current = ''
            iterationAutoplaySectionRef.current = ''
            startIterationSectionPlayback(targetSection, true)
          }
        }
      } else {
        initialPartRequestRef.current = ''
        initialPartPlaybackRef.current = ''
      }
    }
  }, [artifacts, initialArtifactKey, initialPartId, iterationPlayerReadyVersion, open])

  useEffect(() => {
    if (!open || !initialCollectionId || initialArtifactKey) return
    const requested = groups.find((group) => group.entries.some((artifact) => artifact.collectionId === initialCollectionId))
    if (requested) {
      setSelectedId('')
      setOverviewCollectionKey(requested.key)
    }
  }, [groups, initialArtifactKey, initialCollectionId, open])

  useEffect(() => {
    if (!open) return undefined
    const previousOverflow = document.body.style.overflow
    if (presentation === 'fullscreen') document.body.style.overflow = 'hidden'
    backButtonRef.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (!document.fullscreenElement) setOpen(false)
        return
      }
      if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement) return
      if ((event.key === 'ArrowLeft' || event.key === 'ArrowRight') && selectedVariants.length > 1 && selectedVariantIndex >= 0) {
        event.preventDefault()
        const offset = event.key === 'ArrowLeft' ? -1 : 1
        const nextIndex = (selectedVariantIndex + offset + selectedVariants.length) % selectedVariants.length
        const nextArtifact = selectedVariants[nextIndex]
        if (nextArtifact) {
          if (studioModeActive) {
            iterationAutoplayArtifactRef.current = artifactSelectionKey(nextArtifact)
            iterationAutoplaySectionRef.current = iterationSectionIdRef.current
          }
          setSelectedId(artifactSelectionKey(nextArtifact))
          onArtifactNavigate?.(nextArtifact)
        }
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
      if (presentation === 'fullscreen') document.body.style.overflow = previousOverflow
      galleryButtonRef.current?.focus()
    }
  }, [onArtifactNavigate, open, presentation, selectedVariantIndex, selectedVariants, studioModeActive])

  useEffect(() => {
    const syncFullscreenState = () => setPreviewFullscreen(document.fullscreenElement === previewSurfaceRef.current)
    document.addEventListener('fullscreenchange', syncFullscreenState)
    return () => document.removeEventListener('fullscreenchange', syncFullscreenState)
  }, [])

  useEffect(() => {
    if (open) return
    setPreviewFullscreen(false)
  }, [open])

  useEffect(() => {
    if (!open || !selected || !selectedAnimationActive) return undefined
    setPreviewURL('')
    setPreviewText('')
    setPreviewError('')
    setActionError('')
    if (selected.content !== undefined) {
      setPreviewText(selected.content)
      setPreviewLoading(false)
      return undefined
    }
    if (!selected.previewable || selected.status === 'staging' || selected.status === 'failed' || selected.status === 'unavailable') {
      setPreviewLoading(false)
      return undefined
    }
    const controller = new AbortController()
    const isText = selected.mediaType === 'text/markdown' || selected.mediaType === 'text/plain'
    const isHTML = selected.mediaType === 'text/html'
    setPreviewLoading(true)
    const resolvePreview = isHTML
      ? fetchDesktopV3ArtifactPreviewAccess(selected.sessionId, selected.artifactId, controller.signal).then((access) => access.url)
      : isText
        ? fetchDesktopV3ArtifactTextPreview(selected, controller.signal)
        : preflightDesktopV3ArtifactDirectContent(selected, controller.signal)
    void resolvePreview
      .then((value) => {
        if (controller.signal.aborted) return
        if (isHTML || !isText) setPreviewURL(value)
        else setPreviewText(value)
      })
      .catch((error) => {
        if (!controller.signal.aborted) setPreviewError(error instanceof Error ? error.message : 'Artifact preview failed')
      })
      .finally(() => {
        if (!controller.signal.aborted) setPreviewLoading(false)
      })
    return () => controller.abort()
  }, [open, previewRetry, selectedAnimationActive, selected?.artifactId, selected?.content, selected?.mediaType, selected?.previewable, selected?.sessionId, selected?.sourceRef, selected?.status])

  const selectArtifact = (artifact: DesktopV3ArtifactGalleryEntry, navigate = true) => {
    setOverviewCollectionKey('')
    setSelectedId(artifactSelectionKey(artifact))
    if (navigate) onArtifactNavigate?.(artifact)
  }

  const selectFullIterationArtifact = (artifact: DesktopV3ArtifactGalleryEntry) => {
    const artifactKey = artifactSelectionKey(artifact)
    if (studioModeActive) {
      const targetSectionId = iterationSectionIdRef.current
      iterationAutoplayArtifactRef.current = artifactKey
      iterationAutoplaySectionRef.current = targetSectionId
      if (selected && artifactSelectionKey(selected) === artifactKey && iterationDescriptor) {
        const targetSection = iterationDescriptor.sections.find((section) => section.id === targetSectionId)
        if (targetSection) {
          iterationAutoplayArtifactRef.current = ''
          iterationAutoplaySectionRef.current = ''
          startIterationSectionPlayback(targetSection, true)
        }
      }
    }
    selectArtifact(artifact)
  }

  const selectPartIterationArtifact = (artifact: DesktopV3ArtifactGalleryEntry, partId: string) => {
    const artifactKey = artifactSelectionKey(artifact)
    iterationAutoplayArtifactRef.current = artifactKey
    iterationAutoplaySectionRef.current = partId
    iterationSectionIdRef.current = partId
    setIterationSectionId(partId)
    setSelectedPartIds([partId])
    setStudioActiveBranchId(artifactKey)
    if (selected && artifactSelectionKey(selected) === artifactKey && iterationDescriptor) {
      const targetSection = iterationDescriptor.sections.find((section) => section.id === partId)
      if (targetSection) {
        iterationAutoplayArtifactRef.current = ''
        iterationAutoplaySectionRef.current = ''
        startIterationSectionPlayback(targetSection, true)
      }
    }
    selectArtifact(artifact)
  }

  const selectStudioArtifact = (artifact: DesktopV3ArtifactGalleryEntry, section?: DesktopV3ArtifactIterationSection) => {
    const artifactKey = artifactSelectionKey(artifact)
    const targetSectionId = section?.id || desktopV3ArtifactStudioSectionLineage(artifacts, artifact)?.iterationSectionId || iterationSectionIdRef.current
    iterationAutoplayArtifactRef.current = artifactKey
    iterationAutoplaySectionRef.current = targetSectionId
    if (section) {
      iterationSectionIdRef.current = section.id
      setIterationSectionId(section.id)
      setSelectedPartIds(desktopV3ArtifactRevisionHasPart(artifact, section.id) ? [section.id] : [])
    }
    setStudioActiveBranchId(artifactKey)
    if (selected && artifactSelectionKey(selected) === artifactKey && iterationDescriptor) {
      const targetSection = iterationDescriptor.sections.find((candidate) => candidate.id === targetSectionId)
      if (targetSection) {
        iterationAutoplayArtifactRef.current = ''
        iterationAutoplaySectionRef.current = ''
        startIterationSectionPlayback(targetSection, true)
      }
    }
    selectArtifact(artifact)
    if (artifact.status === 'ready' && artifact.collectionId && (artifact.eventSeq ?? 0) > 0 && onActiveBranchChange) {
      const messageSelection = desktopV3ArtifactMessageSelection(artifact, 'use')
      void Promise.resolve(onActiveBranchChange({
        label: messageSelection.label,
        description: [`Active complete artifact branch for section "${desktopV3ArtifactStudioSectionLineage(artifacts, artifact)?.iterationSectionLabel || iterationSection?.label || 'current'}".`, messageSelection.description].filter(Boolean).join(' '),
        selection: desktopV3ArtifactSelection(artifact),
      })).catch((error) => setActionError(error instanceof Error ? error.message : 'Could not attach the active Studio branch'))
    }
  }

  function sendAnimationMessage(type: 'describe' | 'seek' | 'stop', timeMs?: number) {
    const frameWindow = animationFrameRef.current?.contentWindow
    if (!frameWindow) return ''
    const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `artifact-player-${Date.now()}-${Math.random().toString(16).slice(2)}`
    frameWindow.postMessage(desktopV3ArtifactIterationMessage(id, type, timeMs), '*')
    return id
  }

  function requestIterationDescription(artifactKey: string) {
    iterationSeekAcknowledgerRef.current?.reset()
    sendAnimationMessage('stop')
    iterationDescribeArtifactRef.current = artifactKey
    iterationDescribeRequestRef.current = sendAnimationMessage('describe')
  }

  function queueAnimationSeek(timeMs: number) {
    iterationSeekAcknowledgerRef.current?.queue(timeMs)
  }

  function syncIterationSectionToTime(timeMs: number) {
    if (!iterationDescriptor) return
    const section = iterationDescriptor.sections.find((candidate) => timeMs >= candidate.startMs && timeMs < candidate.endMs)
      ?? (timeMs >= iterationDescriptor.durationMs ? iterationDescriptor.sections[iterationDescriptor.sections.length - 1] : undefined)
    if (!section || section.id === iterationSectionIdRef.current) return
    iterationSectionIdRef.current = section.id
    setIterationSectionId(section.id)
    const matchingPart = selected?.parts?.find((part) => part.id === section.id)
      ?? selected?.parts?.find((part) => part.kind === 'temporal' && part.startMs === section.startMs && part.endMs === section.endMs)
    setSelectedPartIds(matchingPart?.id ? [matchingPart.id] : [])
  }

  function seekIteration(timeMs: number) {
    const bounded = iterationDescriptor ? Math.min(iterationDescriptor.durationMs, Math.max(0, Math.round(timeMs))) : Math.max(0, Math.round(timeMs))
    queueAnimationSeek(bounded)
    iterationTimeMsRef.current = bounded
    setIterationTimeMs(bounded)
    syncIterationSectionToTime(bounded)
  }

  function pauseIteration() {
    if (iterationPlaybackFrameRef.current !== null) cancelAnimationFrame(iterationPlaybackFrameRef.current)
    iterationPlaybackFrameRef.current = null
    iterationSeekAcknowledgerRef.current?.setOnSettled(null)
    setIterationPlaying(false)
    seekIteration(iterationTimeMsRef.current)
  }

  function startIterationSectionPlayback(section: DesktopV3ArtifactIterationSection, fromBeginning: boolean) {
    if (iterationPlaybackFrameRef.current !== null) cancelAnimationFrame(iterationPlaybackFrameRef.current)
    iterationPlaybackFrameRef.current = null
    iterationSeekAcknowledgerRef.current?.setOnSettled(null)
    setIterationPlaying(false)
    const startMs = !fromBeginning && iterationTimeMsRef.current >= section.startMs && iterationTimeMsRef.current < section.endMs
      ? iterationTimeMsRef.current
      : section.startMs
    iterationSeekAcknowledgerRef.current?.setOnSettled(() => {
      iterationPlaybackStartMsRef.current = startMs
      iterationPlaybackStartedAtRef.current = performance.now()
      iterationLastUIUpdateRef.current = 0
      setIterationPlaying(true)
      const tick = (now: number) => {
        const nextMs = iterationPlaybackStartMsRef.current + (now - iterationPlaybackStartedAtRef.current)
        if (nextMs >= section.endMs) {
          seekIteration(section.endMs)
          setIterationPlaying(false)
          iterationPlaybackFrameRef.current = null
          return
        }
        queueAnimationSeek(nextMs)
        iterationTimeMsRef.current = nextMs
        syncIterationSectionToTime(nextMs)
        if (now - iterationLastUIUpdateRef.current >= 100) {
          iterationLastUIUpdateRef.current = now
          setIterationTimeMs(nextMs)
        }
        iterationPlaybackFrameRef.current = requestAnimationFrame(tick)
      }
      iterationPlaybackFrameRef.current = requestAnimationFrame(tick)
    })
    seekIteration(startMs)
  }

  function playIterationSection() {
    if (!iterationDescriptor) return
    if (iterationPlaying) {
      pauseIteration()
      return
    }
    const wholeArtifact = { id: 'whole-artifact', label: 'Whole artifact', startMs: 0, endMs: iterationDescriptor.durationMs, narration: [] }
    startIterationSectionPlayback(wholeArtifact, false)
  }

  const selectArtifactPart = (part: NonNullable<DesktopV3ArtifactGalleryEntry['partDefinitions']>[number]) => {
    if (!selected) return
    setSelectedPartIds((current) => current.includes(part.id) ? current.filter((id) => id !== part.id) : [...current, part.id])
    const locator = part.locator
    if (locator?.kind === 'temporal' && locator.endMs > locator.startMs && iterationDescriptor) {
      const matchingSection = iterationDescriptor.sections.find((section) => section.id === part.id)
      if (matchingSection) {
        iterationSectionIdRef.current = matchingSection.id
        setIterationSectionId(matchingSection.id)
      }
      if (matchingSection) startIterationSectionPlayback(matchingSection, true)
    }
  }

  const stagePartChoice = (artifact: DesktopV3ArtifactGalleryEntry, partId: string, locked: boolean) => {
    const slot = artifact.composition?.parts.find((part) => part.partId === partId)
    if (!slot) {
      setActionError(`The exact ${partId} revision is unavailable. Refresh Artifact Studio and try again.`)
      return
    }
    const revision = artifact.partRevisions?.find((candidate) => candidate.reference.partId === partId
      && candidate.reference.partRevisionId === slot.revision.partRevisionId
      && candidate.reference.ownerSessionId === slot.revision.ownerSessionId
      && candidate.reference.digestSha256 === slot.revision.digestSha256)
    if (!revision?.eventSeq) {
      setActionError(`The exact ${partId} revision is unavailable. Refresh Artifact Studio and try again.`)
      return
    }
    setActionError('')
    setStagedPartChoices((current) => ({
      ...current,
      [partId]: { partId, revision: slot.revision, revisionEventSeq: revision.eventSeq, locked },
    }))
  }

  const applyStagedPartChoices = async () => {
    const source = selectedHead ?? selected
    const choices = Object.values(stagedPartChoices)
    if (!source || choices.length === 0) return
    try {
      setActionPending('apply-parts')
      setActionError('')
      setActionConfirmation('')
      await selectDesktopV3ArtifactPartRevisions(source, choices)
      setStagedPartChoices({})
      await onSelectionPersisted?.()
      await refreshOpenDesktopV3ArtifactCatalogs()
      setActionConfirmation(`Applied ${choices.length} exact part choice${choices.length === 1 ? '' : 's'} as one accepted composition.`)
    } catch (error) {
      await onSelectionPersisted?.()
      await refreshOpenDesktopV3ArtifactCatalogs()
      setActionError(error instanceof Error ? `${error.message} Refresh completed; stage your choices again against the current composition.` : 'Part selection conflicted with the current composition. Refresh completed.')
      setStagedPartChoices({})
    } finally {
      setActionPending('')
    }
  }

  const requestPartChanges = async (requestedParts = selectedParts) => {
    const source = selectedHead ?? selected
    if (!source || !onAddToChat || source.status !== 'ready' || !source.collectionId || !(source.eventSeq ?? 0) || requestedParts.length === 0) return
    try {
      setActionPending('ask-part')
      setActionError('')
      await onAddToChat(requestedParts.map((part) => {
        const selection = desktopV3ArtifactPartMessageSelection(source, part.id, 'use')
        return { label: selection.label, description: selection.description, selection }
      }))
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not attach these artifact parts for changes')
    } finally {
      setActionPending('')
    }
  }

  const requestAnotherPartIteration = async (partId: string) => {
    const source = selectedHead ?? selected
    if (!source || !onAddToChat || source.status !== 'ready' || !source.collectionId || !(source.eventSeq ?? 0)) return
    try {
      setActionPending('iterate-part')
      setActionError('')
      const selection = desktopV3ArtifactPartIterationMessageSelection(source, partId, 3)
      await onAddToChat([{ label: selection.label, description: selection.description, selection }])
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not request another iteration for this part')
    } finally {
      setActionPending('')
    }
  }

  const selectIterationSection = (section: DesktopV3ArtifactIterationSection) => {
    iterationSectionIdRef.current = section.id
    setIterationSectionId(section.id)
    setSelectedPartIds(selected && desktopV3ArtifactRevisionHasPart(selected, section.id) ? [section.id] : [])
    iterationAutoplaySectionRef.current = ''
    if (!iterationDescriptor) return
    startIterationSectionPlayback(section, true)
  }

  useEffect(() => {
    if (!selected || iterationPlayerReadyVersion === 0) return
    const artifactKey = iterationAutoplayArtifactRef.current
    const section = resolveDesktopV3ArtifactAutoplaySection(
      { artifactKey, sectionId: iterationAutoplaySectionRef.current },
      artifactSelectionKey(selected),
      iterationPlayerArtifactRef.current,
      iterationDescriptor,
    )
    if (!section || !iterationDescriptor) return
    iterationAutoplayArtifactRef.current = ''
    iterationAutoplaySectionRef.current = ''
    iterationSectionIdRef.current = section.id
    setIterationSectionId(section.id)
    startIterationSectionPlayback(section, true)
  }, [iterationDescriptor, iterationPlayerReadyVersion, selected])

  const requestSectionIteration = async () => {
    if (!iterationRoundSourceArtifact || !iterationSection || !onIterateSection || iterationRoundSourceArtifact.status !== 'ready' || !iterationRoundSourceArtifact.collectionId || !(iterationRoundSourceArtifact.eventSeq ?? 0)) return
    try {
      setActionPending('iterate-section')
      setActionError('')
      const messageSelection = desktopV3ArtifactPartMessageSelection(iterationRoundSourceArtifact, iterationSection.id, 'use')
      await onIterateSection(
        { label: messageSelection.label, description: messageSelection.description, selection: messageSelection },
        desktopV3ArtifactIterationChangeDescription(iterationSection),
        'alternatives',
      )
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not prepare more alternatives for this animation section')
    } finally {
      setActionPending('')
    }
  }

  const requestNextSectionIteration = async () => {
    if (!activeIterationAlternative || !nextIterationSection || !onIterateSection || activeIterationAlternative.status !== 'ready' || !activeIterationAlternative.collectionId || !(activeIterationAlternative.eventSeq ?? 0)) return
    try {
      setActionPending('next-section')
      setActionError('')
      const messageSelection = desktopV3ArtifactMessageSelection(activeIterationAlternative, 'use')
      await onIterateSection(
        { label: messageSelection.label, description: messageSelection.description, selection: desktopV3ArtifactSelection(activeIterationAlternative) },
        desktopV3ArtifactIterationNextSectionDescription(nextIterationSection),
        'next-section',
      )
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not advance this artifact to the next section')
    } finally {
      setActionPending('')
    }
  }

  const attachIterationSection = async (section: DesktopV3ArtifactIterationSection) => {
    if (!selected || !onAddToChat || !selectedCanAttach) return
    try {
      setActionPending('ask-part')
      setActionError('')
      const selection = desktopV3ArtifactPartMessageSelection(selected, section.id, 'use')
      await onAddToChat([{ label: selection.label, description: selection.description, selection }])
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not attach this artifact step for changes')
    } finally {
      setActionPending('')
    }
  }

  const selectAdjacentVariant = (offset: -1 | 1) => {
    if (selectedVariants.length < 2 || selectedVariantIndex < 0) return
    const nextIndex = (selectedVariantIndex + offset + selectedVariants.length) % selectedVariants.length
    const nextArtifact = selectedVariants[nextIndex]
    if (nextArtifact) selectFullIterationArtifact(nextArtifact)
  }

  const selectCollection = (group: ArtifactCollectionGroup) => {
    const target = collectionLandingArtifact(group)
    if (!target) return
    setSelectedId('')
    setOverviewCollectionKey(group.key)
    onCollectionNavigate?.(target)
  }

  const openArtifactLink = (event: MouseEvent<HTMLAnchorElement>, artifact: DesktopV3ArtifactGalleryEntry) => {
    if (!onArtifactNavigate || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    selectArtifact(artifact)
  }

  const openCollectionLink = (event: MouseEvent<HTMLAnchorElement>, group: ArtifactCollectionGroup) => {
    if (!onCollectionNavigate || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    selectCollection(group)
  }

  const togglePreviewFullscreen = async () => {
    const previewSurface = previewSurfaceRef.current
    if (!previewSurface) return
    try {
      setActionError('')
      if (document.fullscreenElement === previewSurface) await document.exitFullscreen()
      else await previewSurface.requestFullscreen()
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not open the artifact preview fullscreen')
    }
  }

  const triggerBlobDownload = (blob: Blob, filename: string) => {
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    anchor.style.display = 'none'
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 60_000)
  }

  const downloadArtifact = async (artifact: DesktopV3ArtifactGalleryEntry) => {
    try {
      setPreviewError('')
      triggerBlobDownload(await fetchDesktopV3ArtifactDownload(artifact), desktopV3ArtifactDownloadName(artifact))
    } catch (error) {
      setPreviewError(error instanceof Error ? error.message : 'Artifact download failed')
    }
  }

  const downloadCollection = async (group: ArtifactCollectionGroup) => {
    const artifact = group.entries.find((entry) => entry.collectionId && entry.status === 'ready')
    if (!artifact?.collectionId) return
    try {
      setActionPending('download-collection')
      setActionError('')
      const blob = await fetchDesktopV3ArtifactCollectionBundle(artifact.sessionId, artifact.collectionId)
      const filename = `${collectionDisplayLabel(group).replace(/[^a-z0-9._-]+/gi, '-').replace(/^[.-]+|[.-]+$/g, '') || 'artifact-collection'}.zip`
      triggerBlobDownload(blob, filename)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Artifact collection download failed')
    } finally {
      setActionPending('')
    }
  }

  const revealArtifact = async (artifact: DesktopV3ArtifactGalleryEntry) => {
    try {
      setActionPending('reveal-artifact')
      setActionError('')
      setActionConfirmation('')
      const result = await revealDesktopV3Artifact(artifact.sessionId, artifact.artifactId)
      setActionConfirmation(`Opened ${result.displayLocation}`)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not open this artifact in the native file manager')
    } finally {
      setActionPending('')
    }
  }

  const revealCollection = async (group: ArtifactCollectionGroup) => {
    const artifact = group.entries.find((entry) => entry.collectionId && entry.status === 'ready')
    if (!artifact?.collectionId) return
    try {
      setActionPending('reveal-collection')
      setActionError('')
      setActionConfirmation('')
      const result = await revealDesktopV3ArtifactCollection(artifact.sessionId, artifact.collectionId)
      setActionConfirmation(`Opened ${result.displayLocation}`)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not open this collection in the native file manager')
    } finally {
      setActionPending('')
    }
  }

  const toggleChatSelection = (artifact: DesktopV3ArtifactGalleryEntry) => {
    if (artifact.status !== 'ready' || !artifact.collectionId || (artifact.eventSeq ?? 0) <= 0) return
    const key = artifactSelectionKey(artifact)
    setActionError('')
    setChatSelectedIds((current) => {
      if (current.includes(key)) return current.filter((id) => id !== key)
      if (current.length >= DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT) {
        setActionError(`Select at most ${DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT} artifacts per message.`)
        return current
      }
      return [...current, key]
    })
  }

  const emitAddToChat = async () => {
    if (pendingChatArtifacts.length === 0 || !onAddToChat) return false
    try {
      setActionPending('add')
      setActionError('')
      await onAddToChat(pendingChatArtifacts.map((artifact) => {
        if (selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected) && selectedPartIds.length === 1 && desktopV3ArtifactRevisionHasPart(artifact, selectedPartIds[0]!)) {
          const selection = desktopV3ArtifactPartMessageSelection(artifact, selectedPartIds[0]!, 'select')
          return { label: selection.label, description: selection.description, selection }
        }
        const selection = desktopV3ArtifactMessageSelection(artifact, 'select')
        return { label: selection.label, description: selection.description, selection }
      }))
      setChatSelectedIds([])
      setOpen(false)
      return true
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not add the artifacts to chat')
      return false
    } finally {
      setActionPending('')
    }
  }

  const requestVideoStillExport = async () => {
    if (!selected || !onExportVideoStills || !desktopV3ArtifactCanExportHTMLStills(selected)) return
    try {
      setActionPending('export-video-stills')
      setActionError('')
      setActionConfirmation('')
      const messageSelection = desktopV3ArtifactMessageSelection(selected, 'select')
      await onExportVideoStills({ label: messageSelection.label, description: messageSelection.description, selection: desktopV3ArtifactSelection(selected) }, DESKTOP_V3_HTML_STILL_EXPORT_PROMPT)
      await refreshOpenDesktopV3ArtifactCatalogs()
      setActionConfirmation('Video-still export request added to chat. The managed PNGs will appear here when ready.')
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not request video-ready still export')
    } finally {
      setActionPending('')
    }
  }

  const persistAndUseDesign = async () => {
    if (!selected || !onUseThisDesign) return
    try {
      setActionPending('use')
      setActionError('')
      if (selected.graphState !== 'git_projection' || !selected.artifactChainId || !selected.artifactStepId) {
        throw new Error('This legacy artifact is unstructured and cannot advance a canonical head')
      }
      const canonicalSelection = await useDesktopV3Artifact(desktopV3ArtifactSelection(selected))
      setDurableSelectedId(artifactSelectionKey(selected))
      await onSelectionPersisted?.()
      const messageSelection = desktopV3ArtifactMessageSelection(selected, 'use')
      await onUseThisDesign({ label: messageSelection.label, description: messageSelection.description, selection: canonicalSelection })
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not use this design')
    } finally {
      setActionPending('')
    }
  }

  const renderStudioBranch = (artifact: DesktopV3ArtifactGalleryEntry, alternativeIndex: number, section: DesktopV3ArtifactIterationSection) => {
    const branchSelected = selected ? artifactSelectionKey(artifact) === artifactSelectionKey(selected) : false
    const activeBranch = artifactSelectionKey(artifact) === studioActiveBranchId
    return <div key={artifactSelectionKey(artifact)} className={cn('flex min-w-0 items-stretch rounded-lg', branchSelected ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]')} data-artifact-studio-branch>
      <button type="button" className="flex min-w-0 flex-1 items-start gap-2 rounded-lg px-2.5 py-2 text-left text-[10px]" aria-current={branchSelected ? 'true' : undefined} onClick={() => selectStudioArtifact(artifact, section)}>
        <span className="grid size-4 shrink-0 place-items-center rounded-full border border-[var(--app-border)] font-mono text-[9px]">{alternativeIndex + 1}</span>
        <span className="min-w-0 flex-1 break-words leading-4">{variantDisplayLabel(artifact, alternativeIndex)}</span>
        {activeBranch ? <Check className="mt-0.5 size-3 shrink-0 text-[var(--app-primary)]" aria-label="Active branch" /> : null}
      </button>
    </div>
  }

  const renderCollection = (group: ArtifactCollectionGroup) => {
    const active = group.key === selectedGroupKey
    const partialFailure = group.progress.failed + group.progress.unavailable
    const landingArtifact = collectionLandingArtifact(group)
    const requirementLabel = formatDesktopV3ArtifactOutputRequirements(group.entries[0]?.outputRequirements)
    return (
      <div key={group.key} className="min-w-0">
        <div
          className={cn(
            'w-64 shrink-0 rounded-xl border px-3 py-2.5 text-left transition md:w-full',
            active
              ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_8%,var(--app-surface))]'
              : 'border-transparent hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]',
          )}
        >
          <div className="flex min-w-0 items-center justify-between gap-2"><button type="button" className="min-w-0 flex-1 text-left" aria-expanded={active} onClick={() => selectCollection(group)}><span className="block truncate text-xs font-semibold text-[var(--app-text)]">{collectionDisplayLabel(group)}</span><span className="mt-0.5 block truncate text-[10px] text-[var(--app-text-subtle)]">{group.sessionLabel} · {group.progress.total} variant{group.progress.total === 1 ? '' : 's'}</span>{requirementLabel ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-muted)]" data-artifact-output-requirements>{requirementLabel}</span> : null}</button>{collectionHref && landingArtifact?.collectionId ? <a href={collectionHref(landingArtifact)} className="shrink-0 rounded px-1.5 py-1 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]" aria-label={`Open unique URL for ${collectionDisplayLabel(group)}`} onClick={(event) => openCollectionLink(event, group)}>Group URL</a> : null}</div>
          <button type="button" className="mt-2 flex w-full flex-wrap gap-1 text-left" aria-label="Collection progress" onClick={() => selectCollection(group)}>
            {group.progress.staging > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-primary-soft)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-primary)]"><Loader2 className="size-2.5 animate-spin" />{group.progress.staging} generating</span> : null}
            {group.progress.ready > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-success-bg)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-success)]"><Check className="size-2.5" />{group.progress.ready} ready</span> : null}
            {partialFailure > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-danger-bg)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-danger)]"><AlertTriangle className="size-2.5" />{partialFailure} failed</span> : null}
          </button>
        </div>
        {active ? (
          <div className="mt-1 grid gap-1 border-l border-[var(--app-border)] pl-2" aria-label="Collection variants">
            {group.entries.map((artifact, index) => {
              const artifactActive = selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected)
              const canonical = artifactSelectionKey(artifact) === canonicalSelectedId
              const chatSelected = chatSelectedIds.includes(artifactSelectionKey(artifact))
              const canSelectForChat = artifact.status === 'ready' && Boolean(artifact.collectionId) && (artifact.eventSeq ?? 0) > 0
              return (
                <div
                  key={artifactSelectionKey(artifact)}
                  className={cn('flex min-w-0 items-center gap-1 rounded-lg transition', artifactActive ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]')}
                >
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
                    aria-current={artifactActive ? 'page' : undefined}
                    onClick={() => selectArtifact(artifact)}
                  >
                    <span className="grid size-5 shrink-0 place-items-center rounded border border-[var(--app-border)] text-[9px] font-semibold">{artifact.lineage?.iterationIndex || index + 1}</span>
                    <span className="min-w-0 flex-1 truncate">{iterationDisplayLabel(artifact, index)}</span>
                    {artifact.status === 'staging' ? <Clock3 className="size-3 shrink-0 text-[var(--app-primary)]" aria-label="Generating" /> : null}
                    {artifact.status === 'failed' || artifact.status === 'unavailable' ? <AlertTriangle className="size-3 shrink-0 text-[var(--app-danger)]" aria-label={artifactStatusLabel(artifact)} /> : null}
                    {canonical ? <Check className="size-3.5 shrink-0 text-[var(--app-success)]" aria-label="Selected design" /> : null}
                  </button>
                  {artifactHref ? <a href={artifactHref(artifact)} className="shrink-0 rounded px-1.5 py-1 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]" aria-label={`Open unique URL for ${artifact.label}`} onClick={(event) => openArtifactLink(event, artifact)}>URL</a> : null}
                  <button
                    type="button"
                    className={cn('mr-1 grid size-7 shrink-0 place-items-center rounded-md border transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]', chatSelected ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-subtle)]')}
                    aria-label={`${chatSelected ? 'Remove' : 'Select'} ${artifact.label} ${chatSelected ? 'from' : 'for'} chat`}
                    aria-pressed={chatSelected}
                    disabled={!canSelectForChat || (!chatSelected && chatSelectedIds.length >= DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT)}
                    onClick={() => toggleChatSelection(artifact)}
                    data-artifact-chat-selection
                  >
                    {chatSelected ? <Check className="size-3.5" aria-hidden="true" /> : <MessageSquarePlus className="size-3.5" aria-hidden="true" />}
                  </button>
                </div>
              )
            })}
          </div>
        ) : null}
      </div>
    )
  }

  const workspaceGroups = organization === 'workspace'
    ? Array.from(new Map(groups.map((group) => [group.workspaceLabel, group.workspaceLabel])).keys())
    : []
  const triggerArtifact = groups.map(collectionLandingArtifact).find((artifact) => artifact?.graphState === 'git_projection')
    ?? artifacts[0]

  if (!showTrigger && !open) return null
  if (showTrigger && artifacts.length === 0) return null

  const portalTarget = presentation === 'embedded'
    ? providedEmbeddedPortalTarget
    : typeof document !== 'undefined' ? document.body : null

  return (
    <div
      className={showTrigger ? 'mt-3' : undefined}
      data-final-handoff-artifacts={showTrigger ? true : undefined}
    >
      {showTrigger ? <button ref={galleryButtonRef} type="button" className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => { if (onTriggerOpen && triggerArtifact) onTriggerOpen(triggerArtifact); else setOpen(true) }}><GalleryHorizontal size={14} aria-hidden="true" />Open gallery ({artifacts.length})</button> : null}
      {open && portalTarget ? createPortal(
        <section aria-label="Artifact collection review" className={cn('flex w-full min-h-0 min-w-0 flex-col overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]', presentation === 'embedded' ? 'absolute inset-0 h-full' : 'fixed inset-0 z-[85] h-[100dvh] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] pt-[env(safe-area-inset-top)]')} data-artifact-gallery-page data-artifact-review-surface data-artifact-studio-review={presentation === 'embedded' || undefined}>
          <header className="flex min-h-12 shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 sm:min-h-14 sm:px-5 sm:py-2">
            <button ref={backButtonRef} type="button" className="inline-flex shrink-0 items-center gap-2 rounded-lg px-2.5 py-2 text-sm font-medium text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => setOpen(false)}><ArrowLeft size={16} aria-hidden="true" />{presentation === 'embedded' ? <span>{backLabel}</span> : <><span className="hidden sm:inline">{backLabel}</span><span className="sm:hidden">Back</span></>}</button>
            <div className="min-w-0 text-center"><div className="flex items-center justify-center gap-2 text-sm font-semibold"><GalleryHorizontal size={16} aria-hidden="true" /> {title}</div><div className="text-[10px] text-[var(--app-text-subtle)]">Live collections · {groups.length}</div></div>
            <button type="button" className="grid size-9 shrink-0 place-items-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" aria-label="Exit artifact gallery" onClick={() => setOpen(false)}><X size={17} /></button>
          </header>
          <div className="hidden flex-wrap items-center gap-2 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:px-5 md:flex">
            <label className="relative min-w-[14rem] flex-1"><span className="sr-only">Search artifact collections</span><Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-[var(--app-text-subtle)]" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search collections, variants, sessions, or types" className="h-9 w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-8 pr-3 text-xs outline-none focus:border-[var(--app-border-active)]" /></label>
            <div className="inline-flex h-9 rounded-lg border border-[var(--app-border)] p-0.5" aria-label="Collection grouping"><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'collection' && 'bg-[var(--app-surface-active)] font-semibold')} aria-pressed={organization === 'collection'} onClick={() => setOrganization('collection')}>Collections</button><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'workspace' && 'bg-[var(--app-surface-active)] font-semibold')} aria-pressed={organization === 'workspace'} onClick={() => setOrganization('workspace')}>By workspace</button></div>
          </div>
          <div className={cn('grid min-h-0 flex-1 grid-cols-1', presentation !== 'embedded' && !studioModeActive && 'md:grid-cols-[310px_minmax(0,1fr)] md:grid-rows-1')}>
            <aside className={cn('hidden min-w-0 bg-[var(--app-surface)]', presentation !== 'embedded' && !studioModeActive && 'md:block md:min-h-0 md:overflow-y-auto md:border-r md:border-[var(--app-border)] md:p-3')} aria-label="Artifact collections" data-artifact-collection-sidebar>
              {catalogLoading ? <div className="flex items-center gap-2 p-3 text-xs text-[var(--app-text-muted)]"><Loader2 className="size-4 animate-spin" />Loading live collections…</div> : null}
              {catalogError ? <div className="m-2 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]">{catalogError}</div> : null}
              {!catalogLoading && !catalogError && groups.length === 0 ? <div className="p-4 text-center text-xs text-[var(--app-text-muted)]">No collections match this search.</div> : null}
              <div className="flex gap-2 md:grid">
                {organization === 'collection'
                  ? groups.map(renderCollection)
                  : workspaceGroups.map((workspace) => <section key={workspace} className="min-w-0"><h2 className="truncate px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">{workspace}</h2><div className="flex gap-2 md:grid">{groups.filter((group) => group.workspaceLabel === workspace).map(renderCollection)}</div></section>)}
              </div>
            </aside>
            <main className={cn('grid min-h-0 min-w-0 bg-[var(--app-bg-alt)]', iterationDescriptor ? 'grid-rows-[auto_minmax(0,1fr)_auto_auto]' : 'grid-rows-[auto_minmax(0,1fr)_auto]')} data-mobile-artifact-three-zone-layout>
              {overviewGroup ? (
                <div className="row-span-3 min-h-0 overflow-y-auto p-4 sm:p-6" data-artifact-collection-overview>
                  <div className="mx-auto max-w-6xl">
                    <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2"><GalleryHorizontal className="size-5 text-[var(--app-primary)]" aria-hidden="true" /><h1 className="text-lg font-semibold">{collectionDisplayLabel(overviewGroup)}</h1></div>
                        {overviewGroup.entries[0]?.collectionDescription ? <p className="mt-1 text-sm text-[var(--app-text-muted)]">{overviewGroup.entries[0].collectionDescription}</p> : null}
                        <p className="mt-1 text-xs text-[var(--app-text-subtle)]">{overviewGroup.progress.total} variant{overviewGroup.progress.total === 1 ? '' : 's'} · {overviewGroup.progress.ready} ready</p>
                      </div>
                      <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
                        {formatDesktopV3ArtifactOutputRequirements(overviewGroup.entries[0]?.outputRequirements) ? <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">{formatDesktopV3ArtifactOutputRequirements(overviewGroup.entries[0]?.outputRequirements)}</span> : null}
                        {overviewGroup.entries.some((artifact) => artifactCanReveal(artifact) && Boolean(artifact.collectionId)) ? <button type="button" className="inline-flex h-9 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-xs font-semibold hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50" disabled={overviewGroup.progress.ready === 0 || Boolean(actionPending)} title="Publish verified working copies to the local cache and open the folder" onClick={() => void revealCollection(overviewGroup)} data-artifact-reveal-collection>{actionPending === 'reveal-collection' ? <Loader2 className="size-4 animate-spin" /> : <FolderOpen className="size-4" />}Show in folder</button> : null}
                        <button type="button" className="inline-flex h-9 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-xs font-semibold hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50" disabled={overviewGroup.progress.ready === 0 || Boolean(actionPending)} onClick={() => void downloadCollection(overviewGroup)} data-artifact-download-collection>{actionPending === 'download-collection' ? <Loader2 className="size-4 animate-spin" /> : <Download className="size-4" />}Download all ready ({overviewGroup.progress.ready})</button>
                      </div>
                    </div>
                    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label="Collection variants">
                      {overviewGroup.entries.map((artifact, index) => {
                        const canonical = artifactSelectionKey(artifact) === canonicalSelectedId
                        return <button key={artifactSelectionKey(artifact)} type="button" className="group min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-left transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => selectArtifact(artifact)}>
                          <div className="flex items-start justify-between gap-3"><span className="grid size-8 shrink-0 place-items-center rounded-lg bg-[var(--app-primary-soft)] text-xs font-semibold text-[var(--app-primary)]">{artifact.lineage?.iterationIndex || index + 1}</span><span className={cn('rounded-full px-2 py-0.5 text-[10px] font-semibold', artifact.status === 'failed' || artifact.status === 'unavailable' ? 'bg-[var(--app-danger-bg)] text-[var(--app-danger)]' : artifact.status === 'staging' ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'bg-[var(--app-success-bg)] text-[var(--app-success)]')}>{artifactStatusLabel(artifact)}</span></div>
                          <div className="mt-3 truncate text-sm font-semibold">{variantDisplayLabel(artifact, index)}</div>
                          {artifact.description && artifact.description !== artifact.collectionDescription && artifact.description !== artifact.label ? <p className="mt-1 line-clamp-2 text-xs text-[var(--app-text-muted)]">{artifact.description}</p> : null}
                          <div className="mt-3 flex items-center justify-between gap-2 text-[10px] text-[var(--app-text-subtle)]"><span>{artifactTypeLabel(artifact)}</span>{canonical ? <span className="inline-flex items-center gap-1 text-[var(--app-success)]"><Check className="size-3" />Selected design</span> : <span className="text-[var(--app-primary)]">Open variant</span>}</div>
                        </button>
                      })}
                    </div>
                  </div>
                </div>
              ) : selected ? <>
                <div className={cn('min-w-0 shrink-0 border-b border-[var(--app-border)] bg-[var(--app-surface)]', presentation !== 'embedded' && 'md:hidden')} data-mobile-generation-selector>
                  {groups.length > 1 ? <div className="flex h-9 min-w-0 items-center gap-1 overflow-x-auto border-b border-[var(--app-border)] px-2" aria-label="Select artifact collection">
                    {groups.map((group) => {
                      const active = group.key === selectedGroupKey
                      return <button key={group.key} type="button" className={cn('h-7 max-w-[12rem] shrink-0 truncate rounded-md px-2.5 text-[10px] font-semibold', active ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]')} aria-pressed={active} onClick={() => selectCollection(group)}>{collectionDisplayLabel(group)}</button>
                    })}
                  </div> : null}
                  <div className="flex h-11 min-w-0 items-center gap-1.5 overflow-x-auto px-2" aria-label="Select generation">
                    {selectedVariants.map((artifact, index) => {
                      const active = artifactSelectionKey(artifact) === artifactSelectionKey(selected)
                      return <button key={artifactSelectionKey(artifact)} type="button" className={cn('inline-flex h-8 max-w-[13rem] shrink-0 items-center gap-1.5 rounded-lg border px-2.5 text-[11px] font-semibold', active ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)]')} aria-current={active ? 'true' : undefined} onClick={() => selectFullIterationArtifact(artifact)}><span className="grid size-4 shrink-0 place-items-center rounded bg-[var(--app-surface)] text-[9px]">{artifact.lineage?.iterationIndex || index + 1}</span><span className="truncate">{variantDisplayLabel(artifact, index)}</span></button>
                    })}
                  </div>
                </div>
                <div className={cn('hidden flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2.5 sm:px-4', presentation !== 'embedded' && 'md:flex')}>
                  <div className="min-w-0 flex-1"><div className="flex min-w-0 flex-wrap items-center gap-2"><div className="truncate text-sm font-semibold">{variantDisplayLabel(selected, Math.max(selectedVariantIndex, 0))}</div><span className="shrink-0 rounded border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-1.5 py-0.5 text-[9px] font-semibold uppercase">{artifactTypeLabel(selected)}</span>{selectedIsCanonical ? <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-[var(--app-success-bg)] px-2 py-0.5 text-[10px] font-semibold text-[var(--app-success)]" data-artifact-selected-design><Check className="size-3" />Selected design</span> : null}{selected.status && selected.status !== 'ready' ? <span className={cn('rounded-full px-2 py-0.5 text-[10px] font-semibold', selected.status === 'staging' ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'bg-[var(--app-danger-bg)] text-[var(--app-danger)]')}>{artifactStatusLabel(selected)}</span> : null}</div>{selected.description && selected.description !== selected.label && selected.description !== selected.collectionDescription ? <p className="truncate text-xs text-[var(--app-text-muted)]">{selected.description}</p> : null}<p className="truncate text-[10px] text-[var(--app-text-subtle)]">{selectedGroup ? collectionDisplayLabel(selectedGroup) : selected.sessionTitle}{selectedVariantIndex >= 0 ? ` · Variant ${selectedVariantIndex + 1} of ${selectedVariants.length}` : ''}</p>{selectedRequirementLabel ? <p className="truncate text-[10px] font-medium text-[var(--app-text-muted)]" data-artifact-output-requirements title="Requested output target; not measured binary dimensions">{selectedRequirementLabel}</p> : null}{selectedAnimationLabel ? <p className="truncate text-[10px] font-medium text-[var(--app-text-muted)]" data-artifact-animation-profile-label>{selectedAnimationLabel}</p> : null}</div>
                  <div className="flex shrink-0 items-center gap-1.5">{artifactHref ? <a href={artifactHref(selected)} className="inline-flex h-8 items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-[10px] font-semibold hover:bg-[var(--app-surface-hover)]" onClick={(event) => openArtifactLink(event, selected)}>Open iteration URL</a> : null}<button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={selectedVariants.length < 2} aria-label="Previous artifact" onClick={() => selectAdjacentVariant(-1)}><ChevronLeft size={15} /></button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={selectedVariants.length < 2} aria-label="Next artifact" onClick={() => selectAdjacentVariant(1)}><ChevronRight size={15} /></button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)]" aria-label="View artifact fullscreen" onClick={() => void togglePreviewFullscreen()}><Maximize2 size={14} /></button>{artifactCanReveal(selected) ? <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs font-medium hover:bg-[var(--app-surface-hover)] disabled:opacity-50" disabled={Boolean(actionPending)} title="Open this artifact in the native file manager" onClick={() => void revealArtifact(selected)}>{actionPending === 'reveal-artifact' ? <Loader2 size={13} className="animate-spin" /> : <FolderOpen size={13} />} <span className="hidden sm:inline">Show in folder</span></button> : null}{selected.content === undefined && selected.status !== 'staging' && selected.status !== 'failed' && selected.status !== 'unavailable' ? <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs font-medium hover:bg-[var(--app-surface-hover)]" onClick={() => void downloadArtifact(selected)}><Download size={13} /> <span className="hidden sm:inline">{desktopV3ArtifactRequiresBundle(selected) ? 'Download bundle' : 'Download file'}</span></button> : null}</div>
                </div>
                <div
                  ref={(node) => {
                    previewSurfaceRef.current = node
                    animationPreviewRef(node)
                  }}
                  className={cn(
                    'relative h-full min-h-0 min-w-0',
                    selected.mediaType.startsWith('image/') || selected.mediaType.startsWith('video/') || selected.kind === 'video' || selected.mediaType === 'text/html' || selected.mediaType === 'application/pdf' ? 'overflow-hidden' : 'overflow-auto',
                    selected.mediaType === 'text/html' || selected.mediaType === 'application/pdf' ? 'p-0' : 'p-3 sm:p-4',
                    studioModeActive && !previewFullscreen && 'overflow-x-hidden md:pl-72 xl:pl-80',
                    previewFullscreen && 'h-[100dvh] w-[100dvw] flex-none bg-[var(--app-bg-alt)]',
                  )}
                  data-artifact-preview-surface
                  data-artifact-animation-active={selectedAnimationActive || undefined}
                >
                  {studioModeActive && !previewFullscreen ? <aside className="absolute inset-y-0 left-0 z-10 hidden w-72 overflow-x-hidden overflow-y-auto border-r border-[var(--app-border)] bg-[var(--app-surface)] p-3 xl:w-80 md:block" aria-label="Artifact Studio steps" data-artifact-studio-step-sidebar data-artifact-studio-unified-iterations>
                    {selectedIsWaveGroup && selectedGroup && selectedVariants.length > 1 ? <section className="sticky top-0 z-20 mb-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-2 shadow-sm" aria-label="Generation group" data-artifact-generation-group>
                      <div className="flex min-w-0 items-start justify-between gap-2 px-1 pb-1.5"><span className="min-w-0"><span className="block truncate text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Generation group</span><span className="mt-0.5 block truncate text-[10px] text-[var(--app-text-muted)]">{collectionDisplayLabel(selectedGroup)}</span></span><span className="shrink-0 rounded-full bg-[var(--app-primary-soft)] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-primary)]">{selectedVariants.length} iterations</span></div>
                      <div className="grid max-h-44 gap-1 overflow-y-auto pr-1" aria-label="Generation group iterations">
                        {selectedVariants.map((artifact, index) => {
                          const active = selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected)
                          return <button key={artifactSelectionKey(artifact)} type="button" className={cn('flex min-w-0 items-center gap-2 rounded-lg border px-2 py-1.5 text-left text-[10px] transition', active ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]')} aria-current={active ? 'true' : undefined} onClick={() => selectFullIterationArtifact(artifact)}>
                            <span className="grid size-5 shrink-0 place-items-center rounded border border-current/20 font-mono text-[9px]">{artifact.lineage?.iterationIndex || index + 1}</span>
                            <span className="min-w-0 flex-1 truncate font-semibold">{variantDisplayLabel(artifact, index)}</span>
                            <span className="shrink-0 text-[9px] opacity-75">{artifact.status === 'staging' ? 'Generating' : artifact.status === 'failed' || artifact.status === 'unavailable' ? 'Failed' : active ? 'Viewing' : 'Ready'}</span>
                          </button>
                        })}
                      </div>
                    </section> : null}
                    {selectedTurns.length ? <section className="mb-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] p-2" aria-label="Artifact turn progression" data-artifact-studio-turns>
                      <div className="px-1 pb-1.5"><p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Artifact progression</p><p className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">Every AI creation round is one turn. Compare complete iterations here; changed parts stay summarized inside the turn.</p></div>
                      <div className="grid gap-2">{selectedTurns.map((turn) => {
                        const currentTurn = turn.id === latestSelectedTurnId
                        const turnTarget = turn.accepted?.entry ?? turn.candidates.find((candidate) => candidate.entry)?.entry
                        const turnRelatedPart = turn.relatedTargets.length === 1 ? turn.relatedTargets[0] : undefined
                        const turnRelatedLabel = turn.relatedTargets.map((target) => target.label).join(', ')
                        const headTurn = turn.candidates.some((candidate) => Boolean(selectedHead && candidate.entry
                          && artifactSelectionKey(candidate.entry) === artifactSelectionKey(selectedHead)))
                        return <section key={turn.id} className={cn('overflow-hidden rounded-lg border', selectedRound?.id === turn.id ? 'border-[var(--app-primary)]' : 'border-[var(--app-border)]')} data-artifact-studio-turn={turn.id}>
                          <button type="button" className="flex w-full items-start justify-between gap-2 bg-[var(--app-surface)] px-2 py-1.5 text-left hover:bg-[var(--app-surface-hover)] disabled:opacity-50" disabled={!turnTarget} onClick={() => { if (!turnTarget) return; if (turnRelatedPart) selectPartIterationArtifact(turnTarget, turnRelatedPart.partId); else selectFullIterationArtifact(turnTarget) }}><span className="min-w-0"><span className="block text-[10px] font-semibold">Turn {turn.revisionNumber}</span>{turnRelatedLabel ? <span className="mt-0.5 block truncate text-[9px] font-semibold text-[var(--app-primary)]" data-artifact-studio-turn-related-parts>Related to {turnRelatedLabel}</span> : null}</span><span className="shrink-0 text-right text-[9px] text-[var(--app-text-subtle)]">{currentTurn ? 'Latest turn' : 'Turn history'} · {headTurn ? 'Composition head' : turn.accepted ? 'Decision recorded' : 'No decision'} · {turn.candidates.length} option{turn.candidates.length === 1 ? '' : 's'}</span></button>
                          {turn.parts.length === 1 ? <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5"><p className="px-0.5 text-[9px] text-[var(--app-text-subtle)]">1 changed part</p>{turn.parts.map((turnPart) => {
                            const definition = selectedPartDefinitions.find((part) => part.id === turnPart.partId)
                            return <section key={turnPart.partId} className="overflow-hidden rounded-md border border-[var(--app-border)]" data-artifact-studio-turn-part={turnPart.partId}>
                              <div className="flex items-center gap-1 px-1 py-0.5"><button type="button" className="flex min-w-0 flex-1 items-center justify-between gap-2 px-1 py-0.5 text-left hover:bg-[var(--app-surface-hover)]" onClick={() => { const target = turnPart.accepted?.entry ?? turnPart.candidates.find((candidate) => candidate.entry)?.entry; if (target) selectPartIterationArtifact(target, turnPart.partId) }}><span className="truncate text-[9px] font-semibold">{definition?.label || turnPart.partId}</span><span className="shrink-0 text-[9px] text-[var(--app-text-subtle)]">{definition?.locator?.kind === 'temporal' ? `${formatDesktopV3ArtifactIterationTime(definition.locator.startMs)} · play here` : definition?.locator?.kind || 'part'}</span></button>{onAddToChat ? <button type="button" className="shrink-0 rounded border border-[var(--app-border)] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)] disabled:opacity-50" disabled={Boolean(actionPending)} onClick={() => void requestAnotherPartIteration(turnPart.partId)} data-artifact-iterate-part>{actionPending === 'iterate-part' ? <Loader2 className="mr-1 inline size-2.5 animate-spin" /> : <Sparkles className="mr-1 inline size-2.5" />}Iterate again</button> : null}</div>
                              <div className="grid gap-1 border-t border-[var(--app-border)] p-1">{turnPart.candidates.map((candidate, index) => {
                                const artifact = candidate.entry
                                const viewed = Boolean(artifact && selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected))
                                const accepted = Boolean(turnPart.accepted && candidate.reference.sessionId === turnPart.accepted.reference.sessionId && candidate.reference.collectionId === turnPart.accepted.reference.collectionId && candidate.reference.variantId === turnPart.accepted.reference.variantId && candidate.reference.eventSeq === turnPart.accepted.reference.eventSeq)
                                const staged = stagedPartChoices[turnPart.partId]
                                const stagedHere = Boolean(candidate.part && staged?.revision.partRevisionId === candidate.part.revision.partRevisionId && staged.revision.ownerSessionId === candidate.part.revision.ownerSessionId)
                                return <div key={`${candidate.reference.sessionId}:${candidate.reference.variantId}:${turnPart.partId}`} className={cn('overflow-hidden rounded border border-[var(--app-border)]', viewed ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]')}>
                                  <button type="button" className="flex w-full min-w-0 items-center gap-2 px-2 py-1.5 text-left text-[9px] hover:bg-[var(--app-surface-hover)]" disabled={!artifact} onClick={() => { if (artifact) selectPartIterationArtifact(artifact, turnPart.partId) }}><span className="grid size-4 shrink-0 place-items-center rounded-full border border-[var(--app-border)] font-mono">{artifact?.candidateIndex || index + 1}</span><span className="min-w-0 flex-1 truncate">Option {artifact?.candidateIndex || index + 1} · {artifact ? variantDisplayLabel(artifact, index) : 'Loading'}</span><span className="shrink-0">{!artifact ? 'Pending' : artifact.status === 'staging' ? 'Generating' : artifact.status === 'failed' || artifact.status === 'unavailable' ? 'Failed' : accepted ? 'Committed' : viewed ? 'Viewing' : 'Available'}</span></button>
                                  {artifact?.status === 'ready' && candidate.part ? <div className="flex items-center gap-1 border-t border-[var(--app-border)] px-2 py-1"><button type="button" className="rounded border border-[var(--app-border)] px-1.5 py-0.5 font-semibold hover:bg-[var(--app-surface-active)]" onClick={() => stagePartChoice(artifact, turnPart.partId, true)}>Stage lock</button><button type="button" className="rounded border border-[var(--app-border)] px-1.5 py-0.5 font-semibold hover:bg-[var(--app-surface-active)]" onClick={() => stagePartChoice(artifact, turnPart.partId, false)}>Stage unlocked</button>{stagedHere ? <span className="ml-auto rounded-full bg-[var(--app-primary)] px-1.5 py-0.5 font-semibold text-white">Staged {staged?.locked ? 'lock' : 'unlocked'}</span> : null}</div> : null}
                                </div>
                              })}</div>
                            </section>
                          })}</div> : turn.candidates.length ? <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5" data-artifact-studio-whole-turn-options={turn.id}>
                            <p className="px-0.5 text-[9px] text-[var(--app-text-subtle)]">{turn.revisionNumber === 1 ? 'Original composition' : turn.parts.length > 1 ? `${turn.parts.length} parts changed together · choose one complete iteration` : turnRelatedLabel ? `Related to ${turnRelatedLabel}` : 'Choose one complete iteration'}</p>
                            {turn.candidates.map((candidate, index) => {
                              const artifact = candidate.entry
                              const viewed = Boolean(artifact && selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected))
                              return <button key={`${candidate.reference.sessionId}:${candidate.reference.variantId}`} type="button" className={cn('flex w-full min-w-0 items-center gap-2 rounded border border-[var(--app-border)] px-2 py-1.5 text-left text-[9px] hover:bg-[var(--app-surface-hover)]', viewed ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]')} disabled={!artifact} onClick={() => { if (!artifact) return; if (turn.parts.length > 1) selectFullIterationArtifact(artifact); else if (turnRelatedPart) selectPartIterationArtifact(artifact, turnRelatedPart.partId); else selectFullIterationArtifact(artifact) }}>
                                <span className="grid size-4 shrink-0 place-items-center rounded-full border border-[var(--app-border)] font-mono">{artifact?.candidateIndex || index + 1}</span>
                                <span className="min-w-0 flex-1 truncate">Option {artifact?.candidateIndex || index + 1} · {artifact ? variantDisplayLabel(artifact, index) : 'Loading'}</span>
                                <span className="shrink-0">{artifact?.status === 'staging' ? 'Generating' : viewed ? 'Viewing' : artifact ? 'Available' : 'Pending'}</span>
                              </button>
                            })}
                          </div> : <div className="border-t border-[var(--app-border)] px-2 py-1.5 text-[9px] text-[var(--app-text-subtle)]">No round options are available yet.</div>}
                        </section>
                      })}</div>
                    </section> : null}
                    {iterationDescriptor ? <><div className="mb-2 px-2 py-1"><p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Studio steps</p><p className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">Choose a step, then select its iteration here. The preview starts at that step.</p></div>
                    <div className="grid gap-2">
                      {iterationDescriptor.sections.map((section, index) => {
                        const active = section.id === iterationSection?.id
                        const alternatives = iterationAlternativesForSection(section.id)
                        const activeBranch = alternatives.find((artifact) => artifactSelectionKey(artifact) === studioActiveBranchId)
                        return <section key={section.id} className={cn('min-w-0 overflow-hidden rounded-xl border', active ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)]' : 'border-[var(--app-border)] bg-[var(--app-bg)]')} data-artifact-studio-step={section.id}>
                          <div className="flex min-w-0 items-stretch">
                            <button type="button" className="min-w-0 flex-1 px-3 py-2.5 text-left" aria-current={active ? 'step' : undefined} onClick={() => selectIterationSection(section)}>
                              <span className="flex min-w-0 items-start gap-2.5"><span className="grid size-5 shrink-0 place-items-center rounded border border-[var(--app-border)] text-[9px] font-semibold">{index + 1}</span><span className="min-w-0 flex-1 break-words text-[11px] font-semibold leading-4">{section.label}</span>{activeBranch ? <Check className="mt-0.5 size-3.5 shrink-0 text-[var(--app-primary)]" aria-label="Active branch" /> : alternatives.length ? <span className="mt-0.5 shrink-0 rounded-full bg-[var(--app-surface-active)] px-1.5 py-0.5 text-[8px] font-semibold">{alternatives.length}</span> : null}</span>
                              <span className="mt-1 block pl-7 font-mono text-[9px] text-[var(--app-text-subtle)]">{formatDesktopV3ArtifactIterationTime(section.startMs)}–{formatDesktopV3ArtifactIterationTime(section.endMs)}</span>
                            </button>
                            {selectedPartDefinitions.some((part) => part.id === section.id) && onAddToChat ? <button type="button" className="m-1.5 grid size-7 shrink-0 place-items-center self-center rounded-md border border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)] disabled:opacity-50" aria-label={`Attach ${section.label} step for AI changes`} title={`Attach ${section.label} as the exact change target`} disabled={!selectedCanAttach || Boolean(actionPending)} onClick={() => void attachIterationSection(section)}><MessageSquarePlus size={13} aria-hidden="true" /></button> : null}
                          </div>
                          {active && alternatives.length ? <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5" aria-label={`${section.label} branches`}>
                            {alternatives.map((artifact, alternativeIndex) => renderStudioBranch(artifact, alternativeIndex, section))}
                          </div> : null}
                        </section>
                      })}
                    </div></> : null}
                  </aside> : null}
                  {previewFullscreen ? <button type="button" className="absolute right-3 top-3 z-20 grid size-9 place-items-center rounded-full border border-white/20 bg-black/60 text-white shadow-lg hover:bg-black/75" aria-label="Exit fullscreen artifact preview" onClick={() => void togglePreviewFullscreen()}><Minimize2 size={16} /></button> : null}
                  {previewLoading ? <div className="grid h-full min-h-40 place-items-center text-sm text-[var(--app-text-muted)]"><span><Loader2 className="mr-2 inline size-4 animate-spin" />Loading preview…</span></div> : null}
                  {previewError ? <div className="mx-auto mt-8 max-w-lg rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]"><p className="font-semibold">Preview unavailable</p><p className="mt-1">{previewError}</p><div className="mt-3 flex flex-wrap gap-2"><button type="button" className="rounded-md border border-current px-2.5 py-1 text-xs font-semibold" onClick={() => setPreviewRetry((value) => value + 1)}>Retry preview</button>{selected.content === undefined ? <button type="button" className="rounded-md border border-current px-2.5 py-1 text-xs font-semibold" onClick={() => void downloadArtifact(selected)}>Download instead</button> : null}</div></div> : null}
                  {!previewLoading && !previewError && selected.status === 'staging' ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-text-muted)]"><div><Loader2 className="mx-auto mb-3 size-6 animate-spin text-[var(--app-primary)]" /><p>This variant is still generating.</p><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The live review surface will refresh when it is ready.</p></div></div> : null}
                  {!previewLoading && !previewError && (selected.status === 'failed' || selected.status === 'unavailable') ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-danger)]"><div><AlertTriangle className="mx-auto mb-3 size-6" /><p>This variant could not be generated.</p>{selected.failureCode ? <p className="mt-1 font-mono text-xs text-[var(--app-text-muted)]">{selected.failureCode}</p> : null}</div></div> : null}
                  {!previewLoading && !previewError && !selected.previewable && selected.content === undefined && selected.status !== 'staging' && selected.status !== 'failed' && selected.status !== 'unavailable' ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-text-muted)]"><div><FileText className="mx-auto mb-2 size-6" /><p>This artifact is available to download, but has no inline preview.</p></div></div> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selected.mediaType.startsWith('image/') && previewURL ? <div className="grid size-full min-h-0 place-items-center"><img key={`${previewURL}:${previewRetry}`} src={previewURL} alt={selected.description || selected.label} className="size-full rounded-lg border border-[var(--app-border)] bg-white object-contain shadow-sm" onError={() => setPreviewError('The browser could not decode or load this image.')} /></div> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selectedVideoProfileCompatible && (selected.mediaType.startsWith('video/') || selected.kind === 'video') && previewURL ? <div className="grid size-full min-h-0 place-items-center bg-black/90 p-2 sm:p-4 rounded-lg"><video key={`${previewURL}:${previewRetry}`} src={previewURL} controls autoPlay={false} playsInline preload="metadata" className="max-h-full max-w-full rounded-lg border border-white/10 object-contain shadow-md" data-artifact-video-player onError={() => setPreviewError('The browser could not decode or load this video.')} /></div> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selected.mediaType === 'text/html' && previewURL ? <iframe ref={animationFrameRef} key={`${previewURL}:${previewRetry}`} title={selected.label} src={previewURL} sandbox="allow-scripts" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" onLoad={() => requestIterationDescription(artifactSelectionKey(selected))} onError={() => setPreviewError('The secure animation runtime could not load this artifact. Access may have expired or the artifact may be incompatible with preview policy.')} /> : null}
                  {iterationDescriptor && !previewLoading && !previewError && !previewFullscreen ? <button type="button" className="absolute bottom-3 left-1/2 z-20 inline-flex h-10 -translate-x-1/2 items-center gap-2 rounded-full border border-white/20 bg-black/75 px-4 text-xs font-semibold text-white shadow-lg backdrop-blur hover:bg-black/90 md:left-[calc(50%+9rem)] xl:left-[calc(50%+10rem)]" aria-label={iterationPlaying ? 'Pause animated artifact' : `Play animated artifact from ${iterationSection?.label || 'current position'}`} onClick={playIterationSection} data-artifact-primary-playback>{iterationPlaying ? <Pause size={15} /> : <Play size={15} />}{iterationPlaying ? 'Pause' : iterationTimeMs > 0 ? `Play from ${iterationSection?.label || 'here'}` : 'Play animation'}</button> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selected.mediaType === 'application/pdf' && previewURL ? <iframe key={`${previewURL}:${previewRetry}`} title={selected.label} src={previewURL} sandbox="" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" onError={() => setPreviewError('The browser could not load this PDF.')} /> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selected.mediaType === 'text/markdown' && previewText ? <div className="mx-auto max-w-4xl rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5"><ChatMarkdown content={previewText} /></div> : null}
                  {!previewLoading && !previewError && selectedAnimationActive && selected.mediaType === 'text/plain' && previewText ? <pre className="mx-auto max-w-4xl whitespace-pre-wrap break-words rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5 font-mono text-xs leading-5">{previewText}</pre> : null}
                </div>
                {iterationDescriptor && iterationSection ? <section className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2" aria-label="Animation sections" data-artifact-iteration-timeline>
                  <div className="grid min-w-0 grid-cols-1 gap-2 pb-2 sm:grid-cols-2 md:hidden">
                    {iterationDescriptor.sections.map((section, index) => {
                      const alternatives = iterationAlternativesForSection(section.id)
                      const activeBranch = alternatives.find((artifact) => artifactSelectionKey(artifact) === studioActiveBranchId)
                      const active = section.id === iterationSection.id
                      return <section key={section.id} className={cn('min-w-0 overflow-hidden rounded-lg border', active ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)]')}>
                        <button type="button" className="w-full min-w-0 px-3 py-2.5 text-left transition hover:bg-[var(--app-surface-hover)]" aria-current={active ? 'true' : undefined} onClick={() => selectIterationSection(section)}>
                          <span className="flex min-w-0 items-start justify-between gap-2"><span className="min-w-0 break-words text-[10px] font-semibold leading-4">{index + 1}. {section.label}</span>{activeBranch ? <Check className="size-3 shrink-0 text-[var(--app-primary)]" aria-label="Section active branch" /> : alternatives.length > 0 ? <span className="rounded-full bg-[var(--app-surface-active)] px-1.5 py-0.5 text-[8px] font-semibold">{alternatives.length}</span> : null}</span>
                          <span className="mt-0.5 block font-mono text-[9px] text-[var(--app-text-subtle)]">{formatDesktopV3ArtifactIterationTime(section.startMs)}–{formatDesktopV3ArtifactIterationTime(section.endMs)}</span>
                        </button>
                        {active && alternatives.length ? <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5 text-[var(--app-text-muted)]" aria-label={`${section.label} branches`}>
                          {alternatives.map((artifact, alternativeIndex) => renderStudioBranch(artifact, alternativeIndex, section))}
                        </div> : null}
                      </section>
                    })}
                  </div>
                  <div className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 lg:flex lg:flex-wrap">
                    <button type="button" className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full bg-[var(--app-primary)] px-3 text-xs font-semibold text-white hover:opacity-90" aria-label={iterationPlaying ? 'Pause whole artifact' : 'Play whole artifact from current position'} onClick={playIterationSection}>{iterationPlaying ? <Pause size={14} /> : <Play size={14} />}<span>{iterationPlaying ? 'Pause' : iterationTimeMs > 0 ? 'Play from here' : 'Play'}</span></button>
                    <input type="range" min={0} max={iterationDescriptor.durationMs} step={1} value={Math.min(iterationDescriptor.durationMs, Math.max(0, iterationTimeMs))} aria-label="Whole artifact timeline" className="w-full min-w-0 accent-[var(--app-primary)] lg:min-w-40 lg:flex-1" onChange={(event) => { if (iterationPlaying) pauseIteration(); seekIteration(Number(event.target.value)) }} />
                    <span className="shrink-0 font-mono text-[10px] text-[var(--app-text-muted)]">{formatDesktopV3ArtifactIterationTime(iterationTimeMs)}</span>
                    <button type="button" className="col-span-3 inline-flex h-9 w-full min-w-0 items-center justify-center gap-1.5 rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary-soft)] px-3 text-xs font-semibold text-[var(--app-primary)] hover:opacity-90 disabled:opacity-50 lg:col-auto lg:h-8 lg:w-auto lg:shrink-0" disabled={!iterationRoundSourceArtifact || !onIterateSection || Boolean(actionPending)} onClick={() => void requestSectionIteration()} data-artifact-iterate-section>{actionPending === 'iterate-section' ? <Loader2 className="size-3 animate-spin" /> : <Sparkles size={13} />}More alternatives</button>
                    {nextIterationSection ? <button type="button" className="col-span-3 inline-flex h-9 w-full min-w-0 items-center justify-center gap-1.5 rounded-lg bg-[var(--app-primary)] px-3 text-xs font-semibold text-white hover:opacity-90 disabled:opacity-50 lg:col-auto lg:h-8 lg:w-auto lg:shrink-0" disabled={!activeIterationAlternative || !onIterateSection || Boolean(actionPending)} onClick={() => void requestNextSectionIteration()} data-artifact-next-section>{actionPending === 'next-section' ? <Loader2 className="size-3 animate-spin" /> : <ChevronRight size={13} />}Continue to {nextIterationSection.label}</button> : null}
                  </div>
                  <div className="mt-1 min-h-7 text-xs text-[var(--app-text-muted)]" data-artifact-section-narration>
                    {iterationNarration ? <><span className="font-medium text-[var(--app-text)]">{iterationNarration.text}</span>{iterationNarration.detail ? <span className="ml-2 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)]">{iterationNarration.detail}</span> : null}</> : <span className="text-[var(--app-text-subtle)]">{iterationSection.narration.length > 0 ? 'Scrub this section to inspect its narration.' : 'No narration is declared for this section.'}</span>}
                  </div>

                </section> : null}
                {selectedPartDefinitions.length ? <section className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2" aria-label="Artifact parts" data-artifact-parts>{Object.keys(stagedPartChoices).length ? <div className="mb-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary-soft)] px-2.5 py-2" data-artifact-staged-part-choices><span className="text-[10px] font-semibold text-[var(--app-primary)]">{Object.keys(stagedPartChoices).length} exact part choice{Object.keys(stagedPartChoices).length === 1 ? '' : 's'} staged</span><span className="flex gap-1"><button type="button" className="rounded-md border border-[var(--app-primary)] px-2 py-1 text-[10px] font-semibold text-[var(--app-primary)]" disabled={Boolean(actionPending)} onClick={() => setStagedPartChoices({})}>Clear</button><button type="button" className="inline-flex items-center gap-1 rounded-md bg-[var(--app-primary)] px-2 py-1 text-[10px] font-semibold text-white disabled:opacity-50" disabled={Boolean(actionPending)} onClick={() => void applyStagedPartChoices()} data-artifact-apply-part-choices>{actionPending === 'apply-parts' ? <Loader2 size={11} className="animate-spin" /> : <Check size={11} />}Apply atomically</button></span></div> : null}<div className="mb-1.5 flex items-center justify-between gap-2"><span className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Parts · select one or more change targets · {selectedAcceptedPartCount}/{selectedPartDefinitions.length} accepted</span>{selectedParts.length ? <button type="button" className="inline-flex items-center gap-1 rounded-md bg-[var(--app-primary)] px-2 py-1 text-[10px] font-semibold text-white disabled:opacity-50" disabled={!onAddToChat || !(selectedHead ?? selectedCanAttach) || Boolean(actionPending)} onClick={() => void requestPartChanges()} data-artifact-ask-selected-parts>{actionPending === 'ask-part' ? <Loader2 size={11} className="animate-spin" /> : <MessageSquarePlus size={11} />}Ask changes to {selectedParts.length}</button> : <span className="text-[10px] text-[var(--app-text-subtle)]">No parts selected</span>}</div><div className="flex min-w-0 gap-2 overflow-x-auto">{selectedPartDefinitions.map((part, index) => { const active = selectedPartIds.includes(part.id); const slot = selected.composition?.parts.find((candidate) => candidate.partId === part.id); const accepted = selectedAcceptedPartHeads.find((candidate) => candidate.partId === part.id); const acceptedCurrent = Boolean(slot && accepted && desktopV3ArtifactStudioSamePartRevision(slot, accepted)); return <button key={part.id} type="button" className={cn('min-w-36 shrink-0 rounded-lg border px-3 py-2 text-left hover:bg-[var(--app-surface-hover)]', active ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)]' : 'border-[var(--app-border)] bg-[var(--app-bg)]')} aria-pressed={active} onClick={() => selectArtifactPart(part)} data-artifact-part={part.id}><span className="flex items-center gap-1.5 text-[10px] font-semibold">{active ? <Check size={11} /> : (part.locator?.kind ?? 'semantic') === 'temporal' ? <Play size={11} /> : null}{index + 1}. {part.label}</span><span className="block text-[9px] text-[var(--app-text-subtle)]">{acceptedCurrent ? 'Accepted revision' : slot?.locked ? 'Locked pending revision' : (part.locator?.kind ?? 'semantic') === 'temporal' ? `${formatDesktopV3ArtifactIterationTime(part.locator?.startMs ?? 0)} · play from here` : (part.locator?.kind ?? 'semantic')}</span></button>})}</div></section> : null}
                {!studioModeActive && selectedRounds.length > 1 ? <section className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2" aria-label="Artifact revision history" data-artifact-revision-history><div className="flex gap-2 overflow-x-auto">{selectedRounds.map((round) => <div key={round.id} className="shrink-0 rounded-lg border border-[var(--app-border)] px-2 py-1.5 text-[10px]"><span className="font-semibold">Revision {round.revisionNumber}</span><span className="ml-1 text-[var(--app-text-subtle)]">{round.candidates.length} candidate{round.candidates.length === 1 ? '' : 's'}</span></div>)}</div></section> : null}
                <footer className={cn('grid shrink-0 gap-1.5 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-2 md:flex md:flex-wrap md:items-center md:justify-between md:gap-2 md:px-4 md:py-3', selectedCanExportVideoStills && onExportVideoStills ? 'grid-cols-4' : 'grid-cols-3')} aria-live="polite" data-mobile-generation-actions>
                  <div className="col-span-full min-w-0 text-[10px] leading-4 md:flex-1 md:text-xs">{actionError ? <span className="text-[var(--app-danger)]">{actionError}</span> : actionConfirmation ? <span className="text-[var(--app-success)]">{actionConfirmation}</span> : <span className="text-[var(--app-text-subtle)]"><strong className="text-[var(--app-text)]">Your choice{selectedChoiceCount === 1 ? '' : 's'}: {selectedChoiceCount}</strong> · Attach for changes keeps the durable selected design unchanged. Use this design selects the previewed choice and continues with iterations.{selectedIsCanonical ? ' This choice is already the durable selected design.' : ''}</span>}</div>
                  {selectedCanExportVideoStills && onExportVideoStills ? <button type="button" className="inline-flex h-10 min-w-0 items-center justify-center gap-1 rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary-soft)] px-2 text-[10px] font-semibold text-[var(--app-primary)] hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 md:h-9 md:px-3 md:text-xs" disabled={Boolean(actionPending)} title="Export declared capture states as managed 1920 × 1080 PNGs, then prepare a pending video plan" onClick={() => void requestVideoStillExport()} data-artifact-export-video-stills>{actionPending === 'export-video-stills' ? <Loader2 className="size-4 shrink-0 animate-spin" /> : <Sparkles className="size-4 shrink-0" />}<span className="truncate">Video stills</span></button> : null}
                  <button type="button" className={cn('inline-flex h-10 min-w-0 items-center justify-center gap-1 rounded-lg border px-2 text-[10px] font-semibold transition disabled:cursor-not-allowed disabled:opacity-50 md:h-9 md:px-3 md:text-xs', selectedIsQueuedForChat ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)]')} disabled={!selectedCanAttach || Boolean(actionPending)} aria-pressed={selectedIsQueuedForChat} onClick={() => selected && toggleChatSelection(selected)}>{selectedIsQueuedForChat ? <Check className="size-4 shrink-0" /> : <MessageSquarePlus className="size-4 shrink-0" />}<span className="truncate md:hidden">{selectedIsQueuedForChat ? 'Chosen' : 'Choose'}</span><span className="hidden md:inline">{selectedIsQueuedForChat ? 'Your choice' : 'Choose this iteration'}</span></button><button type="button" className="inline-flex h-10 min-w-0 items-center justify-center gap-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-[10px] font-semibold hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50 md:h-9 md:px-3 md:text-xs" disabled={pendingChatArtifacts.length === 0 || !onAddToChat || Boolean(actionPending)} title={pendingChatArtifacts.length === 0 ? 'Only ready managed variants can be attached' : undefined} onClick={() => void emitAddToChat()}>{actionPending === 'add' ? <Loader2 className="size-4 shrink-0 animate-spin" /> : <MessageSquarePlus className="size-4 shrink-0" />}<span className="truncate">Attach{pendingChatArtifacts.length > 1 ? ` ${pendingChatArtifacts.length}` : ''}<span className="hidden md:inline"> for changes</span></span></button><button type="button" className="inline-flex h-10 min-w-0 items-center justify-center gap-1 rounded-lg bg-[var(--app-primary)] px-2 text-[10px] font-semibold text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 md:h-9 md:px-3 md:text-xs" disabled={!selectedCanAttach || !onUseThisDesign || Boolean(actionPending)} title={!selectedCanAttach ? 'Only ready managed variants can be used' : undefined} onClick={() => void persistAndUseDesign()}>{actionPending === 'use' ? <Loader2 className="size-4 shrink-0 animate-spin" /> : <Sparkles className="size-4 shrink-0" />}<span className="truncate md:hidden">Use & continue</span><span className="hidden md:inline">Use this design & continue</span></button>
                </footer>
              </> : null}
            </main>
          </div>
        </section>, portalTarget) : null}
    </div>
  )
}

export interface DesktopV3ArtifactCatalogGalleryProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAddToChat?: DesktopV3ArtifactGalleryProps['onAddToChat']
  onUseThisDesign?: DesktopV3ArtifactGalleryProps['onUseThisDesign']
  onIterateSection?: DesktopV3ArtifactGalleryProps['onIterateSection']
  onActiveBranchChange?: DesktopV3ArtifactGalleryProps['onActiveBranchChange']
  onExportVideoStills?: DesktopV3ArtifactGalleryProps['onExportVideoStills']
}

export function DesktopV3ArtifactCatalogGallery({ open, onOpenChange, onAddToChat, onUseThisDesign, onIterateSection, onActiveBranchChange, onExportVideoStills }: DesktopV3ArtifactCatalogGalleryProps) {
  const [artifacts, setArtifacts] = useState<DesktopV3ArtifactCatalogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const refreshCatalog = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const catalog = await fetchDesktopV3ArtifactCatalogResult()
      setArtifacts(catalog.artifacts)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Artifact catalog failed to load')
    } finally {
      setLoading(false)
    }
  }, [])

  useDesktopV3OpenArtifactCatalogRefresh(open, refreshCatalog)

  useEffect(() => {
    if (!open) return
    void refreshCatalog()
  }, [open, refreshCatalog])

  return <DesktopV3ArtifactGallery artifacts={artifacts} open={open} onOpenChange={onOpenChange} onAddToChat={onAddToChat} onUseThisDesign={onUseThisDesign} onIterateSection={onIterateSection} onActiveBranchChange={onActiveBranchChange} onExportVideoStills={onExportVideoStills} onSelectionPersisted={refreshCatalog} showTrigger={false} loading={loading} error={error} title="Artifact Studio" />
}
