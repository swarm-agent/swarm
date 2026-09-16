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
  globalThis.fetch=async()=>new Response('memory provider unavailable',{status:400})
  await assert.rejects(c.request('GET','/v1/memory'),/memory provider unavailable/)
  // run-now must fit the two-minute server execution window, but only that
  // opted-in action receives an extended timeout; ordinary reads stay bounded.
  const timeout=AbortSignal.timeout;const deadlines=[]
  AbortSignal.timeout=ms=>{deadlines.push(ms);return new AbortController().signal}
  try{globalThis.fetch=async()=>new Response('{}',{status:200});await c.request('POST','/v1/memory',{action:'run_now'});await c.request('GET','/v1/memory');assert.equal(deadlines[0],150000);assert.equal(deadlines[1],10000)}finally{AbortSignal.timeout=timeout}
 }finally{globalThis.fetch=original}
})
