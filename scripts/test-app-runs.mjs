import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { Daemon, createRunDir, removeRunDir, ownerRequest } from './e2e/lib/daemon.mjs'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
let step = 'arguments', daemon, dir, liveProject, liveRepo, previousWorkspace, liveJob, environmentCreated
let interrupted = false
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { interrupted = true })
const liveURL = process.argv[2] === '--live' ? process.argv[3] : null
function check(ok, message) { if (!ok) throw new Error(message) }
async function run(name, fn) { step = name; await fn(); console.log(`${name}: PASS`) }
function command(bin, args, cwd = root) {
  return execFileSync(bin, args, { cwd, env: { ...process.env, GOMAXPROCS: '4' }, timeout: 600000, maxBuffer: 4 * 1024 * 1024, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
}
async function api(method, route, body) {
  const res = liveURL?.startsWith('unix:') ? await ownerRequest(new URL(liveURL).pathname, method, route, body) : liveURL ? await (async () => {
    const response = await fetch(new URL(route, liveURL), { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000) })
    const text = await response.text()
    return { status: response.status, text, body: text ? JSON.parse(text) : null }
  })() : await daemon.request(method, route, body)
  check(res.status >= 200 && res.status < 300, `${method} ${route}: HTTP ${res.status}: ${res.text?.slice(0, 400)}`)
  return res.body
}
try {
  check(process.argv.length === 2 || (liveURL && process.argv.length === 4), 'usage: bash scripts/test-app-runs.sh [--live MACHINE_API_URL]')
  check(process.env.TMPDIR, 'TMPDIR must be set')
  check(!liveURL || liveURL.startsWith('unix:///'), '--live requires the local MCP owner transport: unix:///absolute/path/to/api.sock')
  if (liveURL) await run('live-auth', () => api('GET', '/v3/projects'))
  dir = createRunDir(process.env.TMPDIR, 'app')
  const bin = path.join(dir, 'bin'); fs.mkdirSync(bin)
  if (!liveURL) {
  await run('build', () => {
    // Same commands and CGO/trimpath flags as Dockerfile.host, with bounded parallelism.
    for (const name of ['swarmd', 'swarmctl', 'swarm-fff-search']) {
      command('go', ['build', '-mod=readonly', '-p', '4', '-trimpath', '-o', path.join(bin, name), `./cmd/${name}`], path.join(root, 'swarmd'))
    }
    fs.copyFileSync(path.join(root, 'swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so'), path.join(bin, 'libfff_c.so'))
  })
  daemon = new Daemon({ binDir: bin, runDir: dir, sandbox: 'off' })
  await run('health', () => daemon.start())
  await run('auth-bootstrap', async () => {
    const value = await api('POST', '/v1/onboarding', { username: 'app-smoke', swarm_name: 'App smoke' })
    check(value.identity?.bootstrapped, 'identity not bootstrapped')
  })
  }
  const repo = path.join(dir, 'repo'); fs.mkdirSync(repo)
  await run('repository', () => {
    command('git', ['init', '-b', 'dev', repo])
    command('git', ['-c', 'user.name=App smoke', '-c', 'user.email=app-smoke@example.invalid', 'commit', '--allow-empty', '-m', 'Initial'], repo)
  })
  if (liveURL) previousWorkspace = (await api('GET', '/v1/workspace/current')).workspace_path
  if (liveURL) liveRepo = repo
  await run('workspace-register', () => api('POST', '/v1/workspace/add', { path: repo }))
  let project
  await run('project-create', async () => {
    project = (await api('POST', '/v3/projects', { client_request_id: path.basename(dir), name: 'App smoke', workspaces: [{ path: repo }] })).project
    check(project?.id, 'missing project ID')
    if (liveURL) liveProject = project.id
  })
  const readProject = async () => {
    const value = await api('GET', `/v3/projects/${project.id}`)
    check(value.project?.id === project.id, 'project did not read back')
    check(value.project.workspaces?.some(w => w.path === repo), 'workspace did not read back')
  }
  await run('project-workspace-read', readProject)
  await run('busy', () => api('GET', '/v3/box/busy'))
  await run('runs', () => api('GET', '/v3/runs'))
  if (liveURL) {
    await run('sandbox-build', async () => {
      environmentCreated = true
      let env = (await api('PUT', '/v3/sandbox-environments', { workspace_path: repo, base_image: 'swarm-sandbox:local', setup: [], env: {} })).environment
      const deadline = Date.now() + 8 * 60 * 1000
      while (env.status === 'building' && Date.now() < deadline && !interrupted) {
        await new Promise(resolve => setTimeout(resolve, 2000))
        env = (await api('GET', `/v3/sandbox-environments?workspace_path=${encodeURIComponent(repo)}`)).environment
      }
      check(env.status === 'ready', `environment status=${env.status}`)
    })
    await run('live-job', async () => {
      const workers = (await api('POST', '/v3/workers:ensure-presets', {})).workers
      const coder = workers.find(w => w.metadata?.preset === 'coder')
      check(coder?.id, 'coder preset unavailable')
      const initial = (await api('POST', `/v3/workers/${coder.id}/direct`, { prompt: 'Add a file hello.txt containing hi followed by a newline, commit it. Do not change any other files.', input: { project_id: project.id }, workspace_path: repo, idempotency_key: project.id })).run
      check(initial?.id, 'missing job ID')
      liveJob = `/v3/workers/${coder.id}/runs/${initial.id}`
      const deadline = Date.now() + 15 * 60 * 1000
      while (Date.now() < deadline && !interrupted) {
        const job = (await api('GET', `/v3/workers/${coder.id}/runs/${initial.id}`)).run
        if (['failed', 'cancelled', 'blocked'].includes(job.status)) throw new Error(`job ${job.status}`)
        if (['completed', 'succeeded', 'needs_review'].includes(job.status)) {
          let taskID = job.review?.task_id
          if (!taskID && job.session_id) {
            const detail = await api('GET', `/v3/sessions/${job.session_id}?message_limit=0&event_limit=0`)
            check(detail.session?.metadata?.project_id === project.id, 'job belongs to a different project')
            taskID = detail.session.metadata.task_id
          }
          check(taskID, 'job has no task attribution')
          const task = (await api('GET', `/v3/projects/${project.id}/tasks/${taskID}`)).task
          const branch = task?.worktree_branch
          check(branch && branch !== 'dev', 'job has no isolated branch')
          const ref = `refs/heads/${branch}`
          const head = command('git', ['rev-parse', '--verify', ref], repo).trim()
          check(head !== command('git', ['rev-parse', 'dev'], repo).trim(), 'job made no commit')
          check(command('git', ['show', `${head}:hello.txt`], repo) === 'hi\n', 'job commit has incorrect hello.txt')
          return
        }
        await new Promise(resolve => setTimeout(resolve, 2000))
      }
      throw new Error('job commit not found within 15 minutes')
    })
  } else {
  await run('update-smoke', () => {
    // Execute the installer's actual function against this real daemon.
    const installer = fs.readFileSync(path.join(root, 'containers/headless/app/install.sh'), 'utf8')
    const functions = ['local_api', 'post_update_smoke'].map(name => {
      const match = installer.match(new RegExp(`^${name}\\(\\) \\{[\\s\\S]*?^\\}`, 'm'))
      if (name === 'local_api') return installer.split('\n').find(line => line.startsWith('local_api()'))
      check(match, `missing installer function ${name}`)
      return match[0]
    }).join('\n')
    command('bash', ['-euc', `${functions}\nLOCAL_API_SOCKET=$1\npost_update_smoke`, 'update-smoke', daemon.socket])
    let rejected = false
    try { command('bash', ['-euc', `${functions}\nLOCAL_API_SOCKET=$1\npost_update_smoke`, 'update-smoke', path.join(dir, 'absent.sock')]) } catch { rejected = true }
    check(rejected, 'update smoke accepted unavailable daemon')
    command('bash', [path.join(root, 'scripts/test-update-rollback.sh'), daemon.socket])
  })
  await run('stop', () => daemon.stop())
  await run('restart', () => daemon.start({ reusePorts: true }))
  await run('project-survives-restart', readProject)
  console.log('sandbox-build: covered by --live')
  console.log('sandbox-command: covered by --live')
  console.log('worktree-commit-land: covered by --live')
  }
} catch (error) {
  console.error(`APP_RUNS_FAILED:${step}`)
  console.error(`${step}: FAIL: ${error.message}`)
  if (daemon) console.error(`daemon log tail: ${daemon.logTail(4000)}`)
  process.exitCode = 1
} finally {
  const cleanup = async (name, fn) => { try { await run(name, fn) } catch (error) { console.error(`${name}: FAIL: ${error.message}`); process.exitCode = 1 } }
  if (liveJob) await cleanup('job-cancel', async () => {
    const current = (await api('GET', liveJob)).run
    if (!['completed', 'succeeded', 'failed', 'cancelled', 'blocked', 'needs_review'].includes(current.status)) {
      await api('POST', `${liveJob}/cancel`, {})
      const deadline = Date.now() + 60000
      while (Date.now() < deadline) {
        const job = (await api('GET', liveJob)).run
        if (['completed', 'succeeded', 'failed', 'cancelled', 'blocked'].includes(job.status)) return
        await new Promise(resolve => setTimeout(resolve, 1000))
      }
      throw new Error('cancellation did not settle within 60 seconds')
    }
  })
  if (liveProject) {
    try { await run('project-cleanup', () => api('DELETE', `/v3/projects/${liveProject}`)) } catch (error) { console.error(`project-cleanup: FAIL: ${error.message}`); process.exitCode = 1 }
  }
  if (environmentCreated) await cleanup('environment-cleanup', () => api('DELETE', `/v3/sandbox-environments?workspace_path=${encodeURIComponent(liveRepo)}`))
  if (liveRepo) await cleanup('workspace-cleanup', () => api('POST', '/v1/workspace/delete', { path: liveRepo }))
  if (previousWorkspace) await cleanup('workspace-restore', () => api('POST', '/v1/workspace/select', { path: previousWorkspace }))
  if (daemon) await daemon.stop()
  if (dir) removeRunDir(dir, process.env.TMPDIR)
}
