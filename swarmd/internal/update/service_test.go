package update

import "testing"

func TestShouldSuppressAllowsMainLaneDevVersions(t *testing.T) {
	if shouldSuppress("0.0.0-dev+dd78c1f", "main", false) {
		t.Fatalf("expected main lane dev build to allow update checks")
	}
}

func TestShouldSuppressDevLaneStillSuppresses(t *testing.T) {
	if !shouldSuppress("0.0.0-dev+dd78c1f", "dev", false) {
		t.Fatalf("expected dev lane to suppress update checks")
	}
}

func TestSuppressionReasonMainLaneDevVersionIsNotSuppressed(t *testing.T) {
	if got := suppressionReason("0.0.0-dev+dd78c1f", "main", false); got != "" {
		t.Fatalf("expected empty suppression reason, got %q", got)
	}
}

func TestIsVersionNewerTreatsDistinctDevBuildsAsNewer(t *testing.T) {
	if !isVersionNewer("0.0.0-dev+9b4254f", "0.0.0-dev+dd78c1f") {
		t.Fatalf("expected distinct main prerelease builds to compare as newer for update flow")
	}
}

func TestIsVersionNewerRejectsEqualDevBuilds(t *testing.T) {
	if isVersionNewer("0.0.0-dev+dd78c1f", "0.0.0-dev+dd78c1f") {
		t.Fatalf("expected identical dev builds to not compare as newer")
	}
}
