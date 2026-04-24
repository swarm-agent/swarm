import { requestJson } from '../../../app/api'
import type {
  DurableNotificationRecord,
  NotificationClearResponse,
  NotificationSummaryRecord,
  NotificationSummaryResponse,
  NotificationsListResponse,
  NotificationUpdateResponse,
} from './types'

export async function fetchNotifications(limit = 200): Promise<DurableNotificationRecord[]> {
  const response = await requestJson<NotificationsListResponse>(`/v1/notifications?limit=${encodeURIComponent(String(limit))}`)
  return response.notifications ?? []
}

export async function fetchNotificationSummary(): Promise<NotificationSummaryRecord> {
  const response = await requestJson<NotificationSummaryResponse>('/v1/notifications/summary')
  return response.summary ?? {
    swarm_id: '',
    total_count: 0,
    unread_count: 0,
    active_count: 0,
    updated_at: 0,
  }
}

export async function clearNotifications(): Promise<NotificationClearResponse['result']> {
  const response = await requestJson<NotificationClearResponse>('/v1/notifications/clear', {
    method: 'POST',
  })
  return response.result ?? { swarm_id: '', deleted: 0 }
}

export async function updateNotification(id: string, input: {
  read?: boolean
  acked?: boolean
  muted?: boolean
  status?: string
}): Promise<{ notification: DurableNotificationRecord; summary?: NotificationSummaryRecord }> {
  const response = await requestJson<NotificationUpdateResponse>(`/v1/notifications/${encodeURIComponent(id)}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
  if (!response.notification) {
    throw new Error('Notification update response was missing notification payload')
  }
  return { notification: response.notification, summary: response.summary }
}
