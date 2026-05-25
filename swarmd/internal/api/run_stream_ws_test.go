package api

import (
	"testing"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRunStreamOwnerTransportUsesBackgroundAPIOnlyForExplicitBackground(t *testing.T) {
	if got := runStreamOwnerTransport(runruntime.RunRequest{}); got != "ws" {
		t.Fatalf("foreground websocket owner transport = %q, want ws", got)
	}
	if got := runStreamOwnerTransport(runruntime.RunRequest{Background: true}); got != "background_api" {
		t.Fatalf("background websocket owner transport = %q, want background_api", got)
	}
}

func TestRunStreamLifecycleBackgroundReflectsTransportSemantics(t *testing.T) {
	cases := []struct {
		name      string
		lifecycle *pebblestore.SessionLifecycleSnapshot
		want      bool
	}{
		{name: "nil", lifecycle: nil, want: false},
		{name: "foreground ws", lifecycle: &pebblestore.SessionLifecycleSnapshot{OwnerTransport: "ws"}, want: false},
		{name: "background api", lifecycle: &pebblestore.SessionLifecycleSnapshot{OwnerTransport: "background_api"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runStreamLifecycleIsBackground(tc.lifecycle); got != tc.want {
				t.Fatalf("runStreamLifecycleIsBackground() = %v, want %v", got, tc.want)
			}
		})
	}
}
