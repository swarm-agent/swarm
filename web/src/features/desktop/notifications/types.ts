import type {
  DesktopConnectionState,
  DesktopNotificationAction,
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
} from '../types/realtime'

export interface DurableNotificationAction {
  id: string
  label: string
  action_type?: string
  endpoint?: string
  variant?: string
}

export interface DurableNotificationRecord {
  id: string
  swarm_id: string
  origin_swarm_id?: string
  session_id?: string
  run_id?: string
  category: string
  kind?: string
  severity: 'info' | 'warning' | 'error' | string
  title: string
  body: string
  status: 'active' | 'resolved' | string
  source_event_type?: string
  permission_id?: string
  tool_name?: string
  requirement?: string
  session_title?: string
  session_label?: string
  workspace_path?: string
  workspace_name?: string
  origin_label?: string
  worker_id?: string
  verified?: boolean
  action_url?: string
  payload?: Record<string, unknown>
  actions?: DurableNotificationAction[]
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
  summary?: NotificationSummaryRecord
}

export interface NotificationClearResponse {
  ok?: boolean
  result?: {
    swarm_id: string
    deleted: number
  }
}

export type {
  DesktopConnectionState,
  DesktopNotificationAction,
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
}
