import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, Check, ChevronRight, FileDiff, GitCommitHorizontal, Loader2, MessageSquarePlus, RefreshCw, Search, X } from 'lucide-react'

import { readNativeArtifactNavigation, writeNativeArtifactNavigation } from '../../session-v3/artifact-v3-navigation'
import { cn } from '../../../../lib/cn'
import {
  artifactStatusLabel,
  desktopV3NativeArtifactIterationSelection,
  fetchDesktopV3NativeArtifactStudio,
  firstDesktopV3NativeArtifactPendingCandidate,
  pendingDesktopV3NativeArtifactTurns,
  preflightDesktopV3NativeArtifactPreview,
  selectDesktopV3NativeArtifactCandidate,
  type DesktopV3NativeArtifactCandidate,
  type DesktopV3NativeArtifactRevision,
  type DesktopV3NativeArtifactStudio,
  type DesktopV3NativeArtifactSummary,
} from '../../session-v3/artifact-v3-api'
import { useDesktopV3OpenArtifactCatalogRefresh } from '../../session-v3/use-artifact-catalog-refresh'
import { refreshOpenDesktopV3ArtifactCatalogs } from '../../session-v3/artifact-catalog-refresh'
import { artifactV3SelectionProtocol, nativeArtifactPreviewSelectionEvent, toggleNativeArtifactPart } from '../../session-v3/artifact-v3-preview-selection'

export interface DesktopV3ArtifactV3StudioProps {
  artifact: DesktopV3NativeArtifactSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onIterate?: (selection: import('../../session-v3/artifact-api').DesktopV3ArtifactMessageSelection) => void | Promise<void>
  onNavigate?: (artifact: DesktopV3NativeArtifactSummary) => void
  onRefresh?: () => void | Promise<void>
  onRepairDraft?: (artifact: DesktopV3NativeArtifactSummary) => void
}

function shortOid(value: string): string {
  return value ? value.slice(0, 10) : 'pending'
}

function revisionLabel(revision: DesktopV3NativeArtifactRevision, index: number): string {
  return `Revision ${index + 1} · ${shortOid(revision.commitOid)}`
}

function candidateRevision(candidate: DesktopV3NativeArtifactCandidate): DesktopV3NativeArtifactRevision | null {
  return candidate.revision
}

export function DesktopV3ArtifactV3Studio(props: DesktopV3ArtifactV3StudioProps) {
  return <NativeArtifactStudio key={`${props.artifact?.ownerSessionId}:${props.artifact?.artifactId}:${props.open}`} {...props} />
}

function NativeArtifactStudio({ artifact, open, onOpenChange, onIterate, onRefresh, onRepairDraft, onNavigate }: DesktopV3ArtifactV3StudioProps) {
  const [studio, setStudio] = useState<DesktopV3NativeArtifactStudio | null>(null)
  const initialNavigation = readNativeArtifactNavigation(typeof window === 'undefined' ? '' : window.location.search)
  const [waveId, setWaveId] = useState(initialNavigation.artifactId === artifact?.artifactId ? initialNavigation.waveId : '')
  const [selectedRevisionRef, setSelectedRevisionRef] = useState(initialNavigation.artifactId === artifact?.artifactId ? initialNavigation.revisionRef : '')
  const [selectedPartIds, setSelectedPartIds] = useState<string[]>([])
  const [focusedPartId, setFocusedPartId] = useState('')
  const [partsOpen, setPartsOpen] = useState(false)
  const [partQuery, setPartQuery] = useState('')
  const [previewURL, setPreviewURL] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const previewRef = useRef<HTMLIFrameElement>(null)
  const loadRequest = useRef<AbortController | null>(null)
  const explicitlySelectedRevision = useRef(Boolean(selectedRevisionRef))
  useEffect(() => () => loadRequest.current?.abort(), [])

  const load = useCallback(async () => {
    if (!artifact) return
    loadRequest.current?.abort()
    const controller = new AbortController()
    loadRequest.current = controller
    setLoading(true)
    setError('')
    try {
      const next = await fetchDesktopV3NativeArtifactStudio(artifact.ownerSessionId, artifact.artifactId, controller.signal)
      if (controller.signal.aborted) return
      setStudio(next)
      setSelectedRevisionRef((current) => {
        if (explicitlySelectedRevision.current && current && next.revisions.some((revision) => revision.revisionRef === current)) return current
        return firstDesktopV3NativeArtifactPendingCandidate(next.turns)?.candidate.revision?.revisionRef || next.artifact.head?.revisionRef || next.revisions[next.revisions.length - 1]?.revisionRef || ''
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Artifact Studio failed to load')
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [artifact?.ownerSessionId, artifact?.artifactId])

  useEffect(() => {
    if (open && artifact) void load()
    else {
      setStudio(null)
      setSelectedRevisionRef('')
      setSelectedPartIds([])
      setPartQuery('')
      setPreviewURL('')
      setError('')
    }
  }, [open, artifact, load])

  useDesktopV3OpenArtifactCatalogRefresh(open, load)
  useEffect(() => {
    if (open && artifact) writeNativeArtifactNavigation({ artifactId: artifact.artifactId, revisionRef: selectedRevisionRef, waveId })
  }, [open, artifact?.artifactId, selectedRevisionRef, waveId])
  const close = () => { writeNativeArtifactNavigation(null); onOpenChange(false) }
  const groups = studio?.artifact.generationGroups ?? []
  const group = groups.find((entry) => entry.waveId === waveId) ?? groups[groups.length - 1]
  const navigateMember = (member: NonNullable<typeof group>['members'][number]) => {
    if (!artifact || !group || !member.commitOid || !['ready', 'selected'].includes(member.status)) return
    const revisionRef = `revision-${member.commitOid}`
    setWaveId(group.waveId)
    if (member.artifactId === artifact.artifactId) {
      explicitlySelectedRevision.current = true
      setSelectedRevisionRef(revisionRef)
    } else if (onNavigate) {
      writeNativeArtifactNavigation({ artifactId: member.artifactId, revisionRef, waveId: group.waveId })
      onNavigate({ ...artifact, artifactId: member.artifactId, artifactRef: member.artifactId, label: `Option ${member.index}`, head: null })
    }
  }

  const selectedRevision = useMemo(() => studio?.revisions.find((revision) => revision.revisionRef === selectedRevisionRef) ?? null, [selectedRevisionRef, studio])
  const pendingTurns = useMemo(() => pendingDesktopV3NativeArtifactTurns(studio?.turns ?? []), [studio])
  const viewingPending = pendingTurns.find((turn) => turn.candidates.some((candidate) => candidate.revision?.revisionRef === selectedRevisionRef))
  const displayTurns = useMemo(() => studio ? [...pendingTurns, ...studio.turns.filter((turn) => !pendingTurns.includes(turn))] : [], [studio, pendingTurns])
  const historical = Boolean(studio?.artifact.head && selectedRevision && selectedRevision.revisionRef !== studio.artifact.head.revisionRef)
  const visibleParts = selectedRevision?.parts ?? (historical ? [] : studio?.parts ?? [])
  const filteredParts = useMemo(() => {
    const query = partQuery.trim().toLowerCase()
    if (!query) return visibleParts
    return visibleParts.filter((part) => [part.id, part.label, part.description, part.locator.path, ...part.locator.paths].some((value) => value.toLowerCase().includes(query)))
  }, [partQuery, visibleParts])
  useEffect(() => { setSelectedPartIds([]); setFocusedPartId('') }, [selectedRevisionRef])
  // Current draft errors are separate from immutable revision and repair history.
  const currentDraft = studio?.artifact.currentDraft
  const diagnostics = !historical && currentDraft ? currentDraft.diagnostics : selectedRevision?.diagnostics ?? []

  useEffect(() => {
    setPreviewURL('')
    if (!artifact || !selectedRevisionRef || !open) return undefined
    const controller = new AbortController()
    setPreviewLoading(true)
    void preflightDesktopV3NativeArtifactPreview(artifact.ownerSessionId, artifact.artifactId, selectedRevisionRef, controller.signal)
      .then((url) => { if (!controller.signal.aborted) setPreviewURL(url) })
      .catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Artifact revision preview failed') })
      .finally(() => { if (!controller.signal.aborted) setPreviewLoading(false) })
    return () => controller.abort()
  }, [artifact?.artifactId, artifact?.ownerSessionId, open, selectedRevisionRef])

  const syncPreviewSelection = useCallback(() => {
    const selectorIds = selectedPartIds.filter((id) => visibleParts.some((part) => part.id === id && part.locator.kind === 'selector'))
    const focus = visibleParts.find((part) => part.id === focusedPartId && part.locator.kind === 'selector')
    previewRef.current?.contentWindow?.postMessage({
      protocol: artifactV3SelectionProtocol, type: 'selection-state', revision_ref: selectedRevisionRef,
      part_ids: selectorIds, ...(focus ? { focus_part_id: focus.id } : {}),
    }, '*') // The sandbox deliberately has an opaque origin, not same-origin access.
  }, [selectedPartIds, selectedRevisionRef, visibleParts, focusedPartId])

  useEffect(() => { syncPreviewSelection() }, [syncPreviewSelection])
  useEffect(() => {
    if (!open || !previewURL || previewLoading) return
    const receive = (event: MessageEvent) => {
      if (event.source === previewRef.current?.contentWindow && event.data?.protocol === artifactV3SelectionProtocol && event.data?.revision_ref === selectedRevisionRef && event.data?.type === 'scene-seek-error' && visibleParts.some((part) => part.temporal && part.id === event.data.part_id)) {
        setError('The animation did not acknowledge the requested scene time.')
        return
      }
      const message = nativeArtifactPreviewSelectionEvent(event, previewRef.current?.contentWindow, selectedRevisionRef, visibleParts)
      if (!message) return
      if (message.type === 'selection-ready') syncPreviewSelection()
      else {
        setSelectedPartIds((ids) => toggleNativeArtifactPart(ids, message.partId))
        setFocusedPartId('') // Clicking does not jump the document; list navigation does.
      }
    }
    window.addEventListener('message', receive)
    return () => window.removeEventListener('message', receive)
  }, [open, previewURL, previewLoading, selectedRevisionRef, visibleParts, syncPreviewSelection])

  const focusPart = (partId: string) => {
    if (!visibleParts.some((part) => part.id === partId)) return
    setSelectedPartIds((current) => toggleNativeArtifactPart(current, partId))
    setFocusedPartId(partId)
  }

  const iterate = async (partIds: readonly string[], referenceOnly = false) => {
    if (!studio || !onIterate || historical || !selectedRevision || selectedRevision.revisionRef !== studio.artifact.head?.revisionRef) return
    try {
      setBusy('iterate')
      setError('')
      const selection = desktopV3NativeArtifactIterationSelection(studio, partIds)
      await onIterate(referenceOnly ? { ...selection, pending_request: 'Use these exact native source Parts as a style/example reference only. Do not revise or select the source unless separately requested.' } : selection)
      close()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not start an Artifact turn')
    } finally {
      setBusy('')
    }
  }

  const selectCandidate = async (turnId: string, turnRevision: number, candidate: DesktopV3NativeArtifactCandidate) => {
    if (!studio || !candidate.revision || candidate.status !== 'ready') return
    const key = `${turnId}:${candidate.candidateId}`
    try {
      setBusy(key)
      setError('')
      await selectDesktopV3NativeArtifactCandidate({
        sessionId: studio.artifact.ownerSessionId,
        artifactId: studio.artifact.artifactId,
        turnId,
        candidateId: candidate.candidateId,
        expectedHead: studio.artifact.head,
        expectedTurnRevision: turnRevision,
      })
      explicitlySelectedRevision.current = true
      setSelectedRevisionRef(candidate.revision.revisionRef)
      await Promise.all([load(), onRefresh?.(), refreshOpenDesktopV3ArtifactCatalogs()])
    } catch (cause) {
      await load()
      setError(`${cause instanceof Error ? cause.message : 'Artifact head selection conflicted'}. Studio refreshed from the native authority.`)
    } finally {
      setBusy('')
    }
  }

  if (!open || !artifact) return null

  return <div className="fixed inset-0 z-[105] flex min-h-0 flex-col bg-[var(--app-bg)] text-[var(--app-text)]" role="dialog" aria-modal="true" aria-label="Artifact Studio" data-testid="desktop-artifact-v3-studio">
    <header className="flex h-14 shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4">
      <div className="min-w-0"><div className="flex items-center gap-2"><GitCommitHorizontal className="size-4 text-[var(--app-primary)]" /><h1 className="truncate text-sm font-semibold">{studio?.artifact.label || artifact.label}</h1><span className="rounded-full bg-[var(--app-primary-soft)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-primary)]">Artifact</span></div><p className="truncate text-[10px] text-[var(--app-text-subtle)]">{artifactStatusLabel(studio?.artifact ?? artifact)} · {(studio?.artifact ?? artifact).head ? 'Last ready preview available' : 'Your artifact will appear here as soon as its preview is ready'}</p></div>
      <div className="flex items-center gap-1"><button type="button" className="rounded border border-[var(--app-border)] px-2 py-1 text-xs lg:hidden" aria-controls="native-artifact-parts" aria-expanded={partsOpen} onClick={() => setPartsOpen((value) => !value)}>Parts</button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]" onClick={() => void load()} aria-label="Refresh Artifact Studio"><RefreshCw className={cn('size-4', loading && 'animate-spin')} /></button><button type="button" className="grid size-8 place-items-center rounded-lg hover:bg-[var(--app-surface-hover)]" onClick={close} aria-label="Close Artifact Studio"><X className="size-4" /></button></div>
    </header>
    {loading && !studio ? <div className="grid flex-1 place-items-center"><Loader2 className="size-6 animate-spin text-[var(--app-primary)]" /></div> : studio ? <div className="grid min-h-0 flex-1 grid-cols-1 grid-rows-[minmax(0,1fr)_auto] lg:grid-cols-[280px_minmax(0,1fr)_320px] lg:grid-rows-1" data-artifact-v3-studio>
      <aside id="native-artifact-parts" className={cn('min-h-0 overflow-y-auto border-r border-[var(--app-border)] bg-[var(--app-surface)] p-3 lg:static lg:block', partsOpen ? 'absolute inset-y-14 left-0 z-30 w-72 shadow-xl' : 'hidden')} aria-label="Artifact parts and history">
        {group ? <nav aria-label="Generation siblings and iterations" className="mb-4 space-y-2">
          <label className="block text-xs">Generation round<select aria-label="Generation round" className="mt-1 w-full rounded border p-2 bg-[var(--app-bg)]" value={group.waveId} onChange={(event) => setWaveId(event.target.value)}>{groups.map((entry, index) => <option key={entry.waveId} value={entry.waveId}>Round {index + 1} · {entry.count} options</option>)}</select></label>
          {group.members.map((member) => <button key={member.index} type="button" className="block w-full rounded border p-2 text-left text-xs disabled:opacity-50" aria-current={member.artifactId === artifact.artifactId && member.commitOid === selectedRevision?.commitOid ? 'true' : undefined} disabled={!['ready', 'selected'].includes(member.status) || !member.commitOid || (member.artifactId !== artifact.artifactId && !onNavigate)} onClick={() => navigateMember(member)}>Option {member.index} · {member.status}</button>)}
          {group.members.length < group.count ? <p role="status" className="text-xs">{group.count - group.members.length} options awaiting allocation</p> : null}
        </nav> : null}
        <section><div className="flex items-center justify-between"><h2 className="text-xs font-semibold">Parts</h2><span className="text-[9px] text-[var(--app-text-subtle)]">{visibleParts.length}</span></div><p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">Click sections or toggle Parts to select several, then request changes.</p><p className="mt-1 text-[10px]" role="status">{selectedPartIds.length} selected</p><label className="relative mt-2 block"><Search className="absolute left-2 top-1/2 size-3 -translate-y-1/2 text-[var(--app-text-subtle)]" /><input value={partQuery} onChange={(event) => setPartQuery(event.target.value)} className="h-8 w-full rounded-md border border-[var(--app-border)] bg-[var(--app-bg)] pl-7 pr-2 text-[10px]" placeholder="Search parts" aria-label="Search Artifact parts" /></label><div className="mt-2 max-h-[42vh] space-y-1 overflow-y-auto" data-artifact-v3-part-navigator>{filteredParts.map((part) => { const selected = selectedPartIds.includes(part.id); return <button key={part.id} type="button" onClick={() => focusPart(part.id)} aria-pressed={selected} className={cn('w-full rounded-lg border px-2.5 py-2 text-left', selected ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)]' : 'border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]')} data-artifact-v3-part={part.id}><span className="block truncate text-[10px] font-semibold">{part.label}</span><span className="block truncate text-[9px] text-[var(--app-text-subtle)]">{part.temporal ? `Scene · ${part.temporal.startMs / 1000}–${part.temporal.endMs / 1000}s` : `${part.locator.kind} · ${part.locator.path || part.locator.value || `${part.locator.paths.length} source files`}`}</span></button>})}</div>{onIterate ? <button type="button" className="mt-2 inline-flex min-h-8 w-full items-center justify-center gap-1 rounded-md py-2 bg-[var(--app-primary)] px-2 text-[10px] font-semibold text-white disabled:opacity-50" disabled={Boolean(busy) || historical || !studio.artifact.head || !selectedRevision} title={historical ? 'Select this candidate as head before iterating its Parts' : undefined} onClick={() => void iterate(selectedPartIds)} data-artifact-v3-iterate><MessageSquarePlus className="size-3" />{selectedPartIds.length ? `Request changes · ${selectedPartIds.length} selected` : 'Remix whole project'}</button> : null}{onIterate ? <button type="button" className="mt-2 w-full rounded border p-2 text-xs disabled:opacity-50" disabled={Boolean(busy) || historical || !studio.artifact.head || !selectedRevision} onClick={() => void iterate(selectedPartIds, true)}>Use as style/example reference</button> : null}</section>
        <section className="mt-5"><h2 className="text-xs font-semibold">Revision history</h2><p className="mt-0.5 text-[9px] text-[var(--app-text-subtle)]">Open an exact prior commit without moving head.</p><ol className="mt-2 space-y-1" data-artifact-v3-revision-history>{studio.revisions.map((revision, index) => { const selected = revision.revisionRef === selectedRevisionRef; const head = revision.revisionRef === studio.artifact.head?.revisionRef; return <li key={revision.revisionRef}><button type="button" data-artifact-v3-revision={revision.commitOid} className={cn('w-full rounded-lg border px-2 py-1.5 text-left', selected ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)]' : 'border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]')} onClick={() => { explicitlySelectedRevision.current = true; setSelectedRevisionRef(revision.revisionRef) }}><span className="flex justify-between gap-2 text-[9px] font-semibold"><span>{revisionLabel(revision, index)}</span>{head ? <span className="text-[var(--app-success)]">HEAD</span> : null}</span><span className="mt-0.5 block truncate font-mono text-[8px] text-[var(--app-text-subtle)]">{revision.parentCommitOids.length ? `parent ${shortOid(revision.parentCommitOids[0]!)}` : 'root commit'}</span></button></li>})}</ol></section>
      </aside>
      <main className="relative min-h-0 min-w-0 overflow-hidden bg-[var(--app-bg-alt)]" data-artifact-v3-primary-preview>
        {historical ? <div className="absolute left-1/2 top-3 z-20 -translate-x-1/2 rounded-full border border-[var(--app-warning)] bg-[var(--app-warning-bg)] px-3 py-1 text-[10px] font-semibold text-[var(--app-warning)]">{viewingPending ? 'Pending change preview · not accepted · select head to apply' : 'Viewing prior revision · current head unchanged · select head to iterate these Parts'}</div> : null}
        {previewLoading ? <div className="grid size-full place-items-center"><Loader2 className="size-6 animate-spin text-[var(--app-primary)]" /></div> : previewURL ? <iframe key={selectedRevisionRef} ref={previewRef} onLoad={syncPreviewSelection} title={`${studio.artifact.label} complete revision preview`} src={previewURL} sandbox="allow-scripts" referrerPolicy="no-referrer" className="size-full border-0 bg-white" data-artifact-v3-complete-preview data-artifact-v3-preview data-artifact-v3-preview-revision={selectedRevision?.commitOid} /> : <div className="grid size-full place-items-center p-6 text-center text-sm text-[var(--app-text-muted)]">{studio.artifact.head ? 'This revision has no ready preview. Choose a ready revision from history.' : `${artifactStatusLabel(studio.artifact)}. ${studio.artifact.status === 'error' ? 'Your work is retained. Ask Swarm to continue fixing this artifact.' : 'You can close this window and continue chatting.'}`}</div>}
      </main>
      <aside className="min-h-0 overflow-y-auto border-t border-[var(--app-border)] bg-[var(--app-surface)] p-3 lg:border-l lg:border-t-0" aria-label="Artifact turns, diffs, and diagnostics">
        <section><h2 className="text-xs font-semibold">{pendingTurns.length ? `Pending changes · ${pendingTurns.length} turns` : 'Turns and candidates'}</h2><p className="mt-0.5 text-[9px] text-[var(--app-text-subtle)]">Each option is a complete project commit from one exact base.</p><div className="mt-2 space-y-2" data-artifact-v3-turns>{displayTurns.map((turn) => <details key={turn.turnId} open={pendingTurns.includes(turn) || turn === studio.turns[studio.turns.length - 1]} className="rounded-lg border border-[var(--app-border)]" data-artifact-v3-turn={turn.turnId}><summary className="flex cursor-pointer list-none items-center gap-2 px-2.5 py-2"><ChevronRight className="size-3 group-open:hidden" /><span className="min-w-0 flex-1"><span className="block text-[10px] font-semibold">Turn {studio.turns.indexOf(turn) + 1} · {pendingTurns.includes(turn) ? 'Pending · ' : ''}{turn.status.replace(/_/g, ' ')}</span><span className="block truncate text-[8px] text-[var(--app-text-subtle)]">base {shortOid(turn.baseCommitOid)}{turn.targetPartIds.length ? ` · target ${turn.targetPartIds.join(', ')}` : ' · whole artifact'}</span></span></summary><div className="grid gap-1 border-t border-[var(--app-border)] p-1.5">{turn.candidates.map((candidate, candidateIndex) => { const revision = candidateRevision(candidate); const viewing = revision?.revisionRef === selectedRevisionRef; return <div key={candidate.candidateId} className={cn('rounded-md border p-1.5', viewing ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)]' : 'border-[var(--app-border)]')} data-artifact-v3-candidate={candidate.candidateId}><button type="button" className="flex w-full items-center gap-2 text-left text-[9px] disabled:opacity-50" disabled={!revision} onClick={() => { if (revision) { explicitlySelectedRevision.current = true; setSelectedRevisionRef(revision.revisionRef) } }}><span className="grid size-4 place-items-center rounded-full border border-current/20">{candidateIndex + 1}</span><span className="min-w-0 flex-1 truncate">Option {candidateIndex + 1}{viewing ? ' · Viewing' : ''}</span><span>{candidate.selected ? 'Selected' : candidate.status.replace(/_/g, ' ')}</span></button>{candidate.diagnostics.length ? <details className="mt-1 text-[9px]"><summary>Option diagnostics</summary>{candidate.diagnostics.map((diagnostic) => <p key={diagnostic.id}>{diagnostic.message}</p>)}</details> : null}{revision ? <div className="mt-1 flex items-center justify-between border-t border-[var(--app-border)] pt-1"><span className="text-[8px] text-[var(--app-text-subtle)]">{revision.changedFiles.length} files{revision.affectedPartIds.length ? ` · ${revision.affectedPartIds.length} affected parts` : turn.targetPartIds.length ? ` · ${turn.targetPartIds.length} targeted parts` : ' · whole project'}</span>{candidate.status === 'ready' && !candidate.selected ? <button type="button" className="rounded bg-[var(--app-primary)] px-1.5 py-0.5 text-[8px] font-semibold text-white disabled:opacity-50" disabled={Boolean(busy)} onClick={() => void selectCandidate(turn.turnId, turn.revision, candidate)}>{busy === `${turn.turnId}:${candidate.candidateId}` ? <Loader2 className="mr-1 inline size-2 animate-spin" /> : <Check className="mr-1 inline size-2" />}Select head</button> : null}</div> : null}</div>})}</div></details>)}</div></section>
        {selectedRevision ? <section className="mt-5" data-artifact-v3-diff><h2 className="flex items-center gap-1 text-xs font-semibold"><FileDiff className="size-3.5" />Revision diff</h2><p className="mt-0.5 font-mono text-[8px] text-[var(--app-text-subtle)]">{shortOid(selectedRevision.commitOid)} · {selectedRevision.changedFiles.length} changed files</p><div className="mt-2 space-y-1">{selectedRevision.changedFiles.map((file) => <div key={`${file.status}:${file.path}`} className="rounded-md border border-[var(--app-border)] px-2 py-1.5 text-[9px]"><div className="flex items-start gap-2"><span className="rounded bg-[var(--app-bg-alt)] px-1 font-semibold uppercase">{file.status[0]}</span><span className="min-w-0 flex-1 break-all font-mono">{file.path}</span><span className="shrink-0 text-[var(--app-text-subtle)]">{file.additions !== null && file.deletions !== null ? `+${file.additions} −${file.deletions}` : 'Line counts unavailable'}</span></div>{file.affectedPartIds.length ? <p className="mt-1 text-[8px] text-[var(--app-text-subtle)]">Affects {file.affectedPartIds.join(', ')}</p> : null}{file.shared ? <p className="mt-1 rounded bg-[var(--app-warning-bg)] px-1.5 py-1 text-[8px] font-semibold text-[var(--app-warning)]" data-artifact-v3-cross-part-change>Shared file changed across part boundaries</p> : null}</div>)}{selectedRevision.changedFiles.length === 0 ? <p className="rounded-md border border-dashed border-[var(--app-border)] p-2 text-[9px] text-[var(--app-text-subtle)]">No file changes recorded for this revision.</p> : null}</div></section> : null}
        {currentDraft && ['error', 'failed'].includes(currentDraft.status) && onRepairDraft ? <button type="button" className="mt-4 rounded bg-[var(--app-primary)] px-3 py-2 text-sm text-white" data-artifact-repair-draft onClick={() => { onRepairDraft(studio.artifact); onOpenChange(false) }}>Ask Swarm to fix errors</button> : null}
        {diagnostics.length ? <section className="mt-5" data-artifact-v3-diagnostics><h2 className="flex items-center gap-1 text-xs font-semibold"><AlertTriangle className="size-3.5 text-[var(--app-warning)]" />{!historical && currentDraft ? 'Current errors' : 'Diagnostics for this revision'}</h2><div className="mt-2 space-y-1">{diagnostics.map((diagnostic) => <div key={diagnostic.id} className="rounded-md border border-[var(--app-border)] p-2 text-[9px]"><p className="font-semibold">{diagnostic.message}</p><p className="mt-0.5 font-mono text-[8px] text-[var(--app-text-subtle)]">{diagnostic.phase} · {diagnostic.code}{diagnostic.path ? ` · ${diagnostic.path}${diagnostic.line ? `:${diagnostic.line}` : ''}` : ''}</p></div>)}</div></section> : null}
        {currentDraft?.history.some((gate) => gate.diagnostics.length) ? <details className="mt-4 text-xs" data-artifact-repair-history><summary>Previous repair errors</summary>{currentDraft.history.map((gate, index) => <div key={index}>{gate.diagnostics.map((diagnostic) => <p key={diagnostic.id}>{diagnostic.message}</p>)}</div>)}</details> : null}
      </aside>
    </div> : <div className="m-5 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">{error || 'Artifact Studio is unavailable.'}</div>}
    {error && studio ? <div className="absolute bottom-4 left-1/2 z-30 flex max-w-xl -translate-x-1/2 items-start gap-2 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)] shadow-lg" role="alert"><AlertTriangle className="mt-0.5 size-3.5 shrink-0" /><span>{error}</span><button type="button" onClick={() => setError('')} aria-label="Dismiss Artifact error"><X className="size-3" /></button></div> : null}
  </div>
}
