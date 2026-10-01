package pebblestore

import (
	"errors"
	"fmt"
	"testing"
)

// Purpose: queued admission is bounded without provider-slot reservations.
// submitDesignRequestInBatch must reject overflow before any artifact/receipt
// writes, while permitting idempotent replay at capacity. Real Pebble is the
// narrowest layer proving the serialized queue limit and atomic no-write outcome.
func TestDesignAdmissionBounded(t *testing.T) {
	s := openTaskProgramTestStore(t)
	p := designTestOwner
	var first DesignSubmit
	for i := 0; i < MaxPendingDesignRequests; i++ {
		in := designTestSubmit(fmt.Sprintf("queue-%d",i),DesignHTML)
		in.Candidates[0].ArtifactID = fmt.Sprintf("artifact-%d",i)
		if i == 0 { first = in }
		if _, err := s.SubmitDesignRequest(p,in); err != nil { t.Fatal(i,err) }
	}
	if _, err := s.SubmitDesignRequest(p,first); err != nil { t.Fatal("replay at capacity",err) }
	overflow := designTestSubmit("overflow",DesignHTML)
	overflow.Candidates[0].ArtifactID = "overflow-artifact"
	if _, err := s.SubmitDesignRequest(p,overflow); !errors.Is(err,ErrDesignConflict) { t.Fatal("admission",err) }
	if _, err := s.GetDesignRequest(p,overflow.RequestID); !errors.Is(err,ErrDesignNotFound) { t.Fatal("partial request",err) }
	if _, err := s.GetDesignArtifact(p,"overflow-artifact"); !errors.Is(err,ErrDesignNotFound) { t.Fatal("partial artifact",err) }
	if _, err := s.RecordDesignAttempt(p,first.RequestID,DesignAttemptMutation{IdempotencyKey:"cancel",ExpectedRevision:1,Candidate:0,State:DesignCancelRequested}); err != nil { t.Fatal(err) }
	if _, err := s.SubmitDesignRequest(p,overflow); err != nil { t.Fatal("rejected write retained receipt or queue slot",err) }
}
