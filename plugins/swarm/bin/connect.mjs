#!/usr/bin/env node
// Configure connection metadata only. Never accepts, prints or stores a key.
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { configPath, validateConnection } from './lib/connection.mjs';
const [mode, target, ...rest] = process.argv.slice(2);
if (!mode || mode === '--help') {
  console.log('Usage: node connect.mjs native [binary] | docker CONTAINER | http HTTPS_MCP_URL [--token-file ABSOLUTE_PATH]\nSet SWARM_MCP_CONFIG to choose a connection file; otherwise use the user config directory. Existing connection files are never overwritten.');
  process.exit(0);
}
try {
  if (rest.length && !(mode === 'http' && rest.length === 2 && rest[0] === '--token-file')) throw Error('Invalid arguments; use --help.');
  const value = validateConnection(mode === 'http' ? {mode,url:target,tokenFile:rest[1]} : mode === 'docker' ? {mode,container:target} : {mode,binary:target});
  const path = configPath();
  await mkdir(dirname(path), {recursive:true,mode:0o700});
  await writeFile(path, JSON.stringify(value,null,2)+'\n', {mode:0o600,flag:'wx'});
  console.log('Connection configured. Refresh the Swarm plugin MCP, then verify a tool call.');
} catch(error) {
  console.error(error.code === 'EEXIST' ? 'Connection already exists. Inspect its nonsecret settings before deliberately editing it.' : error.message);
  process.exitCode=1;
}
