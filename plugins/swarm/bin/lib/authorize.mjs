import { execFile } from 'node:child_process';
import { constants } from 'node:fs';
import { lstat, open, mkdtemp, writeFile, rename, unlink } from 'node:fs/promises';
import { dirname, join, isAbsolute } from 'node:path';
import { validateConnection } from './connection.mjs';

// Never return command stderr/stdout through an exception: issuance output is a secret.
export function ownerCommand(binary, sudo, args) {
  return new Promise((resolve, reject) => {
    execFile(sudo ? 'sudo' : binary, sudo ? ['-n', '--', binary, ...args] : args,
      { timeout: 30000, maxBuffer: 65536, encoding: 'utf8', shell: false },
      (error, stdout, stderr) => {
        if (!error) { resolve(stdout); return; }
        const safe = Error('Owner command failed.');
        if (sudo && stderr.includes('no new privileges')) safe.code = 'SUDO_SANDBOX_BLOCKED';
        reject(safe);
      });
  });
}

async function readPrivateConfig(path) {
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await file.stat();
    if (!stat.isFile() || stat.uid !== process.getuid() || (stat.mode & 0o077) || stat.size > 8192) throw Error();
    return { text: await file.readFile('utf8'), ino: stat.ino, dev: stat.dev };
  } finally { await file.close(); }
}

// The caller must already have approval to issue a write key on this daemon and,
// if requested, use sudo. This helper does not discover or grant owner authority.
// The key works while it is used and stops after 90 days unused; seconds > 0
// adds a fixed end date (60 seconds to 365 days).
export async function authorizeConnection({ path, binary, sudo = false, seconds = 0 }, run = ownerCommand) {
  if (process.platform === 'win32') throw Error('AI-key file authorization is not qualified on Windows.');
  if (!isAbsolute(path) || !isAbsolute(binary) || !Number.isInteger(seconds) || (seconds !== 0 && (seconds < 60 || seconds > 31536000))) {
    throw Error('Use absolute config/swarmctl paths, and a fixed end date of 60–31536000 seconds or none.');
  }
  let snapshot, connection;
  try {
    const dir = await lstat(dirname(path));
    if (!dir.isDirectory() || dir.uid !== process.getuid() || (dir.mode & 0o077)) throw Error();
    snapshot = await readPrivateConfig(path);
    connection = validateConnection(JSON.parse(snapshot.text));
  } catch { throw Error('Use a private connection file and directory owned by this user (0600/0700), without symlinks.'); }
  if (connection.mode !== 'http' || connection.tokenFile) throw Error('Select an HTTP connection without an existing tokenFile before authorization.');

  // Serialize helpers and leave an audit marker if issuance has an unknown outcome.
  const lockPath = path + '.authorize-lock';
  let lock;
  try { lock = await open(lockPath, 'wx', 0o600); }
  catch { throw Error('Authorization is already running or requires recovery; inspect the nonsecret .authorize-lock before retrying.'); }
  let directory, issuedID, outcome, started = false, release = true;
  const saveReceipt = value => writeFile(join(directory, 'grant.json'), JSON.stringify(value, null, 2) + '\n', { mode: 0o600 });
  try {
    // This is read-only. Detect missing owner access before creating a credential.
    try { await run(binary, sudo, ['setup', 'status']); }
    catch (error) {
      if (error.code === 'SUDO_SANDBOX_BLOCKED') throw Error('This execution sandbox prevents sudo. Run the same helper in the user\'s normal terminal; no credential was created.');
      throw Error(sudo
      ? 'Owner access is unavailable. The user may need to run sudo -v in their own terminal, then run this helper there; no key entry is needed.'
      : 'Owner access is unavailable. Check the selected swarmctl and daemon service account before authorizing.');
    }
    directory = await mkdtemp(join(dirname(path), 'credential-'));
    const name = 'Swarm MCP ' + directory.slice(directory.lastIndexOf('credential-') + 11);
    const receipt = { name, access: 'write', expiresInSeconds: seconds, url: connection.url };
    await lock.writeFile(JSON.stringify({ directory, name }) + '\n');
    await lock.sync();
    await saveReceipt({ ...receipt, state: 'pending' });
    started = true;
    let result;
    try {
      const date = seconds > 0 ? ['--expires-in-seconds', String(seconds)] : [];
      const raw = await run(binary, sudo, ['setup', 'sdk-token', '--name', name, '--ai-access', 'write', ...date]);
      result = JSON.parse(raw);
      // Keep a valid revocation ID even if the rest of the response is malformed.
      if (typeof result?.id === 'string' && /^[a-zA-Z0-9_-]{1,256}$/.test(result.id)) issuedID = result.id;
      if (issuedID) await saveReceipt({ ...receipt, id: issuedID, state: 'issued' });
      if (!issuedID || typeof result.token !== 'string' || !/^swk_[^\s]{1,8192}$/.test(result.token)) throw Error();
    } catch { throw Error('Credential issuance was not confirmed. Inspect the saved grant record before retrying; output was withheld.'); }
    const tokenFile = join(directory, 'ai-key');
    await writeFile(tokenFile, result.token, { mode: 0o600, flag: 'wx' });
    result.token = undefined;
    const staged = join(directory, 'connection.json');
    await writeFile(staged, JSON.stringify({ ...connection, tokenFile }, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    const current = await readPrivateConfig(path);
    if (snapshot.text !== current.text || snapshot.ino !== current.ino || snapshot.dev !== current.dev) throw Error('Connection changed during authorization.');
    // Record enough to recover after a crash before or after the atomic rename.
    await saveReceipt({ ...receipt, id: issuedID, state: 'ready', tokenFile });
    await rename(staged, path);
    // No fallible credential operations after publishing the configured key.
    started = false;
    outcome = { access: 'write', expiresInSeconds: seconds, grantFile: join(directory, 'grant.json') };
    return outcome;
  } catch (error) {
    if (started && issuedID) {
      try {
        await run(binary, sudo, ['setup', 'revoke-sdk-token', '--id', issuedID]);
        await saveReceipt({ id: issuedID, state: 'revoked' });
      } catch {
        release = false;
        throw Error(`Connection was not configured; revocation could not be confirmed. Revoke grant ID ${issuedID} before retrying (this ID is not a key).`);
      }
      throw Error('Connection was not configured. The new key was revoked; existing settings were preserved.');
    }
    if (started) release = false;
    // Only our deliberately safe messages can escape; filesystem errors may expose paths.
    if (!started && !directory) throw error;
    throw Error('Authorization did not finish. Inspect the nonsecret grant record and lock before retrying; no key was displayed.');
  } finally {
    let cleanupFailed = false;
    try { await lock.close(); } catch { cleanupFailed = true; }
    if (release) { try { await unlink(lockPath); } catch { cleanupFailed = true; } }
    if (cleanupFailed) {
      if (outcome) outcome.warning = 'Access was configured, but authorization-lock cleanup needs attention. Inspect the nonsecret lock before another authorization attempt.';
      else throw Error('Authorization did not complete; recovery-lock cleanup also needs attention. Inspect the nonsecret grant and lock before retrying.' + (issuedID ? ` Revocation ID: ${issuedID}.` : ''));
    }
  }
}
