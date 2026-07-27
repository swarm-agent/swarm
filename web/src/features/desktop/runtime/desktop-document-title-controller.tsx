import { useEffect } from 'react'
import { useLocation } from '@tanstack/react-router'

import { useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { composeDesktopDocumentTitle } from './desktop-document-title'

export function DesktopDocumentTitleController() {
  const pathname = useLocation({ select: (location) => location.pathname })
  const title = useDesktopV3CacheSelector((state) => composeDesktopDocumentTitle({
    pathname,
    unreadCount: state.notificationSummary.unreadCount,
    sessionsById: state.sessionsById,
  }))

  useEffect(() => {
    document.title = title
  }, [title])

  return null
}
