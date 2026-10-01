/**
 * Types and interfaces for Swarm multi-cloud deployment automation.
 */

export type CloudTarget = 'gcp-cloud-run' | 'gcp-compute-engine' | 'docker' | string;

export type BuildStrategy = 'distroless' | 'scratch' | 'alpine' | 'prebuilt-binary';

export interface SourceConfig {
  /** Source location type: 'github' clone, 'local' directory, or 'binary' download */
  type: 'github' | 'local' | 'binary';
  /** Canonical Git repository URL (defaults to https://github.com/swarm-agent/swarm) */
  repository?: string;
  /** Git branch, tag, or ref to build (defaults to 'main') */
  branch?: string;
  /** Specific Git commit SHA to checkout */
  commit?: string;
  /** Direct URL to precompiled swarmd binary if type is 'binary' */
  binaryUrl?: string;
  /** Workspace-relative or absolute path if type is 'local' */
  localPath?: string;
  /** Container build strategy (defaults to 'distroless' for optimal cold start) */
  buildStrategy?: BuildStrategy;
}

export interface AgentContextConfig {
  /** System instructions or AGENTS.md content to guide the headless agent */
  instructions?: string;
  /** Initial task, mission, or prompt to execute on startup */
  prompt?: string;
  /** Target AI model identifier (e.g. 'gemini-3.8-flash') */
  model?: string;
  /** Thinking level ('low' | 'medium' | 'high') */
  thinking?: 'low' | 'medium' | 'high';
  /** Target workspace Git repository URL or path */
  workspaceUrl?: string;
  /** Workspace Git branch (defaults to 'main') */
  workspaceBranch?: string;
  /** Optional custom shell hook script to run during boot/context reload */
  bootHook?: string;
}

export interface GcpCloudRunConfig {
  /** Target GCP project ID (defaults to ${PROJECT_ID} placeholder or GCP_PROJECT env) */
  projectId?: string;
  /** GCP region (defaults to 'us-central1') */
  region?: string;
  /** Cloud Run service name (defaults to 'swarm-daemon') */
  serviceName?: string;
  /** Container CPU allocation (e.g. '1', '2') */
  cpu?: string;
  /** Container memory limit (e.g. '512Mi', '1Gi', '2Gi') - 512Mi is optimal for low cold start */
  memory?: string;
  /** Minimum instances (0 for pure scale-to-zero, $0 idle cost; 1 for zero cold-start warm pool) */
  minInstances?: number;
  /** Maximum instances (e.g. 10, 50, 100) */
  maxInstances?: number;
  /** Maximum concurrent requests per instance (default: 80) */
  concurrency?: number;
  /** Container listen port (default: 18080 or Cloud Run $PORT) */
  port?: number;
  /** Enable Startup CPU Boost for <1.5s cold-start latency (default: true) */
  cpuBoost?: boolean;
  /** Cloud Run execution environment: 'gen2' (recommended) or 'gen1' */
  executionEnvironment?: 'gen1' | 'gen2';
  /** Allow unauthenticated public invocations (default: false) */
  allowUnauthenticated?: boolean;
  /** Attached service account email */
  serviceAccount?: string;
  /** Network ingress traffic filter: 'all' | 'internal' | 'internal-and-cloud-load-balancing' */
  ingress?: 'all' | 'internal' | 'internal-and-cloud-load-balancing';
  /** Request timeout in seconds (default: 300) */
  timeoutSeconds?: number;
  /** Additional container image tag or Artifact Registry URL */
  imageUri?: string;
}

export interface GcpComputeEngineConfig {
  /** Target GCP project ID */
  projectId?: string;
  /** GCP compute zone (defaults to 'us-central1-a') */
  zone?: string;
  /** Compute instance name (defaults to 'swarm-vm') */
  instanceName?: string;
  /** GCE machine type (e.g. 'e2-micro' for free tier, 'e2-small' for 100 agents, 'e2-standard-4') */
  machineType?: string;
  /** Boot disk size in GB (default: 30 for GCP Free Tier) */
  diskSizeGb?: number;
  /** Boot disk type: 'pd-standard' | 'pd-ssd' | 'pd-balanced' */
  diskType?: 'pd-standard' | 'pd-ssd' | 'pd-balanced';
  /** Use preemptible/spot instance for lowest cost */
  preemptible?: boolean;
  /** Attached service account email */
  serviceAccount?: string;
  /** Network tags */
  tags?: string[];
  /** Listen port (default: 18080) */
  port?: number;
  /** Enable dynamic GCP metadata context syncing (/usr/local/bin/swarm-sync-context) */
  enableMetadataSync?: boolean;
}

export interface SwarmDeployManifest {
  /** Schema specification version (e.g. '1.0') */
  version: '1.0';
  /** Human-readable deployment name or service title */
  name: string;
  /** Target deployment platform */
  target: CloudTarget;
  /** Source code and binary build configuration */
  source: SourceConfig;
  /** Agent role, instructions, prompt, and workspace context */
  context?: AgentContextConfig;
  /** GCP Cloud Run specific configuration (when target is 'gcp-cloud-run') */
  gcpCloudRun?: GcpCloudRunConfig;
  /** GCP Compute Engine specific configuration (when target is 'gcp-compute-engine') */
  gcpCompute?: GcpComputeEngineConfig;
  /** Environment variables to inject into the deployment */
  env?: Record<string, string>;
  /** Secret Manager secret references (env var -> secret resource name) */
  secrets?: Record<string, string>;
  /** Resource metadata labels */
  labels?: Record<string, string>;
}

export type DeploymentPreset =
  | 'latency-optimized'
  | 'zero-idle-cost'
  | 'always-on'
  | 'free-tier-vm'
  | 'high-concurrency-vm';

export interface CreateManifestOptions {
  /** Deployment preset providing pre-tuned performance and cost defaults */
  preset?: DeploymentPreset;
  /** Deployment name */
  name?: string;
  /** Target platform override */
  target?: CloudTarget;
  /** Source configuration overrides */
  source?: Partial<SourceConfig>;
  /** Agent role, instructions, prompt, and workspace context */
  context?: Partial<AgentContextConfig>;
  /** GCP Cloud Run configuration overrides */
  gcpCloudRun?: Partial<GcpCloudRunConfig>;
  /** GCP Compute Engine configuration overrides */
  gcpCompute?: Partial<GcpComputeEngineConfig>;
  /** Environment variables */
  env?: Record<string, string>;
  /** Secrets */
  secrets?: Record<string, string>;
  /** Labels */
  labels?: Record<string, string>;
}

export interface DeployCommand {
  /** Cosmetic step title */
  title: string;
  /** Shell command to execute */
  command: string;
  /** Plain-English explanation of the command's effect */
  explanation: string;
  /** Pipeline phase */
  phase: 'build' | 'deploy' | 'verify' | 'cleanup';
}

export interface ValidationResult {
  valid: boolean;
  errors: string[];
  warnings: string[];
}

export interface DeployPlan {
  /** Canonical resolved deployment manifest */
  manifest: SwarmDeployManifest;
  /** Target platform */
  target: CloudTarget;
  /** High-level summary of the deployment architecture and cost profile */
  summary: string;
  /** Generated deployment artifacts (filename -> content) such as Dockerfile, scripts */
  artifacts: Record<string, string>;
  /** Sequential commands to build, deploy, and verify */
  commands: DeployCommand[];
  /** Recommended settings applied */
  recommendations: string[];
  /** Actionable warnings or prerequisites */
  warnings: string[];
}

export interface CloudDeployProvider {
  /** Supported cloud target identifier */
  readonly target: CloudTarget;
  /** Validates manifest configuration against provider requirements */
  validate(manifest: SwarmDeployManifest): ValidationResult;
  /** Generates a complete structured deployment plan */
  plan(manifest: SwarmDeployManifest): DeployPlan;
  /** Generates standalone file artifacts (Dockerfile, scripts, config) */
  generateArtifacts(manifest: SwarmDeployManifest): Record<string, string>;
  /** Generates shell commands required to execute the deployment */
  generateCommands(manifest: SwarmDeployManifest): DeployCommand[];
}

export interface ExecuteDeployOptions {
  /** Target directory to write artifacts before execution (defaults to temporary directory) */
  outputDir?: string;
  /** Dry-run mode: generates artifacts and validates commands without executing shell operations */
  dryRun?: boolean;
  /** Custom logger function for streaming command execution stdout/stderr */
  onLog?: (line: string, phase: string) => void;
  /** Additional environment variable overrides for execution processes */
  env?: Record<string, string>;
}

export interface DeployExecutionResult {
  /** Whether the deployment completed successfully */
  success: boolean;
  /** Target deployment platform */
  target: CloudTarget;
  /** Total elapsed time in milliseconds */
  durationMs: number;
  /** Captured outputs by step title */
  stepOutputs: Record<string, string>;
  /** Deployed service URL (if Cloud Run) */
  serviceUrl?: string;
  /** External/NAT IP address (if Compute Engine) */
  externalIp?: string;
  /** Path to output directory containing written artifacts */
  outputDir: string;
  /** Error message if deployment failed */
  error?: string;
}
