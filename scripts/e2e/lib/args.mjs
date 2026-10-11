// Argument parsing for scripts/e2e/ai-path.mjs. Pure: no I/O, so it is unit
// tested in ai-path.test.mjs. The CLI is a contract with the release gate;
// change it only together with the gate wiring.

export const SCENARIOS = Object.freeze(['onboard', 'chat', 'tasks', 'sdk', 'restart'])

export const USAGE = `Usage:
  node scripts/e2e/ai-path.mjs --scenario <onboard|chat|tasks|sdk|restart>
    --bin-dir DIR --work-dir DIR --output FILE
    [--key-file FILE | --load-key-cmd CMD]
    [--model gemini-3.8-flash] [--thinking low] [--timeout-ms 240000] [--sdk-pack TGZ]

  --bin-dir       swarmd, swarmctl and libfff_c.so. In the release layout of
                  scripts/build-main-dist.sh that is <output>/linux-amd64/swarmd/
                  (the archive's swarm-<version>-linux-amd64/linux-amd64/swarmd/).
  --work-dir      parent directory; each run makes its own sw-<scenario>-<random>
                  directory there for one private daemon (own ports and state).
  --output        JSON receipt (schema swarm.ai-path-e2e.v1).
  --key-file      0600 file holding the Google (Gemini) API key; read into memory
                  only and submitted through the provider key page.
  --load-key-cmd  command template run once the daemon is up; {socket} is
                  replaced by the owner socket path and the command loads the key
                  itself (the key page step is then not exercised).
  --model/--thinking  model set on the test daemon only (default
                  gemini-3.8-flash / low).
  --timeout-ms    whole-scenario deadline (default 240000).
  --sdk-pack      @swarm-agent/sdk tarball for the sdk scenario; without it the
                  SDK is built from packages/sdk in a copy inside the run dir.

Without --key-file or --load-key-cmd only the steps that need no model run and
the receipt fails with assertion live.key.`

const VALUE_FLAGS = new Map([
  ['--scenario', 'scenario'],
  ['--bin-dir', 'binDir'],
  ['--work-dir', 'workDir'],
  ['--output', 'output'],
  ['--key-file', 'keyFile'],
  ['--load-key-cmd', 'loadKeyCmd'],
  ['--model', 'model'],
  ['--thinking', 'thinking'],
  ['--timeout-ms', 'timeoutMs'],
  ['--sdk-pack', 'sdkPack'],
])

export class UsageError extends Error {}

// parseArgs returns the options or throws UsageError. Values are taken
// verbatim; only --timeout-ms is converted. A --flag=value form is accepted.
export function parseArgs(argv) {
  const out = { model: 'gemini-3.8-flash', thinking: 'low', timeoutMs: 240000, help: false }
  const seen = new Set()
  for (let i = 0; i < argv.length; i++) {
    let arg = String(argv[i])
    if (arg === '-h' || arg === '--help') {
      out.help = true
      continue
    }
    let value
    const eq = arg.indexOf('=')
    if (arg.startsWith('--') && eq > 0) {
      value = arg.slice(eq + 1)
      arg = arg.slice(0, eq)
    }
    const key = VALUE_FLAGS.get(arg)
    if (!key) throw new UsageError(`unknown argument ${JSON.stringify(arg.slice(0, 40))}`)
    if (value === undefined) {
      if (i + 1 >= argv.length) throw new UsageError(`${arg} needs a value`)
      value = String(argv[++i])
    }
    if (seen.has(key)) throw new UsageError(`${arg} given twice`)
    seen.add(key)
    out[key] = value
  }
  if (out.help) return out
  for (const [flag, key] of [['--scenario', 'scenario'], ['--bin-dir', 'binDir'], ['--work-dir', 'workDir'], ['--output', 'output']]) {
    if (!seen.has(key) || String(out[key]).trim() === '') throw new UsageError(`${flag} is required`)
  }
  if (!SCENARIOS.includes(out.scenario)) throw new UsageError(`--scenario must be one of ${SCENARIOS.join(', ')}`)
  if (seen.has('keyFile') && seen.has('loadKeyCmd')) throw new UsageError('give --key-file or --load-key-cmd, not both')
  if (seen.has('loadKeyCmd') && !String(out.loadKeyCmd).includes('{socket}')) throw new UsageError('--load-key-cmd must contain {socket}')
  const timeout = Number(out.timeoutMs)
  if (!Number.isInteger(timeout) || timeout < 10000 || timeout > 6 * 3600 * 1000) throw new UsageError('--timeout-ms must be an integer from 10000 to 21600000')
  out.timeoutMs = timeout
  if (!/^[A-Za-z0-9._:-]{1,100}$/.test(out.model)) throw new UsageError('--model is not a valid model id')
  if (!/^[A-Za-z0-9_-]{1,40}$/.test(out.thinking)) throw new UsageError('--thinking is not a valid thinking level')
  return out
}
