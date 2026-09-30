package api

import (
 "swarm/packages/swarmd/internal/identity"
 pebblestore "swarm/packages/swarmd/internal/store/pebble"
 sessionruntime "swarm/packages/swarmd/internal/session"
)

// The same selection is used for live outbox advancement and durable reconnect
// replay. Receipts never mutate projections here; replay only invalidates reads.
func (s *Server) usageScopesForRealtimeSubscriptions(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord, subs map[string]v3RealtimeSubscription, worksets map[string]v3RealtimeWorksetSubscription) []pebblestore.UsageScopeTotal {
 if !v3RealtimeRecordVisibleToPrincipal(principal, record) || s.sessions == nil { return nil }
 totals := usageScopesFromRealtimeRecord(record)
 if len(totals) > 96 { return nil } // At most three scopes per bounded lineage hop.
 var selected []pebblestore.UsageScopeTotal
 for _, total := range totals {
  sessionID := record.SessionID
  resource := "projects"
  switch total.Kind {
  case "task":
   task, found, err := s.sessions.Store().GetProjectTask(principal.AccountScopeID, total.ProjectID, total.ID)
   if err != nil || !found { continue }; sessionID = task.SessionID; resource = "projects"
  case "worker_run":
   run, found, err := s.sessions.GetWorkerRun(principal.AccountScopeID, total.ProjectID, total.ID)
   if err != nil || !found { continue }; sessionID = run.SessionID
  case "worker":
   if _, found, err := s.sessions.GetWorker(principal.AccountScopeID, total.ID); err != nil || !found { continue }
   for _, linked := range totals {
    if linked.Kind != "worker_run" || linked.ProjectID != total.ID { continue }
    run, found, err := s.sessions.GetWorkerRun(principal.AccountScopeID, linked.ProjectID, linked.ID)
    if err == nil && found { sessionID = run.SessionID; break }
   }
  default: continue
  }
  session, found, err := s.sessions.GetSession(sessionID)
  if err != nil || !found { continue }
  if usageScopeSubscriptionMatches(principal, session, resource, subs, worksets) { selected = append(selected, total) }
 }
 return selected
}

// Resource visibility is independent of hidden chat navigation, but never of
// principal ownership, explicit resource selection or workspace/session scope.
func usageScopeSubscriptionMatches(principal identity.Principal, session pebblestore.SessionSnapshot, resource string, subs map[string]v3RealtimeSubscription, worksets map[string]v3RealtimeWorksetSubscription) bool {
 if session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID { return false }
 if _, ok := subs[session.ID]; ok { return true }
 visible := session
 visible.Metadata = make(map[string]any, len(session.Metadata))
 for key, value := range session.Metadata { visible.Metadata[key] = value }
 delete(visible.Metadata, "navigation_hidden")
 // Session purpose also hides worker chat; selectors should see its resource.
 delete(visible.Metadata, pebblestore.SessionPurposeMetadataKey)
 for _, workset := range worksets {
  if v3RealtimeWorksetIncludesResource(workset, resource) && v3RealtimeSessionMatchesWorksetSelector(principal, visible, workset.Selector) { return true }
 }
 return false
}
