import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { appendDesktopV3ArtifactMessageSelections, normalizeDesktopV3ArtifactMessageSelection, removeDesktopV3ArtifactMessageSelection } from './artifact-api'
import { desktopV3NativeArtifactIterationSelection, type DesktopV3NativeArtifactStudio } from './artifact-v3-api'
import { createDesktopV3ExistingMessageOperation, persistDesktopV3ExistingMessageOperation, loadDesktopV3ExistingMessageOperation } from './existing-session-flow'
import { createDesktopV3RoutedComposerSnapshot } from './new-session-flow'
import { portableDesktopV3ArtifactMessageSelection, postDesktopV3AppendMessage, postDesktopV3RoutedSessionStart } from './write-api'

// Requirement: native Studio targets use removable composer selections, not draft
// replacement. Threat: lost exact targets on retry or fabricated legacy identity.
// Exercise the actual selection/operation helpers, with a wiring guard for the UI.
const studio: DesktopV3NativeArtifactStudio = {
 artifact: { artifactId:'native', artifactRef:'native', ownerSessionId:'source',label:'Design',description:'',status:'ready',partCount:1,turnCount:0,updatedAt:1,head:{revisionRef:`revision-${'a'.repeat(40)}`,commitOid:'a'.repeat(40),treeOid:'b'.repeat(40),generation:1,selectedEventSeq:1}},
 parts:[{id:'pricing',label:'Pricing',description:'',locator:{kind:'selector',path:'index.html',value:'#pricing',paths:[]}}], turns:[],revisions:[],
}

test('native selection stages removable Parts and preserves draft and exact retry snapshot', () => {
 const selection=desktopV3NativeArtifactIterationSelection(studio,['pricing'])
 const chips=appendDesktopV3ArtifactMessageSelections([], [selection])
 assert.equal(chips[0].label,'Pricing')
 assert.equal(chips[0].collection_id,undefined)
 assert.deepEqual(removeDesktopV3ArtifactMessageSelection(chips,selection),[])
 const operation=createDesktopV3ExistingMessageOperation({sessionId:'chat',prompt:'Keep my draft',artifactSelections:chips})
 assert.equal(operation.request.content,'Keep my draft')
 assert.deepEqual(operation.request.artifact_selections?.[0]?.target_part_ids,['pricing'])
 assert.equal(operation.request.artifact_selections?.[0]?.revision_ref,studio.artifact.head!.revisionRef)
 const storage=new Map<string,string>()
 const previous=Object.getOwnPropertyDescriptor(globalThis,'window')
 Object.defineProperty(globalThis,'window',{configurable:true,value:{sessionStorage:{setItem:(key:string,value:string)=>storage.set(key,value),getItem:(key:string)=>storage.get(key)??null}}})
 try { persistDesktopV3ExistingMessageOperation(operation); assert.deepEqual(loadDesktopV3ExistingMessageOperation('chat'),JSON.parse(JSON.stringify(operation))) }
 finally { if(previous)Object.defineProperty(globalThis,'window',previous); else Reflect.deleteProperty(globalThis,'window') }
 const snapshot=createDesktopV3RoutedComposerSnapshot({prompt:'Keep my draft',artifactSelections:chips})
 chips[0].target_part_ids!.push('changed-after-snapshot')
 assert.equal(snapshot.prompt,'Keep my draft')
 assert.deepEqual(snapshot.artifactSelections[0].target_part_ids,['pricing'])
 assert.equal(portableDesktopV3ArtifactMessageSelection(snapshot.artifactSelections[0]).revision_ref,selection.revision_ref)
 const pane=readFileSync(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx',import.meta.url),'utf8')
 const handler=pane.slice(pane.indexOf('onIterate={(selection)'),pane.indexOf('onIterate={(selection)')+300)
 assert.match(handler,/queueGalleryArtifactSelections\(\[selection\]\)/)
 assert.doesNotMatch(handler,/setDraft|stableSubmit/)
})

test('native selection normalization fails closed for malformed mixed and duplicate targets', () => {
 const valid=desktopV3NativeArtifactIterationSelection(studio,['pricing'])
 for(const patch of [{collection_id:'legacy'},{event_seq:4},{part_id:'pricing'},{revision_ref:'revision-bad'},{target_part_ids:['pricing','pricing']},{target_part_ids:[null]},{artifact_id:''}]) {
  assert.equal(normalizeDesktopV3ArtifactMessageSelection({...valid,...patch}),null)
  assert.throws(()=>portableDesktopV3ArtifactMessageSelection({...valid,...patch} as typeof valid))
 }
 assert.throws(()=>desktopV3NativeArtifactIterationSelection(studio,['unknown']))
 assert.throws(()=>desktopV3NativeArtifactIterationSelection(studio,['pricing','pricing']))
})

// Transport failure must retain exactly the same native envelope for both entry paths.
test('native append and routed HTTP submissions preserve targets without hidden draft text', async () => {
 const selection=desktopV3NativeArtifactIterationSelection(studio,['pricing'])
 const original=globalThis.fetch
 const bodies:Record<string,unknown>[]=[]
 globalThis.fetch=(async (_url,init)=>{ bodies.push(JSON.parse(String(init?.body))); return new Response(JSON.stringify({error:'test transport failure'}),{status:503,headers:{'Content-Type':'application/json'}}) }) as typeof fetch
 try {
  const request={client_request_id:'native-message',message_id:'message',run_id:'run',role:'user' as const,content:'My draft',artifact_selections:[selection]}
  await assert.rejects(postDesktopV3AppendMessage('chat',request))
  await assert.rejects(postDesktopV3AppendMessage('chat',request))
  assert.deepEqual(bodies[0],bodies[1])
  await assert.rejects(postDesktopV3RoutedSessionStart({workspace_path:'/workspace',host_workspace_path:'/workspace',runtime_workspace_path:'/workspace',workspace_binding_id:'binding',swarm_id:'swarm',target_kind:'host',target_relationship:'self',input:'My draft',client_request_id:'native-routed',agent_name:'swarm',plan_mode_requested:false,artifact_selections:[selection]}))
 } finally {globalThis.fetch=original}
 assert.equal(bodies.length,3)
 for(const body of bodies){
  assert.deepEqual(body.artifact_selections,JSON.parse(JSON.stringify([selection])))
  assert.equal(body.content??body.input,'My draft')
  assert.doesNotMatch(JSON.stringify(body),/pending_request|collection_id|variant_id|projection_seq/)
 }
})
