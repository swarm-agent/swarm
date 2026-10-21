package sandbox

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Purpose: ReconcileContainers must never mutate another daemon's containers.
// The ownership predicate is the narrowest deterministic layer proving label
// precedence, missing-identity rejection and legacy state-directory matching.
func TestSandboxInstanceOwnership(t *testing.T) {
	own, other := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name, label, source string
		want                bool
	}{
		{"own", instanceIdentity(own), "", true},
		{"other", instanceIdentity(other), "", false},
		{"foreign label overrides mount", instanceIdentity(other), filepath.Join(own, "resolv.conf"), false},
		{"legacy own", "", filepath.Join(own, "resolv.conf"), true},
		{"legacy other", "", filepath.Join(other, "resolv.conf"), false},
		{"unattributed", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"Config": map[string]any{"Labels": map[string]string{labelInstance: tc.label}}, "Mounts": []map[string]string{{"Source": tc.source, "Destination": "/etc/resolv.conf"}}})
			var item instanceInspection
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			if got := ownsInstance(own, item); got != tc.want {
				t.Fatalf("owns=%v want %v", got, tc.want)
			}
			if ownsInstance("", item) {
				t.Fatal("empty state must fail closed")
			}
		})
	}
}
