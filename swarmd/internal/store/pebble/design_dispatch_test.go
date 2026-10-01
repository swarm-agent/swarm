package pebblestore

import (
 "fmt"
 "path/filepath"
 "testing"
)

// Purpose: daemon startup must discover pending requests across owners without
// returning context/history bytes or depending on a transient wake. Real Pebble
// reopen and pagination are the narrow durable-index contract, not an AI benchmark.
func TestDesignDispatchDiscoveryReopen(t *testing.T) {
 path:=filepath.Join(t.TempDir(),"store")
 db,err:=Open(path);if err!=nil {t.Fatal(err)}
 for i:=0;i<60;i++ {
  owner:=DesignPrincipal{AccountID:fmt.Sprintf("account-%03d",i),PrincipalID:"owner"}
  if _,err=db.SubmitDesignRequest(owner,designTestSubmit("request",DesignHTML));err!=nil {t.Fatal(err)}
 }
 if err=db.Close();err!=nil {t.Fatal(err)}
 db,err=Open(path);if err!=nil {t.Fatal(err)};defer db.Close()
 cursor:="";count:=0;pages:=0
 for {
  rows,next,err:=db.ScanDesignPending(cursor);if err!=nil {t.Fatal(err)}
  count+=len(rows);pages++
  for _,row:=range rows {if row.State!=DesignQueued {t.Fatal("lost queued work")}}
  if next=="" {break};if next==cursor || pages>10 {t.Fatal("unbounded/repeated cursor")};cursor=next
 }
 if count!=60 || pages<2 {t.Fatalf("discovery count=%d pages=%d",count,pages)}
 if _,_,err=db.ScanDesignPending("outside-prefix");err==nil {t.Fatal("invalid cursor accepted")}
 wake:=db.DesignWake();db.WakeDesign();db.WakeDesign()
 select {case <-wake:default:t.Fatal("missing wake")}
 select {case <-wake:t.Fatal("wake not coalesced");default:}
}
