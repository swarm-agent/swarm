import { useId, useState, type ReactNode } from 'react'
import { isMediaGenerationPending } from '../tools/media-library/media-iteration-thread'

export interface CreativeCardOutput {
  id: string
  title: string
  candidateNumber?: number
  status: string
  ready: boolean
  preview: ReactNode
  open: () => void
  actions?: ReactNode
}
export interface CreativeCardTurn {
  id: string
  title: string
  status: string
  outputs: CreativeCardOutput[]
  alerts?: ReactNode
  controls?: ReactNode
}
export function creativeThreadStatus(turns: readonly CreativeCardTurn[]) {
  return turns.find(turn => isMediaGenerationPending(turn.status) || ['pending_approval', 'planning'].includes(turn.status))?.status ?? turns[turns.length - 1]?.status ?? 'queued'
}
export function CreativeThreadCard({ id, title, studio, turns, attention }: {
  id: string; title: string; studio: string; turns: readonly CreativeCardTurn[]; attention?: ReactNode
}) {
  const domId = useId()
  const [selection, setSelection] = useState<{ turn: string; output?: string }>()
  const selected = turns.find(turn => turn.id === selection?.turn) ?? [...turns].reverse().find(turn => turn.outputs.some(output => output.ready)) ?? turns[turns.length - 1]
  const output = selected?.outputs.find(output => output.id === selection?.output) ?? selected?.outputs.find(output => output.ready) ?? selected?.outputs[0]
  const status = creativeThreadStatus(turns)
  const running = isMediaGenerationPending(status)
  const failed = ['failed', 'cancelled', 'interrupted', 'partial_failure', 'partial success', 'rejected'].includes(status)
  const ready = turns.reduce((count, turn) => count + turn.outputs.filter(output => ['ready', 'accepted', 'succeeded'].includes(output.status)).length, 0)
  return <article className={`creative-thread-card ${running ? 'is-running' : ''} ${failed ? 'has-error' : ''}`} data-testid="media-task-card" data-task-id={id}>
    <header className="creative-card-header">
      <div className="min-w-0"><span className="creative-studio">{studio} studio</span><h3 className="creative-card-title">{title}</h3></div>
      <span className="creative-status" role="status">{status.replace(/_/g, ' ')} · {turns.length} {turns.length === 1 ? 'turn' : 'turns'} · {ready} ready</span>
    </header>
    {attention}
    {/* Alerts for a new turn remain visible even when an earlier output is selected. */}
    {turns.map(turn => <div key={turn.id} className="creative-turn-alerts">{turn.alerts}</div>)}
    <section aria-label="Sequential thread iterations" className="creative-strip">
      <div className="creative-strip-label">Sequential turns ({turns.length})</div>
      <div className="creative-turn-rail" role="tablist" aria-label="Thread iteration turns" data-testid="task-media-thumbnails-strip" onKeyDown={event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
        const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
        const index = tabs.indexOf(document.activeElement as HTMLButtonElement)
        if (index < 0) return
        event.preventDefault()
        const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
        setSelection({ turn: turns[next].id })
        tabs[next]?.focus({ preventScroll: true })
        tabs[next]?.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })
      }}>
        {turns.map((turn, index) => {
          // Unchanged turns keep their preview node and exact source while siblings update.
          const preview = (turn === selected ? output : undefined) ?? turn.outputs.find(candidate => candidate.ready)
          return <button key={turn.id} id={`${domId}-turn-${index}`} aria-controls={`${domId}-candidates`} role="tab" aria-selected={turn === selected} tabIndex={turn === selected ? 0 : -1} type="button" className={`creative-turn ${turn === selected ? 'is-selected' : ''} ${isMediaGenerationPending(turn.status) ? 'is-running-turn' : ''}`} onClick={() => setSelection({ turn: turn.id })} onDoubleClick={() => { if (preview?.ready) preview.open() }}>
            <span className="creative-turn-heading"><strong>Turn {index + 1}</strong><span>{turn.status.replace(/_/g, ' ')}</span></span>
            <span className="creative-turn-preview">{turn.outputs.map(candidate => <span key={candidate.id} hidden={candidate.id !== preview?.id} className="creative-preview-output">{candidate.preview}</span>)}{!preview?.ready && <span>{preview && ['ready', 'accepted'].includes(preview.status) ? 'Preview loading' : turn.status.replace(/_/g, ' ')}</span>}</span>
            <span className="creative-turn-summary" title={turn.title}>{turn.title}</span>
            <span className="creative-turn-count">{turn.outputs.length} {turn.outputs.length === 1 ? 'output' : 'outputs'}</span>
          </button>
        })}
      </div>
    </section>
    {selected && <section id={`${domId}-candidates`} role="tabpanel" aria-labelledby={`${domId}-turn-${turns.indexOf(selected)}`} className="creative-candidates-panel">
      <div className="creative-candidates"><span>Candidates in Turn {turns.indexOf(selected) + 1}</span><div className="creative-candidate-chips">{selected.outputs.map((candidate, index) => <button key={candidate.id} type="button" aria-pressed={candidate.id === output?.id} className="creative-candidate-chip" onClick={() => setSelection({ turn: selected.id, output: candidate.id })}>Candidate {candidate.candidateNumber ?? index + 1} · {candidate.status}</button>)}</div></div>
      {output && <div className="creative-output-actions"><button type="button" disabled={!output.ready} onClick={output.open}>Open selected output</button>{output.actions}</div>}
      {selected.controls}
    </section>}
  </article>
}
