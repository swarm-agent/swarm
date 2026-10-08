/** The existing nested-square Swarm identity, shared by startup and workspace UI. */
export function SwarmMark({ className = 'size-7' }: { className?: string }) {
  return <svg viewBox="0 0 400 400" className={className} aria-hidden="true">
    <rect x="20" y="20" width="360" height="360" rx="90" ry="90" fill="currentColor" opacity="0.15" />
    <rect x="60" y="60" width="280" height="280" rx="65" ry="65" fill="currentColor" opacity="0.35" />
    <rect x="100" y="100" width="200" height="200" rx="45" ry="45" fill="currentColor" opacity="0.60" />
    <rect x="140" y="140" width="120" height="120" rx="25" ry="25" fill="currentColor" opacity="0.85" />
  </svg>
}
