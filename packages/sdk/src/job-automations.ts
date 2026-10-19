import type { SwarmTransport } from './transport.js';

/**
 * Job automations: the steps Swarm runs when a job ends. Swarm gives
 * primitives, never a built-in path: with no automation a job ends with its
 * branch and the person integrates it by hand. Reading needs
 * `automations:read`; changing needs `automations:write`.
 * See docs/automations.md.
 */

export type JobAutomationWhen = 'succeeded' | 'failed' | 'always';
export type JobAutomationStepType = 'run' | 'agent' | 'reopen';
export type JobDeliveryState = 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'stopped' | 'blocked';
export type JobReviewVerdict = 'ok' | 'block';

/**
 * One condition of a step's `if`, on an earlier step: its status, or (an
 * agent step) the verdict of the review its job recorded. A missing verdict
 * never matches.
 */
export type JobAutomationCondition =
  | { step: string; status: 'succeeded' | 'failed'; verdict?: never }
  | { step: string; verdict: JobReviewVerdict; status?: never };

/** Which job ends an automation applies to. */
export interface JobAutomationScope {
  /** Absolute path of the project folder. */
  folder: string;
  /** Only this worker's jobs (worker id). */
  worker?: string;
  /** Only this job (job id, e.g. `worker/<worker>/<run>`). */
  job?: string;
  /** Only the jobs of this schedule (schedule id). */
  schedule?: string;
}

export interface JobAutomationStep {
  /** Lowercase id; default `step-N`. */
  id?: string;
  type: JobAutomationStepType;
  /** 1 to 3600; default 600. */
  timeout_seconds?: number;
  /** Default false: a failed step stops the rest. */
  continue_on_failure?: boolean;
  /** Conditions that must all hold for the step to run; otherwise it is skipped and later steps still run. */
  if?: JobAutomationCondition[];
  /** run: path of an executable script inside the folder, e.g. `deliver/push.sh`. */
  command?: string;
  args?: string[];
  /** run: secret names the script needs (granted to the folder). */
  secrets?: string[];
  /** agent: `coder`, `designer`, `finder`, `reviewer` or a worker id. */
  worker?: string;
  /** agent: the job's brief; the triggering job's result is attached (a reviewer also gets its brief, report, commands run and diff). */
  brief?: string;
  /** reopen: the earlier agent step whose findings are sent into the triggering job's own session. */
  from?: string;
  /** reopen: text placed before the findings. */
  message?: string;
  /** reopen: reopens allowed before the delivery ends `blocked` (`rounds_exhausted`); 1 to 5, default 2. */
  max_rounds?: number;
}

export interface JobAutomation {
  id: string;
  name: string;
  scope: JobAutomationScope;
  when: JobAutomationWhen;
  enabled: boolean;
  /** Automatic retries of a failed step, 0 to 3. */
  retries: number;
  /** Ordered steps, with their defaults applied (id, timeout_seconds). */
  steps: JobAutomationStep[];
  /** Rises on every update; a delivery keeps the revision it started with. */
  revision: number;
  created_at: number;
  updated_at: number;
  latest_deliveries?: JobDelivery[];
}

export interface SetJobAutomationParams {
  name: string;
  scope: JobAutomationScope;
  when?: JobAutomationWhen;
  enabled?: boolean;
  retries?: number;
  steps: JobAutomationStep[];
}

export interface JobDeliveryStep {
  id: string;
  type: JobAutomationStepType;
  state: JobDeliveryState;
  attempts: number;
  started_at?: number;
  finished_at?: number;
  next_attempt_at?: number;
  exit_code?: number;
  /** The last non-empty line the step printed (a run step), or the job id (an agent step). */
  output?: string;
  reason?: string;
  /** The next allowed action after a failure, e.g. `rerun` or `fix the script`. */
  next?: string;
  /**
   * An agent step's job: `job_id`, `status`, `head`, `report`, `review`, and
   * `verdict`/`findings` when the job recorded a review (`moved_head` when it
   * moved the delivery head); a reopen step's `followup` and `round`.
   */
  outcome?: Record<string, unknown>;
  /** The last 64 KiB of a run step's output (single-delivery reads only). */
  log_tail?: string;
}

/** One (job end, automation revision) pair; runs at most once. */
export interface JobDelivery {
  delivery_id: string;
  automation_id: string;
  automation: string;
  automation_revision: number;
  job_id: string;
  /** The follow-up this job end belongs to; absent for the job's first run. */
  followup?: string;
  /** Reopens that led to this job end: a follow-up a reopen step sent is the previous round + 1. */
  round: number;
  state: JobDeliveryState;
  /** The commit the delivery delivers: the job's head, moved forward by an agent step that committed on top of it. */
  head?: string;
  /** Why the delivery ended `blocked`, e.g. `rounds_exhausted`. */
  blocked_reason?: string;
  steps: JobDeliveryStep[];
  created_at: number;
  started_at?: number;
  finished_at?: number;
  reruns?: number;
  /** The job's result, on single-delivery reads. */
  result?: JobResult;
}

/** The result record a job end produces; a run step gets it on stdin. */
export interface JobResult {
  v: number;
  job_id: string;
  kind: 'worker' | 'media' | 'design';
  role?: string;
  worker?: { id: string; name?: string };
  run_id: string;
  followup?: string;
  project_id?: string;
  folder?: string;
  status: 'succeeded' | 'failed' | 'cancelled';
  job_status?: string;
  finished_at: number;
  brief?: string;
  report?: string;
  error?: string;
  request_source?: string;
  schedule_id?: string;
  git?: {
    branch: string;
    base_branch?: string;
    base_commit: string;
    head: string;
    commits: Array<{ sha: string; subject: string }>;
    files: string[];
    worktree?: string;
  };
  files?: Array<{ path: string; sha256: string; type: string; size?: number }>;
  cost?: { tokens: number; estimated_usd?: number; model?: string };
  review?: Record<string, unknown>;
}

export interface JobAutomationStop {
  stopped: boolean;
  at?: number;
  by?: string;
}

export interface ListJobDeliveriesParams {
  jobId?: string;
  automationId?: string;
  state?: JobDeliveryState;
  /** Default 50, at most 200. */
  limit?: number;
}

/** Job automations and their deliveries (`/v3/job-automations`, `/v3/job-deliveries`). */
export class SwarmJobAutomationsNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** This account's automations, each with its latest deliveries. */
  async list(): Promise<{ automations: JobAutomation[]; stopped: boolean }> {
    const res = await this.transport.request<{ ok: boolean; automations: JobAutomation[]; stopped: boolean }>('/v3/job-automations', { method: 'GET' });
    return { automations: res.data.automations ?? [], stopped: Boolean(res.data.stopped) };
  }

  async get(id: string): Promise<JobAutomation> {
    const res = await this.transport.request<{ ok: boolean; automation: JobAutomation }>(`/v3/job-automations/${encodeURIComponent(id)}`, { method: 'GET' });
    return res.data.automation;
  }

  /** Creates the automation, or updates the one with this name (its revision rises). */
  async set(params: SetJobAutomationParams): Promise<{ automation: JobAutomation; created: boolean }> {
    const res = await this.transport.request<{ ok: boolean; automation: JobAutomation; created: boolean }>('/v3/job-automations', {
      method: 'POST',
      body: params,
    });
    return { automation: res.data.automation, created: Boolean(res.data.created) };
  }

  async delete(id: string): Promise<void> {
    await this.transport.request(`/v3/job-automations/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  /** The machine-wide stop switch. */
  async stopState(): Promise<JobAutomationStop> {
    const res = await this.transport.request<{ ok: boolean; stop: JobAutomationStop }>('/v3/job-automations/stop', { method: 'GET' });
    return res.data.stop;
  }

  /** While stopped, no new step starts on the machine; running steps finish. */
  async setStopped(stopped: boolean): Promise<JobAutomationStop> {
    const res = await this.transport.request<{ ok: boolean; stop: JobAutomationStop }>('/v3/job-automations/stop', {
      method: 'POST',
      body: { stopped },
    });
    return res.data.stop;
  }

  /** Deliveries, newest first. */
  async deliveries(params: ListJobDeliveriesParams = {}): Promise<JobDelivery[]> {
    const q = new URLSearchParams();
    if (params.jobId) q.set('job_id', params.jobId);
    if (params.automationId) q.set('automation_id', params.automationId);
    if (params.state) q.set('state', params.state);
    if (typeof params.limit === 'number') q.set('limit', String(params.limit));
    const path = q.toString() ? `/v3/job-deliveries?${q.toString()}` : '/v3/job-deliveries';
    const res = await this.transport.request<{ ok: boolean; deliveries: JobDelivery[] }>(path, { method: 'GET' });
    return res.data.deliveries ?? [];
  }

  /** One delivery with its steps' log tails and the job's result. */
  async delivery(id: string): Promise<JobDelivery> {
    const res = await this.transport.request<{ ok: boolean; delivery: JobDelivery }>(`/v3/job-deliveries/${encodeURIComponent(id)}`, { method: 'GET' });
    return res.data.delivery;
  }

  /**
   * Re-runs only the delivery's failed or stopped steps (a blocked delivery
   * runs again from its first step with a new round budget); `rerun` is false
   * when there were none.
   */
  async rerun(id: string): Promise<{ rerun: boolean; delivery: JobDelivery }> {
    const res = await this.transport.request<{ ok: boolean; rerun: boolean; delivery: JobDelivery }>(`/v3/job-deliveries/${encodeURIComponent(id)}/rerun`, {
      method: 'POST',
      body: {},
    });
    return { rerun: Boolean(res.data.rerun), delivery: res.data.delivery };
  }
}
