#!/usr/bin/env node
// Purpose: prove real authenticated memory persistence, CAS, recall and scoped
// provider learning at the supplied owned testbench, never another endpoint.
// Authority: api/memory.go, memory/service.go, MemoryStore, V3 session mutation.
// One provider run at a time; bounded stages; credentials remain in memory.
import assert from 'node:assert/strict'
import {randomUUID} from 'node:crypto'
import {readFile,writeFile} from 'node:fs/promises'
import {WorkspaceTrial} from './workspace-launch.mjs'
const [url,stage,file]=process.argv.slice(2)
assert(url && stage && file,'URL, stage and ignored evidence path required')
const c=new WorkspaceTrial(url,{stageMs:540000}); c.memoryTrial=true
await c.initialize()
assert.equal(c.settings.swarm.action.provider,'codex');assert.equal(c.settings.swarm.action.model,'gpt-5.6-luna');assert.equal(c.settings.swarm.action.thinking,'medium')
let state=stage==='prepare'?{id:`memory-${randomUUID()}`,checks:[]}:JSON.parse(await readFile(file,'utf8'))
// Old evidence may carry retired controls; never submit them to current strict API.
if(state.originalSettings){delete state.originalSettings.spend_microunits;delete state.originalSettings.review_before_apply}
const save=()=>writeFile(file,JSON.stringify(state,null,2),{mode:0o600})
const mem=()=>c.request('GET','/v1/memory')
const post=async body=>c.request('POST','/v1/memory',{expected_revision:(await mem()).revision,reason:'Owned memory end-to-end fixture',...body})
const check=name=>{state.checks.push(name);console.log(JSON.stringify({check:name,status:'pass'}))}
const restoreClient=()=>{c.id=state.trialID;c.evidence=state.evidence;for(const s of state.sessions||[])c.sessions.set(s.id,s)}
try {
 if(stage==='prepare'){
  const d=await mem();assert.equal((d.entries||[]).length,0,'requires empty dedicated memory account');assert.equal(d.settings.automation_enabled,false);state.originalSettings=d.settings
  const current=await c.request('GET','/v1/workspace/current');const topology=await c.get('/v1/swarm/topology');const binding=topology.workspace_bindings.find(b=>b.source_workspace_id===current.workspace_id);assert(binding,'current workspace binding missing')
  const f={path:current.workspace_path,workspace:{workspace_id:current.workspace_id,local_workspace_binding_id:binding.id||binding.workspace_binding_id,name:'memory trial'}};state.fixture=f
  const key=`${state.id}-source`;const result=await c.request('POST','/v3/sessions',{client_request_id:key,idempotency_key:key,title:key,workspace_path:f.path,workspace_binding_id:f.workspace.local_workspace_binding_id,swarm_id:c.identity.runtime_id,target_kind:'host',target_relationship:'self',mode:'auto',agent_name:'swarm',worktree_mode:'on',worktree_branch_name:`agent/memory-${randomUUID()}`,preference:c.settings.swarm.action,model_profile:{use_agent_default:true}})
  const s=result.session;assert(s?.id&&s.worktree_enabled&&s.worktree_root_path!==f.path);c.sessions.set(s.id,s);c.evidence.sessions.push({id:s.id});state.sessions=[s];state.trialID=c.id;state.evidence=c.evidence
  state.entry={id:state.id,kind:'rule',content:`The memory E2E recall code is ${randomUUID()}.`,pinned:true};
  const written=await post({action:'remember',entry:state.entry});state.firstRevision=written.revision
  assert.equal(written.entries.find(e=>e.id===state.id).content,state.entry.content)
  await assert.rejects(c.request('POST','/v1/memory',{action:'remember',expected_revision:d.revision,reason:'stale fixture',entry:{...state.entry,content:'wrong'}}),/409/)
  assert.equal((await mem()).entries.find(e=>e.id===state.id).content,state.entry.content);check('explicit remember and stale-write rejection')
  const selection=await c.request('GET',`/v1/memory?session_id=${s.id}`);assert(JSON.stringify(selection).includes(state.entry.content));check('session injection includes pinned rule')
  await save()
 } else if(stage==='recall'){
  restoreClient();const s=state.sessions[0]
  const {snapshot}=await c.run(s.id,'What is the memory E2E recall code? Return only the code from account memory, no tools. Do not invent it.')
  const messages=snapshot.messages_by_session?.[s.id]||[]
  const code=state.entry.content.match(/[a-f0-9-]{36}/)[0]
  assert(messages.some(m=>m.role==='assistant'&&JSON.stringify(m.content).includes(code)),'provider did not recall stored code')
  check('real provider recalls account memory in a new isolated session')
  const update={...state.entry,content:state.entry.content+' Keep this explicit rule pinned.'};await post({action:'remember',entry:update})
  const restored=await post({action:'restore',entry_id:state.id,restore_revision:state.firstRevision})
  assert.equal(restored.entries.find(e=>e.id===state.id).content,state.entry.content);assert(restored.revision>state.firstRevision);check('history restore creates a new revision')
  await save()
 } else if(stage==='learn'){
  restoreClient();const s=state.sessions[0]
  await c.run(s.id,'This disposable project uses the protocol name Copper Finch and stores its generated reports as UTF-8 JSON. This is a factual project convention for future work. Reply ACK only, without tools.')
  await post({action:'settings',settings:{...state.originalSettings,automation_enabled:true,included_sessions:[s.id],included_workspaces:[],mode:'recurring'}})
  state.jobID=`${state.id}-${randomUUID()}`;await save()
  let job
  try{job=await post({action:'run_now',job_id:state.jobID})}catch(e){const j=(await mem()).jobs?.find(j=>j.id===state.jobID);console.log(JSON.stringify({job_status:j?.status,job_error:j?.error}));throw e}
  assert.equal(job.status,'completed',`learning status ${job.status}`)
  assert(job.sources.every(src=>src.session_id===s.id));
  const d=await mem();state.learnedIDs=d.entries.filter(e=>e.kind==='learned').map(e=>e.id);assert(state.learnedIDs.length>0);assert.equal(d.entries.find(e=>e.id===state.id).content,state.entry.content);check('automatic learning persists without rewriting pinned rule')
  await save()
 } else if(stage==='scheduled'){
  restoreClient();const s=state.sessions[0];const before=await mem();const cursor=before.job_cursors?.[s.id]||0;const oldJobs=new Set((before.jobs||[]).map(j=>j.id));
  await post({action:'settings',settings:{...before.settings,automation_enabled:false}})
  state.incrementalFact=`The project's durable release codename is Amber Heron ${randomUUID()}.`;await save()
  await c.run(s.id,state.incrementalFact+' Reply ACK only. Do not use tools or write memory yourself.')
  const current=await mem();await post({action:'settings',settings:{...current.settings,automation_enabled:true,mode:'recurring',included_sessions:[s.id],included_workspaces:[]}})
  const deadline=Date.now()+240000;let done
  while(Date.now()<deadline){const d=await mem();const jobs=(d.jobs||[]).filter(j=>!oldJobs.has(j.id)&&j.trigger==='scheduled');console.log(JSON.stringify({stage:'scheduled',jobs:jobs.map(j=>({status:j.status,error:j.error})),cursor:d.job_cursors?.[s.id]||0}));assert(!jobs.some(j=>['failed','cancelled','interrupted'].includes(j.status)),'scheduled batch failed');done=jobs.find(j=>j.status==='completed'&&(d.job_cursors?.[s.id]||0)>cursor);if(done){assert(done.sources.every(src=>src.session_id===s.id&&src.event_seq>cursor),'reread committed source');assert(d.entries.some(e=>e.kind==='learned'&&e.content.includes('Amber Heron')),'new fact not automatically learned');assert.equal(d.entries.find(e=>e.id===state.id).content,state.entry.content);state.learnedIDs=d.entries.filter(e=>e.kind==='learned').map(e=>e.id);break}await new Promise(r=>setTimeout(r,15000))}
  assert(done,'scheduler deadline');check('real scheduled incremental update without run-now or approval');await save()
 } else if(stage==='cleanup'){
  restoreClient();const before=await mem();
  await post({action:'settings',settings:state.originalSettings})
  for(const entry of (await mem()).entries||[]){if((entry.id===state.id||(state.learnedIDs||[]).includes(entry.id))&&(await mem()).entries?.some(e=>e.id===entry.id))await post({action:'forget',entry_id:entry.id})}
  const d=await mem();assert(!d.settings.automation_enabled);assert(!(d.entries||[]).some(e=>e.id===state.id||(state.learnedIDs||[]).includes(e.id)));assert(!JSON.stringify(d.history).includes(state.entry.content));check('pause and permanent forget redact fixture history')
  await save()
 } else throw new Error('unknown stage')
} catch(e){state.failure={stage,message:e.message};await save();console.error(JSON.stringify({stage,error:e.message}));process.exitCode=1}
finally{await c.stopOwned();state.evidence=c.evidence;await save()}
