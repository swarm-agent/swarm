import type { BuildStrategy, SourceConfig } from './types.js';
import { SwarmValidationError } from '../errors.js';

export interface DockerfileOptions {
  /** Required qualified release manifest image, never a mutable tag. */
  runtimeImage?: string;
  /** Legacy source-build options are rejected, not silently ignored. */
  source?: SourceConfig;
  port?: number;
  strategy?: BuildStrategy;
  goVersion?: string;
  packagePath?: string;
}

/** Consume the canonical CGO/glibc/FFF runtime; never rebuild the daemon here. */
export function generateOptimizedDockerfile(options: DockerfileOptions = {}): string {
  if (options.source || options.strategy || options.goVersion || options.packagePath || options.port !== undefined) {
    throw new SwarmValidationError('Source builds and listener overrides are unsupported. Use the qualified runtimeImage and the private application recipe.');
  }
  if (!/^ghcr\.io\/swarm-agent\/swarm-headless@sha256:[a-f0-9]{64}$/.test(options.runtimeImage ?? '')) {
    throw new SwarmValidationError('runtimeImage must be ghcr.io/swarm-agent/swarm-headless@sha256:<64 lowercase hex characters> from the qualified release manifest.');
  }
  return `# Canonical runtime; inherits non-root identity, private listeners and durable volumes.\nFROM ${options.runtimeImage}\n`;
}

export function generateDockerIgnore(): string {
  return `.git\n.github\nnode_modules\n**/node_modules\n*.log\n*.tmp\n.env\n.env.*\n*.sock\ntmp\n.vscode\n.idea\n`;
}
