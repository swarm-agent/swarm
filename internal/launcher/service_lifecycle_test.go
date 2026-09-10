package launcher

import (
	"errors"
	"reflect"
	"testing"
)

// Requirement: installing over an active service must activate the candidate;
// systemctl enable --now alone leaves the old process running. This narrow
// lifecycle orchestration test checks ordering and propagation of both failures.
// Actual systemd replacement requires a separate installed-product test.
func TestActivateInstalledCandidate(t *testing.T) {
	failure := errors.New("activation failed")
	for _, stage := range []string{"", "enable", "restart"} {
		t.Run(stage, func(t *testing.T) {
			var calls []string
			invoke := func(name string) func() error {
				return func() error {
					calls = append(calls, name)
					if stage == name {
						return failure
					}
					return nil
				}
			}
			err := activateInstalledCandidate(invoke("enable"), invoke("restart"))
			want := []string{"enable", "restart"}
			if stage == "enable" {
				want = want[:1]
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want %v", calls, want)
			}
			if stage == "" && err != nil {
				t.Fatal(err)
			}
			if stage != "" && !errors.Is(err, failure) {
				t.Fatalf("lost failure: %v", err)
			}
		})
	}
}
