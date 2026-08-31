#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-ssh-fast-testbench.sh
source "${ROOT_DIR}/scripts/lib-ssh-fast-testbench.sh"
swarm_fast_testbench_configure "${ROOT_DIR}" "${1:-}" || exit 1
# Every live provider-boundary run must use the maintained fast deployment path
# when the configured remote candidate is stale or dirty.
swarm_fast_testbench_prepare_candidate "${ROOT_DIR}" || exit 1
CODEX_MODEL_A="${SWARM_E2E_CODEX_MODEL_A:-gpt-5.5}"
CODEX_MODEL_B="${SWARM_E2E_CODEX_MODEL_B:-gpt-5.4}"
CODEX_PLAN_MODEL="${SWARM_E2E_CODEX_PLAN_MODEL:-${CODEX_MODEL_A}}"
CODEX_AUTO_MODEL="${SWARM_E2E_CODEX_AUTO_MODEL:-${CODEX_MODEL_A}}"
CODEX_PLAN_THINKING="${SWARM_E2E_CODEX_PLAN_THINKING:-xhigh}"
CODEX_AUTO_THINKING="${SWARM_E2E_CODEX_AUTO_THINKING:-high}"
FIREWORKS_MODEL="${SWARM_E2E_FIREWORKS_MODEL:-accounts/fireworks/models/deepseek-v4-flash-0731}"
FIREWORKS_THINKING="${SWARM_E2E_FIREWORKS_THINKING:-high}"
E2E_ID="${E2E_ID:-v3-provider-boundary-$(date +%Y%m%d-%H%M%S)-$RANDOM}"
if [[ ! "${E2E_ID}" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "E2E_ID may only contain letters, numbers, dots, underscores, and dashes" >&2
  exit 2
fi
LOCAL_DIR="$(mktemp -d -t "${E2E_ID}.local.XXXXXX")"
REMOTE_DIR="${SWARM_E2E_REMOTE_DIR:-}"
if [[ -z "${REMOTE_DIR}" ]]; then
  REMOTE_DIR="$(ssh "${SSH_HOST}" "mktemp -d -t '${E2E_ID}.remote.XXXXXX'")"
fi
WORKSPACE_DIR="${SWARM_E2E_WORKSPACE_DIR:-${REMOTE_DIR}/workspace}"
RUNNER_LOCAL="${LOCAL_DIR}/runner.mjs"
cat >"${RUNNER_LOCAL}" <<'NODE'
import crypto from 'node:crypto';
import fs from 'node:fs';
import {spawnSync} from 'node:child_process';

const cfg = JSON.parse(fs.readFileSync(process.env.SWARM_E2E_CONFIG, 'utf8'));
const started = new Date();
const startedISO = started.toISOString();
const artifactDir = cfg.artifactDir;
fs.mkdirSync(artifactDir, {recursive: true});
const httpLog = `${artifactDir}/http.ndjson`;
const summaryPath = `${artifactDir}/summary.json`;
fs.writeFileSync(httpLog, '');
const result = {result:'NOT_DONE', e2e_id:cfg.e2eID, workspace:cfg.workspaceDir, started_at:startedISO, candidate:{}, ids:{}, gates:{}, diagnostics:{}, artifacts:{http:httpLog, summary:summaryPath, artifact_dir:artifactDir}, failures:[], failed_gates:[]};
const requiredGates=['candidate','diagnostics_enabled','workspace','codex_same_lineage','codex_model_handoff','codex_to_fireworks_handoff','codex55_to_fireworks_deepseek','exit_plan_mode_splice','post_checkpoint_resume','post_final_new_checkpoint','task_program_multi_worktree','task_program_second_boundary','individual_coder_worktree','individual_coder_second_boundary','checkpoint_fresh_context','codex_usage','fireworks_usage','provider_tool_construction','logs'];
function refreshSummary(){ result.failed_gates=requiredGates.filter(g=>result.gates[g]!==true); }
function save(){ refreshSummary(); fs.writeFileSync(summaryPath, JSON.stringify(result,null,2)); }
function die(msg){ result.failures.push(msg); save(); throw new Error(msg); }
function assert(cond,msg){ if(!cond) die(msg); }
function append(file,obj){ fs.appendFileSync(file, JSON.stringify({t:new Date().toISOString(),...obj})+'\n'); }
function run(cmd,args,opts={}){ const p=spawnSync(cmd,args,{encoding:'utf8',...opts}); return {cmd:[cmd,...args].join(' '), status:p.status, stdout:p.stdout, stderr:p.stderr}; }
function redact(h){ const out={}; for (const [k,v] of Object.entries(h||{})) out[k]=/token|cookie|authorization/i.test(k)?'<redacted>':v; return out; }
async function api(method, route, token, body, label=route, allowError=false, timeoutMs=300000){
  const headers={Accept:'application/json',Origin:cfg.apiURL,Referer:`${cfg.apiURL}/app`,'Sec-Fetch-Site':'same-origin'};
  if(token){ headers['X-Swarm-Token']=token; headers.Cookie=`swarm_desktop_session=${token}`; }
  const ac = new AbortController(); const timer = setTimeout(()=>ac.abort(new Error(`${label} timeout`)), timeoutMs);
  const init={method,headers,signal:ac.signal};
  if(body!==undefined){ headers['Content-Type']='application/json'; init.body=JSON.stringify(body); }
  const t0=Date.now();
  try{
    const res=await fetch(`${cfg.apiURL}${route}`, init); const text=await res.text();
    let json=null; try{ json=text?JSON.parse(text):null; }catch{ json={raw:text}; }
    append(httpLog,{label,method,route,status:res.status,ok:res.ok,request_headers:redact(headers),request_body:body,response:json,ms:Date.now()-t0});
    if(!allowError && !res.ok) die(`${label} ${method} ${route} status=${res.status} body=${text.slice(0,1200)}`);
    return {status:res.status, ok:res.ok, body:json, text};
  } finally { clearTimeout(timer); }
}
async function authDesktop(){ const r=await api('GET','/v1/auth/desktop/session','',undefined,'auth.desktop'); const token=String(r.body?.token||''); assert(token,'desktop auth returned no token'); return token; }
async function setPreference(token, provider, model, thinking='low'){ return (await api('POST','/v1/model',token,{provider,model,thinking},`model.${provider}.${model}`)).body; }
async function ensureE2EAgent(token){
  void token;
  result.ids.agent_name='swarm';
  return 'swarm';
}
async function ensureExitPlanAgent(token){
  void token;
  result.ids.exit_agent_name='swarm';
  return 'swarm';
}
async function ensureWorkflowAgent(token){
  void token;
  result.ids.workflow_agent_name='swarm';
  return 'swarm';
}
async function createSession(token,binding,swarm,agentName, opts={}){
  const mode=opts.mode||'auto';
  const pref=opts.preference||{provider:'codex',model:cfg.codexModelA,thinking:'low'};
  const label=opts.label||'create';
  const workspacePath=opts.workspacePath||result.workspace;
  const workspaceName=opts.workspaceName||cfg.e2eID;
  const body={client_request_id:`${cfg.e2eID}:create:${label}:${crypto.randomBytes(3).toString('hex')}`,title:`${cfg.e2eID} ${label} provider boundary`,workspace_path:workspacePath,workspace_name:workspaceName,workspace_binding_id:binding,swarm_id:swarm,target_kind:'host',target_relationship:'self',mode,agent_name:agentName,preference:pref,metadata:{e2e_id:cfg.e2eID,provider_boundary_e2e:true,label}};
  if(mode==='plan') body.model_profile={use_account_default:true}; else body.model_profile={temporary:{name:`Provider boundary ${label}`,provider:pref.provider,model:pref.model,thinking:pref.thinking||'low',service_tier:pref.service_tier||'',context_mode:pref.context_mode||''}};
  if(opts.worktree){ body.worktree_mode='on'; body.worktree_base_branch='dev'; body.worktree_branch_name=opts.worktreeBranch; }
  const r=await api('POST','/v3/sessions',token,body,`create.session.${label}`); const id=r.body?.session?.id; assert(id,'create missing session id'); return id;
}
async function postMessage(token, sessionID, label, content){
  const body={client_request_id:`${cfg.e2eID}:${label}:${crypto.randomBytes(3).toString('hex')}`,role:'user',content,metadata:{e2e_id:cfg.e2eID,label}};
  const r=await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/messages`,token,body,`message.${label}`,false,600000);
  const runID=r.body?.run_intent?.run_id||r.body?.run_id||'';
  result.ids[`${label}_run_id`]=runID;
  return runID;
}
async function waitForAssistant(token, sessionID, runID, label, timeoutMs=600000){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    const tail=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`,token,undefined,`tail.${label}`,false,60000)).body;
    const assistants=(tail.messages||[]).filter(m=>m.role==='assistant' && (!runID || String(m.metadata?.run_id||'')===runID));
    if(assistants.length>0){ const msg=assistants.at(-1); if(String(msg.content||'').trim()!=='') return msg; }
    const events=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=0&limit=500`,token,undefined,`events.${label}`,false,60000)).body;
    const failed=(events.events||[]).find(e=>String(e.event_type||'').includes('failed') && JSON.stringify(e).includes(runID));
    if(failed) die(`${label} failed: ${JSON.stringify(failed).slice(0,1200)}`);
    await new Promise(r=>setTimeout(r,2000));
  }
  die(`timeout waiting for assistant ${label} run=${runID}`);
}
async function waitForPermission(token, sessionID, runID, toolName, label, timeoutMs=180000){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`,token,undefined,`permissions.${label}`,false,60000)).body;
    const pending=(body.permissions||[]).find(p=>(!runID || String(p.run_id||'')===runID) && (!toolName || String(p.tool_name||'')===toolName));
    if(pending?.id) return pending;
    await new Promise(r=>setTimeout(r,1000));
  }
  die(`timeout waiting for ${toolName||'tool'} permission ${label} run=${runID}`);
}
async function resolvePermission(token, sessionID, permissionID, label){
  const r=await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(permissionID)}/resolve`,token,{action:'allow_once',reason:`${cfg.e2eID} ${label}`},`permission.resolve.${label}`,false,120000);
  assert(r.body?.permission?.status==='approved',`permission ${permissionID} not approved: ${JSON.stringify(r.body)}`);
  return r.body.permission;
}
async function allowExitPlanModeOrObserveTransition(token, sessionID, runID, timeoutMs=180000){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    const permissions=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`,token,undefined,'permissions.exit_plan_mode',false,60000)).body;
    const pending=(permissions.permissions||[]).find(p=>String(p.run_id||'')===runID && String(p.tool_name||'')==='exit_plan_mode');
    if(pending?.id){ await resolvePermission(token,sessionID,pending.id,'exit_plan_mode'); return pending; }
    const view=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}`,token,undefined,'session.exit_plan_mode',false,60000)).body;
    if(String((view.session||view).mode||'')==='auto') return null;
    const events=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=0&limit=500`,token,undefined,'events.exit_plan_mode',false,60000)).body;
    const failed=(events.events||[]).find(e=>String(e.event_type||'').includes('failed') && JSON.stringify(e).includes(runID));
    if(failed) die(`exit_plan_mode run failed: ${JSON.stringify(failed).slice(0,1200)}`);
    await new Promise(r=>setTimeout(r,1000));
  }
  die(`timeout waiting for exit_plan_mode permission or auto transition run=${runID}`);
}
async function waitForSessionMode(token, sessionID, mode, label, timeoutMs=180000){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}`,token,undefined,`session.${label}`,false,60000)).body;
    const session=body.session||body;
    if(String(session.mode||'')===mode) return session;
    await new Promise(r=>setTimeout(r,1000));
  }
  die(`timeout waiting for session mode ${mode} ${label}`);
}
async function approvePendingTools(token, sessionID, allowedTools, label){
  const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`,token,undefined,`permissions.poll.${label}`,false,60000)).body;
  for(const pending of body.permissions||[]){
    const toolName=String(pending.tool_name||'');
    assert(allowedTools.includes(toolName),`${label} requested unexpected permission for ${toolName}: ${JSON.stringify(pending)}`);
    await resolvePermission(token,sessionID,pending.id,`${label}.${toolName}`);
    result.diagnostics.workflow_permissions=result.diagnostics.workflow_permissions||[];
    result.diagnostics.workflow_permissions.push({label,permission_id:pending.id,tool_name:toolName,run_id:pending.run_id||''});
    assert(result.diagnostics.workflow_permissions.length<=40,`${label} exceeded bounded permission approvals`);
  }
}
async function waitForPlanReview(token, sessionID, label, timeoutMs=600000, minCheckpoints=1, allowedTools=[], childAllowedTools=[]){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    if(allowedTools.length) await approvePendingTools(token,sessionID,allowedTools,label);
    if(childAllowedTools.length){
      for(const child of await delegatedChildren(token,sessionID,`${label}.permissions`)) await approvePendingTools(token,child.id,childAllowedTools,`${label}.child.${child.id}`);
    }
    const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/plans/active`,token,undefined,`plan.${label}`,false,60000)).body;
    const plan=body.plan||body.active_plan||body;
    const doc=plan.document||{};
    const checkpoints=doc.checkpoints||[];
    const finalReview=doc.execution_state?.status==='waiting_review' || doc.execution_state?.status==='final_review';
    const requiredComplete=checkpoints.length>=minCheckpoints && checkpoints.slice(0,minCheckpoints).every(cp=>cp.status==='completed');
    if(finalReview && requiredComplete) return plan;
    await new Promise(r=>setTimeout(r,2000));
  }
  die(`timeout waiting for active plan review ${label} with ${minCheckpoints} completed checkpoints`);
}
async function runTurn(token, sessionID, label, provider, model, content, thinking='low'){
  await api('PUT',`/v3/sessions/${encodeURIComponent(sessionID)}/model-profile`,token,{client_request_id:`${cfg.e2eID}:model-profile:${label}:${crypto.randomBytes(3).toString('hex')}`,choice:{temporary:{name:`Provider boundary ${label}`,provider,model,thinking}}},`model_profile.${label}`);
  const runID=await postMessage(token, sessionID, label, content);
  const assistant=await waitForAssistant(token, sessionID, runID, label);
  result.ids[`${label}_assistant_id`]=assistant.id;
  return assistant;
}
function initializeWorkflowRepository(path){
  fs.mkdirSync(path,{recursive:true});
  assert(run('git',['init','-b','dev',path]).status===0,`initialize workflow repository failed at ${path}`);
  assert(run('git',['-C',path,'config','user.name','Swarm Testbench']).status===0,'configure workflow git user.name failed');
  assert(run('git',['-C',path,'config','user.email','swarm-testbench@example.invalid']).status===0,'configure workflow git user.email failed');
  fs.writeFileSync(`${path}/README.md`,`# ${cfg.e2eID} workflow testbench\n`);
  assert(run('git',['-C',path,'add','README.md']).status===0,'stage workflow repository seed failed');
  assert(run('git',['-C',path,'commit','-m','Initialize workflow testbench']).status===0,'commit workflow repository seed failed');
  assert(run('git',['-C',path,'status','--porcelain']).stdout.trim()==='','workflow repository seed is dirty');
}
async function saveAndStartWorkflowPlan(token, sessionID, label, document){
  await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plans`,token,{plan_id:document.id,title:document.title,document,status:'approved',approval_state:'approved',activate:true},`plan.save.${label}`);
  const checkpointID=document.active_checkpoint_id;
  const start=(await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plan-mode/checkpoints/${encodeURIComponent(checkpointID)}/start`,token,{plan_id:document.id},`checkpoint.start.${label}`,false,600000)).body;
  const runID=start.run_start?.run_intent?.run_id||start.run_intent?.run_id||'';
  assert(runID,`${label} checkpoint start missing run_id`);
  result.ids[`${label}_run_id`]=runID;
  return runID;
}
async function getSessionView(token, sessionID, label){
  const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}`,token,undefined,`session.${label}`,false,60000)).body;
  return body.session||body;
}
async function delegatedChildren(token, parentSessionID, label){
  const boot=(await api('POST','/v3/sync/bootstrap',token,{surface:'desktop',selector:{kind:'global',global:true,recent:{limit:200}}},`bootstrap.children.${label}`,false,60000)).body;
  return Object.values(boot.sessions_by_id||{}).filter(session=>String(session.metadata?.parent_session_id||'')===parentSessionID && String(session.metadata?.lineage_kind||'')==='delegated_subagent');
}
function assertCleanGitWorktree(path,label){
  const state=run('git',['-C',path,'status','--porcelain']);
  assert(state.status===0,`${label} git status failed: ${state.stderr}`);
  assert(state.stdout.trim()==='',`${label} worktree is dirty: ${state.stdout}`);
}
async function fetchEvents(token, sessionID, label){
  const all=[];
  const pageLimit=1000;
  let afterSeq=0;
  for(let page=0; page<20; page++){
    const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=${afterSeq}&limit=${pageLimit}`,token,undefined,`events.${label}.${page}`)).body;
    const events=body.events||[];
    all.push(...events);
    const nextSeq=Number(body.next_seq||body.applied_seq||0);
    if(events.length===0 || nextSeq<=afterSeq || events.length<pageLimit) return all;
    afterSeq=nextSeq;
  }
  die(`events.${label} exceeded pagination safety limit`);
}
function diagnosticEvents(events, runID, stage){
  return events.filter(e=>String(e.event_type||'')===stage && (!runID || JSON.stringify(e).includes(runID))).map(e=>{
    if (e && typeof e.payload === 'object' && e.payload !== null) return e.payload;
    try { return JSON.parse(String(e.payload||'{}')); } catch { return {}; }
  });
}
function diagnosticPayload(d){ return d?.payload || d?.Payload || d || {}; }
function providerRequestRecords(events, runID){
  return events.filter(e=>String(e.event_type||'')==='session.diagnostic.provider.request' && (!runID || String(e.run_id||e.runID||'')===runID || JSON.stringify(e).includes(runID))).map(e=>{ const envelope=e?.payload&&typeof e.payload==='object'?e.payload:{}; const p=diagnosticPayload(envelope); return {event:e,envelope,payload:p,request:p.request||{}}; });
}
function requestDiag(events, runID){ const all=providerRequestRecords(events, runID); assert(all.length>0,`missing provider request diagnostic for ${runID}`); return all.at(-1).request || {}; }
function requestRecordBy(events, predicate, label){ const all=providerRequestRecords(events,'').filter(r=>predicate(r.request,r)); assert(all.length>0,`missing provider request diagnostic matching ${label}`); return all.at(-1); }
function recordRunID(record){ return String(record?.event?.run_id||record?.event?.runID||record?.envelope?.run_id||record?.envelope?.runID||record?.payload?.run_id||record?.payload?.runID||''); }
function requestSummary(record){ const r=record.request||{}; return {run_id:recordRunID(record), provider:record.payload?.provider||'', model:r.model||record.payload?.model||'', thinking:r.thinking||'', boundary_reason:r.boundary_reason||'', native_continuation_allowed:r.native_continuation_allowed, force_fresh_provider_context:r.force_fresh_provider_context, provider_lineage_id:r.provider_lineage_id||'', previous_provider_lineage_id:r.previous_provider_lineage_id||'', input_text_chars:r.input_text_chars||0}; }
function apiRequestBodies(events, runID, provider){
  return diagnosticEvents(events, runID, 'session.diagnostic.provider.api.request').filter(d=>diagnosticPayload(d).provider===provider || d.provider===provider).map(d=>String(diagnosticPayload(d).body||d.body||''));
}
function apiChunks(events, runID, provider){
  return diagnosticEvents(events, runID, 'session.diagnostic.provider.api.stream_chunk').filter(d=>diagnosticPayload(d).provider===provider || d.provider===provider).map(d=>String(diagnosticPayload(d).body||d.body||''));
}
function providerUsageDiagnostics(events, runID, provider){
  return diagnosticEvents(events, runID, 'session.diagnostic.provider.usage').filter(d=>String(diagnosticPayload(d).provider||d.provider||'')===provider);
}
function providerToolConstructionEvents(events, runID){
  return events.filter(e=>String(e.event_type||'').startsWith('session.provider_tool_call.') && (!runID || JSON.stringify(e).includes(runID))).map(e=>{
    const payload=diagnosticPayload(e.payload);
    return {event_type:String(e.event_type||''), payload};
  });
}
function assistantMeta(assistant){ return assistant.metadata||{}; }
function assertProviderUsage(label, events, runID, provider, assistant){
  const diagnostics=providerUsageDiagnostics(events, runID, provider);
  const usage=assistantMeta(assistant).usage||null;
  result.diagnostics[label]={diagnostic_count:diagnostics.length, diagnostic_usage:diagnostics.map(d=>diagnosticPayload(d).usage||null).filter(Boolean).slice(-3), assistant_usage:usage};
  assert(diagnostics.length>0 || usage, `${label} missing ${provider} usage metadata evidence`);
}
function assertProviderToolConstruction(label, events, runID, toolName){
  const toolEvents=providerToolConstructionEvents(events, runID);
  const types=toolEvents.map(e=>e.event_type);
  const toolNames=toolEvents.map(e=>String(e.payload?.tool_name||'')).filter(Boolean);
  result.diagnostics[label]={event_count:toolEvents.length, types, tool_names:toolNames, sample:toolEvents.slice(0,8)};
  assert(types.includes('session.provider_tool_call.started'), `${label} missing provider tool-call started event`);
  assert(types.includes('session.provider_tool_call.completed'), `${label} missing provider tool-call completed event`);
  if(toolName) assert(toolNames.includes(toolName), `${label} missing provider tool-call tool_name=${toolName}; got ${toolNames.join(',')}`);
}
function assertBoundary(label, diag, assistant, want){
  result.diagnostics[label]={request:diag, assistant_metadata:assistantMeta(assistant)};
  if(want.boundary) assert(String(diag.boundary_reason||'').includes(want.boundary),`${label} boundary_reason=${diag.boundary_reason}, want ${want.boundary}`);
  if(want.native !== undefined) assert(diag.native_continuation_allowed===want.native,`${label} native_continuation_allowed=${diag.native_continuation_allowed}`);
  if(want.fresh !== undefined) assert(diag.force_fresh_provider_context===want.fresh,`${label} force_fresh_provider_context=${diag.force_fresh_provider_context}`);
  if(want.previousEqualsCurrent !== undefined){ const equal=String(diag.previous_provider_lineage_id||'')!=='' && diag.previous_provider_lineage_id===diag.provider_lineage_id; assert(equal===want.previousEqualsCurrent,`${label} lineage equality=${equal} diag=${JSON.stringify(diag)}`); }
  if(want.model) assert(String(diag.model||'')===want.model,`${label} model=${diag.model}, want ${want.model}`);
  if(want.thinking) assert(String(diag.thinking||'')===want.thinking,`${label} thinking=${diag.thinking}, want ${want.thinking}`);
}
async function main(){
  result.candidate.hostname=run('hostname',[]).stdout.trim(); result.candidate.repo_path=cfg.repo; result.candidate.expected_commit=cfg.expectedCommit; result.candidate.commit=run('git',['-C',cfg.repo,'rev-parse','HEAD']).stdout.trim(); result.candidate.git_status=run('git',['-C',cfg.repo,'status','--short']).stdout;
  const mergeBase=run('git',['-C',cfg.repo,'merge-base','--is-ancestor',cfg.expectedCommit,'HEAD']); result.candidate.expected_commit_is_ancestor=mergeBase.status===0;
  assert(result.candidate.commit,'missing remote git commit'); assert(mergeBase.status===0,`deployed commit ${result.candidate.commit} does not contain expected ${cfg.expectedCommit}`); assert(result.candidate.git_status.trim()==='',`remote git status not clean: ${result.candidate.git_status}`); result.gates.candidate=true;
  const env=run('bash',['-lc',`python3 - <<'PY'
import pathlib, subprocess
pid = subprocess.check_output(['pgrep', '-n', 'swarmd'], text=True).strip()
for item in pathlib.Path('/proc', pid, 'environ').read_bytes().split(b'\\0'):
    text = item.decode('utf-8', 'replace')
    if text.startswith('SWARM_V3_DIAGNOSTICS=') or text.startswith('SWARM_PROVIDER_API_DIAGNOSTICS='):
        print(text)
PY`]); result.candidate.diagnostics_env=env.stdout; assert(env.stdout.includes('SWARM_V3_DIAGNOSTICS=1') && env.stdout.includes('SWARM_PROVIDER_API_DIAGNOSTICS=1'),`diagnostics env not enabled on running swarmd: ${env.stdout}`); result.gates.diagnostics_enabled=true;
  const token=await authDesktop(); const agentName=await ensureE2EAgent(token); const exitAgentName=await ensureExitPlanAgent(token); fs.mkdirSync(result.workspace,{recursive:true}); const topo=(await api('GET','/v1/swarm/topology',token,undefined,'topology')).body; const runtime=(topo.runtimes||[]).find(r=>r.relationship==='self')||(topo.runtimes||[])[0]; assert(runtime?.swarm_id,'missing self runtime'); const w=(await api('POST','/v1/workspace/add',token,{path:result.workspace,name:cfg.e2eID,make_current:false},'workspace.add')).body; const binding=w.local_workspace_binding_id; assert(binding,'workspace add missing binding'); result.ids.workspace_binding_id=binding; result.ids.runtime_swarm_id=runtime.swarm_id; const sessionID=await createSession(token,binding,runtime.swarm_id,agentName,{label:'boundary'}); result.ids.session_id=sessionID; result.gates.workspace=true;
  const huge=`STALE_BOUNDARY_SENTINEL_${cfg.e2eID}_` + 'x'.repeat(120000);
  const a1=await runTurn(token,sessionID,'codex_a1','codex',cfg.codexModelA,`Codex first provider boundary turn ${cfg.e2eID}. Reply exactly CODEX_A1_OK. ${huge}`);
  let events=await fetchEvents(token,sessionID,'after_a1'); let d1=requestDiag(events,result.ids.codex_a1_run_id); assertBoundary('codex_a1',d1,a1,{boundary:'epoch_fresh_context',native:false,fresh:true});
  const a2=await runTurn(token,sessionID,'codex_a2','codex',cfg.codexModelA,`Codex same model continuation ${cfg.e2eID}. Reply exactly CODEX_A2_OK.`);
  events=await fetchEvents(token,sessionID,'after_a2'); let d2=requestDiag(events,result.ids.codex_a2_run_id); assertBoundary('codex_a2',d2,a2,{boundary:'session_turn',native:true,fresh:false,previousEqualsCurrent:true}); assertProviderUsage('codex_usage',events,'','codex',a2); result.gates.codex_same_lineage=true; result.gates.codex_usage=true;
  const b=await runTurn(token,sessionID,'codex_b','codex',cfg.codexModelB,`Codex model switch boundary ${cfg.e2eID}. Reply exactly CODEX_B_OK.`);
  events=await fetchEvents(token,sessionID,'after_b'); let db=requestDiag(events,result.ids.codex_b_run_id); assertBoundary('codex_model_switch',db,b,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false});
  assert(Number(db.input_text_chars||0) < 50000,`Codex handoff input too large: ${db.input_text_chars}`);
  for (const body of apiRequestBodies(events,result.ids.codex_b_run_id,'codex')) { assert(!body.includes('x'.repeat(20000)), 'Codex handoff API request replayed huge stale transcript body'); }
  result.gates.codex_model_handoff=true;
  const f=await runTurn(token,sessionID,'fireworks','fireworks',cfg.fireworksModel,`Fireworks provider switch boundary ${cfg.e2eID}. Reply exactly FIREWORKS_BOUNDARY_OK.`,cfg.fireworksThinking);
  events=await fetchEvents(token,sessionID,'after_fireworks'); let df=requestDiag(events,result.ids.fireworks_run_id); assertBoundary('codex_to_fireworks',df,f,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false});
  assert(Number(df.input_text_chars||0) < 50000,`Fireworks handoff input too large: ${df.input_text_chars}`);
  const fireworksRequests=apiRequestBodies(events,result.ids.fireworks_run_id,'fireworks'); assert(fireworksRequests.length>0,'missing Fireworks API request diagnostic');
  for (const body of fireworksRequests) { assert(!body.includes('x'.repeat(20000)), 'Fireworks handoff API request replayed huge stale transcript body'); }
  result.gates.codex_to_fireworks_handoff=true;
  const deepseekSessionID=await createSession(token,binding,runtime.swarm_id,agentName,{label:'codex55-fireworks-deepseek',preference:{provider:'codex',model:cfg.codexModelA,thinking:'high'}}); result.ids.codex55_deepseek_session_id=deepseekSessionID;
  const deepseekHuge=`STALE_DEEPSEEK_SENTINEL_${cfg.e2eID}_` + 'y'.repeat(12000);
  await runTurn(token,deepseekSessionID,'deepseek_codex55_seed','codex',cfg.codexModelA,`Codex gpt-5.5 seed before Fireworks DeepSeek ${cfg.e2eID}. Reply exactly DEEPSEEK_CODEX_SEED_OK. ${deepseekHuge}`);
  const deepseekF=await runTurn(token,deepseekSessionID,'deepseek_fireworks','fireworks',cfg.fireworksModel,`Fireworks DeepSeek handoff from Codex gpt-5.5 ${cfg.e2eID}. Reply exactly DEEPSEEK_FIREWORKS_HANDOFF_OK.`,cfg.fireworksThinking);
  let deepseekEvents=await fetchEvents(token,deepseekSessionID,'after_deepseek_fireworks'); let dDeepseek=requestDiag(deepseekEvents,result.ids.deepseek_fireworks_run_id); assertBoundary('codex55_to_fireworks_deepseek',dDeepseek,deepseekF,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false,model:cfg.fireworksModel}); assert(Number(dDeepseek.input_text_chars||0)<50000,`Codex 5.5 to Fireworks DeepSeek input too large: ${dDeepseek.input_text_chars}`); for (const body of apiRequestBodies(deepseekEvents,result.ids.deepseek_fireworks_run_id,'fireworks')) { assert(!body.includes('y'.repeat(8000)), 'Codex 5.5 to Fireworks DeepSeek request replayed stale sentinel body'); } result.gates.codex55_to_fireworks_deepseek=true;

  const exitSessionID=await createSession(token,binding,runtime.swarm_id,exitAgentName,{label:'exit-plan-splice',mode:'plan',preference:{provider:'codex',model:cfg.codexPlanModel,thinking:cfg.codexPlanThinking}}); result.ids.exit_session_id=exitSessionID;
  const exitHuge=`STALE_EXIT_PLAN_SENTINEL_${cfg.e2eID}_` + 'z'.repeat(90000);
  const exitRunID=await postMessage(token,exitSessionID,'exit_plan',`Plan mode live splice E2E ${cfg.e2eID}. This pre-splice sentinel must not be replayed after the checkpoint splice: ${exitHuge}\nCreate exactly one checkpoint with id cp-exit-splice whose task is to reply EXIT_CHECKPOINT_OK and then complete it with plan_manage. Submit it now with exit_plan_mode.`);
  const exitPerm=await allowExitPlanModeOrObserveTransition(token,exitSessionID,exitRunID); result.ids.exit_plan_permission_id=exitPerm?.id||''; await waitForSessionMode(token,exitSessionID,'auto','after_exit_plan_mode'); const reviewPlan=await waitForPlanReview(token,exitSessionID,'exit_checkpoint');
  let exitEvents=await fetchEvents(token,exitSessionID,'after_exit_checkpoint'); assertProviderToolConstruction('codex_exit_plan_tool_construction',exitEvents,exitRunID,'exit_plan_mode'); const exitCheckpointCurrentRunID=String(reviewPlan.document?.execution_state?.current_run_id||''); const cpRecord=requestRecordBy(exitEvents,(request,record)=>recordRunID(record)===exitCheckpointCurrentRunID,'completed exit checkpoint run'); const exitCheckpointRunID=recordRunID(cpRecord); result.ids.exit_checkpoint_run_id=exitCheckpointRunID; const dExitCp=cpRecord.request; assertBoundary('exit_plan_mode_splice',dExitCp,{metadata:{}},{boundary:'session_turn',native:true,fresh:false,previousEqualsCurrent:true}); assert(Number(dExitCp.input_text_chars||0)<50000,`exit checkpoint input too large: ${dExitCp.input_text_chars}`); for (const body of apiRequestBodies(exitEvents,exitCheckpointRunID,'codex')) { assert(!body.includes('z'.repeat(20000)), 'exit checkpoint request replayed stale pre-splice transcript body'); } result.diagnostics.exit_plan_review={execution_state:reviewPlan.document?.execution_state||null,checkpoint_report:(reviewPlan.document?.checkpoints||[]).find(cp=>cp.id==='cp-exit-splice')?.report||''}; result.gates.exit_plan_mode_splice=true;
  const postRunID=await postMessage(token,exitSessionID,'post_checkpoint_resume',`Post-checkpoint resume ${cfg.e2eID}. Reply exactly POST_CHECKPOINT_RESUME_OK and do not mention the stale sentinel.`); const postAssistant=await waitForAssistant(token,exitSessionID,postRunID,'post_checkpoint_resume',600000); exitEvents=await fetchEvents(token,exitSessionID,'after_post_checkpoint_resume'); const dPost=requestDiag(exitEvents,postRunID); assertBoundary('post_checkpoint_resume',dPost,postAssistant,{boundary:'epoch_fresh_context',native:false,fresh:true,previousEqualsCurrent:false}); assert(dPost.provider_lineage_id!==dExitCp.provider_lineage_id,`post-checkpoint fresh epoch reused checkpoint lineage ${dPost.provider_lineage_id}`); assert(Number(dPost.input_text_chars||0)<50000,`post-checkpoint resume input too large: ${dPost.input_text_chars}`); assert(!JSON.stringify(dPost).includes('STALE_EXIT_PLAN_SENTINEL'), 'post-checkpoint diagnostic referenced stale sentinel'); for (const body of apiRequestBodies(exitEvents,postRunID,'codex')) { assert(!body.includes('z'.repeat(20000)), 'post-checkpoint user message replayed stale pre-splice transcript body'); } result.gates.post_checkpoint_resume=true;

  const boundaryRunID=await postMessage(token,exitSessionID,'post_final_new_checkpoint',`Create one new ordered checkpoint after the completed final handoff for ${cfg.e2eID}. Call plan_manage transition_checkpoint_boundary now with title Second boundary proof, task Complete the checkpoint with report SECOND_CHECKPOINT_OK, and acceptance criterion The second checkpoint reaches its final handoff. Do not answer this request as an ordinary assistant message.`);
  const secondReviewPlan=await waitForPlanReview(token,exitSessionID,'post_final_new_checkpoint',600000,2);
  const secondCheckpoints=secondReviewPlan.document?.checkpoints||[];
  const secondCheckpoint=secondCheckpoints.at(-1)||{};
  assert(secondCheckpoints.length===2,`new-checkpoint flow produced ${secondCheckpoints.length} checkpoints, want exactly 2`);
  assert(secondCheckpoint.status==='completed',`second checkpoint did not complete: ${JSON.stringify(secondCheckpoint)}`);
  const replay=(await api('GET',`/v3/sessions/${encodeURIComponent(exitSessionID)}/events?after_seq=0&limit=1000`,token,undefined,'replay.post_final_new_checkpoint',false,60000)).body;
  const intents=replay.run_intents||[];
  const parentIntent=intents.find(intent=>String(intent.run_id||'')===boundaryRunID);
  const childIntent=intents.find(intent=>String(intent.run_id||'')===String(secondCheckpoint.run_id||''));
  assert(parentIntent?.run_id===boundaryRunID,`new-checkpoint parent run missing: ${JSON.stringify(parentIntent)}`);
  assert(childIntent?.run_id,`new-checkpoint child run missing: ${JSON.stringify(childIntent)}`);
  assert(String(parentIntent.run_id||'')===String(childIntent.run_id||''),'new-checkpoint boundary did not stay on the trusted parent run');
  assert(String(parentIntent.epoch_id||'')!=='' && parentIntent.epoch_id===childIntent.epoch_id,`new-checkpoint run epoch mismatch: parent=${parentIntent.epoch_id} child=${childIntent.epoch_id}`);
  assert(String(childIntent.source_message_id||'').startsWith('v3msg_'),`new-checkpoint child lost canonical source message: ${JSON.stringify(childIntent)}`);
  const durableMessages=replay.messages||[];
  const secondHandoff=secondCheckpoint.handoff||{};
  assert(String(secondCheckpoint.report||secondCheckpoint.result||'').includes('SECOND_CHECKPOINT_OK'),`second checkpoint terminal evidence missing SECOND_CHECKPOINT_OK: ${JSON.stringify(secondCheckpoint)}`);
  const durableEvidence=JSON.stringify({events:replay.events||[],run_intents:intents,messages:durableMessages});
  assert(!durableEvidence.includes('cannot claim or update'),'validated durable flow contains completed-parent claim/update failure');
  assert(!durableEvidence.includes('provider returned empty assistant response'),'validated durable flow contains empty assistant response failure');
  result.ids.post_final_boundary_run_id=boundaryRunID;
  result.ids.post_final_checkpoint_id=secondCheckpoint.id||'';
  result.ids.post_final_checkpoint_run_id=childIntent.run_id||'';
  result.diagnostics.post_final_new_checkpoint={execution_state:secondReviewPlan.document?.execution_state||null,parent_run:parentIntent,child_run:childIntent,checkpoint_handoff:secondHandoff,checkpoint_count:secondCheckpoints.length};
  result.gates.post_final_new_checkpoint=true;

  const workflowAgentName=await ensureWorkflowAgent(token);
  const workflowRepo=`${cfg.workspaceDir}-delegated-workflows`;
  initializeWorkflowRepository(workflowRepo);
  const workflowWorkspace=(await api('POST','/v1/workspace/add',token,{path:workflowRepo,name:`${cfg.e2eID}-delegated-workflows`,make_current:false},'workspace.workflow.add')).body;
  const workflowBinding=workflowWorkspace.local_workspace_binding_id;
  assert(workflowBinding,'workflow workspace add missing binding');
  result.ids.workflow_workspace_binding_id=workflowBinding;

  const taskProgramSessionID=await createSession(token,workflowBinding,runtime.swarm_id,workflowAgentName,{label:'task-program-worktrees',workspacePath:workflowRepo,workspaceName:`${cfg.e2eID}-delegated-workflows`,worktree:true,worktreeBranch:`agent/${cfg.e2eID}-task-program`});
  result.ids.task_program_session_id=taskProgramSessionID;
  const taskProgramDoc={id:`task-program-plan-${cfg.e2eID}`,title:`${cfg.e2eID} Task Program worktree workflow`,info:{goal:'Prove a staged Task Program can manage and integrate multiple Coder worktrees.'},execution_policy:{mode:'automatic',shape:'checkpointed'},active_checkpoint_id:'cp-task-program',checkpoints:[{id:'cp-task-program',title:'Task Program worktree integration',status:'pending',tasks:['Start the approved task_program with task action=start and no program argument.','After the Task Program scheduler atomically integrates the stage, call manage_worktree recall for its task_call_id to confirm durable child lineage, verify program-a.txt and program-b.txt in this parent session worktree, and complete this checkpoint with report TASK_PROGRAM_WORKTREES_OK. Do not attempt a duplicate integration.'],acceptance_criteria:['Two Coder jobs commit independent files in separate worktrees.','The Task Program scheduler integrates the complete stage into the clean parent session worktree and retains manageable durable lineage.'],task_program:{id:`workflow-program-${cfg.e2eID}`,max_concurrency:2,stages:[{id:'parallel',dependency_evidence:'The two file outputs are independent.'}],jobs:[{id:'program-a',stage_id:'parallel',agent_type:'coder',title:'Program File A',meta_prompt:'Create program-a.txt containing exactly TASK_PROGRAM_A_OK followed by a newline. Commit the change and finish with a clean worktree.',deliverable:'Committed program-a.txt',owned_scope:['program-a.txt'],dependency_evidence:'Independent file output.',acceptance_criteria:['program-a.txt contains TASK_PROGRAM_A_OK and is committed.']},{id:'program-b',stage_id:'parallel',agent_type:'coder',title:'Program File B',meta_prompt:'Create program-b.txt containing exactly TASK_PROGRAM_B_OK followed by a newline. Commit the change and finish with a clean worktree.',deliverable:'Committed program-b.txt',owned_scope:['program-b.txt'],dependency_evidence:'Independent file output.',acceptance_criteria:['program-b.txt contains TASK_PROGRAM_B_OK and is committed.']}]}}]};
  await saveAndStartWorkflowPlan(token,taskProgramSessionID,'task_program_worktrees',taskProgramDoc);
  const taskProgramReview=await waitForPlanReview(token,taskProgramSessionID,'task_program_worktrees',1200000,1,['task','bash','read','manage_worktree'],['git_commit']);
  const taskProgramChildren=await delegatedChildren(token,taskProgramSessionID,'task_program_worktrees.final');
  assert(taskProgramChildren.length===2,`Task Program created ${taskProgramChildren.length} delegated children, want 2`);
  assert(new Set(taskProgramChildren.map(child=>child.worktree_root_path)).size===2,`Task Program children did not receive distinct worktrees: ${JSON.stringify(taskProgramChildren)}`);
  assert(new Set(taskProgramChildren.map(child=>child.metadata?.assignment_label||'')).size===2 && taskProgramChildren.every(child=>child.metadata?.lineage_kind==='delegated_subagent' && child.metadata?.requested_subagent==='coder'),`Task Program child lineage metadata is incomplete: ${JSON.stringify(taskProgramChildren)}`);
  for(const child of taskProgramChildren){ assert(child.worktree_enabled===true && child.worktree_root_path && child.worktree_branch,`Task Program child has incomplete worktree lineage: ${JSON.stringify(child)}`); if(fs.existsSync(child.worktree_root_path)) assertCleanGitWorktree(child.worktree_root_path,`Task Program child ${child.id}`); }
  const taskProgramParent=await getSessionView(token,taskProgramSessionID,'task_program_worktrees.parent');
  assertCleanGitWorktree(taskProgramParent.worktree_root_path,'Task Program parent');
  assert(fs.readFileSync(`${taskProgramParent.worktree_root_path}/program-a.txt`,'utf8').trim()==='TASK_PROGRAM_A_OK','Task Program parent missing integrated program-a.txt');
  assert(fs.readFileSync(`${taskProgramParent.worktree_root_path}/program-b.txt`,'utf8').trim()==='TASK_PROGRAM_B_OK','Task Program parent missing integrated program-b.txt');
  result.diagnostics.task_program_multi_worktree={execution_state:taskProgramReview.document?.execution_state||null,parent:{session_id:taskProgramSessionID,worktree_root_path:taskProgramParent.worktree_root_path,worktree_branch:taskProgramParent.worktree_branch},children:taskProgramChildren.map(child=>({session_id:child.id,program_id:child.metadata?.task_program_id||'',job_id:child.metadata?.task_program_job_id||'',worktree_root_path:child.worktree_root_path,worktree_branch:child.worktree_branch}))};
  result.gates.task_program_multi_worktree=true;

  const taskProgramBoundaryRunID=await postMessage(token,taskProgramSessionID,'task_program_second_boundary',`Create one new ordered checkpoint after the Task Program handoff for ${cfg.e2eID}. Call plan_manage transition_checkpoint_boundary now with title Task Program second boundary. Verify program-a.txt and program-b.txt still contain their acceptance tokens, confirm the current parent worktree remains clean and on the same captured worktree branch, then complete with report TASK_PROGRAM_SECOND_BOUNDARY_OK. Do not answer as an ordinary assistant message.`);
  const taskProgramSecondReview=await waitForPlanReview(token,taskProgramSessionID,'task_program_second_boundary',600000,2,['bash','read'],[]);
  const taskProgramAfterBoundary=await getSessionView(token,taskProgramSessionID,'task_program_second_boundary.parent');
  assert(taskProgramAfterBoundary.worktree_root_path===taskProgramParent.worktree_root_path && taskProgramAfterBoundary.worktree_branch===taskProgramParent.worktree_branch,'Task Program second boundary changed parent worktree lineage');
  assert(fs.readFileSync(`${taskProgramAfterBoundary.worktree_root_path}/program-a.txt`,'utf8').trim()==='TASK_PROGRAM_A_OK' && fs.readFileSync(`${taskProgramAfterBoundary.worktree_root_path}/program-b.txt`,'utf8').trim()==='TASK_PROGRAM_B_OK','Task Program output did not survive second checkpoint boundary');
  assertCleanGitWorktree(taskProgramAfterBoundary.worktree_root_path,'Task Program parent after second boundary');
  result.ids.task_program_second_boundary_run_id=taskProgramBoundaryRunID;
  result.diagnostics.task_program_second_boundary={execution_state:taskProgramSecondReview.document?.execution_state||null,checkpoint_count:(taskProgramSecondReview.document?.checkpoints||[]).length,parent_worktree_root_path:taskProgramAfterBoundary.worktree_root_path,parent_worktree_branch:taskProgramAfterBoundary.worktree_branch};
  result.gates.task_program_second_boundary=true;

  const individualSessionID=await createSession(token,workflowBinding,runtime.swarm_id,workflowAgentName,{label:'individual-coder-worktree',workspacePath:workflowRepo,workspaceName:`${cfg.e2eID}-delegated-workflows`,worktree:true,worktreeBranch:`agent/${cfg.e2eID}-individual-coder`});
  result.ids.individual_coder_session_id=individualSessionID;
  const individualDoc={id:`individual-coder-plan-${cfg.e2eID}`,title:`${cfg.e2eID} individual Coder worktree workflow`,info:{goal:'Prove an individual Coder worktree remains manageable across integration and checkpoint boundaries.'},execution_policy:{mode:'automatic',shape:'checkpointed'},active_checkpoint_id:'cp-individual-coder',checkpoints:[{id:'cp-individual-coder',title:'Individual Coder worktree integration',status:'pending',tasks:['Launch exactly one regular Coder with owned_scope individual-coder.txt. Instruct it to write exactly INDIVIDUAL_CODER_OK followed by a newline, commit, and finish clean.','After it succeeds, call manage_worktree recall for that child session, integrate it into this parent session worktree, verify individual-coder.txt, and complete this checkpoint with report INDIVIDUAL_CODER_WORKTREE_OK.'],acceptance_criteria:['The individual Coder has a distinct clean committed worktree.','Its change is integrated into the clean parent session worktree.']} ]};
  await saveAndStartWorkflowPlan(token,individualSessionID,'individual_coder_worktree',individualDoc);
  const individualReview=await waitForPlanReview(token,individualSessionID,'individual_coder_worktree',1200000,1,['task','bash','read','manage_worktree'],['git_commit']);
  const individualChildren=await delegatedChildren(token,individualSessionID,'individual_coder_worktree.final');
  assert(individualChildren.length===1,`individual Coder workflow created ${individualChildren.length} children, want 1`);
  const individualChild=individualChildren[0];
  assert(!individualChild.metadata?.task_program_id && !individualChild.metadata?.task_program_job_id,`individual Coder was incorrectly recorded as Task Program work: ${JSON.stringify(individualChild.metadata)}`);
  assert(individualChild.worktree_enabled===true && individualChild.worktree_root_path && individualChild.worktree_branch,`individual Coder has incomplete worktree lineage: ${JSON.stringify(individualChild)}`);
  assertCleanGitWorktree(individualChild.worktree_root_path,'individual Coder child');
  const individualParent=await getSessionView(token,individualSessionID,'individual_coder_worktree.parent');
  assertCleanGitWorktree(individualParent.worktree_root_path,'individual Coder parent');
  assert(fs.readFileSync(`${individualParent.worktree_root_path}/individual-coder.txt`,'utf8').trim()==='INDIVIDUAL_CODER_OK','individual Coder parent missing integrated output');
  result.diagnostics.individual_coder_worktree={execution_state:individualReview.document?.execution_state||null,parent:{session_id:individualSessionID,worktree_root_path:individualParent.worktree_root_path,worktree_branch:individualParent.worktree_branch},child:{session_id:individualChild.id,worktree_root_path:individualChild.worktree_root_path,worktree_branch:individualChild.worktree_branch}};
  result.gates.individual_coder_worktree=true;

  const individualBoundaryRunID=await postMessage(token,individualSessionID,'individual_coder_second_boundary',`Create one new ordered checkpoint after the individual Coder handoff for ${cfg.e2eID}. Call plan_manage transition_checkpoint_boundary now with title Individual Coder second boundary. Verify individual-coder.txt still contains INDIVIDUAL_CODER_OK, confirm the current parent worktree remains clean and on the same captured worktree branch, then complete with report INDIVIDUAL_CODER_SECOND_BOUNDARY_OK. Do not answer as an ordinary assistant message.`);
  const individualSecondReview=await waitForPlanReview(token,individualSessionID,'individual_coder_second_boundary',600000,2,['bash','read'],[]);
  const individualAfterBoundary=await getSessionView(token,individualSessionID,'individual_coder_second_boundary.parent');
  assert(individualAfterBoundary.worktree_root_path===individualParent.worktree_root_path && individualAfterBoundary.worktree_branch===individualParent.worktree_branch,'individual Coder second boundary changed parent worktree lineage');
  assert(fs.readFileSync(`${individualAfterBoundary.worktree_root_path}/individual-coder.txt`,'utf8').trim()==='INDIVIDUAL_CODER_OK','individual Coder output did not survive second checkpoint boundary');
  assertCleanGitWorktree(individualAfterBoundary.worktree_root_path,'individual Coder parent after second boundary');
  result.ids.individual_coder_second_boundary_run_id=individualBoundaryRunID;
  result.diagnostics.individual_coder_second_boundary={execution_state:individualSecondReview.document?.execution_state||null,checkpoint_count:(individualSecondReview.document?.checkpoints||[]).length,parent_worktree_root_path:individualAfterBoundary.worktree_root_path,parent_worktree_branch:individualAfterBoundary.worktree_branch};
  result.gates.individual_coder_second_boundary=true;

  const doc={id:`plan-${cfg.e2eID}`,title:`${cfg.e2eID} checkpoint plan`,info:{goal:'E2E checkpoint fresh context boundary'},execution_policy:{mode:'automatic',shape:'checkpointed'},active_checkpoint_id:'cp-boundary',checkpoints:[{id:'cp-boundary',title:'checkpoint provider boundary',status:'pending',tasks:['Call plan_manage complete_checkpoint for cp-boundary with report CHECKPOINT_BOUNDARY_OK, then keep any final assistant response brief.'],acceptance_criteria:['Fresh context run completes','Fireworks provider tool-call construction events are durable']} ]};
  await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plans`,token,{plan_id:doc.id,title:doc.title,document:doc,status:'approved',approval_state:'approved',activate:true},'plan.save');
  const start=(await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plan-mode/checkpoints/cp-boundary/start`,token,{plan_id:doc.id},'checkpoint.start',false,600000)).body; const checkpointRunID=start.run_start?.run_intent?.run_id||start.run_intent?.run_id||''; assert(checkpointRunID,'checkpoint start missing run_id'); result.ids.checkpoint_run_id=checkpointRunID; const checkpointReview=await waitForPlanReview(token,sessionID,'checkpoint',600000,1,['plan_manage'],[]); const cp=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`,token,undefined,'tail.checkpoint.final',false,60000)).body.messages?.filter(m=>m.role==='assistant').at(-1)||{metadata:{}};
  events=await fetchEvents(token,sessionID,'after_checkpoint'); let dcp=requestDiag(events,checkpointRunID); assertBoundary('checkpoint_fresh_context',dcp,cp,{boundary:'session_turn',native:false,fresh:true}); result.diagnostics.checkpoint_review=checkpointReview.document?.execution_state||null; assert(Number(dcp.input_text_chars||0) < 50000,`checkpoint input too large: ${dcp.input_text_chars}`); for (const body of apiRequestBodies(events,checkpointRunID,'fireworks')) { assert(!body.includes('x'.repeat(20000)), 'Checkpoint API request replayed huge stale transcript body'); }
  assertProviderToolConstruction('fireworks_checkpoint_tool_construction',events,checkpointRunID,'plan_manage'); result.gates.provider_tool_construction=true;
  result.gates.checkpoint_fresh_context=true;
  const usageEvents=providerUsageDiagnostics(events,result.ids.fireworks_run_id,'fireworks').concat(providerUsageDiagnostics(events,checkpointRunID,'fireworks'));
  const usageBodies=apiChunks(events,result.ids.fireworks_run_id,'fireworks').concat(apiChunks(events,checkpointRunID,'fireworks'));
  result.diagnostics.fireworks_usage={usage_event_count:usageEvents.length, usage_samples:usageEvents.map(d=>diagnosticPayload(d).usage||null).filter(Boolean).slice(-3), stream_chunk_usage_seen:usageBodies.some(b=>b.includes('"usage"') && !b.includes('"usage":null')), assistant_usage:f.metadata?.usage||cp.metadata?.usage||null};
  assert(result.diagnostics.fireworks_usage.stream_chunk_usage_seen || usageEvents.length>0 || result.diagnostics.fireworks_usage.assistant_usage, 'Fireworks stream usage/token evidence missing'); result.gates.fireworks_usage=true;
  const logs=run('bash',['-lc',`journalctl -u ${cfg.serviceUnit} --since '${startedISO}' --no-pager | grep -E 'panic|websocket error|provider boundary e2e fatal|cannot claim or update|provider returned empty assistant response' || true`]); fs.writeFileSync(`${artifactDir}/journal-matches.log`, logs.stdout+logs.stderr); result.logs={matches_path:`${artifactDir}/journal-matches.log`, forbidden_matches:(logs.stdout||'').split('\n').filter(Boolean)}; assert(result.logs.forbidden_matches.length===0,`unexpected service log matches: ${result.logs.forbidden_matches.slice(0,10).join(' | ')}`); result.gates.logs=true;
  refreshSummary(); result.result=result.failed_gates.length?'NOT_DONE':'PASS'; save();
}
main().catch(err=>{ result.result='NOT_DONE'; result.error=err?.stack||String(err); save(); console.error(result.error); process.exitCode=2; }).finally(()=>{ console.log(JSON.stringify(result,null,2)); });
NODE

node --check "${RUNNER_LOCAL}"
CONFIG_LOCAL="${LOCAL_DIR}/config.json"
cat >"${CONFIG_LOCAL}" <<JSON
{"apiURL":"${API_URL}","serviceUnit":"${SERVICE_UNIT}","repo":"${REMOTE_REPO}","e2eID":"${E2E_ID}","artifactDir":"${REMOTE_DIR}","workspaceDir":"${WORKSPACE_DIR}","expectedCommit":"${EXPECTED_COMMIT}","codexModelA":"${CODEX_MODEL_A}","codexModelB":"${CODEX_MODEL_B}","codexPlanModel":"${CODEX_PLAN_MODEL}","codexAutoModel":"${CODEX_AUTO_MODEL}","codexPlanThinking":"${CODEX_PLAN_THINKING}","codexAutoThinking":"${CODEX_AUTO_THINKING}","fireworksModel":"${FIREWORKS_MODEL}","fireworksThinking":"${FIREWORKS_THINKING}"}
JSON

ssh "${SSH_HOST}" "mkdir -p '${REMOTE_DIR}'"
scp "${RUNNER_LOCAL}" "${CONFIG_LOCAL}" "${SSH_HOST}:${REMOTE_DIR}/" >/dev/null
set +e
ssh "${SSH_HOST}" "SWARM_E2E_CONFIG='${REMOTE_DIR}/config.json' node '${REMOTE_DIR}/runner.mjs'" | tee "${LOCAL_DIR}/runner-output.log"
status=${PIPESTATUS[0]}
set -e
scp -r "${SSH_HOST}:${REMOTE_DIR}/"* "${LOCAL_DIR}/" >/dev/null || true
if [[ -f "${LOCAL_DIR}/summary.json" ]]; then
  jq -r '.result' "${LOCAL_DIR}/summary.json"
fi
exit "${status}"
