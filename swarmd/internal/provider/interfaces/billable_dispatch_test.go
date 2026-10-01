package provideriface

import (
	"context"
	"errors"
	"testing"
)

// Purpose: media preflight must not reserve allowance until the actual billable
// transport, and cancelled work must reject without calling the reservation
// callback. Owner CheckBillableDispatch; interface-level context seam is the
// narrowest deterministic proof of ordering and ordinary uncapped behavior.
func TestBillableDispatchGuard(t *testing.T) {
	calls := 0
	denied := errors.New("allowance exhausted")
	ctx := WithBillableDispatchGuard(context.Background(), func() error { calls++; return denied })
	if calls != 0 {
		t.Fatal("context creation reserved budget")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := CheckBillableDispatch(cancelled); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancel reserved: %d %v", calls, err)
	}
	if err := CheckBillableDispatch(ctx); !errors.Is(err, denied) || calls != 1 {
		t.Fatalf("dispatch guard: %d %v", calls, err)
	}
	if err := CheckBillableDispatch(context.Background()); err != nil {
		t.Fatalf("uncapped: %v", err)
	}
}
