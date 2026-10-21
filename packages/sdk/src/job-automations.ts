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
 * An agent step's `fixes` option (reviewer workers only; the only value).
 * The daemon refuses it for any other worker.
 */
export type JobReviewerFixes = 'security';
/**
 * Where an agent step's agent works. `same`: a new session in the worktree of
 * the job the step follows, read-only (a reviewer only). `new`: its own
 * worktree at the job's head. `none`: the folder itself.
 */
export type JobAgentCheckout = 'same' | 'new' | 'none';
/** The most lines (added plus removed) a reviewer's fix commits may change. */
export const JOB_REVIEWER_FIX_MAX_LINES = 80;

/**
 * One condition of a step's `if`, on an earlier step: its status, or (an
 * agent step) the verdict of the review its job recorded. A missing verdict
 * never matches.
 */
export type JobAutomationCondition =
  | { step: string; status: 'succeeded' | 'failed'; verdict?: never }
  | { step: string; verdict: JobReviewVerdict; status?: never };

/** Which task and job ends a workflow applies to. */
export interface JobAutomationScope {
  /** Absolute path of the project folder. */
  folder: string;
  /** Only the tasks this one of Swarm's own agents finished in the folder. */
  role?: 'swarm' | 'coder' | 'finder' | 'designer';
  /** Only this worker's jobs (worker id). */
  worker?: string;
  /** Only this task or job (`task/<project>/<task>`, `worker/<worker>/<run>`, ...). */
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
  /**
   * run: the machine owner's script, as an absolute path or a path relative
   * to the folder (e.g. `deliver/push.sh`). It runs on the host as the
   * daemon's user, in the folder, with the machine's own credentials, and
   * only if the file (symlinks resolved) is owned by root and not writable by
   * group or others. A relative path must resolve to a singly linked file
   * inside the folder; prefer an absolute path in a directory only root can
   * write. A run step has no `secrets`: a step that sends them is refused.
   * Only the machine's owner may save a run step: the local owner or a key
   * with the `admin` scope; any other key gets 403 and nothing is stored.
   */
  command?: string;
  args?: string[];
  /**
   * agent: one of Swarm's own agents (`swarm`, `coder`, `finder`, `designer`),
   * run as a task in the folder (a coder starts at the work's head); `reviewer`;
   * or a worker id.
   */
  worker?: string;
  /** agent: the job's brief; the triggering job's result is attached (a reviewer also gets its brief, report, commands run and diff). */
  brief?: string;
  /**
   * agent, reviewer worker only: `security` lets the reviewer commit small,
   * security-only fixes on a branch at the reviewed head instead of blocking
   * for them. Its commits move the delivery head only with an OK verdict and
   * at most {@link JOB_REVIEWER_FIX_MAX_LINES} changed lines; more fails the
   * step (`reviewer fixes too large`); an OK whose fixes cannot be read from
   * the reviewer's checkout fails it (`reviewer fixes unreadable`). Without
   * it a reviewer is read-only.
   */
  fixes?: JobReviewerFixes;
  /**
   * agent: where the agent works; omit it for the agent's default. A reviewer
   * reads the reviewed job's own worktree in place (`same`, read-only; it
   * starts only at exactly the reviewed head with nothing uncommitted, and its
   * verdict is a block if the worktree changes during the review) or gets its
   * own checkout (`new`). A reviewer with `fixes`, the coder and any other
   * worker use `new`; swarm, finder and designer work in the folder (`none`).
   * The daemon refuses any other combination.
   */
  checkout?: JobAgentCheckout;
  /**
   * reopen: the earlier agent step whose findings are sent into the triggering
   * job's own session. Optional when `message` is set.
   */
  from?: string;
  /**
   * reopen: text placed before the findings, or sent alone when `from`
   * recorded none (an OK review) or is omitted. `{{steps.<id>.output}}` is
   * replaced by that earlier step's recorded output line. A reopen needs
   * `from`, `message` or both; with no findings and no message it fails.
   */
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
   * moved the delivery head); with `fixes`, the reviewer's `fix_commits`,
   * `fix_lines`, `fixes_applied` and `fixes_reason`; a reopen step's
   * `followup` and `round`.
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
  /** The reviewer fix commits the delivery head was moved to (agent steps with `fixes`). */
  fix_commits?: Array<{ sha: string; subject: string }>;
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
  kind: 'task' | 'worker' | 'media' | 'design';
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

/** Workflows and their deliveries (`/v3/workflows`, `/v3/job-deliveries`). */
export class SwarmJobAutomationsNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** This account's automations, each with its latest deliveries. */
  async list(): Promise<{ automations: JobAutomation[]; stopped: boolean }> {
    const res = await this.transport.request<{ ok: boolean; automations: JobAutomation[]; stopped: boolean }>('/v3/workflows', { method: 'GET' });
    return { automations: res.data.automations ?? [], stopped: Boolean(res.data.stopped) };
  }

  async get(id: string): Promise<JobAutomation> {
    const res = await this.transport.request<{ ok: boolean; automation: JobAutomation }>(`/v3/workflows/${encodeURIComponent(id)}`, { method: 'GET' });
    return res.data.automation;
  }

  /** Creates the automation, or updates the one with this name (its revision rises). */
  async set(params: SetJobAutomationParams): Promise<{ automation: JobAutomation; created: boolean }> {
    const res = await this.transport.request<{ ok: boolean; automation: JobAutomation; created: boolean }>('/v3/workflows', {
      method: 'POST',
      body: params,
    });
    return { automation: res.data.automation, created: Boolean(res.data.created) };
  }

  async delete(id: string): Promise<void> {
    await this.transport.request(`/v3/workflows/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }

  /** The machine-wide stop switch. */
  async stopState(): Promise<JobAutomationStop> {
    const res = await this.transport.request<{ ok: boolean; stop: JobAutomationStop }>('/v3/workflows/stop', { method: 'GET' });
    return res.data.stop;
  }

  /** While stopped, no new step starts on the machine; running steps finish. */
  async setStopped(stopped: boolean): Promise<JobAutomationStop> {
    const res = await this.transport.request<{ ok: boolean; stop: JobAutomationStop }>('/v3/workflows/stop', {
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
