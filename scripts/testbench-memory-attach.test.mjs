import test from 'node:test'
import assert from 'node:assert/strict'
import {AttachClient} from './testbench-attach.mjs'
// Purpose: the attach client must not enable memory mutations for other runners
// or disclose arbitrary provider errors. The narrow fake-HTTP boundary checks
// rejected calls never reach transport and only fixed diagnostics are exposed.
test('memory attach requires opt-in and preserves private errors',async()=>{
 const original=globalThis.fetch;let calls=0
 globalThis.fetch=async()=>{calls++;return new Response('private provider content',{status:400})}
 try{
  const c=new AttachClient('http://127.0.0.1:12345/')
  await assert.rejects(c.request('POST','/v1/memory',{action:'remember'}),/unreviewed/);assert.equal(calls,0)
  c.memoryTrial=true
  await assert.rejects(c.request('POST','/v1/memory',{action:'unknown'}),/unreviewed/);assert.equal(calls,0)
  await assert.rejects(c.request('GET','/v1/memory'),error=>error.message==='attach read returned HTTP 400');assert.equal(calls,1)
  globalThis.fetch=async()=>new Response('memory provider cannot guarantee configured output and spend limits',{status:400})
  await assert.rejects(c.request('GET','/v1/memory'),/cannot guarantee configured/)
 }finally{globalThis.fetch=original}
})
