import { rmSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

// Remove only this package's generated output so npm pack cannot retain stale files.
rmSync(new URL('../dist', import.meta.url), { recursive: true, force: true });
const result = spawnSync(process.execPath, [fileURLToPath(new URL('../node_modules/typescript/bin/tsc', import.meta.url))], {
  cwd: fileURLToPath(new URL('..', import.meta.url)), stdio: 'inherit', shell: false,
});
if (result.error || result.status !== 0) process.exitCode = 1;
