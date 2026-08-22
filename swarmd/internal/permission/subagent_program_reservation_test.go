package permission

import "testing"

func TestReserveSubagentProgramCountsOneInvocationAndOnlyReadyCapacity(t *testing.T) {
	svc, _ := openSubagentReservationTestServices(t)
	result, err := svc.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "parent", AccountScopeID: "account", RunID: "run", CallID: "program-call", ManifestHash: "program-hash",
		LaunchCount: 6, Program: true, ReadyCount: 4, MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatalf("reserve program: %v", err)
	}
	if result.Decision != SubagentReservationApprove || !result.Reservation.Program || result.Reservation.LaunchCount != 6 || result.Reservation.ActiveCount != 2 {
		t.Fatalf("reservation = %#v", result)
	}
	again, err := svc.ReserveSubagentWave(SubagentReservationRequest{
		SessionID: "parent", AccountScopeID: "account", RunID: "run", CallID: "program-call", ManifestHash: "program-hash",
		LaunchCount: 6, Program: true, ReadyCount: 4, MaxConcurrency: 2,
	})
	if err != nil || again.Reservation.CallID != result.Reservation.CallID {
		t.Fatalf("idempotent reservation = %#v err=%v", again, err)
	}
}
