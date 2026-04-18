import { useState } from 'react'
import { Lock, Key, Shield, ChevronLeft } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { useDesktopStore } from '../../state/use-desktop-store'

export function DesktopVaultGate() {
  const vault = useDesktopStore((state) => state.vault)
  const unlock = useDesktopStore((state) => state.unlockVault)
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [view, setView] = useState<'locked' | 'unlocking'>('locked')

  const submit = async () => {
    if (!password.trim()) {
      setError('Vault password is required.')
      return
    }
    setError(null)
    try {
      await unlock(password)
      setPassword('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to unlock vault')
    }
  }

  return (
    <div className="absolute inset-0 flex items-center justify-center bg-[radial-gradient(circle_at_top,#1b2f45,transparent_52%),var(--app-bg)] px-6">
      <Card className="relative w-full max-w-md overflow-hidden border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] transition-all duration-500 ease-in-out">
        <div className="flex flex-col">
          {view === 'locked' ? (
            <div className="flex flex-col items-center justify-center space-y-6 p-10 transition-opacity duration-300">
              <div className="flex h-20 w-20 items-center justify-center rounded-full bg-[var(--app-surface-soft)] text-[var(--app-text-muted)] ring-4 ring-[var(--app-border)]">
                <Lock className="h-10 w-10" />
              </div>
              <div className="w-full space-y-4">
                <Input
                  type="password"
                  autoFocus
                  className="h-12 bg-[var(--app-surface-soft)] border-transparent focus:border-[var(--app-primary)] transition-all"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') {
                      event.preventDefault()
                      void submit()
                    }
                  }}
                  placeholder="Enter password to unlock"
                />

                {error || vault.error ? (
                  <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-xs text-[var(--app-danger)]">
                    {error || vault.error}
                  </div>
                ) : null}

                <Button 
                  className="h-12 w-full rounded-xl bg-[var(--app-primary)] hover:bg-[var(--app-primary-hover)] text-white shadow-lg shadow-[var(--app-primary)]/10"
                  onClick={() => void submit()} 
                  disabled={vault.loading}
                >
                  {vault.loading ? (
                    <span className="flex items-center gap-2">
                      <div className="h-4 w-4 animate-spin rounded-full border-2 border-white/20 border-t-white" />
                      Unlocking…
                    </span>
                  ) : 'Unlock Vault'}
                </Button>
              </div>
            </div>
          ) : (
            <div className="flex flex-col space-y-6 p-8 transition-opacity duration-300">
              <div className="flex items-center justify-between">
                <button 
                  onClick={() => {
                    setView('locked')
                    setError(null)
                  }}
                  className="group flex h-10 w-10 items-center justify-center rounded-full hover:bg-[var(--app-surface-soft)] transition-colors"
                >
                  <ChevronLeft className="h-5 w-5 text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]" />
                </button>
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-[var(--app-surface-soft)] text-[var(--app-text-muted)]">
                  <Key className="h-5 w-5" />
                </div>
                <div className="w-10" /> {/* Spacer */}
              </div>

              <div className="space-y-2">
                <h2 className="text-xl font-semibold text-[var(--app-text)]">Enter Vault Password</h2>
                <p className="text-sm text-[var(--app-text-muted)] leading-relaxed">
                  Swarm stays unlocked only until the app exits.
                </p>
              </div>

              {vault.warning ? (
                <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-4 py-3 text-xs text-[var(--app-warning)] flex items-start gap-3">
                  <Shield className="h-4 w-4 mt-0.5 shrink-0" />
                  <span>{vault.warning}</span>
                </div>
              ) : null}

              <div className="space-y-4">
                <div className="space-y-2">
                  <label className="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-muted)]" htmlFor="desktop-vault-password">
                    Vault Password
                  </label>
                  <Input
                    id="desktop-vault-password"
                    type="password"
                    autoFocus
                    className="h-12 bg-[var(--app-surface-soft)] border-transparent focus:border-[var(--app-primary)] transition-all"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter') {
                        event.preventDefault()
                        void submit()
                      }
                    }}
                    placeholder="••••••••"
                  />
                </div>

                {error || vault.error ? (
                  <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-xs text-[var(--app-danger)]">
                    {error || vault.error}
                  </div>
                ) : null}

                <Button 
                  className="h-12 w-full rounded-xl bg-[var(--app-primary)] hover:bg-[var(--app-primary-hover)] text-white shadow-lg shadow-[var(--app-primary)]/10"
                  onClick={() => void submit()} 
                  disabled={vault.loading}
                >
                  {vault.loading ? (
                    <span className="flex items-center gap-2">
                      <div className="h-4 w-4 animate-spin rounded-full border-2 border-white/20 border-t-white" />
                      Unlocking…
                    </span>
                  ) : 'Unlock Vault'}
                </Button>
              </div>
            </div>
          )}
        </div>
      </Card>
    </div>
  )
}
