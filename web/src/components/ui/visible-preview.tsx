import { useEffect, useRef, useState, type ReactNode } from 'react'

/** Mount expensive preview content only as it enters the scroll viewport. */
export function VisiblePreview({ children, label = 'Preview' }: { children: ReactNode; label?: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') { setVisible(true); return }
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { setVisible(true); observer.disconnect() }
    })
    if (ref.current) observer.observe(ref.current)
    return () => observer.disconnect()
  }, [])
  return <span ref={ref} className="block w-full h-full min-h-16">{visible ? children : <span>{label}</span>}</span>
}
