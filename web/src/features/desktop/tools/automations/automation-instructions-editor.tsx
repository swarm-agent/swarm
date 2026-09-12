import { useState } from 'react'
import { requestJson } from '../../../../app/api'
import type { AutomationDefinition } from '../../state/desktop-automation-api'
import { automationDocumentDigest } from './automation-plan-approval'
import { automationControl } from './automation-editor'

type Plan = { id: string; version: number; status: string; approval_state: string; document: Record<string, unknown> }
// Create a separately approved immutable instruction plan; never overwrite a live
// pin or silently save changed instruction text against the old reference.
export function AutomationInstructionsEditor({ binding, parentSessionId, onPin }: { binding: AutomationDefinition['plans'][number]; parentSessionId?: string; onPin: (pin: AutomationDefinition['plans'][number]['plan']) => void }) {
  const [source, setSource] = useState<Plan | null>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function load() {
    setBusy(true); setError('')
    try {
      if (!parentSessionId || binding.plan.session_id !== parentSessionId) throw new Error('Instructions must belong to this automation conversation.')
      const result = await requestJson<{ plan: Plan }>(`/v3/sessions/${encodeURIComponent(parentSessionId)}/plans/${encodeURIComponent(binding.plan.plan_id)}`)
      const plan = result.plan
      if (plan.version !== binding.plan.revision || await automationDocumentDigest(plan.document) !== binding.plan.document_sha256) throw new Error('Pinned instructions differ from the current plan. Select the exact approved revision before editing.')
      if (plan.document.automation) throw new Error('An automation proposal is not executable instructions.')
      setSource(plan)
      setText(String((plan.document.info as Record<string, unknown> | undefined)?.goal ?? ''))
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Load failed') }
    finally { setBusy(false) }
  }
  async function approve() {
    if (!source || !parentSessionId || !text.trim() || busy) return
    setBusy(true); setError('')
    try {
      const document = { ...source.document, id: crypto.randomUUID(), status: 'approved', info: { ...(source.document.info as object), goal: text } }
      const result = await requestJson<{ plan: Plan }>(`/v3/sessions/${encodeURIComponent(parentSessionId)}/plans`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan_id: document.id, title: 'Automation instructions', document, status: 'approved', approval_state: 'approved', activate: false }) })
      if (result.plan.status !== 'approved' || result.plan.approval_state !== 'approved') throw new Error('Instruction revision was not approved; automation pins are unchanged.')
      onPin({ session_id: parentSessionId, plan_id: result.plan.id, revision: result.plan.version, document_sha256: await automationDocumentDigest(result.plan.document) })
      setSource(null)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Instruction approval failed') }
    finally { setBusy(false) }
  }
  return <section><button type="button" className={automationControl} disabled={busy} onClick={() => void load()}>Edit executable instructions</button>{source && <><label className="block">Executable objective<textarea className={automationControl} value={text} onChange={event => setText(event.target.value)} /></label><details><summary>Retained checkpoints and complete source</summary><pre className="max-h-48 overflow-auto">{JSON.stringify(source.document, null, 2)}</pre></details><p>Approve a new canonical instruction plan, then save configuration to pin it. Existing checkpoints are retained. Configuration saves paused and requires fresh policy approval; admitted occurrences keep their old pins.</p><button type="button" className={automationControl} disabled={busy || !text.trim()} onClick={() => void approve()}>Approve new instruction revision</button></>}{error && <p role="alert">{error}</p>}</section>
}
