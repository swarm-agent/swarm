package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestFetchLatestStableReleaseSelectsNewestPublishedStable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"0.0.0-dev+9b4254f","draft":false,"prerelease":false,"published_at":"2026-04-22T11:35:12Z"},
			{"tag_name":"v0.1.0","html_url":"https://example.com/v0.1.0","draft":false,"prerelease":false,"published_at":"2026-04-23T10:00:00Z","assets":[{"name":"swarm-v0.1.0-linux-amd64.tar.gz","browser_download_url":"https://example.com/swarm-v0.1.0-linux-amd64.tar.gz","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]},
			{"tag_name":"v0.0.9","draft":false,"prerelease":false,"published_at":"2026-04-22T09:00:00Z"},
			{"tag_name":"v0.1.1-rc1","draft":false,"prerelease":false,"published_at":"2026-04-23T11:30:00Z"},
			{"tag_name":"0.0.0-dev+c2c0cae","draft":false,"prerelease":true,"published_at":"2026-04-23T11:00:00Z"}
		]`))
	}))
	defer server.Close()

	svc := NewService("main", false)
	svc.client = server.Client()
	svc.repoOwner = "ignored"
	svc.repoName = "ignored"
	oldFmt := releasesAPIFormat
	releasesAPIFormat = server.URL + "/%s/%s"
	defer func() { releasesAPIFormat = oldFmt }()

	release, err := svc.fetchLatestStableRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestStableRelease error: %v", err)
	}
	if release.TagName != "v0.1.0" {
		t.Fatalf("expected v0.1.0, got %q", release.TagName)
	}
}

func TestFetchLatestStableReleaseErrorsWhenNoStableReleaseExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"0.0.0-dev+c2c0cae","draft":false,"prerelease":true,"published_at":"2026-04-23T11:00:00Z"},
			{"tag_name":"draft-tag","draft":true,"prerelease":false,"published_at":"2026-04-23T12:00:00Z"}
		]`))
	}))
	defer server.Close()

	svc := NewService("main", false)
	svc.client = server.Client()
	svc.repoOwner = "ignored"
	svc.repoName = "ignored"
	oldFmt := releasesAPIFormat
	releasesAPIFormat = server.URL + "/%s/%s"
	defer func() { releasesAPIFormat = oldFmt }()

	_, err := svc.fetchLatestStableRelease(context.Background())
	if err == nil {
		t.Fatalf("expected error when no stable release exists")
	}
}

func TestFetchLatestStableReleaseCanUseHarnessOverrideForPrerelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.1.0","draft":false,"prerelease":false,"published_at":"2026-04-22T10:00:00Z"},
			{"tag_name":"v0.1.1-test","html_url":"https://example.com/pr","draft":false,"prerelease":true,"published_at":"2026-04-23T10:00:00Z","assets":[{"name":"swarm-v0.1.1-test-linux-amd64.tar.gz","browser_download_url":"https://example.com/swarm-v0.1.1-test-linux-amd64.tar.gz","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}
		]`))
	}))
	defer server.Close()

	t.Setenv(releasesURLTemplateEnv, server.URL+"/%s/%s")
	t.Setenv(includeUnstableReleasesEnv, "true")
	svc := NewService("main", false)
	svc.client = server.Client()

	release, err := svc.fetchLatestStableRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestStableRelease error: %v", err)
	}
	if release.TagName != "v0.1.1-test" {
		t.Fatalf("expected prerelease override v0.1.1-test, got %q", release.TagName)
	}
}
