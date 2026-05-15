import { ArrowLeft, KeyRound, Link2, Plus, ShieldCheck, Sparkles } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'

const placeholderCards = [
  {
    title: 'Auth choices',
    description: 'Local CLI login, one-time paste, OS keychain, and Swarm Vault will appear here as probed options.',
    icon: KeyRound,
  },
  {
    title: 'SecretRefs only',
    description: 'Credentials will be modeled as references and auth providers, not raw secrets in normal app state.',
    icon: ShieldCheck,
  },
  {
    title: 'Action review',
    description: 'Pending actions will render human-readable previews before any write, publish, or scheduled side effect.',
    icon: Sparkles,
  },
]

export function IntegrationsPage() {
  const navigate = useNavigate()

  return (
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-6 py-6 sm:px-8 sm:py-8">
        <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0">
            <Button variant="ghost" className="mb-5 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={() => void navigate({ to: '/' })}>
              <ArrowLeft size={15} />
              Back to launcher
            </Button>
            <div className="flex items-center gap-3">
              <span className="grid h-11 w-11 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm">
                <Link2 size={20} strokeWidth={1.8} />
              </span>
              <div className="min-w-0">
                <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm-wide</p>
                <h1 className="mt-1 text-3xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Integrations</h1>
              </div>
            </div>
          </div>
          <Button className="h-11 rounded-xl px-4" onClick={() => {}}>
            <Plus size={16} />
            Add Integration
          </Button>
        </header>

        <main className="flex-1 py-8">
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-6">
            <div className="max-w-3xl">
              <p className="text-[11px] font-medium uppercase tracking-[0.22em] text-[var(--app-text-subtle)]">Base page</p>
              <h2 className="mt-2 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Integrations are global, not workspace-based.</h2>
              <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">
                This is the frontend shell for creating and managing integration packs across the whole swarm. Backend validation,
                credential storage, builder sessions, and approval workflows will plug into this page later.
              </p>
            </div>
          </section>

          <section className="mt-6 grid gap-4 md:grid-cols-3">
            {placeholderCards.map((card) => {
              const Icon = card.icon
              return (
                <Card key={card.title} className="border-[var(--app-border)] bg-[var(--app-surface)] p-5">
                  <span className="grid h-10 w-10 place-items-center rounded-xl border border-[color-mix(in_srgb,var(--app-primary)_34%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_10%,transparent)] text-[var(--app-primary)]">
                    <Icon size={18} strokeWidth={1.8} />
                  </span>
                  <h3 className="mt-4 text-base font-semibold tracking-[-0.03em] text-[var(--app-text)]">{card.title}</h3>
                  <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">{card.description}</p>
                </Card>
              )
            })}
          </section>
        </main>
      </div>
    </div>
  )
}
