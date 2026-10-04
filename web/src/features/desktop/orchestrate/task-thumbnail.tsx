import { useState } from 'react'

// A failed derivative is an explicit lightweight placeholder, never an original
// image retry or a video metadata request. A new exact source gets a fresh try.
export function TaskThumbnail({ src, title }: { src?: string; title: string }) {
  const [failedSource, setFailedSource] = useState<string>()
  return src && failedSource !== src
    ? <img src={src} alt={title} loading="lazy" decoding="async" fetchPriority="low" onError={() => setFailedSource(src)} />
    : <span role="img" aria-label={`${title}: small preview unavailable`}>Preview unavailable · open original</span>
}
