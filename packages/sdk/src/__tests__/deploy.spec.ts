import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createSwarmClient } from '../client.js';
import {
  createDeployManifest,
  generateDockerIgnore,
  generateOptimizedDockerfile,
  loadDeployManifest,
  serializeDeployManifest,
} from '../deploy/index.js';
import { GcpCloudRunProvider } from '../deploy/providers/gcp-cloud-run.js';
import { GcpComputeEngineProvider } from '../deploy/providers/gcp-compute.js';
import type { CloudDeployProvider, DeployPlan, SwarmDeployManifest } from '../deploy/types.js';
import { SwarmNotFoundError, SwarmValidationError } from '../errors.js';

test('createDeployManifest: defaults to latency-optimized Cloud Run with Startup CPU Boost and scale-to-zero', () => {
  const manifest = createDeployManifest({ name: 'test-service' });

  assert.equal(manifest.version, '1.0');
  assert.equal(manifest.name, 'test-service');
  assert.equal(manifest.target, 'gcp-cloud-run');
  assert.equal(manifest.source.type, 'github');
  assert.equal(manifest.source.repository, 'https://github.com/swarm-agent/swarm');
  assert.equal(manifest.source.buildStrategy, 'distroless');

  assert.ok(manifest.gcpCloudRun);
  assert.equal(manifest.gcpCloudRun.cpuBoost, true);
  assert.equal(manifest.gcpCloudRun.executionEnvironment, 'gen2');
  assert.equal(manifest.gcpCloudRun.minInstances, 0);
  assert.equal(manifest.gcpCloudRun.memory, '512Mi');
  assert.equal(manifest.gcpCloudRun.cpu, '1');
  assert.equal(manifest.gcpCloudRun.concurrency, 80);
});

test('createDeployManifest: supports free-tier-vm preset for GCP Compute Engine', () => {
  const manifest = createDeployManifest({
    preset: 'free-tier-vm',
    name: 'free-swarm-node',
  });

  assert.equal(manifest.target, 'gcp-compute-engine');
  assert.ok(manifest.gcpCompute);
  assert.equal(manifest.gcpCompute.machineType, 'e2-micro');
  assert.equal(manifest.gcpCompute.diskSizeGb, 30);
  assert.equal(manifest.gcpCompute.diskType, 'pd-standard');
});

test('loadDeployManifest: parses valid JSON and interpolates environment variables', () => {
  const jsonStr = JSON.stringify({
    version: '1.0',
    name: 'custom-swarm',
    target: 'gcp-cloud-run',
    source: {
      type: 'github',
      repository: 'https://github.com/swarm-agent/swarm',
    },
    gcpCloudRun: {
      projectId: '${DEPLOY_PROJECT}',
      region: 'europe-west1',
    },
  });

  const parsed = loadDeployManifest(jsonStr, { DEPLOY_PROJECT: 'my-gcp-prod-456' });
  assert.equal(parsed.name, 'custom-swarm');
  assert.equal(parsed.gcpCloudRun?.projectId, 'my-gcp-prod-456');
  assert.equal(parsed.gcpCloudRun?.region, 'europe-west1');
});

test('loadDeployManifest: throws SwarmValidationError on invalid payloads', () => {
  // Invalid JSON
  assert.throws(() => loadDeployManifest('{ broken json'), SwarmValidationError);

  // Missing version
  assert.throws(
    () => loadDeployManifest({ name: 'foo', target: 'gcp-cloud-run', source: { type: 'github' } }),
    SwarmValidationError
  );

  // Missing source
  assert.throws(
    () => loadDeployManifest({ version: '1.0', name: 'foo', target: 'gcp-cloud-run' }),
    SwarmValidationError
  );

  // Invalid source type
  assert.throws(
    () =>
      loadDeployManifest({
        version: '1.0',
        name: 'foo',
        target: 'gcp-cloud-run',
        source: { type: 'unsupported' },
      }),
    SwarmValidationError
  );
});

test('serializeDeployManifest: produces formatted JSON', () => {
  const manifest = createDeployManifest({ name: 'sample' });
  const serialized = serializeDeployManifest(manifest);
  assert.ok(serialized.includes('"version": "1.0"'));
  assert.ok(serialized.includes('"name": "sample"'));
});

test('generateOptimizedDockerfile: distroless multi-stage build without UPX', () => {
  const dockerfile = generateOptimizedDockerfile({
    source: {
      type: 'github',
      repository: 'https://github.com/swarm-agent/swarm',
      branch: 'main',
      buildStrategy: 'distroless',
    },
    port: 18080,
  });

  // Verify multi-stage build
  assert.ok(dockerfile.includes('FROM golang:1.24-bookworm AS builder'));
  assert.ok(dockerfile.includes('FROM gcr.io/distroless/static-debian12:nonroot'));

  // Verify stripped symbols (-s -w) and CGO_ENABLED=0
  assert.ok(dockerfile.includes('CGO_ENABLED=0'));
  assert.ok(dockerfile.includes('-ldflags="-s -w"'));

  // Invariant: UPX MUST NOT be used (prevents CPU startup decompression lag)
  assert.ok(!dockerfile.includes('upx'));

  // Verify non-root user
  assert.ok(dockerfile.includes('USER 65532:65532'));

  // Verify port
  assert.ok(dockerfile.includes('EXPOSE 18080'));
});

test('generateDockerIgnore: excludes node_modules, git, and local artifacts', () => {
  const ignore = generateDockerIgnore();
  assert.ok(ignore.includes('.git'));
  assert.ok(ignore.includes('node_modules'));
  assert.ok(ignore.includes('.env'));
});

test('GcpCloudRunProvider: validation, artifacts, commands, and plan', () => {
  const provider = new GcpCloudRunProvider();
  const manifest = createDeployManifest({
    name: 'agent-swarm-prod',
    gcpCloudRun: {
      serviceName: 'swarmd-agent',
      region: 'us-central1',
      cpuBoost: true,
      minInstances: 0,
      memory: '512Mi',
    },
    env: {
      LOG_LEVEL: 'info',
    },
  });

  // Validation
  const val = provider.validate(manifest);
  assert.equal(val.valid, true);
  assert.equal(val.errors.length, 0);

  // Artifacts
  const artifacts = provider.generateArtifacts(manifest);
  assert.ok(artifacts['Dockerfile']);
  assert.ok(artifacts['.dockerignore']);
  assert.ok(artifacts['cloudbuild.yaml']);
  assert.ok(artifacts['deploy.sh']);
  assert.ok(artifacts['deploy.sh'].includes('SERVICE_NAME="swarmd-agent"'));
  assert.ok(artifacts['deploy.sh'].includes('--cpu-boost'));

  // Commands
  const commands = provider.generateCommands(manifest);
  assert.equal(commands.length, 3);
  assert.equal(commands[0].phase, 'build');
  assert.equal(commands[1].phase, 'deploy');
  assert.equal(commands[2].phase, 'verify');
  assert.ok(commands[1].command.includes('--cpu-boost'));
  assert.ok(commands[1].command.includes('--execution-environment gen2'));
  assert.ok(commands[1].command.includes('--min-instances 0'));

  // Plan
  const plan = provider.plan(manifest);
  assert.equal(plan.target, 'gcp-cloud-run');
  assert.ok(plan.summary.includes('$0.00/month idle cost'));
  assert.ok(plan.recommendations.length > 0);
});

test('GcpCloudRunProvider: detects invalid naming conventions and emits warnings', () => {
  const provider = new GcpCloudRunProvider();

  // Invalid service name (uppercase letters, underscores)
  const badManifest = createDeployManifest({
    gcpCloudRun: {
      serviceName: 'Invalid_Name_123',
    },
  });
  const valBad = provider.validate(badManifest);
  assert.equal(valBad.valid, false);
  assert.ok(valBad.errors.some((e) => e.includes('Invalid Cloud Run serviceName')));

  // Warning when cpuBoost is false
  const warnManifest = createDeployManifest({
    gcpCloudRun: {
      serviceName: 'valid-name',
      cpuBoost: false,
      minInstances: 2,
    },
  });
  const valWarn = provider.validate(warnManifest);
  assert.equal(valWarn.valid, true);
  assert.ok(valWarn.warnings.some((w) => w.includes('cpuBoost is set to false')));
  assert.ok(valWarn.warnings.some((w) => w.includes('minInstances is set to 2')));
});

test('GcpComputeEngineProvider: validation, artifacts, and commands', () => {
  const provider = new GcpComputeEngineProvider();
  const manifest = createDeployManifest({
    preset: 'free-tier-vm',
    name: 'vm-swarm',
    gcpCompute: {
      instanceName: 'my-swarm-node',
      zone: 'us-central1-b',
    },
  });

  const val = provider.validate(manifest);
  assert.equal(val.valid, true);

  const artifacts = provider.generateArtifacts(manifest);
  assert.ok(artifacts['startup-script.sh']);
  assert.ok(artifacts['deploy-vm.sh']);
  assert.ok(artifacts['startup-script.sh'].includes('useradd -r -m -s /bin/bash'));
  assert.ok(artifacts['startup-script.sh'].includes('swarmd.service'));

  const commands = provider.generateCommands(manifest);
  assert.equal(commands.length, 3);
  assert.ok(commands[0].command.includes('gcloud compute instances create my-swarm-node'));
  assert.ok(commands[2].command.includes('gcloud compute instances delete my-swarm-node'));

  const plan = provider.plan(manifest);
  assert.ok(plan.summary.includes('GCP Free Tier'));
});

test('SwarmClient.deploy: full lifecycle integration on SwarmClient', () => {
  const client = createSwarmClient();

  assert.ok(client.deploy);
  const providers = client.deploy.listProviders();
  assert.ok(providers.length >= 2);
  assert.ok(providers.some((p) => p.target === 'gcp-cloud-run'));
  assert.ok(providers.some((p) => p.target === 'gcp-compute-engine'));

  // Test plan generation via client
  const plan = client.deploy.plan({
    preset: 'latency-optimized',
    name: 'client-deploy-test',
  });
  assert.equal(plan.target, 'gcp-cloud-run');
  assert.ok(plan.artifacts['Dockerfile']);
  assert.ok(plan.commands.length >= 3);

  // Test generateArtifacts and generateCommands
  const artifacts = client.deploy.generateArtifacts({
    preset: 'free-tier-vm',
    name: 'vm-deploy-test',
  });
  assert.ok(artifacts['startup-script.sh']);

  const commands = client.deploy.generateCommands({
    preset: 'latency-optimized',
    name: 'cmd-test',
  });
  assert.ok(commands[1].command.includes('gcloud run deploy'));

  // Test unregistered provider error
  assert.throws(
    () => client.deploy.getProvider('azure-container-apps'),
    (err: any) => err instanceof SwarmNotFoundError && err.code === 'PROVIDER_NOT_FOUND'
  );

  // Test custom provider registration
  const customProvider: CloudDeployProvider = {
    target: 'fly-io',
    validate: () => ({ valid: true, errors: [], warnings: [] }),
    plan: (m) => ({
      manifest: m,
      target: 'fly-io',
      summary: 'Fly.io app',
      artifacts: { 'fly.toml': 'app = "swarm"' },
      commands: [{ title: 'Deploy', command: 'fly deploy', explanation: 'Deploy', phase: 'deploy' }],
      recommendations: [],
      warnings: [],
    }),
    generateArtifacts: () => ({ 'fly.toml': 'app = "swarm"' }),
    generateCommands: () => [{ title: 'Deploy', command: 'fly deploy', explanation: 'Deploy', phase: 'deploy' }],
  };

  client.deploy.registerProvider(customProvider);
  assert.equal(client.deploy.getProvider('fly-io').target, 'fly-io');
});

test('GcpComputeEngineProvider: context management, metadata sync, prebuilt binary, and env vars', () => {
  const provider = new GcpComputeEngineProvider();
  const manifest = createDeployManifest({
    preset: 'free-tier-vm',
    name: 'context-agent-vm',
    source: {
      type: 'binary',
      binaryUrl: 'https://example.com/downloads/swarmd-v1.0.0-linux-amd64',
    },
    context: {
      instructions: 'You are an autonomous customer support agent.',
      prompt: 'Monitor incoming tickets and report resolutions.',
      model: 'gemini-3.8-flash',
      thinking: 'low',
      workspaceUrl: 'https://github.com/example/support-repo.git',
      bootHook: 'echo "Custom boot hook complete" >> /var/log/boot.log',
    },
    env: {
      SUPPORT_QUEUE: 'tier-1',
      MAX_PARALLEL_JOBS: '10',
    },
    gcpCompute: {
      instanceName: 'support-vm',
      serviceAccount: 'custom-sa@my-proj.iam.gserviceaccount.com',
      projectId: 'my-proj-123',
    },
  });

  const artifacts = provider.generateArtifacts(manifest);
  assert.ok(artifacts['startup-script.sh']);
  assert.ok(artifacts['AGENTS.md']);
  assert.ok(artifacts['PROMPT.txt']);
  assert.equal(artifacts['AGENTS.md'], 'You are an autonomous customer support agent.');
  assert.equal(artifacts['PROMPT.txt'], 'Monitor incoming tickets and report resolutions.');

  // Prebuilt binary download
  assert.ok(artifacts['startup-script.sh'].includes('https://example.com/downloads/swarmd-v1.0.0-linux-amd64'));
  // Environment variables
  assert.ok(artifacts['startup-script.sh'].includes('SUPPORT_QUEUE=tier-1'));
  assert.ok(artifacts['startup-script.sh'].includes('MAX_PARALLEL_JOBS=10'));
  assert.ok(artifacts['startup-script.sh'].includes('SWARM_MODEL=gemini-3.8-flash'));
  assert.ok(artifacts['startup-script.sh'].includes('SWARM_THINKING=low'));
  // Dynamic metadata context sync script
  assert.ok(artifacts['startup-script.sh'].includes('swarm-sync-context'));
  assert.ok(artifacts['startup-script.sh'].includes('computeMetadata/v1/instance/attributes'));
  // Custom user boot hook
  assert.ok(artifacts['startup-script.sh'].includes('Custom boot hook complete'));

  const commands = provider.generateCommands(manifest);
  // Verify project, service account, and metadata flags in gcloud command
  assert.ok(commands[0].command.includes('--project my-proj-123'));
  assert.ok(commands[0].command.includes('--service-account custom-sa@my-proj.iam.gserviceaccount.com'));
  assert.ok(commands[0].command.includes('swarm-instructions=AGENTS.md'));
  assert.ok(commands[0].command.includes('swarm-prompt=PROMPT.txt'));
});

test('SwarmClient.deploy: writeArtifacts and execute dry-run', async () => {
  const client = createSwarmClient();
  const manifest = client.deploy.createManifest({
    preset: 'free-tier-vm',
    name: 'headless-test-agent',
    context: {
      prompt: 'Execute autonomous pipeline benchmark',
      instructions: 'Strictly follow AGENTS.md',
    },
  });

  // Test execute dry-run
  const result = await client.deploy.execute(manifest, { dryRun: true });
  assert.equal(result.success, true);
  assert.equal(result.target, 'gcp-compute-engine');
  assert.ok(result.outputDir);
  assert.ok(result.stepOutputs['Provision GCE Virtual Machine']);
  assert.ok(result.stepOutputs['Provision GCE Virtual Machine'].includes('(simulated:'));
});
