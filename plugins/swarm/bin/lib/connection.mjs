import { homedir } from 'node:os';
import { resolve, isAbsolute, join } from 'node:path';
import { readFile, open, access } from 'node:fs/promises';
import { constants } from 'node:fs';

export function configPath() {
  if (process.env.SWARM_MCP_CONFIG) return resolve(process.env.SWARM_MCP_CONFIG);
  const root = process.platform === 'win32'
    ? (process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local'))
    : (process.env.XDG_CONFIG_HOME || join(homedir(), '.config'));
  return join(root, 'swarm-mcp', 'connection.json');
}

// The Swarm box on this machine: its installer writes the person's AI key here
// and serves Swarm's MCP on loopback (containers/headless/app/install.sh).
export const BOX_MCP_URL = 'http://127.0.0.1:7783/mcp';
export function boxKeyPath() {
  return process.env.SWARM_AI_KEY_FILE ? resolve(process.env.SWARM_AI_KEY_FILE) : join(homedir(), '.config', 'swarm', 'ai-key');
}

// Plain HTTP only to this machine's loopback: the key never crosses a network
// unencrypted.
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]']);

export function validateConnection(value) {
  if (!value || !['native', 'docker', 'http'].includes(value.mode)) throw Error('Choose native, docker or http mode.');
  if (value.mode === 'http') {
    let url;
    try { url = new URL(value.url); } catch { throw Error('Supply the HTTPS Swarm /mcp URL.'); }
    const secure = url.protocol === 'https:' || (url.protocol === 'http:' && LOOPBACK_HOSTS.has(url.hostname));
    if (!secure || url.username || url.password || url.search || url.hash || url.pathname !== '/mcp') {
      throw Error('Use an HTTPS /mcp URL (or http://127.0.0.1 on this machine) without credentials, query parameters or fragment.');
    }
    if (value.tokenFile && (!isAbsolute(value.tokenFile) || typeof value.tokenFile !== 'string')) throw Error('tokenFile must be an absolute private-file path.');
    return {mode:'http', url:url.href, ...(value.tokenFile ? {tokenFile:value.tokenFile} : {})};
  }
  if (value.mode === 'docker') {
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(value.container || '')) throw Error('Supply the existing Docker container name.');
    return {mode:'docker', container:value.container};
  }
  if (value.binary && typeof value.binary !== 'string') throw Error('binary must be a path or command name.');
  if (value.socket && !isAbsolute(value.socket)) throw Error('socket must be an absolute path.');
  return {mode:'native', ...(value.binary ? {binary:value.binary} : {}), ...(value.socket ? {socket:value.socket} : {})};
}

export async function loadConnection() {
  try { return validateConnection(JSON.parse(await readFile(configPath(), 'utf8'))); }
  catch (error) {
    if (error.code === 'ENOENT' && !process.env.SWARM_MCP_CONFIG) {
      // No saved connection: the Swarm box's key on this machine, else native.
      try { await access(boxKeyPath(), constants.R_OK); return {mode:'http', url:BOX_MCP_URL, tokenFile:boxKeyPath()}; }
      catch { return {mode:'native'}; }
    }
    throw Error('Cannot load Swarm MCP connection. Run the plugin setup; check connection.json.');
  }
}

export async function readToken(path) {
  if (process.platform === 'win32') throw Error('Token-file authentication on Windows is not qualified; use a tailnet grant or native client OAuth.');
  const file = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW || 0));
  try {
    const stat = await file.stat();
    if (!stat.isFile() || (process.platform !== 'win32' && ((stat.mode & 0o077) || stat.uid !== process.getuid()))) {
      throw Error('AI key must be in a private regular file owned by this user (mode 0600).');
    }
    if (stat.size > 8192) throw Error('AI key file is too large.');
    const token = (await file.readFile('utf8')).trim();
    if (!token || /\s/.test(token)) throw Error('AI key file must contain just the key.');
    return token;
  } finally { await file.close(); }
}
