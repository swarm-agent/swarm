#!/usr/bin/env node
import { readFileSync } from 'node:fs';
import { HELP, parseArgs, engineRunner, runLauncher } from '../src/launcher.js';

try {
  const opts = parseArgs(process.argv.slice(2));
  const pin = JSON.parse(readFileSync(new URL('../runtime-image.json', import.meta.url), 'utf8'));
  if (opts.command === 'help') console.log(HELP);
  else if (opts.command === 'version') console.log(pin.version);
  else {
    if (process.platform !== 'linux') throw Error('This candidate supports Linux hosts only');
    runLauncher(opts, pin, engineRunner(opts.engine), { stdinTTY: process.stdin.isTTY, stdoutTTY: process.stdout.isTTY });
  }
} catch (error) {
  // Only our fixed messages are safe; filesystem/JSON errors can contain paths/data.
  const message = error instanceof Error && !error.code && !(error instanceof SyntaxError)
    ? error.message : 'Cannot read package metadata or selected project';
  console.error(`swarm-headless: ${message}`);
  process.exitCode = 1;
}
