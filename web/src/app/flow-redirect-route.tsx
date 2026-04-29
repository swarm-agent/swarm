import { useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'

export function FlowRedirectRoute() {
  const navigate = useNavigate()

  useEffect(() => {
    void navigate({ to: '/settings', search: { tab: 'flows' }, replace: true })
  }, [navigate])

  return null
}
