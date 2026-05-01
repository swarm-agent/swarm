package remotedeploy

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRemoteUpdateRecordEligibleRequiresAttachedSSHSession(t *testing.T) {
	base := pebblestore.RemoteDeploySessionRecord{
		ID:               "remote-one",
		Status:           "attached",
		SSHSessionTarget: "remote.example",
		RemoteRoot:       remoteRoot("remote-one"),
	}
	cases := []struct {
		name   string
		mutate func(*pebblestore.RemoteDeploySessionRecord)
		want   bool
	}{
		{name: "attached", want: true},
		{name: "waiting", mutate: func(r *pebblestore.RemoteDeploySessionRecord) { r.Status = "waiting_for_approval" }},
		{name: "approved", mutate: func(r *pebblestore.RemoteDeploySessionRecord) { r.Status = "approved" }},
		{name: "failed", mutate: func(r *pebblestore.RemoteDeploySessionRecord) { r.Status = "failed" }},
		{name: "missing ssh", mutate: func(r *pebblestore.RemoteDeploySessionRecord) { r.SSHSessionTarget = "" }},
		{name: "missing root", mutate: func(r *pebblestore.RemoteDeploySessionRecord) { r.RemoteRoot = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			if tc.mutate != nil {
				tc.mutate(&record)
			}
			if got := remoteUpdateRecordEligible(record); got != tc.want {
				t.Fatalf("remoteUpdateRecordEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRemoteDevReplacementScriptStopsBeforeRenameAndRestoresOnFailure(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                      "remote-one",
		Name:                    "remote-one",
		RemoteRoot:              remoteRoot("remote-one"),
		RemoteRuntime:           "docker",
		TransportMode:           startupconfig.NetworkModeLAN,
		RemoteAdvertiseHost:     "10.0.0.2",
		SudoMode:                "sudo",
		SSHSessionTarget:        "remote.example",
		ImageRef:                "localhost/swarm-remote-child:old",
		ImageDeliveryMode:       remoteImageDeliveryArchive,
		ImagePrefix:             remoteImageNamePrefix,
		ImageSignature:          "old",
		ImageArchiveBytes:       1,
		SystemdAvailable:        false,
		RemoteNetworkCandidates: []string{"10.0.0.2"},
	}
	artifact := remoteRuntimeArtifact{
		ImageRef:     "localhost/swarm-remote-child:new",
		ArchiveName:  remoteImageArchiveName(startupconfig.NetworkModeLAN),
		ArchivePath:  "/tmp/swarm-image-new.tar",
		ArchiveBytes: 1,
		Signature:    "new",
	}
	script := remoteDevReplacementScript(record, artifact)
	for _, needle := range []string{
		`runtime_cmd stop "$container_name"`,
		`runtime_cmd rename "$container_name" "$backup_name"`,
		`runtime_cmd rename "$backup_name" "$container_name"`,
		`cp "$backup_start_script" "$start_script"`,
		`runtime_cmd start "$container_name"`,
		`export SWARM_STARTUP_MODE=box`,
		`-e "SWARM_STARTUP_MODE=box"`,
		`REMOTE_UPDATE_ERROR=replacement-not-running`,
		`REMOTE_UPDATE_ERROR=replacement-not-ready`,
		`REMOTE_UPDATE_STATE=replaced`,
		`SWARM_REMOTE_URL=%s`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("replacement script missing %q\n%s", needle, script)
		}
	}
	stopIndex := strings.Index(script, `runtime_cmd stop "$container_name"`)
	renameIndex := strings.Index(script, `runtime_cmd rename "$container_name" "$backup_name"`)
	if stopIndex < 0 || renameIndex < 0 || stopIndex > renameIndex {
		t.Fatalf("expected stop before rename\n%s", script)
	}
}

func TestParseRemoteDevReplacementOutput(t *testing.T) {
	parsed := parseRemoteDevReplacementOutput("noise\nREMOTE_UPDATE_PREVIOUS_IMAGE=old\nREMOTE_UPDATE_STATE=replaced\nREMOTE_UPDATE_ENDPOINT=https://remote\n")
	if parsed.State != "replaced" || parsed.PreviousImageRef != "old" || parsed.RemoteEndpoint != "https://remote" {
		t.Fatalf("parsed output = %+v", parsed)
	}
}

func TestRemoteUpdateJobSummaryCounts(t *testing.T) {
	var result UpdateJobResult
	result.addUpdateJobItem(UpdateJobItem{State: "replaced"})
	result.addUpdateJobItem(UpdateJobItem{State: "skipped", Reason: "already-current"})
	result.addUpdateJobItem(UpdateJobItem{State: "failed"})
	if result.Summary.Total != 3 || result.Summary.Replaced != 1 || result.Summary.Skipped != 1 || result.Summary.AlreadyCurrent != 1 || result.Summary.Failed != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}
