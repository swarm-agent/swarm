// Purpose: prove exact-origin authenticated attach, deadlines, byte bounds and
// unchanged shared settings. Authority: AttachClient and scripts/run-runner-test.sh.
// Fake HTTP is the narrowest observable boundary; no real daemon/provider used.
import assert from 'node:assert/strict'
import http from 'node:http'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { AttachClient, desktopOrigin } from '../../scripts/testbench-attach.mjs'
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')

async function server(t, handler) {
  const s = http.createServer(handler)
  s.listen(0, '127.0.0.1')
  await once(s, 'listening')
  t.after(() => { s.closeAllConnections(); s.close() })
  return `http://127.0.0.1:${s.address().port}/`
}

function fixture(requests, { mismatch = false } = {}) {
  let reads = 0
  return (req, res) => {
    requests.push({method:req.method, url:req.url})
    assert.equal(req.method, 'GET')
    assert.equal(req.headers.origin, `http://${req.headers.host}`)
    if (req.url !== '/v1/auth/desktop/session') assert.equal(req.headers['x-swarm-token'], 'fixture-token')
    res.setHeader('Content-Type', 'application/json')
    if (req.url === '/v1/auth/desktop/session') res.end(JSON.stringify({token:'fixture-token'}))
    else if (req.url === '/v1/swarm/topology') res.end(JSON.stringify({runtimes:[{relationship:'self',swarm_id:'fixture-runtime',status:'online'}],workspace_bindings:[]}))
    else if (req.url === '/v1/agent-model-settings') {
      reads++
      res.end(JSON.stringify({agent_model_settings:{swarm:{action:{model:mismatch && reads>1?'changed':'fixed'},plan:{model:'fixed'}}}}))
    } else { res.statusCode=404; res.end('{}') }
  }
}

async function shell(args, env = {}) {
  const p = spawn('bash', args, {cwd:root,env:{...process.env,...env},stdio:['ignore','pipe','pipe']})
  let output=''
  const timer=setTimeout(()=>p.kill('SIGTERM'),15000)
  for (const stream of [p.stdout,p.stderr]) stream.on('data', b=>{output+=b; if(output.length>32768)p.kill('SIGTERM')})
  const [code] = await once(p,'exit'); clearTimeout(timer)
  return {code,output}
}

test('exact origin, credential confinement and unchanged settings', {timeout:5000}, async t=>{
  const requests=[]; const url=await server(t,fixture(requests))
  const result=await new AttachClient(url).inspect('fixture-runtime')
  assert.equal(result.settings_unchanged,true)
  assert.equal(requests.length,4)
  assert.ok(!JSON.stringify(result).includes('fixture-token'))
  await assert.rejects(new AttachClient(url).inspect('wrong-runtime'),/identity mismatch/)
  for(const bad of ['http://localhost:1234/','http://127.0.0.1:1234/path','http://x@127.0.0.1:1234/','http://127.0.0.1:1234/?token=x']) assert.throws(()=>desktopOrigin(bad))
})
test('settings drift rejects without restoring over concurrent changes', {timeout:5000}, async t=>{
  const requests=[];const url=await server(t,fixture(requests,{mismatch:true}))
  await assert.rejects(new AttachClient(url).inspect(),/settings changed/)
  assert.ok(requests.every(r=>r.method==='GET'))
})
test('request deadline, response cap and redirects fail closed', {timeout:5000}, async t=>{
  const hang=await server(t,()=>{})
  await assert.rejects(new AttachClient(hang,{requestMs:50}).inspect())
  const noisy=await server(t,(_req,res)=>res.end('x'.repeat(4096)))
  await assert.rejects(new AttachClient(noisy,{maxBytes:128}).inspect(),/limit/)
  const redirect=await server(t,(_req,res)=>{res.writeHead(302,{Location:noisy});res.end()})
  await assert.rejects(new AttachClient(redirect).inspect())
})
// Purpose: the kept runner launcher refuses attach-only mode before any
// connection or shared-state mutation; no daemon or testbench is involved.
test('runner launcher refuses attach-only mode', {timeout:10000}, async ()=>{
  const denied=await shell(['scripts/run-runner-test.sh','run'],{SWARM_TESTBENCH_ATTACH_ONLY:'1'})
  assert.equal(denied.code,2)
  assert.match(denied.output,/not attach-safe/)
})
