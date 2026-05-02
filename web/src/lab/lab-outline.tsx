import { useMemo, useState, type ReactNode } from 'react'

type LabVariant = {
  id: string
  render: () => ReactNode
}

type LabOutlinePageProps = {
  variants: LabVariant[]
}

export function LabOutlinePage({ variants }: LabOutlinePageProps) {
  const [index, setIndex] = useState(0)
  const activeVariant = variants[index] ?? variants[0]
  const previousIndex = useMemo(() => (index - 1 + variants.length) % variants.length, [index, variants.length])
  const nextIndex = useMemo(() => (index + 1) % variants.length, [index, variants.length])

  if (!activeVariant) {
    return null
  }

  return (
    <main className="min-h-screen bg-[var(--app-bg)] text-[var(--app-text)]">
      <section className="mx-auto flex min-h-screen w-full max-w-[1500px] items-center px-5 py-6">
        <div className="w-full">{activeVariant.render()}</div>
      </section>

      <nav className="fixed bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]/90 p-2 shadow-2xl backdrop-blur-xl">
        <button type="button" className="h-10 w-10 rounded-xl text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]" onClick={() => setIndex(previousIndex)} aria-label="Previous">
          ‹
        </button>
        {variants.map((variant, variantIndex) => (
          <button
            key={variant.id}
            type="button"
            className={`h-10 min-w-10 rounded-xl px-3 font-mono text-xs transition ${variantIndex === index ? 'bg-[var(--app-primary)] text-[var(--app-primary-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}
            onClick={() => setIndex(variantIndex)}
            aria-label={variant.id}
          >
            {variant.id}
          </button>
        ))}
        <button type="button" className="h-10 w-10 rounded-xl text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]" onClick={() => setIndex(nextIndex)} aria-label="Next">
          ›
        </button>
      </nav>
    </main>
  )
}
