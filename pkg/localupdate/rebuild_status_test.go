package localupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateJobStatusRoundTripsHostPhases(t *testing.T) {
	dataDir := t.TempDir()
	status := UpdateJobStatus{
		ID:     " job-1 ",
		Kind:   " dev ",
		Status: " running ",
		Hosts: []UpdateJobHostStatus{{
			HostID:       " managed-1 ",
			Name:         " SwarmTarget2 ",
			Role:         " managed ",
			CurrentPhase: " sync ",
			Status:       " running ",
			Phases: []UpdateJobHostPhase{{
				Name:   " sync ",
				Status: " running ",
			}},
		}},
	}
	if err := WriteUpdateJobStatus(dataDir, status); err != nil {
		t.Fatalf("WriteUpdateJobStatus: %v", err)
	}
	got, ok, err := ReadUpdateJobStatusPath(UpdateJobStatusPath(dataDir))
	if err != nil || !ok {
		t.Fatalf("ReadUpdateJobStatusPath ok=%v err=%v", ok, err)
	}
	if got.ID != "job-1" || got.Kind != "dev" || got.Status != "running" {
		t.Fatalf("trimmed status = %#v", got)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].HostID != "managed-1" || got.Hosts[0].Name != "SwarmTarget2" || got.Hosts[0].CurrentPhase != "sync" {
		t.Fatalf("host status = %#v", got.Hosts)
	}
	if len(got.Hosts[0].Phases) != 1 || got.Hosts[0].Phases[0].Name != "sync" || got.Hosts[0].Phases[0].Status != "running" {
		t.Fatalf("host phases = %#v", got.Hosts[0].Phases)
	}
	if filepath.Clean(UpdateJobStatusPath(dataDir)) != filepath.Join(dataDir, updateJobStatusRelativePath) {
		t.Fatalf("unexpected update status path %q", UpdateJobStatusPath(dataDir))
	}
	info, err := os.Stat(UpdateJobStatusPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("status mode = %o, want 600", got)
	}
}

func TestUpdateJobStatusRejectsStaleWriter(t *testing.T) {
	dataDir := t.TempDir()
	if err := WriteUpdateJobStatus(dataDir, UpdateJobStatus{ID: "new-job", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	written, err := WriteUpdateJobStatusIfCurrent(dataDir, "old-job", UpdateJobStatus{ID: "old-job", Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Fatal("stale writer unexpectedly replaced current job")
	}
	got, ok, err := ReadUpdateJobStatusPath(UpdateJobStatusPath(dataDir))
	if err != nil || !ok || got.ID != "new-job" || got.Status != "running" {
		t.Fatalf("status = %#v ok=%v err=%v", got, ok, err)
	}
}
