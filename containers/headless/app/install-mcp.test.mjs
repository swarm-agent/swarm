// Purpose: install_mcp_launcher and package-mcp.sh must deliver the shared bridge,
// reading only the calling user's private key across fresh processes, including
// the loopback SSH-tunnel route. Real stdio/HTTP is the narrowest integration
// layer; the HTTP fixture is not a daemon/provider/agent qualification.
import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, mkdir, writeFile, chmod, symlink, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn, spawnSync} from 'node:child_process';
import {createServer} from 'node:http';

const here = dirname(fileURLToPath(import.meta.url));
test('packaged stdio launcher reads a private caller key in fresh sessions', {timeout: 20000}, async t => {
  const work = await mkdtemp(join(tmpdir(), 'swarm-installed-mcp-'));
  t.after(() => rm(work, {recursive: true, force: true}));
  const release = join(work, 'release');
  const home = join(work, 'home');
  await mkdir(join(release, 'bin'), {recursive: true});
  await mkdir(join(home, '.config', 'swarm'), {recursive: true});
  await symlink(process.execPath, join(release, 'bin', 'node'));
  await symlink(release, join(work, 'current'));
  const key = join(home, '.config', 'swarm', 'ai-key');
  await writeFile(key, 'test-only-not-a-credential\n', {mode: 0o600});
  const env = {PATH: process.env.PATH, TMPDIR: process.env.TMPDIR, HOME: home, XDG_CONFIG_HOME: join(home, '.config')};
  const prepared = spawnSync('bash', ['-c', `set -e; source <(sed '$d' "$1/install.sh"); OPT=$2; SWARM_MCP=$2/swarm-mcp; bash "$1/package-mcp.sh" "$2/release"; install_mcp_launcher "$2/release"`, 'sh', here, work], {env, encoding: 'utf8'});
  assert.equal(prepared.status, 0, prepared.stderr);
  let requests = 0;
  const server = createServer((req, res) => {
    requests++;
    assert.equal(req.url, '/mcp');
    assert.equal(req.headers.authorization, 'Bearer test-only-not-a-credential');
    let body = '';
    req.on('data', c => {body += c;});
    req.on('end', () => {
      const message = JSON.parse(body);
      assert.equal(message.method, 'tools/list');
      res.writeHead(200, {'Content-Type': 'application/json'});
      res.end(JSON.stringify({jsonrpc: '2.0', id: message.id, result: {tools: [{name: 'fixture_tool', inputSchema: {type: 'object'}}]}}));
    });
  });
  // Use an isolated loopback endpoint, never the operator's daemon port.
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(0, '127.0.0.1', resolve);});
  t.after(() => server.close());
  env.SWARM_MCP_CONFIG = join(work, 'connection.json');
  await writeFile(env.SWARM_MCP_CONFIG, JSON.stringify({
    mode: 'http', url: `http://127.0.0.1:${server.address().port}/mcp`, tokenFile: key,
  }), {mode: 0o600});
  async function invoke() {
    const child = spawn(join(work, 'swarm-mcp'), [], {env, cwd: '/', timeout: 5000});
    let stdout = '', stderr = '';
    child.stdout.on('data', c => {stdout += c;});
    child.stderr.on('data', c => {stderr += c;});
    child.stdin.on('error', () => {});
    child.stdin.end('{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n');
    const code = await new Promise((resolve, reject) => {child.once('error', reject); child.once('close', resolve);});
    return {code, stdout, stderr};
  }
  for (let session = 0; session < 2; session++) {
    const r = await invoke();
    assert.equal(r.code, 0, r.stderr);
    assert.equal(JSON.parse(r.stdout).result.tools[0].name, 'fixture_tool');
    assert.equal(r.stderr, '');
  }
  assert.equal(requests, 2);
  await chmod(key, 0o644);
  const rejected = await invoke();
  assert.notEqual(rejected.code, 0);
  assert.equal(rejected.stdout, '');
  assert.equal(requests, 2, 'unsafe key must not reach HTTP');
  assert.ok(!rejected.stderr.includes('test-only-not-a-credential'));
});
