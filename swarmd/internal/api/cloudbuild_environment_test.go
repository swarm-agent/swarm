package api

import "testing"

// Purpose: SetEnvironmentProvider must accept the Cloud Build provider at the
// startup boundary. This direct test reproduces rejection without a live daemon.
func TestCloudBuildEnvironmentSelection(t *testing.T) {
	if err := (&Server{}).SetEnvironmentProvider("gcp-cloudbuild"); err != nil {
		t.Fatal(err)
	}
}
