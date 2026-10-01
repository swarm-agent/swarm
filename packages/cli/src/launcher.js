import { spawnSync } from 'node:child_process';
import { lstatSync, realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, parse, relative } from 'node:path';
import { createHash } from 'node:crypto';

const LABEL = 'dev.swarm.headless';
const VOLUMES = { config: '/etc/swarmd', data: '/var/lib/swarmd', cache: '/var/cache/swarmd', logs: '/var/log/swarmd' };
export const HELP = `swarm-headless <start|stop|status|setup> [options]
  --engine docker|podman   Local engine (default docker)
  --name NAME              Instance name (default swarm-headless)
  start --project PATH [--port 7783] [--recreate]
  setup [options] -- <status|identity|credential|model|workspace|complete|sdk-token|revoke-sdk-token> [flags]
  --version | --help       No engine access
No install-time actions, automatic pulls, model defaults or volume deletion.
Stop preserves the container and all named state volumes. Recreate requires a
stopped, launcher-owned container and the same project. See README for setup.`;

export function parseArgs(argv) {
  const [command, ...rest] = argv;
  if (!command || ['--help', 'help'].includes(command)) return { command: 'help' };
  if (command === '--version') return { command: 'version' };
  if (!['start', 'stop', 'status', 'setup'].includes(command)) throw Error('Unknown command; use --help');
  const opts = { command, engine: 'docker', name: 'swarm-headless', port: 7783, recreate: false, setup: [] };
  const seen = new Set();
  for (let i = 0; i < rest.length; i++) {
    const key = rest[i];
    if (key === '--' && command === 'setup') { opts.setup = rest.slice(i + 1); break; }
    if (seen.has(key)) throw Error('Duplicate option');
    seen.add(key);
    if (key === '--recreate' && command === 'start') { opts.recreate = true; continue; }
    if (!['--engine', '--name', ...(command === 'start' ? ['--project', '--port'] : [])].includes(key)) throw Error('Invalid option; secrets must use stdin, never flags');
    const value = rest[++i];
    if (!value || value.startsWith('--')) throw Error('Option needs a value');
    opts[key.slice(2)] = value;
  }
  if (!['docker', 'podman'].includes(opts.engine)) throw Error('Engine must be docker or podman');
  if (!/^[a-z][a-z0-9-]{0,47}$/.test(opts.name)) throw Error('Invalid instance name');
  if (!/^\d+$/.test(String(opts.port)) || Number(opts.port) < 1024 || Number(opts.port) > 65535) throw Error('Port must be 1024–65535');
  opts.port = Number(opts.port);
  if (command === 'setup') validateSetup(opts.setup);
  return opts;
}

function validateSetup(args) {
  const flags = {
    status: [], complete: [], identity: ['username', 'name'],
    credential: ['provider', 'api-key-stdin'],
    model: ['role', 'provider', 'model', 'thinking', 'service-tier', 'context-mode'],
    workspace: ['path', 'name'], 'sdk-token': ['expires-in-seconds'], 'revoke-sdk-token': ['id'],
  };
  if (!Object.hasOwn(flags, args[0])) throw Error('Select a supported setup operation after --');
  const seen = new Set();
  for (let i = 1; i < args.length; i++) {
    const flag = args[i];
    if (!flag.startsWith('--') || !flags[args[0]].includes(flag.slice(2)) || seen.has(flag)) throw Error('Invalid setup flags; secrets must use stdin');
    seen.add(flag);
    if (flag === '--api-key-stdin') continue;
    const value = args[++i];
    if (!value || value.startsWith('--')) throw Error('Setup flag needs a value');
    if (flag === '--path' && value !== '/project') throw Error('Workspace must be /project');
  }
  if (args[0] === 'credential' && !seen.has('--api-key-stdin')) throw Error('Credentials require --api-key-stdin');
}

export function selectedProject(path) {
  if (!path || !isAbsolute(path)) throw Error('Select an absolute project path');
  const result = realpathSync(path);
  const home = realpathSync(homedir());
  const toHome = relative(result, home);
  if (result === parse(result).root || !toHome || (!toHome.startsWith('..') && !isAbsolute(toHome))) throw Error('Cannot mount the home directory or its ancestors');
  if (/[\r\n,]/.test(result) || !lstatSync(result).isDirectory()) throw Error('Invalid project mount path');
  // Linked worktrees can refer to metadata outside the only authorized mount.
  if (!lstatSync(join(result, '.git')).isDirectory() || lstatSync(join(result, '.git')).isSymbolicLink()) throw Error('Select a normal Git checkout, not a linked worktree');
  return result;
}

export function engineRunner(engine, env = process.env) {
  if (env.DOCKER_CONTEXT || env.CONTAINER_CONNECTION || env.CONTAINER_HOST ||
      (env.DOCKER_HOST && !env.DOCKER_HOST.startsWith('unix://'))) throw Error('Only a local Unix-socket engine is supported; unset remote context variables');
  const prefix = engine === 'podman' ? ['--remote=false'] : ['--host', env.DOCKER_HOST || 'unix:///var/run/docker.sock'];
  return (args, options = {}) => {
    const result = spawnSync(engine, [...prefix, ...args], {
      shell: false, encoding: 'utf8', maxBuffer: 1024 * 1024, timeout: 150000,
      stdio: options.stream ? ['inherit', 'inherit', 'pipe'] : ['ignore', 'pipe', 'pipe'],
    });
    // Do not print engine stderr/argv: exec failures can contain private values.
    if (result.error || result.status !== 0) throw Error('Container engine operation failed; inspect the engine and instance before retrying (details withheld)');
    return result.stdout || '';
  };
}

function decode(text) {
  const value = JSON.parse(text);
  if (!Array.isArray(value) || value.length !== 1) throw Error('Unexpected engine inspection response');
  return value[0];
}
const imageID = value => `sha256:${String(value).replace(/^sha256:/, '')}`;
const lines = text => text.trim().split('\n').filter(Boolean);

export function runLauncher(opts, pin, run, io = {}) {
  const show = io.show || console.log;
  if (opts.command === 'help') { show(HELP); return; }
  if (opts.command === 'version') { show(pin.version); return; }
  if (!/^sha256:[a-f0-9]{64}$/.test(pin.imageId) || pin.platform !== 'linux/amd64') throw Error('Invalid packaged image pin');
  const names = lines(run(['container', 'ls', '--all', '--format', '{{.Names}}']));
  const current = names.includes(opts.name) ? decode(run(['container', 'inspect', opts.name])) : null;
  if (current && current.Config?.Labels?.[LABEL] !== opts.name) throw Error('Instance name belongs to an unmanaged container; refusing access');
  if (opts.command === 'status') {
    show(current ? `state=${current.State.Status} image=${imageID(current.Image)} endpoint=http://127.0.0.1:${current.Config.Labels[`${LABEL}.port`]}` : 'state=absent');
    return;
  }
  if (opts.command === 'stop') {
    if (!current) throw Error('Instance does not exist');
    if (current.State.Running) run(['container', 'stop', '--time', '30', current.Id]);
    show('Stopped; named state volumes preserved.'); return;
  }
  if (opts.command === 'setup') {
    if (!current?.State.Running) throw Error('Start the instance before setup');
    if (imageID(current.Image) !== pin.imageId) throw Error('Instance does not match the packaged image pin');
    if (opts.setup[0] === 'credential' && io.stdinTTY) throw Error('Redirect a private key file or secret-manager pipe to stdin');
    if (opts.setup[0] === 'sdk-token' && io.stdoutTTY) throw Error('Redirect token output to a new private file or pipe');
    run(['exec', ...(opts.setup[0] === 'credential' ? ['-i'] : []), current.Id, 'swarmctl', 'setup', ...opts.setup], { stream: true });
    return;
  }
  const project = (io.project || selectedProject)(opts.project);
  const spec = createHash('sha256').update(JSON.stringify([pin.imageId, project, opts.port])).digest('hex');
  if (current) {
    if (current.Config.Labels[`${LABEL}.project`] !== project) throw Error('Recreation must keep the same project');
    if (current.State.Running) {
      if (opts.recreate || current.Config.Labels[`${LABEL}.spec`] !== spec || imageID(current.Image) !== pin.imageId) throw Error('Stop the existing instance before changing or recreating it');
      show('Already running.'); return;
    }
    if (!opts.recreate) throw Error('Instance is stopped; use start --recreate with the same project to preserve its volumes');
  }
  // Inspect by immutable local image ID. No pull, mutable tag resolution or fallback.
  const image = decode(run(['image', 'inspect', pin.imageId]));
  if (imageID(image.Id) !== pin.imageId || image.Os !== 'linux' || image.Architecture !== 'amd64') throw Error('Load the exact matching Linux amd64 candidate image first');
  const network = `${opts.name}-network`;
  const networks = lines(run(['network', 'ls', '--format', '{{.Name}}']));
  if (networks.includes(network)) {
    const n = decode(run(['network', 'inspect', network]));
    if ((n.Labels || n.labels)?.[LABEL] !== opts.name) throw Error('Network name collision; refusing reuse');
  }
  const volumes = lines(run(['volume', 'ls', '--format', '{{.Name}}']));
  // A partial existing set is never a new instance: do not silently reset identity.
  const present = Object.keys(VOLUMES).filter(key => volumes.includes(`${opts.name}-${key}`)).length;
  if (present !== 0 && present !== Object.keys(VOLUMES).length) throw Error('Incomplete state volumes; inspect and recover the existing instance before retrying');
  // Check every collision before creating a volume or removing a stopped container.
  for (const suffix of Object.keys(VOLUMES)) {
    const name = `${opts.name}-${suffix}`;
    if (volumes.includes(name)) {
      const v = decode(run(['volume', 'inspect', name]));
      if (v.Labels?.[LABEL] !== opts.name || v.Labels?.[`${LABEL}.project`] !== project) throw Error('Volume ownership/project collision; refusing reuse');
    } else if (current) throw Error('Existing instance has a missing state volume; refusing silent reset');
  }
  if (!networks.includes(network)) run(['network', 'create', '--label', `${LABEL}=${opts.name}`, network]);
  for (const suffix of Object.keys(VOLUMES)) {
    const name = `${opts.name}-${suffix}`;
    if (!volumes.includes(name)) run(['volume', 'create', '--label', `${LABEL}=${opts.name}`, '--label', `${LABEL}.project=${project}`, name]);
  }
  if (current) run(['container', 'rm', current.Id]); // Never --force or --volumes.
  const mounts = Object.entries(VOLUMES).flatMap(([key, dst]) => ['--mount', `type=volume,src=${opts.name}-${key},dst=${dst}`]);
  run(['run', '--detach', '--pull=never', '--platform', 'linux/amd64', '--name', opts.name,
    '--label', `${LABEL}=${opts.name}`, '--label', `${LABEL}.project=${project}`,
    '--label', `${LABEL}.port=${opts.port}`, '--label', `${LABEL}.spec=${spec}`,
    '--network', network, '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
    '--publish', `127.0.0.1:${opts.port}:7783`, ...mounts,
    '--mount', `type=bind,src=${project},dst=/project`, pin.imageId, '--container-sdk-port=7783']);
  show(`Started; endpoint=http://127.0.0.1:${opts.port}. Run setup status when the private socket is ready.`);
}
