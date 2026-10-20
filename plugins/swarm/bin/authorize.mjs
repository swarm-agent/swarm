#!/usr/bin/env node
import { parseArgs } from 'node:util';
import { configPath } from './lib/connection.mjs';
import { authorizeConnection } from './lib/authorize.mjs';

let options;
try {
  options = parseArgs({ options: {
    swarmctl: { type: 'string' }, sudo: { type: 'boolean', default: false },
    'expires-in-seconds': { type: 'string', default: '0' }, help: { type: 'boolean' }
  } }).values;
} catch { console.error('Invalid authorization arguments. Use --help; never pass a key.'); process.exit(1); }
if (options.help) {
  console.log('Usage: node bin/authorize.mjs --swarmctl /absolute/path/to/swarmctl [--sudo] [--expires-in-seconds N]\nAfter user approval, creates a write key (it works while used and stops after 90 days unused; --expires-in-seconds adds a fixed end date) on the local daemon and privately configures the selected HTTP connection. First verify the URL belongs to this daemon. Run as the ordinary user; only swarmctl is elevated with --sudo. Sudo is noninteractive. Secrets never appear in output.');
} else {
  try {
    if (!options.swarmctl) throw Error('Select an absolute swarmctl path with --swarmctl.');
    const result = await authorizeConnection({ path: configPath(), binary: options.swarmctl, sudo: options.sudo, seconds: Number(options['expires-in-seconds']) });
    const life = result.expiresInSeconds > 0 ? `until a fixed end date in ${result.expiresInSeconds} seconds` : "while it is used (it stops after 90 days unused)";
    console.log(`Write access configured; the key works ${life}. Key saved privately; refresh MCP and verify a tool call.\nNonsecret revocation record: ${result.grantFile}`);
    if (result.warning) console.error('Warning: ' + result.warning);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
