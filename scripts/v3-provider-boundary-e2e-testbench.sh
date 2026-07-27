#!/usr/bin/env bash
set -euo pipefail

SSH_HOST="${1:-${SWARM_PRIMARY_SSH:-testbench}}"
API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
EXPECTED_COMMIT="${SWARM_EXPECTED_COMMIT:-}"
REMOTE_REPO="${SWARM_REMOTE_REPO:-}"
if [[ -z "${REMOTE_REPO}" ]]; then
  echo "SWARM_REMOTE_REPO must point to the candidate repository on ${SSH_HOST}" >&2
  exit 2
fi
if [[ -z "${EXPECTED_COMMIT}" ]]; then
  echo "SWARM_EXPECTED_COMMIT must name the backend fix commit expected in the deployed candidate" >&2
  exit 2
fi
CODEX_MODEL_A="${SWARM_E2E_CODEX_MODEL_A:-gpt-5.5}"
CODEX_MODEL_B="${SWARM_E2E_CODEX_MODEL_B:-gpt-5.4}"
CODEX_PLAN_MODEL="${SWARM_E2E_CODEX_PLAN_MODEL:-${CODEX_MODEL_A}}"
CODEX_AUTO_MODEL="${SWARM_E2E_CODEX_AUTO_MODEL:-${CODEX_MODEL_A}}"
CODEX_PLAN_THINKING="${SWARM_E2E_CODEX_PLAN_THINKING:-xhigh}"
CODEX_AUTO_THINKING="${SWARM_E2E_CODEX_AUTO_THINKING:-high}"
FIREWORKS_MODEL="${SWARM_E2E_FIREWORKS_MODEL:-accounts/fireworks/models/glm-5p2}"
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
const requiredGates=['candidate','diagnostics_enabled','workspace','codex_same_lineage','codex_model_handoff','codex_to_fireworks_handoff','codex55_to_fireworks_glm','exit_plan_mode_splice','post_checkpoint_resume','checkpoint_fresh_context','codex_usage','fireworks_usage','provider_tool_construction','logs'];
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
  const agentName=`provider-boundary-${cfg.e2eID}`;
  const body={mode:'primary',description:'Temporary provider boundary E2E agent',prompt:'You are a provider boundary E2E agent. Follow the user request exactly and avoid tool calls unless the checkpoint prompt requires plan_manage.',runtime_mode:'plan_auto',exit_plan_mode_enabled:true,enabled:true,tool_contract:{preset:'custom',tools:{plan_manage:{enabled:true},exit_plan_mode:{enabled:true}}}};
  const r=await api('PUT',`/v2/agents/${encodeURIComponent(agentName)}`,token,body,'agent.create');
  assert(r.body?.ok===true,`agent create failed: ${JSON.stringify(r.body)}`);
  result.ids.agent_name=agentName;
  return agentName;
}
async function ensureExitPlanAgent(token){
  const agentName=`provider-boundary-exit-${cfg.e2eID}`;
  const body={mode:'primary',description:'Temporary provider boundary exit-plan E2E agent',prompt:'You are a provider boundary exit-plan E2E agent. When asked to exit plan mode, create the requested one-checkpoint plan and call exit_plan_mode. In checkpoint runs, satisfy the checkpoint with plan_manage and keep the final assistant response brief.',runtime_mode:'plan_auto',exit_plan_mode_enabled:true,model_mode:'split',plan_provider:'codex',plan_model:cfg.codexPlanModel,plan_thinking:cfg.codexPlanThinking,auto_provider:'codex',auto_model:cfg.codexAutoModel,auto_thinking:cfg.codexAutoThinking,enabled:true,tool_contract:{preset:'custom',tools:{plan_manage:{enabled:true},exit_plan_mode:{enabled:true}}}};
  const r=await api('PUT',`/v2/agents/${encodeURIComponent(agentName)}`,token,body,'agent.exit.create');
  assert(r.body?.ok===true,`exit agent create failed: ${JSON.stringify(r.body)}`);
  result.ids.exit_agent_name=agentName;
  return agentName;
}
async function createSession(token,binding,swarm,agentName, opts={}){
  const mode=opts.mode||'auto';
  const pref=opts.preference||{provider:'codex',model:cfg.codexModelA,thinking:'low'};
  const label=opts.label||'create';
  const body={client_request_id:`${cfg.e2eID}:create:${label}:${crypto.randomBytes(3).toString('hex')}`,title:`${cfg.e2eID} ${label} provider boundary`,workspace_path:result.workspace,workspace_name:cfg.e2eID,workspace_binding_id:binding,swarm_id:swarm,target_kind:'host',target_relationship:'self',mode,agent_name:agentName,preference:pref,metadata:{e2e_id:cfg.e2eID,provider_boundary_e2e:true,label}};
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
async function waitForPlanReview(token, sessionID, label, timeoutMs=600000){
  const end=Date.now()+timeoutMs;
  while(Date.now()<end){
    const body=(await api('GET',`/v3/sessions/${encodeURIComponent(sessionID)}/plans/active`,token,undefined,`plan.${label}`,false,60000)).body;
    const plan=body.plan||body.active_plan||body;
    const doc=plan.document||{};
    if(doc.execution_state?.status==='waiting_review' || doc.execution_state?.status==='final_review') return plan;
    await new Promise(r=>setTimeout(r,2000));
  }
  die(`timeout waiting for active plan review ${label}`);
}
async function runTurn(token, sessionID, label, provider, model, content){
  await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/preference`,token,{provider,model,thinking:'low'},`preference.${label}`);
  const runID=await postMessage(token, sessionID, label, content);
  const assistant=await waitForAssistant(token, sessionID, runID, label);
  result.ids[`${label}_assistant_id`]=assistant.id;
  return assistant;
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
  return events.filter(e=>String(e.event_type||'')==='session.diagnostic.provider.request' && (!runID || String(e.run_id||e.runID||'')===runID || JSON.stringify(e).includes(runID))).map(e=>{ const p=diagnosticPayload(e.payload); return {event:e,payload:p,request:p.request||{}}; });
}
function requestDiag(events, runID){ const all=providerRequestRecords(events, runID); assert(all.length>0,`missing provider request diagnostic for ${runID}`); return all.at(-1).request || {}; }
function requestRecordBy(events, predicate, label){ const all=providerRequestRecords(events,'').filter(r=>predicate(r.request,r)); assert(all.length>0,`missing provider request diagnostic matching ${label}`); return all.at(-1); }
function recordRunID(record){ return String(record?.event?.run_id||record?.event?.runID||record?.payload?.run_id||record?.payload?.runID||''); }
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
  let events=await fetchEvents(token,sessionID,'after_a1'); let d1=requestDiag(events,result.ids.codex_a1_run_id); assertBoundary('codex_a1',d1,a1,{boundary:'session_turn',native:false,fresh:true});
  const a2=await runTurn(token,sessionID,'codex_a2','codex',cfg.codexModelA,`Codex same model continuation ${cfg.e2eID}. Reply exactly CODEX_A2_OK.`);
  events=await fetchEvents(token,sessionID,'after_a2'); let d2=requestDiag(events,result.ids.codex_a2_run_id); assertBoundary('codex_a2',d2,a2,{boundary:'session_turn',native:true,fresh:false,previousEqualsCurrent:true}); assertProviderUsage('codex_usage',events,'','codex',a2); result.gates.codex_same_lineage=true; result.gates.codex_usage=true;
  const b=await runTurn(token,sessionID,'codex_b','codex',cfg.codexModelB,`Codex model switch boundary ${cfg.e2eID}. Reply exactly CODEX_B_OK.`);
  events=await fetchEvents(token,sessionID,'after_b'); let db=requestDiag(events,result.ids.codex_b_run_id); assertBoundary('codex_model_switch',db,b,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false});
  assert(Number(db.input_text_chars||0) < 50000,`Codex handoff input too large: ${db.input_text_chars}`);
  for (const body of apiRequestBodies(events,result.ids.codex_b_run_id,'codex')) { assert(!body.includes('x'.repeat(20000)), 'Codex handoff API request replayed huge stale transcript body'); }
  result.gates.codex_model_handoff=true;
  const f=await runTurn(token,sessionID,'fireworks','fireworks',cfg.fireworksModel,`Fireworks provider switch boundary ${cfg.e2eID}. Reply exactly FIREWORKS_BOUNDARY_OK.`);
  events=await fetchEvents(token,sessionID,'after_fireworks'); let df=requestDiag(events,result.ids.fireworks_run_id); assertBoundary('codex_to_fireworks',df,f,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false});
  assert(Number(df.input_text_chars||0) < 50000,`Fireworks handoff input too large: ${df.input_text_chars}`);
  for (const body of apiRequestBodies(events,result.ids.fireworks_run_id,'fireworks')) { assert(body.includes('"stream_options"') && body.includes('"include_usage":true'), 'Fireworks request missing stream_options.include_usage'); assert(!body.includes('x'.repeat(20000)), 'Fireworks handoff API request replayed huge stale transcript body'); }
  result.gates.codex_to_fireworks_handoff=true;
  const glmSessionID=await createSession(token,binding,runtime.swarm_id,agentName,{label:'codex55-fireworks-glm',preference:{provider:'codex',model:cfg.codexModelA,thinking:'high'}}); result.ids.codex55_fireworks_session_id=glmSessionID;
  const glmHuge=`STALE_GLM_SENTINEL_${cfg.e2eID}_` + 'y'.repeat(90000);
  await runTurn(token,glmSessionID,'glm_codex55_seed','codex',cfg.codexModelA,`Codex gpt-5.5 seed before Fireworks GLM ${cfg.e2eID}. Reply exactly GLM_CODEX_SEED_OK. ${glmHuge}`);
  const glmF=await runTurn(token,glmSessionID,'glm_fireworks','fireworks',cfg.fireworksModel,`Fireworks GLM handoff from Codex gpt-5.5 ${cfg.e2eID}. Reply exactly GLM_FIREWORKS_HANDOFF_OK.`);
  let glmEvents=await fetchEvents(token,glmSessionID,'after_glm_fireworks'); let dglm=requestDiag(glmEvents,result.ids.glm_fireworks_run_id); assertBoundary('codex55_to_fireworks_glm',dglm,glmF,{boundary:'provider_model_runtime_handoff',native:false,fresh:true,previousEqualsCurrent:false,model:cfg.fireworksModel}); assert(Number(dglm.input_text_chars||0)<50000,`Codex 5.5 to Fireworks GLM input too large: ${dglm.input_text_chars}`); for (const body of apiRequestBodies(glmEvents,result.ids.glm_fireworks_run_id,'fireworks')) { assert(!body.includes('y'.repeat(20000)), 'Codex 5.5 to Fireworks GLM request replayed stale sentinel body'); } result.gates.codex55_to_fireworks_glm=true;

  const exitSessionID=await createSession(token,binding,runtime.swarm_id,exitAgentName,{label:'exit-plan-splice',mode:'plan',preference:{provider:'codex',model:cfg.codexPlanModel,thinking:cfg.codexPlanThinking}}); result.ids.exit_session_id=exitSessionID;
  const exitHuge=`STALE_EXIT_PLAN_SENTINEL_${cfg.e2eID}_` + 'z'.repeat(90000);
  const exitRunID=await postMessage(token,exitSessionID,'exit_plan',`Plan mode live splice E2E ${cfg.e2eID}. This pre-splice sentinel must not be replayed after the checkpoint splice: ${exitHuge}\nCreate exactly one checkpoint with id cp-exit-splice whose task is to reply EXIT_CHECKPOINT_OK and then complete it with plan_manage. Submit it now with exit_plan_mode.`);
  const exitPerm=await waitForPermission(token,exitSessionID,exitRunID,'exit_plan_mode','exit_plan_mode'); result.ids.exit_plan_permission_id=exitPerm.id; await resolvePermission(token,exitSessionID,exitPerm.id,'exit_plan_mode'); await waitForSessionMode(token,exitSessionID,'auto','after_exit_plan_mode'); const reviewPlan=await waitForPlanReview(token,exitSessionID,'exit_checkpoint');
  let exitEvents=await fetchEvents(token,exitSessionID,'after_exit_checkpoint'); assertProviderToolConstruction('codex_exit_plan_tool_construction',exitEvents,exitRunID,'exit_plan_mode'); const cpRecords=providerRequestRecords(exitEvents,'').filter(r=>String(r.request?.boundary_reason||'').includes('checkpoint_fresh_context') && String(r.request?.model||'')===cfg.codexAutoModel); if(cpRecords.length===0){ const observed=providerRequestRecords(exitEvents,'').map(requestSummary); const staleReplay=apiRequestBodies(exitEvents,'','codex').some(body=>body.includes('z'.repeat(20000))); result.diagnostics.exit_plan_blocker={reason:'missing checkpoint_fresh_context provider request after approved exit_plan_mode', observed_provider_requests:observed, stale_sentinel_in_codex_api_requests:staleReplay, active_plan_execution_state:reviewPlan.document?.execution_state||null}; save(); die(`exit_plan_mode did not create a checkpoint_fresh_context provider request; observed=${JSON.stringify(observed)}`); } const cpRecord=cpRecords.at(-1); const exitCheckpointRunID=recordRunID(cpRecord)||exitRunID; result.ids.exit_checkpoint_run_id=exitCheckpointRunID; const dExitCp=cpRecord.request; assertBoundary('exit_plan_mode_splice',dExitCp,{metadata:{}},{boundary:'checkpoint_fresh_context',native:false,fresh:true,model:cfg.codexAutoModel,thinking:cfg.codexAutoThinking}); assert(Number(dExitCp.input_text_chars||0)<50000,`exit checkpoint input too large: ${dExitCp.input_text_chars}`); for (const body of apiRequestBodies(exitEvents,exitCheckpointRunID,'codex')) { assert(!body.includes('z'.repeat(20000)), 'exit checkpoint request replayed stale pre-splice transcript body'); } const exitAssistant=await waitForAssistant(token,exitSessionID,exitRunID,'exit_checkpoint_final',600000); result.ids.exit_checkpoint_assistant_id=exitAssistant.id||''; result.diagnostics.exit_plan_review={execution_state:reviewPlan.document?.execution_state||null}; result.gates.exit_plan_mode_splice=true;
  const postRunID=await postMessage(token,exitSessionID,'post_checkpoint_resume',`Post-checkpoint resume ${cfg.e2eID}. Reply exactly POST_CHECKPOINT_RESUME_OK and do not mention the stale sentinel.`); const postAssistant=await waitForAssistant(token,exitSessionID,postRunID,'post_checkpoint_resume',600000); exitEvents=await fetchEvents(token,exitSessionID,'after_post_checkpoint_resume'); const dPost=requestDiag(exitEvents,postRunID); assertBoundary('post_checkpoint_resume',dPost,postAssistant,{boundary:'session_turn',native:true,fresh:false,previousEqualsCurrent:true,model:cfg.codexAutoModel,thinking:cfg.codexAutoThinking}); assert(dPost.provider_lineage_id===dExitCp.provider_lineage_id,`post-checkpoint lineage ${dPost.provider_lineage_id} does not match checkpoint splice lineage ${dExitCp.provider_lineage_id}`); assert(Number(dPost.input_text_chars||0)<50000,`post-checkpoint resume input too large: ${dPost.input_text_chars}`); assert(!JSON.stringify(dPost).includes('STALE_EXIT_PLAN_SENTINEL'), 'post-checkpoint diagnostic referenced stale sentinel'); for (const body of apiRequestBodies(exitEvents,postRunID,'codex')) { assert(!body.includes('z'.repeat(20000)), 'post-checkpoint user message replayed stale pre-splice transcript body'); } result.gates.post_checkpoint_resume=true;

  const doc={id:`plan-${cfg.e2eID}`,title:`${cfg.e2eID} checkpoint plan`,info:{goal:'E2E checkpoint fresh context boundary'},execution_policy:{mode:'automatic',shape:'checkpointed'},active_checkpoint_id:'cp-boundary',checkpoints:[{id:'cp-boundary',title:'checkpoint provider boundary',status:'pending',tasks:['Call plan_manage complete_checkpoint for cp-boundary with report CHECKPOINT_BOUNDARY_OK, then keep any final assistant response brief.'],acceptance_criteria:['Fresh context run completes','Fireworks provider tool-call construction events are durable']} ]};
  await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plans`,token,{plan_id:doc.id,title:doc.title,document:doc,status:'approved',approval_state:'approved',activate:true},'plan.save');
  const start=(await api('POST',`/v3/sessions/${encodeURIComponent(sessionID)}/plan-mode/checkpoints/cp-boundary/start`,token,{plan_id:doc.id},'checkpoint.start',false,600000)).body; const checkpointRunID=start.run_start?.run_intent?.run_id||start.run_intent?.run_id||''; assert(checkpointRunID,'checkpoint start missing run_id'); result.ids.checkpoint_run_id=checkpointRunID; const cp=await waitForAssistant(token,sessionID,checkpointRunID,'checkpoint',600000);
  events=await fetchEvents(token,sessionID,'after_checkpoint'); let dcp=requestDiag(events,checkpointRunID); assertBoundary('checkpoint_fresh_context',dcp,cp,{boundary:'checkpoint_fresh_context',native:false,fresh:true}); assert(Number(dcp.input_text_chars||0) < 50000,`checkpoint input too large: ${dcp.input_text_chars}`); for (const body of apiRequestBodies(events,checkpointRunID,'fireworks')) { assert(!body.includes('x'.repeat(20000)), 'Checkpoint API request replayed huge stale transcript body'); }
  assertProviderToolConstruction('fireworks_checkpoint_tool_construction',events,checkpointRunID,'plan_manage'); result.gates.provider_tool_construction=true;
  result.gates.checkpoint_fresh_context=true;
  const usageEvents=providerUsageDiagnostics(events,result.ids.fireworks_run_id,'fireworks').concat(providerUsageDiagnostics(events,checkpointRunID,'fireworks'));
  const usageBodies=apiChunks(events,result.ids.fireworks_run_id,'fireworks').concat(apiChunks(events,checkpointRunID,'fireworks'));
  result.diagnostics.fireworks_usage={usage_event_count:usageEvents.length, usage_samples:usageEvents.map(d=>diagnosticPayload(d).usage||null).filter(Boolean).slice(-3), stream_chunk_usage_seen:usageBodies.some(b=>b.includes('"usage"') && !b.includes('"usage":null')), assistant_usage:f.metadata?.usage||cp.metadata?.usage||null};
  assert(result.diagnostics.fireworks_usage.stream_chunk_usage_seen || usageEvents.length>0 || result.diagnostics.fireworks_usage.assistant_usage, 'Fireworks stream usage/token evidence missing'); result.gates.fireworks_usage=true;
  const logs=run('bash',['-lc',`journalctl -u ${cfg.serviceUnit} --since '${startedISO}' --no-pager | grep -E 'panic|websocket error|provider boundary e2e fatal' || true`]); fs.writeFileSync(`${artifactDir}/journal-matches.log`, logs.stdout+logs.stderr); result.logs={matches_path:`${artifactDir}/journal-matches.log`, forbidden_matches:(logs.stdout||'').split('\n').filter(Boolean)}; assert(result.logs.forbidden_matches.length===0,`unexpected service log matches: ${result.logs.forbidden_matches.slice(0,10).join(' | ')}`); result.gates.logs=true;
  refreshSummary(); result.result=result.failed_gates.length?'NOT_DONE':'PASS'; save();
}
main().catch(err=>{ result.result='NOT_DONE'; result.error=err?.stack||String(err); save(); console.error(result.error); process.exitCode=2; }).finally(()=>{ console.log(JSON.stringify(result,null,2)); });
NODE

CONFIG_LOCAL="${LOCAL_DIR}/config.json"
cat >"${CONFIG_LOCAL}" <<JSON
{"apiURL":"${API_URL}","serviceUnit":"${SERVICE_UNIT}","repo":"${REMOTE_REPO}","e2eID":"${E2E_ID}","artifactDir":"${REMOTE_DIR}","workspaceDir":"${WORKSPACE_DIR}","expectedCommit":"${EXPECTED_COMMIT}","codexModelA":"${CODEX_MODEL_A}","codexModelB":"${CODEX_MODEL_B}","codexPlanModel":"${CODEX_PLAN_MODEL}","codexAutoModel":"${CODEX_AUTO_MODEL}","codexPlanThinking":"${CODEX_PLAN_THINKING}","codexAutoThinking":"${CODEX_AUTO_THINKING}","fireworksModel":"${FIREWORKS_MODEL}"}
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
