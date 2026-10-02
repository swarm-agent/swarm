import { SwarmValidationError } from '../../errors.js';
import type { CloudDeployProvider, DeployCommand, DeployPlan, SwarmDeployManifest, ValidationResult } from '../types.js';

const reason = 'Legacy Compute Engine provisioning is unsupported: its source build, public daemon and storage paths do not match the canonical runtime. Provision an approved single-writer host separately and use the qualified immutable headless image with persistent volumes and a private same-container BFF.';

/** No second daemon installer or implicit cloud provisioning authority. */
export class GcpComputeEngineProvider implements CloudDeployProvider {
  readonly target = 'gcp-compute-engine';
  validate(_manifest: SwarmDeployManifest): ValidationResult {
    return { valid: false, errors: [reason], warnings: [] };
  }
  generateArtifacts(_manifest: SwarmDeployManifest): Record<string, string> { throw new SwarmValidationError(reason); }
  generateCommands(_manifest: SwarmDeployManifest): DeployCommand[] { throw new SwarmValidationError(reason); }
  plan(_manifest: SwarmDeployManifest): DeployPlan { throw new SwarmValidationError(reason); }
}
