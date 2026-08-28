#!/usr/bin/env bash
set -euo pipefail

SSH_HOST="${1:-${SWARM_PRIMARY_SSH:-}}"
if [[ -z "${SSH_HOST}" ]]; then
  echo "pass an SSH target as the first argument or set SWARM_PRIMARY_SSH" >&2
  exit 2
fi
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
MODEL="${SWARM_LIVE_STREAM_MODEL:-}"
PROVIDER="${SWARM_LIVE_STREAM_PROVIDER:-fireworks}"
E2E_ID="${E2E_ID:-v3sync-fireworks-$(date +%Y%m%d-%H%M%S)-$RANDOM}"
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
import net from 'node:net';
import {spawnSync} from 'node:child_process';

const cfg = JSON.parse(fs.readFileSync(process.env.SWARM_E2E_CONFIG, 'utf8'));
const apiURL = new URL(cfg.apiURL);
const host = apiURL.hostname;
const port = Number(apiURL.port || 80);
const started = new Date();
const startedISO = started.toISOString();
const artifactDir = cfg.artifactDir;
fs.mkdirSync(artifactDir, {recursive: true});
const httpLog = `${artifactDir}/http.ndjson`;
const wsLog = `${artifactDir}/websocket.ndjson`;
const summaryPath = `${artifactDir}/summary.json`;
fs.writeFileSync(httpLog, ''); fs.writeFileSync(wsLog, '');

const result = {result:'NOT_DONE', e2e_id:cfg.e2eID, workspace:cfg.workspaceDir, started_at:startedISO, candidate:{}, principals:{}, ids:{}, gates:{}, diagnostics:{}, artifacts:{http:httpLog, websocket:wsLog, summary:summaryPath, artifact_dir:artifactDir}, artifact_dir:artifactDir, failures:[], failed_gates:[]};
function run(cmd,args){ const p=spawnSync(cmd,args,{encoding:'utf8'}); return {cmd:[cmd,...args].join(' '), status:p.status, stdout:p.stdout, stderr:p.stderr}; }
const requiredGates=['candidate','http_workspace_stream','http_message_stream','http_global_stream','invalid_cursors_fail','hydrate_no_widening','tombstone_delete','websocket_live_and_repair','fireworks','logs'];
function refreshSummary(){ result.failed_gates=requiredGates.filter(g=>result.gates[g]!==true); result.artifact_dir=artifactDir; result.principals.distinct=Boolean(result.principals?.p1 && result.principals?.p2 && (result.principals.p1.user_id!==result.principals.p2.user_id || result.principals.p1.account_scope_id!==result.principals.p2.account_scope_id)); }
function save(){ refreshSummary(); fs.writeFileSync(summaryPath, JSON.stringify(result,null,2)); }
function die(msg){ result.failures.push(msg); save(); throw new Error(msg); }
function assert(cond,msg){ if(!cond) die(msg); }
function append(file,obj){ fs.appendFileSync(file, JSON.stringify({t:new Date().toISOString(),...obj})+'\n'); }
function redact(h){ const out={}; for (const [k,v] of Object.entries(h||{})) out[k]=/token|cookie|authorization/i.test(k)?'<redacted>':v; return out; }
function isCursor(v){ return typeof v === 'string' && v.startsWith('v3c1.') && !v.startsWith('cursor-'); }
function tamper(c){ return c.slice(0,-1)+(c.endsWith('A')?'B':'A'); }
function rawLeak(obj){ const s=JSON.stringify(obj); return ['cursor-','endpoint_seq','user_id','account_scope_id','created_at'].filter(x=>s.includes(x)); }
function gate(name, fn){ try{ fn(); result.gates[name]=true; } catch(e){ result.gates[name]=false; throw e; } }
function countEvent(events, sid, needle){ return (events||[]).filter(e=>e.session_id===sid && (!needle || JSON.stringify(e).includes(needle))).length; }

async function api(method, route, token, body, label=route, allowError=false){
  const headers={Accept:'application/json',Origin:cfg.apiURL,Referer:`${cfg.apiURL}/app`,'Sec-Fetch-Site':'same-origin'};
  if(token){ headers['X-Swarm-Token']=token; headers.Cookie=`swarm_desktop_session=${token}`; }
  const init={method,headers};
  if(body!==undefined){ headers['Content-Type']='application/json'; init.body=JSON.stringify(body); }
  const t0=Date.now(); const res=await fetch(`${cfg.apiURL}${route}`, init); const text=await res.text();
  let json=null; try{ json=text?JSON.parse(text):null; }catch{ json={raw:text}; }
  append(httpLog,{label,method,route,status:res.status,ok:res.ok,request_headers:redact(headers),request_body:body,response:json,ms:Date.now()-t0});
  if(!allowError && !res.ok) die(`${label} ${method} ${route} status=${res.status} body=${text.slice(0,1200)}`);
  return {status:res.status, ok:res.ok, body:json, text};
}
async function authDesktop(label){
  const r=await api('GET','/v1/auth/desktop/session','',undefined,label);
  const token=String(r.body?.token||'');
  assert(token,`${label} returned no token`);
  const me=await api('GET','/me',token,undefined,`${label}.me`);
  return {available:true, token, user_id:String(me.body?.userID||r.body.user_id||''), account_scope_id:String(me.body?.accountScopeID||r.body.account_scope_id||''), username:String(me.body?.username||r.body.username||'')};
}
async function authPrimary(){ return authDesktop('auth.primary'); }
async function authSecondary(){
  const provided=String(cfg.secondaryToken||'').trim();
  if(!provided) return authDesktop('auth.secondary');
  const r=await api('GET','/me',provided,undefined,'auth.secondary.me',true);
  if(!r.ok) die(`provided SWARM_SECONDARY_TOKEN was not accepted by /me: status=${r.status}`);
  return {available:true, token:provided, user_id:String(r.body?.userID||r.body?.user_id||''), account_scope_id:String(r.body?.accountScopeID||r.body?.account_scope_id||''), username:String(r.body?.username||r.body?.userID||r.body?.user_id||'secondary')};
}
function wsHeader(route, token){ const key=crypto.randomBytes(16).toString('base64'); return [`GET ${route} HTTP/1.1`,`Host: ${host}:${port}`,'Upgrade: websocket','Connection: Upgrade',`Sec-WebSocket-Key: ${key}`,'Sec-WebSocket-Version: 13',`Origin: ${cfg.apiURL}`,'Sec-Fetch-Site: same-origin',`X-Swarm-Token: ${token}`,`Cookie: swarm_desktop_session=${token}`,'',''].join('\r\n'); }
function sendWS(socket,obj){ const payload=Buffer.from(JSON.stringify(obj)); const h=payload.length<126?Buffer.from([0x81,payload.length|0x80]):Buffer.from([0x81,126|0x80,(payload.length>>8)&255,payload.length&255]); const mask=crypto.randomBytes(4); const masked=Buffer.from(payload.map((v,i)=>v^mask[i%4])); append(wsLog,{direction:'client',frame:obj}); socket.write(Buffer.concat([h,mask,masked])); }
function pong(socket,payload){ const h=Buffer.from([0x8a,payload.length|0x80]); const mask=crypto.randomBytes(4); const masked=Buffer.from(payload.map((v,i)=>v^mask[i%4])); socket.write(Buffer.concat([h,mask,masked])); }
function openWS(route, token){
  const frames=[];
  return new Promise((resolve,reject)=>{
    const socket=net.createConnection({host,port}); let handshake=Buffer.alloc(0), buf=Buffer.alloc(0), upgraded=false, settled=false;
    const fail=e=>{ if(!settled){settled=true; reject(e);} try{socket.destroy();}catch{} };
    socket.setTimeout(300000,()=>fail(new Error(`websocket timeout ${route}`))); socket.on('error',fail); socket.on('connect',()=>socket.write(wsHeader(route, token)));
    socket.on('data',chunk=>{
      if(!upgraded){ handshake=Buffer.concat([handshake,chunk]); const m=handshake.indexOf('\r\n\r\n'); if(m<0)return; const head=handshake.slice(0,m).toString(); if(!head.startsWith('HTTP/1.1 101')&&!head.startsWith('HTTP/1.0 101')) return fail(new Error(`upgrade failed ${head}`)); upgraded=true; append(wsLog,{direction:'handshake',route,status:101,head:head.split('\r\n')[0]}); if(!settled){settled=true; resolve({socket,frames,send:o=>sendWS(socket,o),close:()=>socket.end()});} buf=handshake.slice(m+4); }
      else buf=Buffer.concat([buf,chunk]);
      while(upgraded && buf.length>=2){ const op=buf[0]&15; let off=2; let len=buf[1]&127; if(len===126){ if(buf.length<4)return; len=buf.readUInt16BE(2); off=4;} else if(len===127){ if(buf.length<10)return; len=Number(buf.readBigUInt64BE(2)); off=10;} const masked=!!(buf[1]&128); let mask=null; if(masked){ if(buf.length<off+4)return; mask=buf.slice(off,off+4); off+=4;} if(buf.length<off+len)return; let payload=buf.slice(off,off+len); buf=buf.slice(off+len); if(masked&&mask) payload=Buffer.from(payload.map((v,i)=>v^mask[i%4])); if(op===8){ append(wsLog,{direction:'server',frame:{kind:'close'}}); socket.end(); continue;} if(op===9){ pong(socket,payload); continue;} if(op!==1) continue; let frame; const txt=payload.toString(); try{frame=JSON.parse(txt);}catch{frame={kind:'unparsed',raw:txt};} frames.push(frame); append(wsLog,{direction:'server',route,frame}); }
    });
  });
}
async function wait(frames,pred,ms,label){ const end=Date.now()+ms; while(Date.now()<end){ const f=frames.find(pred); if(f) return f; await new Promise(r=>setTimeout(r,50)); } die(`timeout waiting for ${label}; recent=${JSON.stringify(frames.slice(-8))}`); }
async function createSession(token,label,binding,swarm){ const body={client_request_id:`${cfg.e2eID}:create:${label}:${crypto.randomBytes(3).toString('hex')}`,title:`${cfg.e2eID} ${label}`,workspace_path:result.workspace,workspace_name:cfg.e2eID,workspace_binding_id:binding,swarm_id:swarm,target_kind:'host',target_relationship:'self',mode:'auto',agent_name:'swarm',preference:{provider:cfg.provider,model:cfg.model,thinking:'low'},metadata:{e2e_id:cfg.e2eID,label}}; const r=await api('POST','/v3/sessions',token,body,`create.${label}`); const id=r.body?.session?.id; assert(id,`create ${label} missing session id`); return id; }
async function mutate(token,id,key){ return (await api('POST',`/v3/sessions/${encodeURIComponent(id)}/metadata`,token,{client_request_id:`${cfg.e2eID}:metadata:${key}:${crypto.randomBytes(3).toString('hex')}`,metadata:{[key]:cfg.e2eID,e2e_id:cfg.e2eID}},`metadata.${key}`)).body; }
async function main(){
  result.candidate.hostname=run('hostname',[]).stdout.trim(); result.candidate.repo_path=cfg.repo; result.candidate.expected_commit=cfg.expectedCommit; result.candidate.commit=run('git',['-C',cfg.repo,'rev-parse','HEAD']).stdout.trim(); result.candidate.git_status=run('git',['-C',cfg.repo,'status','--short']).stdout; result.candidate.service_status=run('bash',['-lc',`(systemctl --user --no-pager status ${cfg.serviceUnit} || sudo -n systemctl --no-pager status ${cfg.serviceUnit} || true) | sed -n '1,30p'`]).stdout; result.candidate.process=run('bash',['-lc','ps -eo pid,lstart,cmd | grep -E "[s]warm(d| )" || true']).stdout;
  const expectedCommit=String(cfg.expectedCommit||'').trim(); const mergeBase=expectedCommit?run('git',['-C',cfg.repo,'merge-base','--is-ancestor',expectedCommit,'HEAD']):{status:2,stdout:'',stderr:'missing SWARM_EXPECTED_COMMIT'}; result.candidate.expected_commit_is_ancestor=mergeBase.status===0;
  assert(result.candidate.commit, 'missing remote git commit'); assert(expectedCommit,'missing SWARM_EXPECTED_COMMIT'); assert(mergeBase.status===0, `deployed commit ${result.candidate.commit} does not contain expected commit ${expectedCommit}: ${mergeBase.stderr||mergeBase.stdout}`); assert(result.candidate.git_status.trim()==='', `remote git status not clean: ${result.candidate.git_status}`); result.gates.candidate=true;
  const p1=await authPrimary(); result.principals.p1={user_id:p1.user_id, account_scope_id:p1.account_scope_id, username:p1.username};
  if(!String(cfg.model||'').trim()){ const settings=(await api('GET','/v1/agent-model-settings',p1.token,undefined,'model.settings')).body?.agent_model_settings||{}; const assigned=settings?.swarm?.action||{}; assert(String(assigned.provider||'').trim()===cfg.provider,`configured Swarm action provider ${assigned.provider||'\u003cmissing\u003e'} does not match requested ${cfg.provider}`); assert(String(assigned.model||'').trim(),'configured Swarm action model is missing'); cfg.model=String(assigned.model).trim(); }
  result.provider_response={provider:cfg.provider,model:cfg.model};
  fs.mkdirSync(result.workspace,{recursive:true}); const topo=(await api('GET','/v1/swarm/topology',p1.token,undefined,'topology')).body; const runtime=(topo.runtimes||[]).find(r=>r.relationship==='self')||(topo.runtimes||[])[0]; assert(runtime?.swarm_id,'missing self runtime'); const w=(await api('POST','/v1/workspace/add',p1.token,{path:result.workspace,name:cfg.e2eID,make_current:false},'workspace.add')).body; const binding=w.local_workspace_binding_id; assert(binding,'workspace add missing binding'); result.ids.workspace_binding_id=binding; result.ids.runtime_swarm_id=runtime.swarm_id;
  const A=await createSession(p1.token,'A',binding,runtime.swarm_id); const B=await createSession(p1.token,'B',binding,runtime.swarm_id); result.ids.A=A; result.ids.B=B; const selectorWS={kind:'workspace',workspace_path:result.workspace,recent:{limit:20}};
  const boot=(await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:selectorWS},'bootstrap.workspace')).body; const bootCursor=boot.snapshot_endpoint_cursor; assert(boot.ok===true && isCursor(bootCursor),'bootstrap missing ok/signed cursor'); assert(boot.sessions_by_id?.[A] && boot.sessions_by_id?.[B],'bootstrap missing A/B'); assert(boot.tombstones_by_session && typeof boot.tombstones_by_session==='object','bootstrap missing tombstones map');
  await mutate(p1.token,A,'after_boot_A'); const s1=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:selectorWS,endpoint_cursor:bootCursor,limit:100},'stream.A')).body; assert(s1.ok===true && isCursor(s1.endpoint_cursor),'stream A not ok/signed'); assert(countEvent(s1.events,A,'after_boot_A')===1,'stream A mutation not exactly once'); assert(rawLeak(s1.events).length===0,`stream A raw leak ${rawLeak(s1.events)}`);
  await mutate(p1.token,B,'after_stream_B'); const s2=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:selectorWS,endpoint_cursor:s1.endpoint_cursor,limit:100},'stream.B')).body; assert(countEvent(s2.events,B,'after_stream_B')===1,'stream B mutation not exactly once'); assert(countEvent(s2.events,A,'after_boot_A')===0,'stream B replayed A'); assert(rawLeak(s2.events).length===0,`stream B raw leak ${rawLeak(s2.events)}`); result.gates.http_workspace_stream=true;
  const msgBoot=(await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:selectorWS},'bootstrap.message_stream')).body; const msgNeedle=`HTTP_STREAM_MESSAGE_${cfg.e2eID}`; const msgReq={client_request_id:`${cfg.e2eID}:http-message:${crypto.randomBytes(3).toString('hex')}`,role:'user',content:`${msgNeedle} simple append for sync stream gate`,metadata:{e2e_id:cfg.e2eID,gate:'http_message_stream'},dispatch_authority:{runtime_swarm_id:'blocked-for-sync-stream-gate'}}; const msgAppend=(await api('POST',`/v3/sessions/${encodeURIComponent(A)}/messages`,p1.token,msgReq,'message.http_stream')).body; result.ids.http_message_run_id=msgAppend.run_intent?.run_id||msgAppend.run_id||''; const msgStream=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:selectorWS,endpoint_cursor:msgBoot.snapshot_endpoint_cursor,limit:100},'stream.message')).body; assert(msgStream.ok===true && isCursor(msgStream.endpoint_cursor),'message stream not ok/signed'); assert(countEvent(msgStream.events,A,msgNeedle)===1,'HTTP message append mutation not exactly once in sync stream'); assert(rawLeak(msgStream.events).length===0,`message stream raw leak ${rawLeak(msgStream.events)}`); result.gates.http_message_stream=true;
  const globalBoot=(await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:{kind:'global',global:true,recent:{limit:20}}},'bootstrap.global')).body; await mutate(p1.token,A,'global_after_boot'); const gs=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:{kind:'global',global:true,recent:{limit:20}},endpoint_cursor:globalBoot.snapshot_endpoint_cursor,limit:100},'stream.global')).body; assert(countEvent(gs.events,A,'global_after_boot')===1,'global stream missed mutation'); result.gates.http_global_stream=true;
  for(const [label,body] of Object.entries({empty:{surface:'desktop',selector:selectorWS,endpoint_cursor:''},legacy:{surface:'desktop',selector:selectorWS,endpoint_cursor:'cursor-1'},tampered:{surface:'desktop',selector:selectorWS,endpoint_cursor:tamper(bootCursor)},wrong_scope:{surface:'desktop',selector:selectorWS,endpoint_cursor:globalBoot.snapshot_endpoint_cursor}})){ const r=await api('POST','/v3/sync/stream',p1.token,body,`cursor.${label}`,true); assert(!r.ok && r.body?.ok!==true,`${label} cursor did not fail closed`); }
  const unbounded=await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:{kind:'workspace',workspace_path:result.workspace}},'selector.unbounded',true); assert(!unbounded.ok && unbounded.body?.ok!==true,'unbounded workspace did not fail'); result.gates.invalid_cursors_fail=true;
  const hyd=(await api('POST','/v3/sync/hydrate',p1.token,{surface:'desktop',session_ids:[A],include_active:true,resources:{run_intents:true}},'hydrate.A')).body; assert(hyd.sessions_by_id?.[A] && !hyd.sessions_by_id?.[B] && !(hyd.session_order||[]).includes(B),'hydrate widened beyond A'); await mutate(p1.token,A,'hydrate_A'); await mutate(p1.token,B,'hydrate_B'); const hs=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:{kind:'session_ids',session_ids:[A]},include_active:true,resources:{run_intents:true},endpoint_cursor:hyd.snapshot_endpoint_cursor,limit:100},'stream.hydrate')).body; assert(countEvent(hs.events,A,'hydrate_A')===1 && countEvent(hs.events,B,'hydrate_B')===0,'hydrate stream widened/leaked'); result.gates.hydrate_no_widening=true;
  const C=await createSession(p1.token,'C',binding,runtime.swarm_id); result.ids.C=C; const preDel=(await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:selectorWS},'bootstrap.pre_delete')).body; const del=(await api('DELETE',`/v3/sessions/${encodeURIComponent(C)}`,p1.token,undefined,'delete.C')).body; assert(del.ok===true && del.deleted===true,'delete route failed'); const ds=(await api('POST','/v3/sync/stream',p1.token,{surface:'desktop',selector:selectorWS,endpoint_cursor:preDel.snapshot_endpoint_cursor,limit:100},'stream.delete')).body; assert(countEvent(ds.events,C,'session.deleted')===1 || countEvent(ds.events,C,'deleted')===1,'delete/tombstone event missing in stream'); const postDel=(await api('POST','/v3/sync/bootstrap',p1.token,{surface:'desktop',selector:selectorWS},'bootstrap.post_delete')).body; assert(!postDel.sessions_by_id?.[C] && postDel.tombstones_by_session?.[C],'post-delete bootstrap missing tombstone or still has session'); result.gates.tombstone_delete=true;
  let ws=await openWS('/v3/realtime/stream',p1.token); const hello=await wait(ws.frames,f=>f.kind==='hello',5000,'ws hello'); assert(isCursor(hello.endpoint_cursor),'ws hello cursor not signed'); ws.send({protocol:'v3.realtime',protocol_version:1,kind:'hello',endpoint_cursor:'cursor-evil',event:{bad:true},subscription_id:'evil'}); const denied=await wait(ws.frames,f=>f.kind==='auth.denied'||f.kind==='cursor.error',5000,'invalid inbound rejection'); assert(denied.kind==='auth.denied' && !JSON.stringify(denied).includes('cursor-evil'),'invalid inbound not cleanly rejected'); ws.close(); await new Promise(r=>setTimeout(r,200));
  ws=await openWS('/v3/realtime/stream',p1.token); const h2=await wait(ws.frames,f=>f.kind==='hello',5000,'ws hello2'); ws.send({protocol:'v3.realtime',protocol_version:1,kind:'subscribe.session',session_id:A,subscription_id:`sub-${cfg.e2eID}`,endpoint_cursor:h2.endpoint_cursor}); await wait(ws.frames,f=>f.kind==='replay.started'&&f.session_id===A,5000,'replay started'); await mutate(p1.token,A,'ws_live'); const live=await wait(ws.frames,f=>f.kind==='event'&&f.event?.session_id===A&&JSON.stringify(f).includes('ws_live'),10000,'ws live'); assert(isCursor(live.endpoint_cursor),'ws live cursor not signed'); const oldCursor=live.endpoint_cursor; ws.close(); await new Promise(r=>setTimeout(r,250)); await mutate(p1.token,A,'missed_1'); await mutate(p1.token,A,'missed_2'); const ws2=await openWS(`/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(oldCursor)}`,p1.token); await wait(ws2.frames,f=>f.kind==='hello',5000,'resume hello'); ws2.send({protocol:'v3.realtime',protocol_version:1,kind:'subscribe.session',session_id:A,subscription_id:`resume-${cfg.e2eID}`}); await wait(ws2.frames,f=>f.kind==='event'&&f.event?.session_id===A&&JSON.stringify(f).includes('missed_1'),10000,'missed 1'); await wait(ws2.frames,f=>f.kind==='event'&&f.event?.session_id===A&&JSON.stringify(f).includes('missed_2'),10000,'missed 2'); const beforeB=ws2.frames.length; await mutate(p1.token,B,'filtered_B'); await new Promise(r=>setTimeout(r,1500)); assert(!ws2.frames.slice(beforeB).some(f=>f.kind==='event'&&f.event?.session_id===B),'B delivered to A-only ws'); result.gates.websocket_live_and_repair=true;
  const fire=(await api('POST',`/v3/sessions/${encodeURIComponent(A)}/messages`,p1.token,{client_request_id:`${cfg.e2eID}:fireworks`,role:'user',content:`Fireworks A-Z ${cfg.e2eID}. Reply exactly V3_FIREWORKS_ACCEPTANCE_OK and do not call tools.`,metadata:{e2e_id:cfg.e2eID}},'fireworks.message')).body; result.ids.fireworks_run_id=fire.run_intent?.run_id||fire.run_id||''; const term=await wait(ws2.frames,f=>['session.assistant.completed','session.assistant.failed','session.run.failed','session.run.completed','session.run.cancelled','session.run.expired','session.run.interrupted'].includes(f.event?.event_type||f.event_type),240000,'Fireworks terminal'); const tail=(await api('GET',`/v3/sessions/${encodeURIComponent(A)}/messages?tail=true&limit=200`,p1.token,undefined,'fireworks.tail')).body; const assistants=(tail.messages||[]).filter(m=>m.role==='assistant'); const assistantOutput=String(assistants.at(-1)?.content||''); assert((term.event?.event_type||term.event_type)==='session.assistant.completed' && assistants.length>0,'Fireworks did not complete with assistant output'); assert(assistantOutput.includes('V3_FIREWORKS_ACCEPTANCE_OK'),`Fireworks assistant output missing acceptance token: ${assistantOutput.slice(0,500)}`); result.provider_response={provider:cfg.provider,model:cfg.model,run_id:result.ids.fireworks_run_id,terminal_event:term.event?.event_type||term.event_type,assistant_output:assistantOutput}; result.gates.fireworks=true; ws2.close();
  const logs=run('bash',['-lc',`journalctl -u ${cfg.serviceUnit} --since '${startedISO}' --no-pager | grep -E '/v3/sessions:workset|/v3/tui/sessions:workset|/v3/sessions:discover|endpoint_cursor_legacy_unsupported|endpoint_cursor_gap|endpoint_membership_unavailable|sync_websocket_unsupported|panic|websocket error|Fireworks|provider|error' || true`]); fs.writeFileSync(`${artifactDir}/journal-matches.log`, logs.stdout+logs.stderr); const forbiddenLogs=(logs.stdout||'').split('\n').filter(line=>/(\/v3\/sessions:workset|\/v3\/tui\/sessions:workset|\/v3\/sessions:discover|endpoint_cursor_gap|endpoint_membership_unavailable|sync_websocket_unsupported|panic|websocket error)/i.test(line)); const legacyCursorLogs=(logs.stdout||'').split('\n').filter(line=>/endpoint_cursor_legacy_unsupported/i.test(line)); result.logs={command:logs.cmd,matches_path:`${artifactDir}/journal-matches.log`,forbidden_matches:forbiddenLogs,endpoint_cursor_legacy_unsupported:{allowed_for:'intentional cursor.legacy negative test',count:legacyCursorLogs.length}}; assert(forbiddenLogs.length===0,`unexpected service log matches: ${forbiddenLogs.slice(0,10).join(' | ')}`); result.gates.logs=true;
  refreshSummary(); if(result.failed_gates.length>0){ result.result='NOT_DONE'; save(); return; } result.result='PASS'; save();
}
main().catch(err=>{ result.result='NOT_DONE'; result.error=err?.stack||String(err); save(); console.error(result.error); process.exitCode=2; }).finally(()=>{ console.log(JSON.stringify(result,null,2)); });
NODE

CONFIG_LOCAL="${LOCAL_DIR}/config.json"
cat >"${CONFIG_LOCAL}" <<JSON
{"apiURL":"${API_URL}","serviceUnit":"${SERVICE_UNIT}","repo":"${REMOTE_REPO}","provider":"${PROVIDER}","model":"${MODEL}","e2eID":"${E2E_ID}","artifactDir":"${REMOTE_DIR}","workspaceDir":"${WORKSPACE_DIR}","expectedCommit":"${EXPECTED_COMMIT}","secondaryToken":"${SWARM_SECONDARY_TOKEN:-}"}
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
