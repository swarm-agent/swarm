import { useState } from 'react'
import { Copy, ExternalLink, ShieldAlert } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import type { CodexOAuthSession } from '../../types/auth'

interface CodexDeviceCodeProps {
  session: CodexOAuthSession
  disabled?: boolean
}

function formatDeviceCodeExpiry(expiresAt: number): string {
  if (!Number.isFinite(expiresAt) || expiresAt <= 0) {
    return 'This one-time code expires in about 15 minutes.'
  }

  const expires = new Date(expiresAt * 1_000)
  if (Number.isNaN(expires.getTime())) {
    return 'This one-time code expires in about 15 minutes.'
  }

  return `Device codes are valid for up to 15 minutes. This code expires at ${expires.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}.`
}

export function CodexDeviceCode({ session, disabled = false }: CodexDeviceCodeProps) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')

  const copyCode = async () => {
    if (!session.userCode || typeof navigator === 'undefined' || !navigator.clipboard) {
      setCopyState('error')
      return
    }

    try {
      await navigator.clipboard.writeText(session.userCode)
      setCopyState('copied')
      window.setTimeout(() => setCopyState('idle'), 1_500)
    } catch {
      setCopyState('error')
    }
  }

  return (
    <div className="grid gap-4 rounded-xl border border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_8%,var(--app-surface))] p-4">
      <div className="grid gap-1">
        <span className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-muted)]">One-time device code</span>
        <p className="text-sm leading-6 text-[var(--app-text-muted)]">
          Open OpenAI’s verification page, sign in to ChatGPT, then enter this code. Swarm will recognize completion automatically.
        </p>
      </div>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <code className="select-all rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] px-5 py-3 text-center text-2xl font-semibold tracking-[0.18em] text-[var(--app-text)]">
          {session.userCode || 'Waiting…'}
        </code>
        <Button type="button" variant="outline" onClick={() => void copyCode()} disabled={disabled || !session.userCode} aria-live="polite">
          <Copy size={16} />
          {copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Copy failed' : 'Copy code'}
        </Button>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          onClick={() => {
            if (typeof window !== 'undefined' && session.verificationURL) {
              window.open(session.verificationURL, '_blank', 'noopener,noreferrer')
            }
          }}
          disabled={disabled || !session.verificationURL}
        >
          <ExternalLink size={16} />
          Open OpenAI verification
        </Button>
        {session.verificationURL ? (
          <a
            href={session.verificationURL}
            target="_blank"
            rel="noreferrer"
            className="break-all text-sm text-[var(--app-primary)] underline underline-offset-4"
          >
            {session.verificationURL}
          </a>
        ) : null}
      </div>

      <p className="text-sm text-[var(--app-text-muted)]">{formatDeviceCodeExpiry(session.expiresAt)}</p>
      <div className="flex items-start gap-2 rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm leading-5 text-[var(--app-warning)]">
        <ShieldAlert size={16} className="mt-0.5 shrink-0" />
        <span>Phishing warning: only enter this code at the OpenAI link shown above. Never send the code to another person or paste it into chat.</span>
      </div>
    </div>
  )
}
