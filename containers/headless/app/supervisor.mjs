import { spawn } from 'node:child_process';
import { get } from 'node:http';
import { fileURLToPath } from 'node:url';

export function ready() {
  return new Promise(resolve => {
    const req = get({ socketPath: '/var/lib/swarmd/local-transport/api.sock', path: '/readyz', timeout: 1000 }, res => {
      res.resume(); resolve(res.statusCode === 200);
    });
    req.on('timeout', () => req.destroy());
    req.on('error', () => resolve(false));
  });
}

// Process groups include descendants; tini reaps any orphaned grandchildren.
export async function supervise({ daemon, app, probe = ready, startupMs = 30000, graceMs = 10000, signals = process }) {
  const children = [];
  let stopping = false, timer, poll;
  let finish;
  const result = new Promise(resolve => { finish = resolve; });
  const kill = signal => { for (const child of children) if (child.pid) { try { process.kill(-child.pid, signal); } catch {} } };
  const stop = async code => {
    if (stopping) return;
    stopping = true; clearTimeout(timer); clearTimeout(poll);
    kill('SIGTERM');
    const guard = setTimeout(() => kill('SIGKILL'), graceMs);
    await Promise.all(children.map(child => child.closed));
    clearTimeout(guard); kill('SIGKILL');
    signals.off('SIGTERM', term); signals.off('SIGINT', interrupt);
    finish(code);
  };
  const launch = argv => {
    const child = spawn(argv[0], argv.slice(1), { detached: true, stdio: 'inherit' });
    child.closed = new Promise(resolve => child.once('close', resolve));
    children.push(child);
    child.once('error', () => { void stop(1); });
    child.once('exit', code => { void stop(code || 1); });
    return child;
  };
  const term = () => { void stop(143); }, interrupt = () => { void stop(130); };
  signals.on('SIGTERM', term); signals.on('SIGINT', interrupt);
  launch(daemon);
  timer = setTimeout(() => { void stop(1); }, startupMs);
  const check = async () => {
    let healthy = false;
    try { healthy = await probe(); } catch { /* Bounded retry until startup deadline. */ }
    if (stopping) return;
    if (healthy) { clearTimeout(timer); launch(app); }
    else poll = setTimeout(check, 100);
  };
  void check();
  return result;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const app = process.argv.slice(2);
  if (!app.length) { console.error('Application command required.'); process.exitCode = 1; }
  else process.exitCode = await supervise({ daemon: ['/usr/local/bin/swarmd', '--desktop-port=0', '--cwd=/project', '--container-sdk-port=7783'], app });
}
