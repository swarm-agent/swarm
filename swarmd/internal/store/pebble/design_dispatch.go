package pebblestore

import (
 "strings"
 "github.com/cockroachdb/pebble"
)

// DesignWake is a coalesced hint only. Durable discovery repairs missed hints.
func (s *Store) DesignWake() <-chan struct{} {
 s.designWakeMu.Lock()
 defer s.designWakeMu.Unlock()
 if s.designWake == nil { s.designWake = make(chan struct{}, 1) }
 return s.designWake
}
func (s *Store) WakeDesign() {
 s.DesignWake()
 s.designWakeMu.Lock()
 defer s.designWakeMu.Unlock()
 select { case s.designWake <- struct{}{}: default: }
}

// ScanDesignPending is daemon-only discovery across owners. Cursor bounds work
// to 256 keys per page; historical content is never decoded during discovery.
func (s *Store) ScanDesignPending(after string) ([]DesignRequest, string, error) {
 const prefix = "design:v1/"
 if after != "" && !strings.HasPrefix(after, prefix) { return nil, "", ErrDesignInvalid }
 it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix+"\xff")})
 if err != nil { return nil, "", err }
 defer it.Close()
 rows := []DesignRequest{}
 cursor := ""
 start := prefix
 if after != "" { start = after+"\x00" }
 n := 0
 for ok := it.SeekGE([]byte(start)); ok; {
  if n == 256 { return rows, cursor, it.Error() }
  n++
  cursor = string(it.Key())
  parts := strings.Split(strings.TrimPrefix(cursor,prefix), "/")
  if len(parts) < 3 { ok=it.Next(); continue }
  ownerPrefix:=prefix+parts[0]+"/"+parts[1]+"/"
  if parts[2] < "pending" { ok=it.SeekGE([]byte(ownerPrefix+"pending/")); continue }
  if parts[2] > "pending" { ok=it.SeekGE([]byte(ownerPrefix+"\xff")); continue }
  if len(parts) != 4 { ok=it.Next(); continue }
  r, err := s.GetDesignRequest(DesignPrincipal{AccountID:parts[0], PrincipalID:parts[1]}, parts[3])
  if err != nil { return nil, "", err }
  rows = append(rows,r)
  ok=it.Next()
 }
 return rows, "", it.Error()
}

// FailQueuedDesign reports stable pre-allocation failures without inventing
// execution provenance. Capacity waits and revision conflicts must not use it.
func (s *Store) FailQueuedDesign(p DesignPrincipal, id string, revision uint64, candidate int, reason string) (DesignRequest,error) {
 var zero DesignRequest
 if err:=designOwner(p);err!=nil { return zero,err }
 if !designID(id) || (reason!="model_unavailable" && reason!="allocation_unavailable") { return zero,ErrDesignInvalid }
 s.designMu.Lock(); defer s.designMu.Unlock()
 r,err:=s.GetDesignRequest(p,id);if err!=nil {return zero,err}
 if r.Revision!=revision {return zero,ErrDesignConflict}
 if candidate<0 || candidate>=len(r.Candidates) {return zero,ErrDesignInvalid}
 c:=&r.Candidates[candidate]
 if c.State!=DesignQueued || len(c.Attempts)!=0 {return zero,ErrDesignConflict}
 c.State=DesignFailed; c.FailureReason=reason
 c.RouterAlert="Designer execution unavailable; no child was allocated."
 if reason=="model_unavailable" {c.RouterAlert="Designer assignment and account default model are unavailable; configure an account model before submitting again."}
 b:=s.db.NewBatch();defer b.Close()
 return s.designCommitRequest(p,r,designKey(p,"dispatch-failure",id+"/"+c.Spec.ArtifactID),struct{Revision uint64; Candidate int; Reason string}{revision,candidate,reason},b)
}
