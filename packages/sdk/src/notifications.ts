import type { SwarmTransport } from './transport.js';
import type {
  NotificationListFilter,
  NotificationRecord,
  NotificationSummary,
  SubmitNotificationParams,
  UpdateNotificationParams,
} from './types.js';

export class SwarmNotificationsNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /**
   * Submits a new notification to the Swarm Inbox (e.g. AI deliverable, pairing request, background job alert).
   */
  async submit(params: SubmitNotificationParams): Promise<NotificationRecord> {
    const res = await this.transport.request<{
      ok: boolean;
      notification: NotificationRecord;
      summary?: NotificationSummary;
    }>('/v1/notifications/inbox', {
      method: 'POST',
      body: params,
    });
    return res.data.notification;
  }

  /**
   * Lists notifications across the account/swarm, optionally filtered by limit and swarm_id.
   */
  async list(filter: NotificationListFilter = {}): Promise<NotificationRecord[]> {
    const q = new URLSearchParams();
    if (typeof filter.limit === 'number') q.set('limit', String(filter.limit));
    if (filter.swarm_id) q.set('swarm_id', filter.swarm_id);

    const path = q.toString() ? `/v1/notifications?${q.toString()}` : '/v1/notifications';
    const res = await this.transport.request<{
      ok: boolean;
      notifications?: NotificationRecord[];
    }>(path, {
      method: 'GET',
    });
    return res.data.notifications || [];
  }

  /**
   * Gets the notification summary count (total, unread, active).
   */
  async summary(swarmId?: string): Promise<NotificationSummary> {
    const path = swarmId
      ? `/v1/notifications/summary?swarm_id=${encodeURIComponent(swarmId)}`
      : '/v1/notifications/summary';
    const res = await this.transport.request<{
      ok: boolean;
      summary: NotificationSummary;
    }>(path, {
      method: 'GET',
    });
    return res.data.summary;
  }

  /**
   * Updates a notification (mark as read, acknowledged, muted, or update status).
   */
  async update(id: string, params: UpdateNotificationParams = {}): Promise<NotificationRecord> {
    const res = await this.transport.request<{
      ok: boolean;
      notification: NotificationRecord;
      summary?: NotificationSummary;
    }>(`/v1/notifications/${encodeURIComponent(id)}`, {
      method: 'POST',
      body: params,
    });
    return res.data.notification;
  }

  /**
   * Clears (deletes) resolved notifications.
   */
  async clear(swarmId?: string): Promise<{ deleted: number }> {
    const path = swarmId
      ? `/v1/notifications/clear?swarm_id=${encodeURIComponent(swarmId)}`
      : '/v1/notifications/clear';
    const res = await this.transport.request<{
      ok: boolean;
      result: { deleted: number; swarm_id: string };
    }>(path, {
      method: 'POST',
    });
    return { deleted: res.data.result?.deleted ?? 0 };
  }
}
