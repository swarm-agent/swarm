/**
 * How a client with no explicit address finds this machine's Swarm.
 *
 * Order: an explicit `socketPath`/`baseUrl`, then `SWARM_SOCKET_PATH`, then
 * `SWARM_API_URL`/`SWARM_DESKTOP_URL`, then (Node only) the owner socket of a
 * system install when it exists and this process may connect to it, then the
 * daemon's default loopback API at http://127.0.0.1:7781.
 *
 * Browsers never touch the filesystem: the probe uses Node's synchronous
 * `process.getBuiltinModule`, which no bundler resolves and no browser has.
 */

declare const process: any;

/** Owner socket of a Linux system install (`/var/lib/swarmd` data root, main lane). */
export const SWARM_OWNER_SOCKET_PATH = '/var/lib/swarmd/local-transport/api.sock';

/** The daemon's default loopback API address. */
export const SWARM_DEFAULT_API_URL = 'http://127.0.0.1:7781';

export interface SwarmConnectionInput {
  baseUrl?: string;
  socketPath?: string;
}

export interface SwarmConnectionOptions {
  /** Environment to read; defaults to `process.env` in Node, `{}` elsewhere. */
  env?: Record<string, string | undefined>;
  /** Owner socket to probe; defaults to {@link SWARM_OWNER_SOCKET_PATH}. */
  ownerSocketPath?: string;
  /** Returns true when `path` is a socket this process may connect to. */
  isUsableSocket?: (path: string) => boolean;
}

export interface SwarmConnection {
  baseUrl: string;
  socketPath?: string;
}

function processEnv(): Record<string, string | undefined> {
  return typeof process !== 'undefined' && process?.env ? process.env : {};
}

/**
 * True when `path` exists, is a Unix socket and is readable and writable by
 * this process. Any error (missing, no permission on a parent directory, not
 * Node) means "not usable"; it never throws.
 */
export function isUsableSwarmSocket(path: string): boolean {
  if (typeof process === 'undefined' || typeof process?.getBuiltinModule !== 'function') return false;
  try {
    const fs = process.getBuiltinModule('node:fs');
    if (!fs?.statSync(path)?.isSocket()) return false;
    fs.accessSync(path, fs.constants.R_OK | fs.constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

/** Resolves where a client with the given explicit settings connects. */
export function resolveSwarmConnection(
  input: SwarmConnectionInput = {},
  options: SwarmConnectionOptions = {},
): SwarmConnection {
  const env = options.env ?? processEnv();
  const explicitUrl = input.baseUrl || env.SWARM_API_URL || env.SWARM_DESKTOP_URL;
  const baseUrl = (explicitUrl || SWARM_DEFAULT_API_URL).replace(/\/+$/, '');

  if (input.socketPath) return { baseUrl, socketPath: input.socketPath };
  if (input.baseUrl) return { baseUrl };
  if (env.SWARM_SOCKET_PATH) return { baseUrl, socketPath: env.SWARM_SOCKET_PATH };
  if (explicitUrl) return { baseUrl };

  const ownerSocket = options.ownerSocketPath ?? SWARM_OWNER_SOCKET_PATH;
  const usable = options.isUsableSocket ?? isUsableSwarmSocket;
  if (usable(ownerSocket)) return { baseUrl, socketPath: ownerSocket };
  return { baseUrl };
}
