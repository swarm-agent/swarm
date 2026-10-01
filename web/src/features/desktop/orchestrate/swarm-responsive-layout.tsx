import { useEffect, useLayoutEffect, useId, useRef, useState, type RefObject } from 'react'

export function visibleFocusTargets(root: HTMLElement) {
  return Array.from(root.querySelectorAll<HTMLElement>('a[href], button, input, textarea, select, [tabindex]'))
    .filter(node => node.tabIndex >= 0 && !node.matches(':disabled') && !node.closest('[inert]') && node.getClientRects().length > 0 && getComputedStyle(node).visibility !== 'hidden')
    .sort((a, b) => (a.tabIndex > 0 ? a.tabIndex : Infinity) - (b.tabIndex > 0 ? b.tabIndex : Infinity))
}

// Presentation-only modal ownership. Nested model dialogs temporarily own focus.
export function useSwarmModalFocus(ref: RefObject<HTMLElement | null>, open: boolean, onClose: () => void) {
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  useLayoutEffect(() => {
    const dialog = ref.current
    if (!open || !dialog) return
    const previous = document.activeElement as HTMLElement | null
    const ownsFocus = () => {
      const modals = Array.from(document.querySelectorAll<HTMLElement>('[aria-modal="true"]')).filter(node => node.getClientRects().length > 0)
      const layer = (node: HTMLElement) => {
        let value = 0
        for (let element: HTMLElement | null = node; element; element = element.parentElement) value = Math.max(value, Number.parseInt(getComputedStyle(element).zIndex) || 0)
        return value
      }
      const top = modals.reduce<HTMLElement | undefined>((current, node) => !current || layer(node) >= layer(current) ? node : current, undefined)
      return !top || top === dialog
    }
    const focusFirst = () => (visibleFocusTargets(dialog)[0] ?? dialog).focus()
    focusFirst()
    const keydown = (event: KeyboardEvent) => {
      if (!ownsFocus()) return
      if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); closeRef.current(); return }
      if (event.key !== 'Tab') return
      const items = visibleFocusTargets(dialog)
      const first = items[0], last = items[items.length - 1]
      if (!first) { event.preventDefault(); dialog.focus(); return }
      if (!dialog.contains(document.activeElement) || (event.shiftKey && document.activeElement === first)) { event.preventDefault(); (event.shiftKey ? last : first).focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
    }
    const focusin = (event: FocusEvent) => { if (ownsFocus() && !dialog.contains(event.target as Node)) focusFirst() }
    document.addEventListener('keydown', keydown)
    document.addEventListener('focusin', focusin)
    return () => {
      document.removeEventListener('keydown', keydown)
      document.removeEventListener('focusin', focusin)
      // Inert and media-query presentation must settle before restoring focus.
      queueMicrotask(() => {
        if (previous?.isConnected && previous.getClientRects().length && !previous.closest('[inert]') && getComputedStyle(previous).visibility !== 'hidden') previous.focus()
        else {
          const shell = dialog.closest<HTMLElement>('.swarm-responsive-shell')
          if (shell) {
            const fallback = shell.dataset.layout === 'expanded' ? shell.querySelector<HTMLElement>('.swarm-route-navigation a') : shell.querySelector<HTMLElement>('.swarm-layout-controls button')
            fallback?.focus()
          }
        }
      })
    }
  }, [ref, open])
}

// Route, project and session/draft authority stay with OrchestrateView.
export function useSwarmResponsiveLayout(root: HTMLElement | null) {
  const [breakpoints, setBreakpoints] = useState({ mode: 'phone' as 'phone' | 'rail' | 'expanded', split: false })
  const { mode, split } = breakpoints
  const [panel, setPanel] = useState<'main' | 'chat'>('main')
  const [navigationOpen, setNavigationOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const mainRef = useRef<HTMLButtonElement>(null)
  const chatRef = useRef<HTMLButtonElement>(null)
  const navigationRef = useRef<HTMLElement | null>(null)
  const navigationId = useId()

  useEffect(() => {
    if (!root) return
    const measure = (width: number) => {
      const next = { mode: width >= 1280 ? 'expanded' as const : width >= 640 ? 'rail' as const : 'phone' as const, split: width >= 1100 }
      setBreakpoints(current => current.mode === next.mode && current.split === next.split ? current : next)
    }
    measure(root.getBoundingClientRect().width)
    const observer = new ResizeObserver(entries => {
      const box = entries[0]?.borderBoxSize
      measure((Array.isArray(box) ? box[0]?.inlineSize : (box as unknown as ResizeObserverSize | undefined)?.inlineSize) ?? root.getBoundingClientRect().width)
    })
    observer.observe(root)
    return () => observer.disconnect()
  }, [root])

  useEffect(() => { setNavigationOpen(false) }, [mode])
  navigationRef.current = root?.querySelector<HTMLElement>('.swarm-navigation-sidebar') ?? null
  useSwarmModalFocus(navigationRef, navigationOpen, () => setNavigationOpen(false))

  useLayoutEffect(() => {
    if (!root) return
    const nav = navigationRef.current
    const controls = root.querySelector<HTMLElement>('.swarm-layout-controls')
    const panes = root.querySelectorAll<HTMLElement>(':scope > .swarm-main-panel, :scope > .swarm-conversation-panel')
    for (const element of panes) {
      const name = element.classList.contains('swarm-main-panel') ? 'main' : 'chat'
      const inactive = navigationOpen || (!split && panel !== name)
      if (inactive && element.contains(document.activeElement)) {
        if (navigationOpen && nav) (visibleFocusTargets(nav)[0] ?? nav).focus()
        else (panel === 'main' ? mainRef : chatRef).current?.focus()
      }
      element.inert = inactive
      if (inactive) element.setAttribute('aria-hidden', 'true')
      else element.removeAttribute('aria-hidden')
    }
    if (controls) {
      if (navigationOpen && controls.contains(document.activeElement)) (nav && (visibleFocusTargets(nav)[0] ?? nav))?.focus()
      controls.inert = navigationOpen
      if (navigationOpen) controls.setAttribute('aria-hidden', 'true')
      else controls.removeAttribute('aria-hidden')
    }
    if (!navigationOpen && nav?.contains(document.activeElement) && mode === 'phone') triggerRef.current?.focus()
    if (!navigationOpen && document.activeElement instanceof HTMLElement && (!document.activeElement.getClientRects().length || getComputedStyle(document.activeElement).visibility === 'hidden')) {
      if (mode === 'expanded' || split) root.querySelector<HTMLElement>('.swarm-route-navigation a')?.focus()
      else (panel === 'main' ? mainRef : chatRef).current?.focus()
    }
  })

  return { mode, split, panel, setPanel, navigationOpen, setNavigationOpen, navigationId, triggerRef, mainRef, chatRef }
}

export function SwarmLayoutControls({ layout }: { layout: ReturnType<typeof useSwarmResponsiveLayout> }) {
  return <>
    <header className="swarm-layout-controls" aria-label="Swarm panels">
      <button type="button" ref={layout.triggerRef} aria-label="Open Swarm navigation" aria-expanded={layout.navigationOpen} aria-controls={layout.navigationId} onClick={() => layout.setNavigationOpen(true)}>☰ <span>Swarm</span></button>
      <div className="swarm-panel-switch" role="group" aria-label="Visible panel">
        <button type="button" ref={layout.mainRef} aria-pressed={layout.panel === 'main'} onClick={() => layout.setPanel('main')}>Main</button>
        <button type="button" ref={layout.chatRef} aria-pressed={layout.panel === 'chat'} onClick={() => layout.setPanel('chat')}>Chat</button>
      </div>
    </header>
    {layout.navigationOpen && <button type="button" className="swarm-navigation-backdrop" aria-label="Close Swarm navigation" tabIndex={-1} onClick={() => layout.setNavigationOpen(false)} />}
  </>
}
