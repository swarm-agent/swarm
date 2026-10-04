// Task-card definitions and session execution snapshots arrive independently.
// A stale session snapshot must never hide a newly submitted task definition.
export function selectTaskPlanDocument(task: any, sessionPlan: any): any {
 const taskDoc=task.planDocument || task.plan_document
 const binding=task.planBinding || task.plan_binding
 const id=binding?.planId || binding?.plan_id
 const revision=binding?.definitionRevision ?? binding?.definition_revision
 if(task.boardSummary?.plan_binding_stale)return undefined
 if(!taskDoc){
   if(!task.boardSummary)return sessionPlan?.document
   const plan=task.boardSummary.plan
   const sessionId=sessionPlan?.id || sessionPlan?.plan_id
   const version=sessionPlan?.definition_revision ?? sessionPlan?.version
   // A compact row is not reviewable prose. Only an exact bound hydration may supply it.
   if(!plan || sessionId!==id || sessionPlan?.session_id!==plan.session_id || version!==plan.version)return undefined
   return sessionPlan?.document
 }
 if(!sessionPlan?.document)return taskDoc
 // Pending review must display exactly the definition sent by the task API.
 // A session execution version is not an acceptance revision.
 if(task.status==='pending_approval' || task.status==='planning')return taskDoc
 const sessionId=sessionPlan.id || sessionPlan.plan_id || sessionPlan.document.id
 const version=sessionPlan.definition_revision ?? sessionPlan.version
 if(sessionId!==id || typeof version!=='number' || typeof revision!=='number' || version<revision)return taskDoc
 return sessionPlan.document
}
