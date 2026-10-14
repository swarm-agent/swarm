#!/usr/bin/env node
// Transport adapter for Swarm's stateless JSON Streamable HTTP endpoint.
// Tool schemas, authorization and execution remain in Swarm, not this plugin.
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline';
import { loadConnection, readToken } from './lib/connection.mjs';

function run(command,args,env=process.env) {
  const child=spawn(command,args,{stdio:'inherit',shell:false,env});
  child.on('error',()=>{ console.error('Swarm MCP could not start. Check the selected binary/container and run plugin setup.'); process.exitCode=1; });
  child.on('exit',(code)=>{process.exitCode=code ?? 1;});
  for(const signal of ['SIGTERM','SIGINT']) process.on(signal,()=>child.kill(signal));
}
function failure(id,message) {
  process.stdout.write(JSON.stringify({jsonrpc:'2.0',id,error:{code:-32000,message}})+'\n');
}
async function http(connection) {
  const token=connection.tokenFile ? await readToken(connection.tokenFile) : null;
  let inFlight=0;
  const lines=createInterface({input:process.stdin,crlfDelay:Infinity});
  lines.on('line',line=>{
    let message;
    try { if(Buffer.byteLength(line)>1<<20) throw Error(); message=JSON.parse(line); if(!message || Array.isArray(message) || message.jsonrpc!=='2.0' || typeof message.method!=='string') throw Error(); }
    catch { failure(null,'Invalid MCP request (maximum 1 MiB, one JSON-RPC message per line).'); return; }
    const hasId=Object.hasOwn(message,'id');
    if(inFlight>=16) { if(hasId) failure(message.id,'Too many concurrent Swarm requests.'); return; }
    inFlight++;
    (async()=>{
      try {
        const response=await fetch(connection.url,{method:'POST',redirect:'error',signal:AbortSignal.timeout(120000),headers:{'Content-Type':'application/json','Accept':'application/json',...(token ? {Authorization:`Bearer ${token}`} : {})},body:line});
        if(!response.ok) {
          const error=response.status===401 ? 'Swarm is reachable but access is not authorized. Configure an AI key privately or grant this device tailnet access; do not reinstall Swarm.' : response.status===403 ? 'Swarm denied this operation. Check the key scope or tailnet grant; do not bypass it.' : `Swarm returned HTTP ${response.status}.`;
          await response.body?.cancel();
          throw Error(error);
        }
        if(response.status===202 || response.status===204) { if(hasId) throw Error('Swarm returned an invalid MCP response.'); return; }
        const reader=response.body.getReader(); let size=0; const chunks=[];
        while(true) { const {done,value}=await reader.read(); if(done) break; size+=value.length; if(size>16<<20) {await reader.cancel(); throw Error('Swarm response exceeded 16 MiB.');} chunks.push(value); }
        const result=JSON.parse(Buffer.concat(chunks).toString('utf8'));
        if(hasId && result.jsonrpc==='2.0' && result.id===message.id) process.stdout.write(JSON.stringify(result)+'\n');
        else if(hasId) throw Error('Swarm returned an invalid MCP response.');
      } catch(error) {
        // Never relay fetch exceptions containing URLs/headers or remote error bodies.
        const safe=/^(Swarm is reachable|Swarm denied|Swarm returned HTTP|Swarm response exceeded|Swarm returned an invalid)/.test(error.message) ? error.message : 'Cannot complete the Swarm MCP request. Check Tailscale/network connectivity. If a write timed out, inspect existing work before retrying.';
        if(hasId) failure(message.id,safe); else console.error(safe);
      } finally {inFlight--;}
    })();
  });
}
try {
  const connection=await loadConnection();
  if(connection.mode==='http') await http(connection);
  else if(connection.mode==='docker') run('docker',['exec','-i',connection.container,'swarmctl','mcp']);
  else if(connection.binary) run(connection.binary,['mcp',...(connection.socket ? ['--socket',connection.socket] : [])]);
  else if(process.platform==='win32') { throw Error('Configure Docker mode with the setup skill; native Windows Swarm is not supported.'); }
  // Native mode is the shell launcher's; SWARM_MCP_NATIVE keeps it from handing
  // a saved connection back here.
  else run('sh',[fileURLToPath(new URL('./swarm-mcp',import.meta.url))],{...process.env,SWARM_MCP_NATIVE:'1',...(connection.socket ? {SWARM_MCP_SOCKET:connection.socket} : {})});
} catch {console.error('Swarm MCP configuration/key file is missing or invalid. Run plugin setup; keep keys in private files, never chat.');process.exitCode=1;}
