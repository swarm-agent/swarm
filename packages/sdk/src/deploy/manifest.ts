/**
 * Manifest parsing, creation, and resolution for Swarm deployments.
 */

import { SwarmValidationError } from '../errors.js';
import type {
  CreateManifestOptions,
  DeploymentPreset,
  GcpCloudRunConfig,
  GcpComputeEngineConfig,
  SourceConfig,
  SwarmDeployManifest,
} from './types.js';

declare const process: any;

const CANONICAL_REPO = 'https://github.com/swarm-agent/swarm';

/**
 * Creates a well-formed SwarmDeployManifest using proven best-practice presets.
 */
export function createDeployManifest(options: CreateManifestOptions = {}): SwarmDeployManifest {
  const preset: DeploymentPreset = options.preset || 'latency-optimized';
  const name = options.name || 'swarm-agent-service';

  // Base source defaults to canonical GitHub repo
  const source: SourceConfig = {
    type: 'github',
    repository: CANONICAL_REPO,
    branch: 'main',
    buildStrategy: 'distroless',
    ...options.source,
  };

  let target = options.target;
  let gcpCloudRun: GcpCloudRunConfig | undefined;
  let gcpCompute: GcpComputeEngineConfig | undefined;

  switch (preset) {
    case 'latency-optimized':
      target = target || 'gcp-cloud-run';
      gcpCloudRun = {
        projectId: '${PROJECT_ID}',
        region: 'us-central1',
        serviceName: 'swarmd',
        cpu: '1',
        memory: '512Mi',
        minInstances: 0,
        maxInstances: 10,
        concurrency: 80,
        port: 18080,
        cpuBoost: true,
        executionEnvironment: 'gen2',
        allowUnauthenticated: false,
        timeoutSeconds: 300,
        ingress: 'all',
        ...options.gcpCloudRun,
      };
      break;

    case 'zero-idle-cost':
      target = target || 'gcp-cloud-run';
      gcpCloudRun = {
        projectId: '${PROJECT_ID}',
        region: 'us-central1',
        serviceName: 'swarmd-serverless',
        cpu: '1',
        memory: '512Mi',
        minInstances: 0,
        maxInstances: 5,
        concurrency: 80,
        port: 18080,
        cpuBoost: true,
        executionEnvironment: 'gen2',
        allowUnauthenticated: false,
        timeoutSeconds: 300,
        ingress: 'all',
        ...options.gcpCloudRun,
      };
      break;

    case 'always-on':
      target = target || 'gcp-cloud-run';
      gcpCloudRun = {
        projectId: '${PROJECT_ID}',
        region: 'us-central1',
        serviceName: 'swarmd-always-on',
        cpu: '1',
        memory: '512Mi',
        minInstances: 1,
        maxInstances: 20,
        concurrency: 80,
        port: 18080,
        cpuBoost: true,
        executionEnvironment: 'gen2',
        allowUnauthenticated: false,
        timeoutSeconds: 300,
        ingress: 'all',
        ...options.gcpCloudRun,
      };
      break;

    case 'free-tier-vm':
      target = target || 'gcp-compute-engine';
      gcpCompute = {
        projectId: '${PROJECT_ID}',
        zone: 'us-central1-a',
        instanceName: 'swarm-free-vm',
        machineType: 'e2-micro',
        diskSizeGb: 30,
        diskType: 'pd-standard',
        preemptible: false,
        port: 18080,
        tags: ['swarm-agent', 'http-server'],
        ...options.gcpCompute,
      };
      break;

    case 'high-concurrency-vm':
      target = target || 'gcp-compute-engine';
      gcpCompute = {
        projectId: '${PROJECT_ID}',
        zone: 'us-central1-a',
        instanceName: 'swarm-cluster-node',
        machineType: 'e2-small',
        diskSizeGb: 50,
        diskType: 'pd-balanced',
        preemptible: false,
        port: 18080,
        tags: ['swarm-agent', 'http-server'],
        ...options.gcpCompute,
      };
      break;

    default:
      target = target || 'gcp-cloud-run';
      gcpCloudRun = {
        projectId: '${PROJECT_ID}',
        region: 'us-central1',
        serviceName: 'swarmd',
        cpu: '1',
        memory: '512Mi',
        minInstances: 0,
        maxInstances: 10,
        concurrency: 80,
        port: 18080,
        cpuBoost: true,
        executionEnvironment: 'gen2',
        allowUnauthenticated: false,
        ...options.gcpCloudRun,
      };
  }

  // Ensure default target if not set
  if (!target) {
    target = 'gcp-cloud-run';
  }

  return {
    version: '1.0',
    name,
    target,
    source,
    ...(options.context ? { context: options.context } : {}),
    ...(gcpCloudRun ? { gcpCloudRun } : {}),
    ...(gcpCompute ? { gcpCompute } : {}),
    env: options.env || { SWARM_ENV: 'production' },
    secrets: options.secrets || {},
    labels: options.labels || { managed_by: 'swarm_sdk' },
  };
}

/**
 * Loads, validates, and resolves a SwarmDeployManifest from a JSON string or raw object.
 * Automatically expands ${VAR_NAME} placeholders using environment variables or optional dictionary.
 */
export function loadDeployManifest(
  input: string | Record<string, unknown>,
  envLookup?: Record<string, string>
): SwarmDeployManifest {
  let raw: Record<string, unknown>;

  if (typeof input === 'string') {
    try {
      raw = JSON.parse(input) as Record<string, unknown>;
    } catch (err) {
      throw new SwarmValidationError(
        `Failed to parse Swarm deployment manifest JSON: ${(err as Error).message}`
      );
    }
  } else if (typeof input === 'object' && input !== null) {
    raw = { ...input };
  } else {
    throw new SwarmValidationError('Deployment manifest must be a JSON string or an object');
  }

  // Interpolate ${VAR} placeholders
  const resolved = interpolateEnvVars(raw, envLookup);

  // Validate core schema
  if (resolved.version !== '1.0') {
    throw new SwarmValidationError(
      `Invalid manifest version: expected '1.0', got '${String(resolved.version)}'`
    );
  }

  if (!resolved.name || typeof resolved.name !== 'string') {
    throw new SwarmValidationError("Deployment manifest requires a non-empty string 'name'");
  }

  if (!resolved.target || typeof resolved.target !== 'string') {
    throw new SwarmValidationError("Deployment manifest requires a non-empty string 'target'");
  }

  if (!resolved.source || typeof resolved.source !== 'object') {
    throw new SwarmValidationError("Deployment manifest requires a 'source' object");
  }

  const source = resolved.source as Record<string, unknown>;
  if (!source.type || !['github', 'local', 'binary'].includes(String(source.type))) {
    throw new SwarmValidationError(
      "Manifest source.type must be one of: 'github', 'local', 'binary'"
    );
  }

  return resolved as unknown as SwarmDeployManifest;
}

/**
 * Serializes a deployment manifest to formatted JSON.
 */
export function serializeDeployManifest(manifest: SwarmDeployManifest, indent = 2): string {
  return JSON.stringify(manifest, null, indent);
}

function interpolateEnvVars(
  val: unknown,
  envLookup?: Record<string, string>
): any {
  if (typeof val === 'string') {
    return val.replace(/\$\{([a-zA-Z0-9_]+)\}/g, (_, varName: string) => {
      if (envLookup && varName in envLookup) {
        return envLookup[varName];
      }
      if (typeof process !== 'undefined' && process?.env && varName in process.env) {
        return process.env[varName] || '';
      }
      // Retain placeholder if unresolved so command generators can use shell variables
      return `\${${varName}}`;
    });
  }

  if (Array.isArray(val)) {
    return val.map((item) => interpolateEnvVars(item, envLookup));
  }

  if (typeof val === 'object' && val !== null) {
    const res: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(val)) {
      res[k] = interpolateEnvVars(v, envLookup);
    }
    return res;
  }

  return val;
}
