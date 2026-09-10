import { useState } from 'react'
import { Button } from '../../../../components/ui/button'
import { prepareBaseline, reviewRepository, type BaselineRequest, type RepositoryReview } from '../../../workspaces/launcher/services/repository-review'

// A request is frozen once submitted. Response-loss retries reuse its exact consent.
export function RepositoryReviewPanel({ path, onReady, onCancel }: { path: string; onReady: (path: string, committedOnly: boolean) => Promise<void>; onCancel: () => void }) {
 const [review, setReview] = useState<RepositoryReview | null>(null)
 const [selected, setSelected] = useState<string[]>([])
 const [omissions, setOmissions] = useState(false)
 const [attempt, setAttempt] = useState<BaselineRequest | null>(null)
 const [prepared, setPrepared] = useState(false)
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const load = async () => {
  setBusy(true); setError('')
  try { setReview(await reviewRepository(path)); setSelected([]); setOmissions(false); setAttempt(null); setPrepared(false) }
  catch (e) { setError(e instanceof Error ? e.message : 'Review failed') }
  finally { setBusy(false) }
 }
 const apply = async () => {
  if (!review || !omissions || busy) return
  setBusy(true); setError('')
  try {
   if (!review.repository.headCommit && !prepared) {
    const request = attempt || { path: review.repository.path, expected_resolved_path: review.repository.path, review_digest: review.digest, selected_paths: selected, confirm_baseline: true, confirm_omissions: omissions }
    setAttempt(request)
    await prepareBaseline(request)
    setPrepared(true)
   }
   await onReady(review.repository.path, omissions)
  } catch (e) { setError(e instanceof Error ? e.message : 'Preparation failed') }
  finally { setBusy(false) }
 }
 return <section aria-label="Repository content review" className="grid gap-3 rounded-lg border border-[var(--app-border)] p-4">
  <h3>Review project content</h3><p className="break-all">{path}</p>
  <p>Only committed content enters managed worktrees. No provider is required. Existing history and staged files are preserved.</p>
  {review ? <>
   <p>{review.warning}</p>
   {!review.repository.headCommit ? <div className="max-h-64 overflow-auto">{review.files.map(file => <label className="flex gap-2 break-all" key={file.path}>
    <input type="checkbox" checked={selected.includes(file.path)} disabled={busy || !!attempt || !file.selectable} onChange={e => setSelected(e.target.checked ? [...selected, file.path] : selected.filter(p => p !== file.path))} />
    {file.path} ({file.size} bytes){!file.selectable ? ' — cannot import' : ''}
   </label>)}</div> : <p>Use the existing commit. Uncommitted and ignored content stays in the source folder only.</p>}
   <label className="flex gap-2"><input type="checkbox" checked={omissions} disabled={busy || !!attempt} onChange={e => setOmissions(e.target.checked)} />I understand omitted/uncommitted files will not enter managed worktrees.</label>
   <Button type="button" disabled={busy || !omissions} onClick={() => void apply()}>{prepared ? 'Retry saving / opening workspace' : attempt ? 'Retry exact baseline request' : review.repository.headCommit ? 'Use committed content and save' : 'Create selected baseline and save'}</Button>
  </> : null}
  {error ? <p role="alert">{error}</p> : null}
  <Button type="button" disabled={busy} onClick={() => void load()}>{review ? 'Refresh review and discard selection' : 'Load content review'}</Button>
  <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>Cancel review</Button>
 </section>
}
