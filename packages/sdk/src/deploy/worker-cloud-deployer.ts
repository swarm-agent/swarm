import type { WorkerStorageHub } from '../storage/hub.js';
import type { WorkerActorSpec } from '../storage/types.js';
import { SwarmValidationError } from '../errors.js';

export interface WorkerDeployValidationResult { valid: boolean; errors: string[]; warnings: string[] }
export interface CloudRunJobDeployCommands { jobDeployCommand: string; jobDeployArgs: string[]; schedulerCommand?: string; schedulerArgs?: string[] }
export interface TaskExecuteCommand { executeCommand: string; executeArgs: string[] }
const reason = 'Cloud worker actor deployment is unsupported. Object storage is not a worker executor or the durable daemon store. Use canonical workers through Swarm Orchestrator deployment and approval.';

/** Compatibility tombstone: never provision a parallel worker runtime. */
export class WorkerCloudDeployer {
  static validate(_spec: WorkerActorSpec): WorkerDeployValidationResult {
    return { valid: false, errors: [reason], warnings: [] };
  }
  static createDefaultSpec(_params: { id: string; name: string; description?: string; instructions: string; model?: string; gcpProject?: string; cronSchedule?: string }): WorkerActorSpec {
    throw new SwarmValidationError(reason);
  }
  static async uploadToBucket(_spec: WorkerActorSpec, _hub: WorkerStorageHub): Promise<void> { throw new SwarmValidationError(reason); }
  static generateDeployCommands(_spec: WorkerActorSpec, _bucketName: string): CloudRunJobDeployCommands { throw new SwarmValidationError(reason); }
  static generateTaskExecuteCommand(_spec: WorkerActorSpec, _taskId: string): TaskExecuteCommand { throw new SwarmValidationError(reason); }
}
