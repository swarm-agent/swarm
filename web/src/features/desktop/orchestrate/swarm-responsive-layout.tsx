import { useEffect, useLayoutEffect, useId, useRef, useState } from 'react'

// Presentation only: route, project selection and session/draft authority stay with
// OrchestrateView. Neither width changes nor pane switches replace its children.
export function useSwarmResponsiveLayout(root: HTMLElement | null) {
  const [width, setWidth] = useState(0)
  const [panel, setPanel] = useState<'main' | 'chat'>('main')
  const [navigationOpen, setNavigationOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const mainRef = useRef<HTMLButtonElement>(null)
  const chatRef = useRef<HTMLButtonElement>(null)
  const navigationId = useId()
  const mode = width >= 1280 ? 'expanded' : width >= 640 ? 'rail' : 'phone'
  const split = width >= 1100

  useEffect(() => {
    if (!root) return
    const observer = new ResizeObserver(entries => setWidth(entries[0].borderBoxSize[0]?.inlineSize ?? root.getBoundingClientRect().width))
    observer.observe(root)
    return () => observer.disconnect()
  }, [root])

  useEffect(() => { setNavigationOpen(false) }, [mode])

  useLayoutEffect(() => {
    if (!root) return
    const nav = root.querySelector<HTMLElement>('.swarm-navigation-sidebar')
    if (mode === 'phone' && !navigationOpen && nav?.contains(document.activeElement)) triggerRef.current?.focus()
  }, [root, mode, navigationOpen])

  useLayoutEffect(() => {
    if (!root) return
    const main = root.querySelector<HTMLElement>(':scope > .swarm-main-panel')
    const chat = root.querySelector<HTMLElement>(':scope > .swarm-conversation-panel')
    for (const [element, name] of [[main, 'main'], [chat, 'chat']] as const) {
      if (!element) continue
      const inactive = navigationOpen || (!split && panel !== name)
      if (inactive && element.contains(document.activeElement)) {
        (navigationOpen ? triggerRef : panel === 'main' ? mainRef : chatRef).current?.focus()
      }
      element.inert = inactive
      if (inactive) element.setAttribute('aria-hidden', 'true')
      else element.removeAttribute('aria-hidden')
    }
  })

  useEffect(() => {
    if (!root || !navigationOpen) return
    const nav = root.querySelector<HTMLElement>('.swarm-navigation-sidebar')
    if (!nav) return
    const previous = document.activeElement as HTMLElement | null
    const focusable = () => Array.from(nav.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]')).filter(node => node.getClientRects().length > 0)
    focusable()[0]?.focus()
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); setNavigationOpen(false) }
      if (event.key !== 'Tab') return
      const items = focusable()
      const first = items[0], last = items[items.length - 1]
      if (!first) { event.preventDefault(); nav.focus(); return }
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
    }
    const focusin = (event: FocusEvent) => {
      if (!nav.contains(event.target as Node)) focusable()[0]?.focus()
    }
    document.addEventListener('keydown', keydown)
    document.addEventListener('focusin', focusin)
    return () => {
      document.removeEventListener('keydown', keydown)
      document.removeEventListener('focusin', focusin)
      if (previous?.isConnected && previous.getClientRects().length) previous.focus()
      else if (triggerRef.current?.getClientRects().length) triggerRef.current.focus()
      else root.querySelector<HTMLElement>('.swarm-route-navigation a')?.focus()
    }
  }, [root, navigationOpen])

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
