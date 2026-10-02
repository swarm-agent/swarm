import { useEffect, useRef, useState } from 'react'
import { requestJson, subscribeDesktopSessionReset } from '../../../app/api'

type AvatarResponse = { image: string; user_id: string; account_scope_id: string }

// Keyed by authenticated identity at the call site; no shared image cache or
// object URLs can survive account switches. Only saved server pixels are shown.
export function PersonalAvatar({ userId, accountScopeId, name }: { userId: string; accountScopeId: string; name: string }) {
  const input = useRef<HTMLInputElement>(null)
  const [image, setImage] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [identityExpired, setIdentityExpired] = useState(false)
  const alive = useRef(true)
  const generation = useRef(0)
  const url = `/v1/account/avatar?${new URLSearchParams({ user_id: userId, account_scope_id: accountScopeId })}`
  const accept = (response: AvatarResponse) => {
    if (response.user_id !== userId || response.account_scope_id !== accountScopeId || typeof response.image !== 'string' || (response.image !== '' && !response.image.startsWith('data:image/png;base64,'))) throw new Error('Profile changed. Reload and try again.')
    setImage(response.image)
  }
  useEffect(() => {
    alive.current = true
    const controller = new AbortController()
    const unsubscribe = subscribeDesktopSessionReset(() => {
      alive.current = false
      generation.current++
      controller.abort()
      setImage('')
      setError('')
      setBusy(false)
      setIdentityExpired(true)
    })
    const current = generation.current
    if (userId && accountScopeId) void requestJson<AvatarResponse>(url, { signal: controller.signal }).then(response => {
      if (alive.current && current === generation.current) accept(response)
    }).catch(() => { if (alive.current && !controller.signal.aborted && current === generation.current) setError('Could not load picture. Choose a PNG to retry.') })
    return () => { alive.current = false; controller.abort(); unsubscribe() }
  }, [url])
  async function upload(file?: File) {
    if (!file) return
    setError('')
    if (file.type !== 'image/png' || file.size === 0 || file.size > 2 * 1024 * 1024) {
      setError('Choose a PNG up to 2 MiB and 1024 × 1024 pixels.')
      return
    }
    generation.current++
    setBusy(true)
    try {
      const response = await requestJson<AvatarResponse>(url, { method: 'PUT', headers: { 'Content-Type': 'image/png' }, body: file })
      if (alive.current) accept(response)
    } catch (err) {
      if (alive.current) setError(err instanceof Error ? err.message : 'Upload failed. Try another PNG.')
    } finally {
      if (alive.current) setBusy(false)
    }
  }
  return <div className="swarm-personal-avatar">
    <input ref={input} type="file" accept="image/png,.png" hidden aria-label="Profile PNG file" onChange={event => {
      const file = event.currentTarget.files?.[0]
      event.currentTarget.value = ''
      void upload(file)
    }} />
    <button type="button" aria-label="Change profile picture" title="Change profile picture (PNG, up to 2 MiB, 1024 × 1024)" disabled={busy || identityExpired || !userId || !accountScopeId} onClick={() => input.current?.click()}>
      {image ? <img src={image} alt="Your profile picture" /> : name.slice(0, 2).toUpperCase()}
    </button>
    {busy && <span role="status">Uploading picture…</span>}
    {error && <span role="alert">{error}</span>}
  </div>
}
