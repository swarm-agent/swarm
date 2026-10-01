/**
 * Cloud Worker Actor Deployer for Google Cloud Platform (Cloud Run Jobs & Cloud Scheduler).
 * Manages authoring, validation, GCS bucket synchronization, and unattended gcloud provisioning.
 */

import type { WorkerStorageHub } from '../storage/hub.js';
import type { WorkerActorSpec, WorkerActorTask } from '../storage/types.js';

export interface WorkerDeployValidationResult {
  valid: boolean;
  errors: string[];
  warnings: string[];
}

export interface CloudRunJobDeployCommands {
  jobDeployCommand: string;
  jobDeployArgs: string[];
  schedulerCommand?: string;
  schedulerArgs?: string[];
}

export interface TaskExecuteCommand {
  executeCommand: string;
  executeArgs: string[];
}

export class WorkerCloudDeployer {
  /**
   * Validates a WorkerActorSpec before deployment or upload.
   */
  static validate(spec: WorkerActorSpec): WorkerDeployValidationResult {
    const errors: string[] = [];
    const warnings: string[] = [];

    if (!spec.id || typeof spec.id !== 'string') {
      errors.push('Worker id is required.');
    } else if (!/^[a-z0-9-]+$/.test(spec.id)) {
      errors.push(`Invalid worker id "${spec.id}": must be lowercase alphanumeric with hyphens.`);
    }

    if (!spec.name || typeof spec.name !== 'string') {
      errors.push('Worker name is required.');
    }

    if (!spec.brain || typeof spec.brain !== 'object') {
      errors.push('Worker brain configuration is required.');
    } else {
      if (!spec.brain.model) {
        errors.push('Worker brain.model is required.');
      }
      if (!spec.brain.instructions || spec.brain.instructions.trim().length === 0) {
        errors.push('Worker brain.instructions cannot be empty.');
      }
    }

    if (spec.schedule) {
      if (spec.schedule.kind === 'cron' && !spec.schedule.cron) {
        errors.push('schedule.cron string is required when schedule.kind is "cron".');
      }
    }

    if (spec.cloud) {
      if (spec.cloud.provider !== 'gcp' && spec.cloud.provider !== 'aws') {
        errors.push(`Unsupported cloud provider "${spec.cloud.provider}". Supported: "gcp", "aws".`);
      }
      if (spec.cloud.provider === 'gcp') {
        if (!spec.cloud.project) {
          warnings.push('cloud.project is empty; will default to active GCP project or lease.');
        }
      }
    }

    return {
      valid: errors.length === 0,
      errors,
      warnings,
    };
  }

  /**
   * Creates a default WorkerActorSpec with best-practice serverless defaults.
   */
  static createDefaultSpec(params: {
    id: string;
    name: string;
    description?: string;
    instructions: string;
    model?: string;
    gcpProject?: string;
    cronSchedule?: string;
  }): WorkerActorSpec {
    const now = new Date().toISOString();
    return {
      $schema: 'https://swarmagent.dev/schemas/v1/worker-actor.json',
      id: params.id,
      name: params.name,
      description: params.description,
      version: '1.0.0',
      status: 'pending_approval',
      cloud: {
        provider: 'gcp',
        project: params.gcpProject,
        region: 'us-central1',
        runtime: 'cloud-run-job',
        job_name: `worker-${params.id}`,
        service_account: '448561931009-compute@developer.gserviceaccount.com',
      },
      schedule: params.cronSchedule
        ? {
            kind: 'cron',
            cron: params.cronSchedule,
            timezone: 'America/New_York',
            enabled: true,
          }
        : {
            kind: 'trigger',
            enabled: true,
          },
      brain: {
        model: params.model || 'gemini-3.8-flash',
        thinking_level: 'low',
        instructions: params.instructions,
      },
      tasks: [],
      createdAt: now,
      updatedAt: now,
    };
  }

  /**
   * Uploads the worker actor specification to the designated storage bucket.
   */
  static async uploadToBucket(spec: WorkerActorSpec, hub: WorkerStorageHub): Promise<void> {
    const validation = this.validate(spec);
    if (!validation.valid) {
      throw new Error(`Invalid worker specification: ${validation.errors.join(', ')}`);
    }
    await hub.saveWorkerActor(spec);
  }

  /**
   * Generates standard gcloud deployment commands for Cloud Run Job and Cloud Scheduler.
   */
  static generateDeployCommands(spec: WorkerActorSpec, bucketName: string): CloudRunJobDeployCommands {
    const cloud = spec.cloud || { provider: 'gcp', runtime: 'cloud-run-job' };
    const project = cloud.project || '${PROJECT_ID}';
    const region = cloud.region || 'us-central1';
    const jobName = cloud.job_name || `worker-${spec.id}`;
    const image = cloud.image || `us-docker.pkg.dev/${project}/swarm/worker-runner:latest`;
    const serviceAccount = cloud.service_account || '448561931009-compute@developer.gserviceaccount.com';

    const jobDeployArgs = [
      'run',
      'jobs',
      'deploy',
      jobName,
      `--project=${project}`,
      `--region=${region}`,
      `--image=${image}`,
      `--set-env-vars=GCS_BUCKET=${bucketName},WORKER_ID=${spec.id},SWARM_DISABLE_MINT_REPORT=1`,
      `--service-account=${serviceAccount}`,
      '--task-timeout=600s',
      '--max-retries=0',
    ];

    const jobDeployCommand = `gcloud ${jobDeployArgs.join(' ')}`;

    let schedulerCommand: string | undefined;
    let schedulerArgs: string[] | undefined;

    if (spec.schedule?.kind === 'cron' && spec.schedule.cron) {
      const scheduleJobName = `${jobName}-cron`;
      const timezone = spec.schedule.timezone || 'UTC';
      const uri = `https://run.googleapis.com/v2/projects/${project}/locations/${region}/jobs/${jobName}:run`;

      schedulerArgs = [
        'scheduler',
        'jobs',
        'create',
        'http',
        scheduleJobName,
        `--project=${project}`,
        `--location=${region}`,
        `--schedule=${spec.schedule.cron}`,
        `--time-zone=${timezone}`,
        `--uri=${uri}`,
        '--http-method=POST',
        `--oauth-service-account-email=${serviceAccount}`,
      ];

      schedulerCommand = `gcloud ${schedulerArgs.join(' ')}`;
    }

    return {
      jobDeployCommand,
      jobDeployArgs,
      schedulerCommand,
      schedulerArgs,
    };
  }

  /**
   * Generates gcloud execution command for triggering a specific one-off task.
   */
  static generateTaskExecuteCommand(spec: WorkerActorSpec, taskId: string): TaskExecuteCommand {
    const cloud = spec.cloud || { provider: 'gcp', runtime: 'cloud-run-job' };
    const project = cloud.project || '${PROJECT_ID}';
    const region = cloud.region || 'us-central1';
    const jobName = cloud.job_name || `worker-${spec.id}`;

    const executeArgs = [
      'run',
      'jobs',
      'execute',
      jobName,
      `--project=${project}`,
      `--region=${region}`,
      `--args=--task-id=${taskId}`,
    ];

    return {
      executeCommand: `gcloud ${executeArgs.join(' ')}`,
      executeArgs,
    };
  }
}
