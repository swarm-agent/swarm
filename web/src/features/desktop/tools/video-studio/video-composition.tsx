import { useEffect, useMemo, useRef, useState, type PointerEvent } from 'react'
import { Button } from '../../../../components/ui/button'

export type VideoCompositionRectWire = { x: number; y: number; width: number; height: number }
export type VideoCompositionCropWire = { top?: number; right?: number; bottom?: number; left?: number }
export type VideoCompositionMaskWire = { kind: 'none' | 'rounded_rect' | 'ellipse'; radius?: number }
export type VideoCompositionSourceWire = {
  source_ref: string
  media_type: string
  source_start_ms: number
  source_end_ms: number
  timeline_start_ms: number
  timeline_end_ms: number
  audio_policy: 'mute' | 'include'
  gain?: number
}
export type VideoCompositionSlotWire = {
  id: string
  requirement: string
  geometry: VideoCompositionRectWire
  z_index: number
  fit: 'contain' | 'cover'
  alignment_x: number
  alignment_y: number
  crop?: VideoCompositionCropWire
  mask: VideoCompositionMaskWire
  aspect_lock?: number
  source?: VideoCompositionSourceWire
}
export type VideoCompositionLayoutWire = { id: string; extends_layout_id?: string; slots: VideoCompositionSlotWire[] }
export type VideoCompositionCatalogWire = { schema_version: number; layouts: VideoCompositionLayoutWire[] }
export type VideoCompositionSlotOverrideWire = {
  slot_id: string
  requirement?: string
  geometry?: VideoCompositionRectWire
  z_index?: number
  fit?: 'contain' | 'cover'
  alignment_x?: number
  alignment_y?: number
  crop?: VideoCompositionCropWire
  mask?: VideoCompositionMaskWire
  aspect_lock?: number
  source?: VideoCompositionSourceWire
  clear_source?: boolean
}
export type VideoCompositionLinkWire = {
  layout_id?: string
  overrides?: VideoCompositionSlotOverrideWire[]
  detached?: boolean
  detached_slots?: VideoCompositionSlotWire[]
  disabled?: boolean
}
export type ResolvedVideoCompositionSlot = VideoCompositionSlotWire & { pixels: { x: number; y: number; width: number; height: number } }

export type VideoCompositionEditorSource = { sourceRef: string; name: string; mediaType?: string; durationMs?: number }

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, Number.isFinite(value) ? value : min))
const evenFloor = (value: number) => Math.floor(value / 2) * 2
const evenCeil = (value: number) => Math.ceil(value / 2) * 2

export function videoCompositionViewport(containerWidth: number, containerHeight: number, outputWidth: number, outputHeight: number) {
  const scale = Math.min(containerWidth / Math.max(1, outputWidth), containerHeight / Math.max(1, outputHeight))
  const rawWidth = outputWidth * scale
  const rawHeight = outputHeight * scale
  const width = Math.abs(rawWidth - containerWidth) < 1e-9 ? containerWidth : rawWidth
  const height = Math.abs(rawHeight - containerHeight) < 1e-9 ? containerHeight : rawHeight
  const rawX = (containerWidth - width) / 2
  const rawY = (containerHeight - height) / 2
  return { x: Math.abs(rawX) < 1e-9 ? 0 : rawX, y: Math.abs(rawY) < 1e-9 ? 0 : rawY, width, height, scale }
}

function mergeSlot(base: VideoCompositionSlotWire, override: VideoCompositionSlotOverrideWire): VideoCompositionSlotWire {
  return {
    ...base,
    ...(override.requirement !== undefined ? { requirement: override.requirement } : {}),
    ...(override.geometry ? { geometry: override.geometry } : {}),
    ...(override.z_index !== undefined ? { z_index: override.z_index } : {}),
    ...(override.fit ? { fit: override.fit } : {}),
    ...(override.alignment_x !== undefined ? { alignment_x: override.alignment_x } : {}),
    ...(override.alignment_y !== undefined ? { alignment_y: override.alignment_y } : {}),
    ...(override.crop ? { crop: override.crop } : {}),
    ...(override.mask ? { mask: override.mask } : {}),
    ...(override.aspect_lock !== undefined ? { aspect_lock: override.aspect_lock } : {}),
    ...(override.clear_source ? { source: undefined } : override.source ? { source: override.source } : {}),
  }
}

export function resolveVideoCompositionLayout(catalog: VideoCompositionCatalogWire | null | undefined, layoutId: string): VideoCompositionSlotWire[] {
  if (!catalog || !layoutId) return []
  const byId = new Map(catalog.layouts.map((layout) => [layout.id, layout]))
  const resolve = (id: string, stack: Set<string>): VideoCompositionSlotWire[] => {
    if (stack.has(id)) return []
    const layout = byId.get(id)
    if (!layout) return []
    const nextStack = new Set(stack).add(id)
    const inherited = layout.extends_layout_id ? resolve(layout.extends_layout_id, nextStack) : []
    const slots = [...inherited]
    for (const slot of layout.slots ?? []) {
      const index = slots.findIndex((item) => item.id === slot.id)
      if (index >= 0) slots[index] = slot
      else slots.push(slot)
    }
    return slots
  }
  return resolve(layoutId, new Set())
}

export function resolveVideoComposition(
  catalog: VideoCompositionCatalogWire | null | undefined,
  link: VideoCompositionLinkWire | null | undefined,
  outputWidth: number,
  outputHeight: number,
): ResolvedVideoCompositionSlot[] {
  if (!link || link.disabled) return []
  let slots = link.detached ? [...(link.detached_slots ?? [])] : resolveVideoCompositionLayout(catalog, link.layout_id ?? '')
  for (const override of link.overrides ?? []) slots = slots.map((slot) => slot.id === override.slot_id ? mergeSlot(slot, override) : slot)
  return slots.map((slot) => {
    const x0 = evenFloor(slot.geometry.x * outputWidth)
    const y0 = evenFloor(slot.geometry.y * outputHeight)
    const x1 = Math.min(outputWidth, evenCeil((slot.geometry.x + slot.geometry.width) * outputWidth))
    const y1 = Math.min(outputHeight, evenCeil((slot.geometry.y + slot.geometry.height) * outputHeight))
    let width = Math.max(2, x1 - x0)
    let height = Math.max(2, y1 - y0)
    let x = x0
    let y = y0
    if ((slot.aspect_lock ?? 0) > 0) {
      const target = slot.aspect_lock!
      if (width / height > target) width = Math.max(2, evenFloor(height * target))
      else height = Math.max(2, evenFloor(width / target))
      x += evenFloor((x1 - x0 - width) * slot.alignment_x)
      y += evenFloor((y1 - y0 - height) * slot.alignment_y)
    }
    return { ...slot, pixels: { x, y, width, height } }
  }).sort((left, right) => left.z_index - right.z_index || left.id.localeCompare(right.id))
}

function materializeSlot(resolved: ResolvedVideoCompositionSlot): VideoCompositionSlotWire {
  const { pixels, ...slot } = resolved
  void pixels
  return slot
}

export function updateVideoCompositionGeometry(input: {
  catalog: VideoCompositionCatalogWire
  link: VideoCompositionLinkWire
  slotId: string
  geometry: VideoCompositionRectWire
  scope: 'linked' | 'shot'
}): { catalog: VideoCompositionCatalogWire; link: VideoCompositionLinkWire } {
  const geometry = {
    x: clamp(input.geometry.x, 0, .999), y: clamp(input.geometry.y, 0, .999),
    width: clamp(input.geometry.width, .001, 1), height: clamp(input.geometry.height, .001, 1),
  }
  geometry.width = Math.min(geometry.width, 1 - geometry.x)
  geometry.height = Math.min(geometry.height, 1 - geometry.y)
  if (input.scope === 'linked' && !input.link.detached && input.link.layout_id) {
    const targetLayout = input.catalog.layouts.find((layout) => layout.id === input.link.layout_id)
    const inheritedSlot = resolveVideoComposition(input.catalog, input.link, 1920, 1080).find((slot) => slot.id === input.slotId)
    if (targetLayout && inheritedSlot) {
      const catalog = { ...input.catalog, layouts: input.catalog.layouts.map((layout) => layout.id === input.link.layout_id
        ? { ...layout, slots: layout.slots.some((slot) => slot.id === input.slotId)
          ? layout.slots.map((slot) => slot.id === input.slotId ? { ...slot, geometry } : slot)
          : [...layout.slots, { ...materializeSlot(inheritedSlot), geometry }] }
        : layout) }
      return { catalog, link: input.link }
    }
  }
  if (input.link.detached) {
    return { catalog: input.catalog, link: { ...input.link, detached_slots: (input.link.detached_slots ?? []).map((slot) => slot.id === input.slotId ? { ...slot, geometry } : slot) } }
  }
  const overrides = [...(input.link.overrides ?? [])]
  const index = overrides.findIndex((override) => override.slot_id === input.slotId)
  const override = { ...(index >= 0 ? overrides[index] : { slot_id: input.slotId }), geometry }
  if (index >= 0) overrides[index] = override
  else overrides.push(override)
  return { catalog: input.catalog, link: { ...input.link, overrides } }
}

export function detachVideoComposition(catalog: VideoCompositionCatalogWire, link: VideoCompositionLinkWire): VideoCompositionLinkWire {
  const slots = resolveVideoComposition(catalog, link, 1920, 1080).map(materializeSlot)
  return { detached: true, detached_slots: slots }
}

function cropStyle(slot: ResolvedVideoCompositionSlot) {
  const crop = slot.crop ?? {}
  const left = clamp(crop.left ?? 0, 0, .99)
  const right = clamp(crop.right ?? 0, 0, .99)
  const top = clamp(crop.top ?? 0, 0, .99)
  const bottom = clamp(crop.bottom ?? 0, 0, .99)
  const remainingX = Math.max(.01, 1 - left - right)
  const remainingY = Math.max(.01, 1 - top - bottom)
  return { width: `${100 / remainingX}%`, height: `${100 / remainingY}%`, left: `${-left * 100 / remainingX}%`, top: `${-top * 100 / remainingY}%` }
}

export function CompositionVideo(props: { slot: ResolvedVideoCompositionSlot; localTime: number; playing: boolean; src: string }) {
  const ref = useRef<HTMLVideoElement | null>(null)
  useEffect(() => {
    const video = ref.current
    const source = props.slot.source
    if (!video || !source) return
    const sourceSpan = source.source_end_ms - source.source_start_ms
    const timelineSpan = source.timeline_end_ms - source.timeline_start_ms
    const sourceMs = source.source_start_ms + Math.max(0, props.localTime - source.timeline_start_ms) * sourceSpan / Math.max(1, timelineSpan)
    const seek = () => {
      if (Math.abs(video.currentTime * 1000 - sourceMs) > 100) try { video.currentTime = sourceMs / 1000 } catch { /* metadata is not ready */ }
    }
    if (video.readyState >= HTMLMediaElement.HAVE_METADATA) seek()
    else video.addEventListener('loadedmetadata', seek, { once: true })
    video.volume = clamp(source.gain ?? 1, 0, 1)
    video.muted = source.audio_policy === 'mute'
    if (props.playing && video.paused) void video.play().catch(() => undefined)
    if (!props.playing && !video.paused) video.pause()
    return () => video.removeEventListener('loadedmetadata', seek)
  }, [props.localTime, props.playing, props.slot.source, props.src])
  return <video ref={ref} src={props.src} preload="auto" playsInline crossOrigin="use-credentials" style={{ ...cropStyle(props.slot), position: 'absolute', objectFit: props.slot.fit, objectPosition: `${props.slot.alignment_x * 100}% ${props.slot.alignment_y * 100}%` }} />
}

export function VideoCompositionOverlay(props: {
  slots: ResolvedVideoCompositionSlot[]
  outputWidth: number
  outputHeight: number
  playheadMs: number
  partStartMs: number
  playing: boolean
  sourceURL: (sourceRef: string) => string
  editing?: boolean
  selectedSlotId?: string | null
  onSelectSlot?: (slotId: string) => void
}) {
  const localTime = props.playheadMs - props.partStartMs
  return <div className="pointer-events-none absolute inset-0 overflow-hidden" data-video-composition-overlay>
    {props.slots.map((slot) => {
      const sourceActive = slot.source && localTime >= slot.source.timeline_start_ms && localTime < slot.source.timeline_end_ms
      const radius = slot.mask.kind === 'ellipse' ? '50%' : slot.mask.kind === 'rounded_rect' ? `${(slot.mask.radius ?? 0) * 100}%` : '0'
      return <div key={slot.id} className={`absolute overflow-hidden ${props.editing ? 'pointer-events-auto' : ''} ${props.selectedSlotId === slot.id ? 'ring-2 ring-amber-300' : ''}`} style={{ left: `${slot.pixels.x / props.outputWidth * 100}%`, top: `${slot.pixels.y / props.outputHeight * 100}%`, width: `${slot.pixels.width / props.outputWidth * 100}%`, height: `${slot.pixels.height / props.outputHeight * 100}%`, zIndex: slot.z_index + 20, borderRadius: radius, clipPath: slot.mask.kind === 'ellipse' ? 'ellipse(50% 50% at 50% 50%)' : undefined }} onPointerDown={() => props.onSelectSlot?.(slot.id)} data-video-composition-slot={slot.id}>
        {sourceActive ? <CompositionVideo slot={slot} localTime={localTime} playing={props.playing} src={props.sourceURL(slot.source!.source_ref)} /> : props.editing ? <div className="grid h-full w-full place-items-center border border-dashed border-amber-300/80 bg-amber-950/45 p-2 text-center text-[10px] text-amber-100"><span><strong>{slot.id}</strong><br />{slot.requirement}<br />{slot.source ? 'Outside slot timing' : 'Source needed'}</span></div> : null}
      </div>
    })}
  </div>
}

function numberInput(value: number, onChange: (value: number) => void, label: string) {
  return <label className="text-[9px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">{label}<input aria-label={`Composition ${label}`} type="number" min="0" max="1" step={.01} value={Number(value.toFixed(3))} onChange={(event) => onChange(Number(event.target.value))} className="mt-1 h-7 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-[10px] normal-case tracking-normal" /></label>
}

export function VideoCompositionEditor(props: {
  catalog: VideoCompositionCatalogWire
  link: VideoCompositionLinkWire
  partTitle: string
  durationMs: number
  sources: VideoCompositionEditorSource[]
  pending: boolean
  productionState?: 'pending' | 'ready'
  disabled?: boolean
  onPropose: (catalog: VideoCompositionCatalogWire, link: VideoCompositionLinkWire, summary: string, productionState: 'pending' | 'ready') => Promise<void>
}) {
  const [catalog, setCatalog] = useState(props.catalog)
  const [link, setLink] = useState(props.link)
  const [selectedSlotId, setSelectedSlotId] = useState('')
  const [scope, setScope] = useState<'linked' | 'shot'>('shot')
  const [productionState, setProductionState] = useState<'pending' | 'ready'>(props.productionState ?? 'ready')
  const [busy, setBusy] = useState(false)
  const slots = useMemo(() => resolveVideoComposition(catalog, link, 1920, 1080), [catalog, link])
  const selected = slots.find((slot) => slot.id === selectedSlotId) ?? slots[0]
  useEffect(() => { if (!selectedSlotId && slots[0]) setSelectedSlotId(slots[0].id) }, [selectedSlotId, slots])
  useEffect(() => { setCatalog(props.catalog); setLink(props.link); setProductionState(props.productionState ?? 'ready'); setSelectedSlotId('') }, [props.catalog, props.link, props.productionState])
  const setGeometry = (geometry: VideoCompositionRectWire) => {
    if (!selected) return
    const next = updateVideoCompositionGeometry({ catalog, link, slotId: selected.id, geometry, scope })
    setCatalog(next.catalog); setLink(next.link)
  }
  const setOverride = (patch: Partial<VideoCompositionSlotOverrideWire>) => {
    if (!selected) return
    if (link.detached) {
      setLink({ ...link, detached_slots: (link.detached_slots ?? []).map((slot) => slot.id === selected.id ? mergeSlot(slot, { slot_id: selected.id, ...patch }) : slot) })
      return
    }
    const overrides = [...(link.overrides ?? [])]
    const index = overrides.findIndex((item) => item.slot_id === selected.id)
    const next = { ...(index >= 0 ? overrides[index] : { slot_id: selected.id }), ...patch }
    if (index >= 0) overrides[index] = next
    else overrides.push(next)
    setLink({ ...link, overrides })
  }
  const dragRef = useRef<{ x: number; y: number; geometry: VideoCompositionRectWire; resize: boolean } | null>(null)
  const beginDrag = (event: PointerEvent<HTMLDivElement>, resize: boolean, slot?: ResolvedVideoCompositionSlot) => {
    const activeSlot: ResolvedVideoCompositionSlot | undefined = slot ?? selected
    if (!activeSlot) return
    event.currentTarget.setPointerCapture(event.pointerId)
    dragRef.current = { x: event.clientX, y: event.clientY, geometry: { ...activeSlot.geometry }, resize }
  }
  const moveDrag = (event: PointerEvent<HTMLDivElement>) => {
    if (!selected) return
    const drag = dragRef.current
    if (!drag) return
    const bounds = event.currentTarget.parentElement?.getBoundingClientRect()
    if (!bounds) return
    const dx = (event.clientX - drag.x) / bounds.width
    const dy = (event.clientY - drag.y) / bounds.height
    setGeometry(drag.resize ? { ...drag.geometry, width: drag.geometry.width + dx, height: drag.geometry.height + dy } : { ...drag.geometry, x: drag.geometry.x + dx, y: drag.geometry.y + dy })
  }
  if (!selected) return null
  const source = selected.source
  const unresolvedSources = slots.filter((slot) => !slot.source).length
  const effectiveProductionState = unresolvedSources > 0 && productionState === 'ready' ? 'pending' : productionState
  const changed = JSON.stringify(catalog) !== JSON.stringify(props.catalog) || JSON.stringify(link) !== JSON.stringify(props.link) || effectiveProductionState !== (props.productionState ?? 'ready')
  const boundedSecondsInput = (value: number, onChange: (value: number) => void, label: string) => <label className="text-[9px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">{label}<input aria-label={`Composition ${label}`} type="number" min="0" step="0.1" value={Number(value.toFixed(3))} onChange={(event) => onChange(Math.max(0, Number(event.target.value) || 0))} className="mt-1 h-7 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-[10px] normal-case tracking-normal" /></label>
  return <section className="order-2 mt-4 border border-amber-300/30 bg-[var(--app-surface)] p-3" aria-label="Spatial composition editor" data-video-composition-editor>
    <div className="flex flex-wrap items-start justify-between gap-3"><div><p className="text-xs font-semibold">Spatial composition · {props.partTitle}</p><p className="mt-1 text-[10px] text-[var(--app-text-muted)]">{link.detached ? 'Detached shot layout' : `Linked layout · ${link.layout_id}`} · {slots.length} slot{slots.length === 1 ? '' : 's'} · {props.pending ? 'pending proposal' : 'accepted cut'}</p></div><div className="flex gap-2"><select aria-label="Composition slot" value={selected.id} onChange={(event) => setSelectedSlotId(event.target.value)} className="h-8 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-[10px]">{slots.map((slot) => <option key={slot.id} value={slot.id}>{slot.id}</option>)}</select>{!link.detached ? <Button variant="outline" className="h-8 px-2 text-[10px]" onClick={() => setLink(detachVideoComposition(catalog, link))}>Detach shot</Button> : props.catalog.layouts[0] ? <Button variant="outline" className="h-8 px-2 text-[10px]" onClick={() => setLink({ layout_id: props.catalog.layouts[0].id })}>Relink</Button> : null}<Button variant="ghost" className="h-8 px-2 text-[10px] text-red-400" onClick={() => setLink({ disabled: true })}>Remove</Button></div></div>
    <div className="mt-3 grid gap-3 lg:grid-cols-[minmax(280px,1fr)_minmax(320px,1.2fr)]">
      <div className="relative aspect-video bg-black" aria-label="Composition geometry surface">{slots.map((slot) => <div key={slot.id} className={`absolute cursor-move border ${slot.id === selected.id ? 'border-amber-300 bg-amber-400/15' : 'border-white/35 bg-white/5'}`} style={{ left: `${slot.geometry.x * 100}%`, top: `${slot.geometry.y * 100}%`, width: `${slot.geometry.width * 100}%`, height: `${slot.geometry.height * 100}%`, zIndex: slot.z_index }} onPointerDown={(event) => { setSelectedSlotId(slot.id); beginDrag(event, false, slot) }} onPointerMove={moveDrag} onPointerUp={() => { dragRef.current = null }}><span className="pointer-events-none px-1 text-[9px] text-white">{slot.id}</span>{slot.id === selected.id ? <div className="absolute bottom-0 right-0 h-4 w-4 cursor-se-resize bg-amber-300" onPointerDown={(event) => { event.stopPropagation(); beginDrag(event, true) }} /> : null}</div>)}</div>
      <div className="grid content-start gap-3">
        <fieldset className="flex gap-4 text-[10px]"><legend className="sr-only">Geometry scope</legend><label><input type="radio" checked={scope === 'shot'} onChange={() => setScope('shot')} /> Shot override</label><label><input type="radio" checked={scope === 'linked'} disabled={link.detached} onChange={() => setScope('linked')} /> Apply to linked shots</label></fieldset>
        <div className="grid grid-cols-4 gap-2">{numberInput(selected.geometry.x, (x) => setGeometry({ ...selected.geometry, x }), 'x')}{numberInput(selected.geometry.y, (y) => setGeometry({ ...selected.geometry, y }), 'y')}{numberInput(selected.geometry.width, (width) => setGeometry({ ...selected.geometry, width }), 'width')}{numberInput(selected.geometry.height, (height) => setGeometry({ ...selected.geometry, height }), 'height')}</div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4"><label className="text-[9px] uppercase">Aspect<input aria-label="Composition aspect lock" type="number" min="0" step="0.01" value={selected.aspect_lock ?? 0} onChange={(event) => setOverride({ aspect_lock: Number(event.target.value) })} className="mt-1 h-7 w-full border bg-[var(--app-bg)] px-2" /></label><label className="text-[9px] uppercase">Fit<select aria-label="Composition fit" value={selected.fit} onChange={(event) => setOverride({ fit: event.target.value as 'contain' | 'cover' })} className="mt-1 h-7 w-full border bg-[var(--app-bg)] px-2"><option value="cover">Cover</option><option value="contain">Contain</option></select></label><label className="text-[9px] uppercase">Mask<select aria-label="Composition mask" value={selected.mask.kind} onChange={(event) => setOverride({ mask: { kind: event.target.value as VideoCompositionMaskWire['kind'], radius: event.target.value === 'rounded_rect' ? selected.mask.radius ?? .04 : undefined } })} className="mt-1 h-7 w-full border bg-[var(--app-bg)] px-2"><option value="none">None</option><option value="rounded_rect">Rounded</option><option value="ellipse">Ellipse</option></select></label><label className="text-[9px] uppercase">Layer<input aria-label="Composition layer" type="number" min="0" max="255" value={selected.z_index} onChange={(event) => setOverride({ z_index: Number(event.target.value) })} className="mt-1 h-7 w-full border bg-[var(--app-bg)] px-2" /></label></div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">{numberInput(selected.alignment_x, (alignment_x) => setOverride({ alignment_x }), 'align x')}{numberInput(selected.alignment_y, (alignment_y) => setOverride({ alignment_y }), 'align y')}{numberInput(selected.crop?.left ?? 0, (left) => setOverride({ crop: { ...(selected.crop ?? {}), left } }), 'crop left')}{numberInput(selected.crop?.top ?? 0, (top) => setOverride({ crop: { ...(selected.crop ?? {}), top } }), 'crop top')}</div>
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_140px]"><label className="text-[9px] uppercase tracking-[0.12em]">Source<select aria-label="Composition source" value={source?.source_ref ?? ''} onChange={(event) => { const selectedSource = props.sources.find((item) => item.sourceRef === event.target.value); setOverride(selectedSource ? { clear_source: false, source: { source_ref: selectedSource.sourceRef, media_type: selectedSource.mediaType || 'video/mp4', source_start_ms: 0, source_end_ms: selectedSource.durationMs ?? props.durationMs, timeline_start_ms: 0, timeline_end_ms: Math.min(props.durationMs, selectedSource.durationMs ?? props.durationMs), audio_policy: 'mute' } } : { source: undefined, clear_source: true }) }} className="mt-1 h-8 w-full border bg-[var(--app-bg)] px-2 text-[10px] normal-case tracking-normal"><option value="">Unassigned · {selected.requirement}</option>{props.sources.map((item) => <option key={item.sourceRef} value={item.sourceRef}>{item.name} · {item.sourceRef}</option>)}</select></label><label className="text-[9px] uppercase tracking-[0.12em]">Production<select aria-label="Composition production state" value={effectiveProductionState} onChange={(event) => setProductionState(event.target.value as 'pending' | 'ready')} className="mt-1 h-8 w-full border bg-[var(--app-bg)] px-2 text-[10px] normal-case tracking-normal"><option value="pending">Pending</option><option value="ready" disabled={unresolvedSources > 0}>Ready{unresolvedSources > 0 ? ` · ${unresolvedSources} source${unresolvedSources === 1 ? '' : 's'} needed` : ''}</option></select></label></div>
        {source ? <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">{boundedSecondsInput(source.source_start_ms / 1000, (value) => setOverride({ source: { ...source, source_start_ms: Math.round(value * 1000) } }), 'trim in')}{boundedSecondsInput(source.source_end_ms / 1000, (value) => setOverride({ source: { ...source, source_end_ms: Math.round(value * 1000) } }), 'trim out')}{boundedSecondsInput(source.timeline_start_ms / 1000, (value) => setOverride({ source: { ...source, timeline_start_ms: Math.round(value * 1000) } }), 'start')}{boundedSecondsInput(source.timeline_end_ms / 1000, (value) => setOverride({ source: { ...source, timeline_end_ms: Math.round(value * 1000) } }), 'end')}<label className="text-[9px] uppercase">Audio<select aria-label="Composition audio" value={source.audio_policy} onChange={(event) => setOverride({ source: { ...source, audio_policy: event.target.value as 'mute' | 'include', gain: event.target.value === 'mute' ? 0 : source.gain || 1 } })} className="mt-1 h-7 w-full border bg-[var(--app-bg)] px-2"><option value="mute">Mute</option><option value="include">Include</option></select></label></div> : null}
      </div>
    </div>
    <div className="mt-3 flex items-center justify-between border-t border-[var(--app-border)] pt-3"><p className="text-[10px] text-[var(--app-text-muted)]">All changes create or update a pending proposal. The accepted cut is unchanged until confirmation.</p><Button className="h-8 px-3 text-[10px]" disabled={!changed || busy || props.disabled} onClick={() => { setBusy(true); void props.onPropose(catalog, link, `${scope === 'linked' ? 'Update linked layout' : 'Update shot composition'} · ${selected.id}`, effectiveProductionState).finally(() => setBusy(false)) }}>{busy ? 'Proposing…' : 'Propose composition changes'}</Button></div>
  </section>
}
