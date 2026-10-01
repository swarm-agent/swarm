/**
 * Deployment namespace and multi-cloud providers for Swarm.
 */

import { SwarmNotFoundError, SwarmValidationError } from '../errors.js';
import type { SwarmTransport } from '../transport.js';
import { generateDockerIgnore, generateOptimizedDockerfile } from './dockerfile.js';
import { createDeployManifest, loadDeployManifest, serializeDeployManifest } from './manifest.js';
import { GcpCloudRunProvider } from './providers/gcp-cloud-run.js';
import { GcpComputeEngineProvider } from './providers/gcp-compute.js';
import type {
  CloudDeployProvider,
  CloudTarget,
  CreateManifestOptions,
  DeployCommand,
  DeployExecutionResult,
  DeployPlan,
  ExecuteDeployOptions,
  SwarmDeployManifest,
  ValidationResult,
} from './types.js';

declare const process: any;

export * from './types.js';
export * from './manifest.js';
export * from './dockerfile.js';
export * from './providers/gcp-cloud-run.js';
export * from './providers/gcp-compute.js';
export * from './worker-cloud-deployer.js';

export class SwarmDeployNamespace {
  private readonly providers = new Map<string, CloudDeployProvider>();

  constructor(protected readonly transport?: SwarmTransport) {
    // Register default providers
    this.registerProvider(new GcpCloudRunProvider());
    this.registerProvider(new GcpComputeEngineProvider());
  }

  /**
   * Registers a new cloud deployment provider (extensible for AWS, Azure, Fly.io, etc.).
   */
  registerProvider(provider: CloudDeployProvider): void {
    this.providers.set(provider.target, provider);
  }

  /**
   * Retrieves the deployment provider registered for a target platform.
   */
  getProvider(target: CloudTarget): CloudDeployProvider {
    const provider = this.providers.get(target);
    if (!provider) {
      const available = Array.from(this.providers.keys()).join(', ');
      throw new SwarmNotFoundError(
        `No deployment provider registered for target '${target}'. Available targets: ${available}`,
        { code: 'PROVIDER_NOT_FOUND' }
      );
    }
    return provider;
  }

  /**
   * Returns all currently registered cloud deployment providers.
   */
  listProviders(): CloudDeployProvider[] {
    return Array.from(this.providers.values());
  }

  /**
   * Creates a typed deployment manifest using proven best-practice presets.
   */
  createManifest(options?: CreateManifestOptions): SwarmDeployManifest {
    return createDeployManifest(options);
  }

  /**
   * Parses, resolves environment variables, and validates a deployment manifest.
   */
  loadManifest(
    input: string | Record<string, unknown>,
    envLookup?: Record<string, string>
  ): SwarmDeployManifest {
    return loadDeployManifest(input, envLookup);
  }

  /**
   * Serializes a manifest to formatted JSON.
   */
  serializeManifest(manifest: SwarmDeployManifest, indent = 2): string {
    return serializeDeployManifest(manifest, indent);
  }

  /**
   * Validates a deployment manifest against its target provider rules.
   */
  validate(manifest: SwarmDeployManifest): ValidationResult {
    const provider = this.getProvider(manifest.target);
    return provider.validate(manifest);
  }

  /**
   * Generates a complete, structured deployment plan including artifacts, shell commands, and cost estimates.
   */
  plan(
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown>
  ): DeployPlan {
    const manifest = this.ensureManifest(input);
    const provider = this.getProvider(manifest.target);
    return provider.plan(manifest);
  }

  /**
   * Generates all required deployment artifacts (Dockerfile, scripts, config files) as a key-value record.
   */
  generateArtifacts(
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown>
  ): Record<string, string> {
    const manifest = this.ensureManifest(input);
    const provider = this.getProvider(manifest.target);
    return provider.generateArtifacts(manifest);
  }

  /**
   * Generates sequential shell commands required to execute the build and deployment.
   */
  generateCommands(
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown>
  ): DeployCommand[] {
    const manifest = this.ensureManifest(input);
    const provider = this.getProvider(manifest.target);
    return provider.generateCommands(manifest);
  }

  /**
   * Writes all generated deployment artifacts to a target directory on disk.
   */
  async writeArtifacts(
    targetDir: string,
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown> | DeployPlan
  ): Promise<string[]> {
    const plan =
      typeof input === 'object' && input !== null && 'artifacts' in input
        ? (input as DeployPlan)
        : this.plan(input as any);
    const modFs = 'node:fs';
    const modPath = 'node:path';
    const fs: any = await import(modFs);
    const path: any = await import(modPath);
    await fs.promises.mkdir(targetDir, { recursive: true });
    const writtenFiles: string[] = [];
    for (const [filename, content] of Object.entries(plan.artifacts)) {
      const filePath = path.join(targetDir, filename);
      await fs.promises.writeFile(filePath, content, 'utf8');
      if (filename.endsWith('.sh')) {
        await fs.promises.chmod(filePath, 0o755).catch(() => {});
      }
      writtenFiles.push(filePath);
    }
    return writtenFiles;
  }

  /**
   * Programmatically executes the deployment plan.
   * In dryRun mode, validates prerequisites, writes artifacts, and simulates command execution.
   * In live mode, executes sequential pipeline commands and verifies service reachability.
   */
  async execute(
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown> | DeployPlan,
    options: ExecuteDeployOptions = {}
  ): Promise<DeployExecutionResult> {
    const startTime = Date.now();
    const plan =
      typeof input === 'object' && input !== null && 'artifacts' in input
        ? (input as DeployPlan)
        : this.plan(input as any);

    const modFs = 'node:fs';
    const modPath = 'node:path';
    const modOs = 'node:os';
    const modCp = 'node:child_process';
    const modUtil = 'node:util';
    const fs: any = await import(modFs);
    const path: any = await import(modPath);
    const os: any = await import(modOs);
    const { exec }: any = await import(modCp);
    const { promisify }: any = await import(modUtil);
    const execAsync = promisify(exec);

    const targetDir =
      options.outputDir || (await fs.promises.mkdtemp(path.join(os.tmpdir(), 'swarm-deploy-')));

    await this.writeArtifacts(targetDir, plan);

    const stepOutputs: Record<string, string> = {};
    let serviceUrl: string | undefined;
    let externalIp: string | undefined;

    if (options.dryRun) {
      for (const cmd of plan.commands) {
        options.onLog?.(`[DRY RUN] [${cmd.phase}] ${cmd.title}: ${cmd.command}`, cmd.phase);
        stepOutputs[cmd.title] = `(simulated: ${cmd.command})`;
      }
      return {
        success: true,
        target: plan.target,
        durationMs: Date.now() - startTime,
        stepOutputs,
        outputDir: targetDir,
      };
    }

    try {
      for (const cmd of plan.commands) {
        // Skip cleanup step during initial deployment execution
        if (cmd.phase === 'cleanup') {
          continue;
        }

        options.onLog?.(`[${cmd.phase}] ${cmd.title}...`, cmd.phase);

        const procEnv = typeof process !== 'undefined' ? process.env : {};
        const execEnv = {
          ...procEnv,
          ...(options.env || {}),
        };

        const { stdout, stderr } = await execAsync(cmd.command, {
          cwd: targetDir,
          env: execEnv,
          timeout: 600_000,
        });

        const output = (stdout + (stderr ? `\n${stderr}` : '')).trim();
        stepOutputs[cmd.title] = output;
        options.onLog?.(output, cmd.phase);

        // Extract Cloud Run URL or Compute NAT IP if present
        const urlMatch = output.match(/https:\/\/[a-z0-9-]+\.[a-z0-9.-]+\.run\.app/i);
        if (urlMatch) {
          serviceUrl = urlMatch[0];
        }
        const ipMatch = output.match(/\b(?:\d{1,3}\.){3}\d{1,3}\b/);
        if (ipMatch && plan.target === 'gcp-compute-engine' && cmd.phase === 'verify') {
          externalIp = ipMatch[0];
        }
      }

      return {
        success: true,
        target: plan.target,
        durationMs: Date.now() - startTime,
        stepOutputs,
        serviceUrl,
        externalIp,
        outputDir: targetDir,
      };
    } catch (err: any) {
      return {
        success: false,
        target: plan.target,
        durationMs: Date.now() - startTime,
        stepOutputs,
        outputDir: targetDir,
        error: err.message || String(err),
      };
    }
  }

  private ensureManifest(
    input: SwarmDeployManifest | CreateManifestOptions | string | Record<string, unknown>
  ): SwarmDeployManifest {
    if (typeof input === 'string') {
      return this.loadManifest(input);
    }
    if (typeof input === 'object' && input !== null) {
      if ('version' in input && input.version === '1.0' && 'target' in input && 'source' in input) {
        return input as SwarmDeployManifest;
      }
      return this.createManifest(input as CreateManifestOptions);
    }
    return this.createManifest();
  }
}
