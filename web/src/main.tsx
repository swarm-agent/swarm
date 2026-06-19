import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { queryClient } from './app/query-client'
import { router } from './app/router'
import { readDesktopV3StartupInputFromLocation, startDesktopV3Refresh } from './features/desktop/state/desktop-v3-startup'
import './theme.css'

const navigatorWithStandalone = navigator as Navigator & { standalone?: boolean }
const isIOS = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)

if (isIOS && navigatorWithStandalone.standalone === true) {
  document.documentElement.classList.add('ios-standalone-pwa')
}

if ('serviceWorker' in navigator && window.isSecureContext) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => undefined)
  })
}

startDesktopV3Refresh(readDesktopV3StartupInputFromLocation(window.location))

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
)
