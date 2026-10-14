package runtime

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A sandbox with secrets started by an earlier daemon process holds a gateway
// token this process never issued; its fingerprint must differ so the sandbox
// is rebuilt (regression: after an update the reused sandbox got HTTP 407 on
// every request).
func TestGrantFingerprintChangesWithTheDaemonProcess(t *testing.T) {
	grants := []pebblestore.ActiveGrant{{Account: "acct", Slot: pebblestore.SecretSlot{Name: "KEY", Hosts: []string{"api.example.com"}}}}
	first := grantFingerprint(grants)
	if again := grantFingerprint(grants); again != first {
		t.Fatal("the same grants in the same process must keep their fingerprint")
	}
	saved := gatewayEpoch
	t.Cleanup(func() { gatewayEpoch = saved })
	gatewayEpoch = "another-process"
	if grantFingerprint(grants) == first {
		t.Fatal("a new daemon process must change the fingerprint so old sandboxes are rebuilt")
	}
}
