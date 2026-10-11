import { homedir } from 'node:os';
import { resolve, isAbsolute, join } from 'node:path';
import { readFile, open } from 'node:fs/promises';
import { constants } from 'node:fs';

export function configPath() {
  if (process.env.SWARM_MCP_CONFIG) return resolve(process.env.SWARM_MCP_CONFIG);
  const root = process.platform === 'win32'
    ? (process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local'))
    : (process.env.XDG_CONFIG_HOME || join(homedir(), '.config'));
  return join(root, 'swarm-mcp', 'connection.json');
}

export function validateConnection(value) {
  if (!value || !['native', 'docker', 'http'].includes(value.mode)) throw Error('Choose native, docker or http mode.');
  if (value.mode === 'http') {
    let url;
    try { url = new URL(value.url); } catch { throw Error('Supply the HTTPS Swarm /mcp URL.'); }
    if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.pathname !== '/mcp') {
      throw Error('Use an HTTPS /mcp URL without credentials, query parameters or fragment.');
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
    if (error.code === 'ENOENT' && !process.env.SWARM_MCP_CONFIG) return {mode:'native'};
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
