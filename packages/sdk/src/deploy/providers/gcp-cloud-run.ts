import { SwarmValidationError } from '../../errors.js';
import type { CloudDeployProvider, DeployCommand, DeployPlan, SwarmDeployManifest, ValidationResult } from '../types.js';

const reason = 'Cloud Run deployment is unsupported: ephemeral/scaled instances cannot own the single-writer durable Swarm store. Use a qualified immutable headless image with persistent volumes and a private same-container BFF. Worker deployment requires Swarm Orchestrator approval.';

/** Retained only to give existing callers an explicit migration error. */
export class GcpCloudRunProvider implements CloudDeployProvider {
  readonly target = 'gcp-cloud-run';
  validate(_manifest: SwarmDeployManifest): ValidationResult {
    return { valid: false, errors: [reason], warnings: [] };
  }
  generateArtifacts(_manifest: SwarmDeployManifest): Record<string, string> { throw new SwarmValidationError(reason); }
  generateCommands(_manifest: SwarmDeployManifest): DeployCommand[] { throw new SwarmValidationError(reason); }
  plan(_manifest: SwarmDeployManifest): DeployPlan { throw new SwarmValidationError(reason); }
}
