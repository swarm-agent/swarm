import assert from 'node:assert/strict'
import test from 'node:test'
import { projectTimelineToTimelineSegments, timelineSegmentsToProjectTimeline, videoAnimationPartAtClip } from './video-tool-page'
import { artifactV3VideoMediaUrl, videoPlanPartStoryboardContext, type ArtifactV3VideoReferenceWire } from '../video-studio/video-studio-surface'

const ref: ArtifactV3VideoReferenceWire = { session_id:'source',artifact_id:'artifact',revision_id:'revision-a',commit_oid:'a'.repeat(40),tree_oid:'b'.repeat(40),manifest_digest_sha256:'c'.repeat(64),build_id:'build',validation_id:'validation',event_seq:7,derivative_id:'av3der_'+'d'.repeat(64),digest_sha256:'d'.repeat(64),media_type:'video/mp4',duration_ms:4000,fps:30,animation_profile:'motion_ui' }

// Requirement: native V3 identity reaches decoded-video playback without legacy
// collection/variant aliases and survives timeline edits. Threat: blank frames,
// wrong source kind, or accidentally routing native motion into the V1 iframe.
// Pure player mappings prove the exact URL/type/identity; browser proof is separate.
test('native V3 motion maps to authenticated video and survives timeline serialization',()=>{
 const timeline = {schema_version:1,output_preset:'landscape_1080p',width:1920,height:1080,fps:30,total_duration_ms:4000,clips:[{id:'motion',name:'Motion',track:0,sequence:0,source_kind:'managed_artifact',artifact_v3_ref:ref,media_type:'video/mp4',source_start_ms:0,source_end_ms:4000,timeline_start_ms:0,timeline_end_ms:4000,duration_ms:4000,visible:true}]}
 const segments = projectTimelineToTimelineSegments(timeline,{},[],'video-session')
 assert.equal(segments[0].type,'video')
 assert.equal(segments[0].src,artifactV3VideoMediaUrl(ref))
 assert.equal(segments[0].artifactRef,undefined)
 assert.deepEqual(segments[0].artifactV3Ref,ref)
 const serialized=timelineSegmentsToProjectTimeline(segments,[])
 assert.equal(serialized.clips[0].source_kind,'managed_artifact')
 assert.equal(serialized.clips[0].source_ref,undefined)
 assert.deepEqual(serialized.clips[0].artifact_v3_ref,ref)
 const part = {id:'motion',title:'Motion',duration_ms:4000,artifact_v3_visual:ref,animation_candidates:{status:'ready' as const,candidates:[{id:'a',artifact_v3_source:ref}],selected_candidate_id:'a',artifact_v3_selected_source:ref,artifact_v3_derivative:ref}}
 assert.equal(videoAnimationPartAtClip({kind:'initial',parts:[part]},'motion'),null)
})

// Requirement: pending filming keeps explicit exact native state provenance.
test('native storyboard context retains state still and filming requirements',()=>{
 const still={...ref,media_type:'image/png',capture_state_id:'opening'}
 const part={id:'opening',title:'Opening',duration_ms:4000,production_state:'pending' as const,capture_state_id:'opening',filming_requirements:['Film intro'],artifact_v3_source:ref,artifact_v3_still:still,artifact_v3_visual:still}
 const context=videoPlanPartStoryboardContext(part)
 assert.equal(context?.productionState,'pending')
 assert.deepEqual(context?.artifactV3Still,still)
 assert.equal(context?.source,undefined)
 assert.equal(context?.captureStateId,'opening')
})
