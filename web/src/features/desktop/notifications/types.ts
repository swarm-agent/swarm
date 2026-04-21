import type {
  DesktopConnectionState,
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
} from '../types/realtime'

export interface DurableNotificationRecord {
  id: string
  swarm_id: string
  origin_swarm_id?: string
  session_id?: string
  run_id?: string
  category: string
  severity: 'info' | 'warning' | 'error' | string
  title: string
  body: string
  status: 'active' | 'resolved' | string
  source_event_type?: string
  permission_id?: string
  tool_name?: string
  requirement?: string
  read_at?: number
  acked_at?: number
  muted_at?: number
  created_at: number
  updated_at: number
}

export interface NotificationSummaryRecord {
  swarm_id: string
  total_count: number
  unread_count: number
  active_count: number
  updated_at: number
}

export interface NotificationsListResponse {
  ok?: boolean
  notifications?: DurableNotificationRecord[]
}

export interface NotificationSummaryResponse {
  ok?: boolean
  summary?: NotificationSummaryRecord
}

export interface NotificationUpdateResponse {
  ok?: boolean
  notification?: DurableNotificationRecord
}

export type {
  DesktopConnectionState,
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
}
