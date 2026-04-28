package update

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	sharedbuildinfo "swarm-refactor/swarmtui/pkg/buildinfo"
)

const (
	defaultRepoOwner           = "swarm-agent"
	defaultRepoName            = "swarm"
	defaultCacheTTL            = 30 * time.Minute
	defaultLookupTimeout       = 5 * time.Second
	defaultUserAgent           = "swarmd-update-status"
	linuxAMD64ArchiveFmt       = "swarm-%s-linux-amd64.tar.gz"
	comparisonSource           = "github_releases"
	releasesURLTemplateEnv     = "SWARM_UPDATE_RELEASES_URL_TEMPLATE"
	includeUnstableReleasesEnv = "SWARM_UPDATE_INCLUDE_UNSTABLE_RELEASES"
)

var (
	releasesAPIFormat = "https://api.github.com/repos/%s/%s/releases?per_page=100"
	stableTagPattern  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
)

type Service struct {
	client      *http.Client
	repoOwner   string
	repoName    string
	cacheTTL    time.Duration
	timeout     time.Duration
	userAgent   string
	lane        string
	devMode     bool
	current     string
	currentFunc func() string

	mu     sync.Mutex
	cached cachedStatus
}

type cachedStatus struct {
	status    Status
	expiresAt time.Time
}

type Status struct {
	CurrentVersion   string `json:"current_version"`
	CurrentLane      string `json:"current_lane,omitempty"`
	DevMode          bool   `json:"dev_mode"`
	Suppressed       bool   `json:"suppressed"`
	Reason           string `json:"reason,omitempty"`
	CheckedAtUnixMS  int64  `json:"checked_at_unix_ms,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	LatestURL        string `json:"latest_url,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	ComparisonSource string `json:"comparison_source,omitempty"`
	Error            string `json:"error,omitempty"`
	Stale            bool   `json:"stale,omitempty"`
}

type ApplyPlan struct {
	CurrentVersion   string `json:"current_version"`
	CurrentLane      string `json:"current_lane,omitempty"`
	TargetVersion    string `json:"target_version"`
	ReleaseURL       string `json:"release_url,omitempty"`
	AssetName        string `json:"asset_name"`
	AssetURL         string `json:"asset_url"`
	SHA256           string `json:"sha256"`
	ComparisonSource string `json:"comparison_source,omitempty"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

func NewService(lane string, devMode bool) *Service {
	return &Service{
		client:      &http.Client{Timeout: defaultLookupTimeout},
		repoOwner:   defaultRepoOwner,
		repoName:    defaultRepoName,
		cacheTTL:    defaultCacheTTL,
		timeout:     defaultLookupTimeout,
		userAgent:   defaultUserAgent,
		lane:        strings.ToLower(strings.TrimSpace(lane)),
		devMode:     devMode,
		current:     sharedbuildinfo.DisplayVersion(),
		currentFunc: sharedbuildinfo.DisplayVersion,
	}
}

func (s *Service) Status(ctx context.Context, force bool) Status {
	currentVersion := strings.TrimSpace(s.current)
	if s.currentFunc != nil {
		currentVersion = strings.TrimSpace(s.currentFunc())
	}
	s.mu.Lock()
	if s.current != currentVersion {
		s.current = currentVersion
		s.cached = cachedStatus{}
	}
	s.mu.Unlock()
	base := Status{
		CurrentVersion: currentVersion,
		CurrentLane:    strings.TrimSpace(s.lane),
		DevMode:        s.devMode,
	}
	if base.CurrentVersion == "" {
		base.CurrentVersion = sharedbuildinfo.DisplayVersion()
	}
	if shouldSuppress(base.CurrentVersion, base.CurrentLane, s.devMode) {
		base.Suppressed = true
		base.Reason = suppressionReason(base.CurrentVersion, base.CurrentLane, s.devMode)
		return base
	}

	now := time.Now()
	s.mu.Lock()
	cached := s.cached
	if !force && !cached.expiresAt.IsZero() && now.Before(cached.expiresAt) {
		status := cached.status
		s.mu.Unlock()
		return status
	}
	previous := cached.status
	s.mu.Unlock()

	status := base
	status.ComparisonSource = comparisonSource
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	release, err := s.fetchLatestStableRelease(ctx)
	status.CheckedAtUnixMS = time.Now().UnixMilli()
	if err != nil {
		status.CheckedAtUnixMS = 0
		status.Error = err.Error()
		if previous.CheckedAtUnixMS != 0 {
			status = previous
			status.Stale = true
			status.Error = err.Error()
			status.CheckedAtUnixMS = time.Now().UnixMilli()
		}
		s.store(status)
		return status
	}

	status.LatestVersion = strings.TrimSpace(release.TagName)
	if release.Draft {
		status.LatestVersion = ""
		status.Error = "latest release is marked draft"
		s.store(status)
		return status
	}
	if release.Prerelease {
		status.Reason = "latest_prerelease"
	}
	status.LatestURL = strings.TrimSpace(release.HTMLURL)
	status.UpdateAvailable = isVersionNewer(status.LatestVersion, status.CurrentVersion)
	if !status.UpdateAvailable && status.Reason == "latest_prerelease" {
		status.Reason = ""
	}
	status.Stale = false
	status.Error = ""
	s.store(status)
	return status
}

func (s *Service) Apply(ctx context.Context) (ApplyPlan, error) {
	currentVersion := strings.TrimSpace(s.current)
	if s.currentFunc != nil {
		currentVersion = strings.TrimSpace(s.currentFunc())
	}
	plan := ApplyPlan{
		CurrentVersion:   currentVersion,
		CurrentLane:      strings.TrimSpace(s.lane),
		ComparisonSource: comparisonSource,
	}
	if plan.CurrentVersion == "" {
		plan.CurrentVersion = sharedbuildinfo.DisplayVersion()
	}
	if shouldSuppress(plan.CurrentVersion, plan.CurrentLane, s.devMode) {
		return ApplyPlan{}, fmt.Errorf("update apply is suppressed: %s", suppressionReason(plan.CurrentVersion, plan.CurrentLane, s.devMode))
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	release, err := s.fetchLatestStableRelease(ctx)
	if err != nil {
		return ApplyPlan{}, err
	}
	if release.Draft {
		return ApplyPlan{}, errors.New("latest release is marked draft")
	}
	plan.TargetVersion = strings.TrimSpace(release.TagName)
	plan.ReleaseURL = strings.TrimSpace(release.HTMLURL)
	if !isVersionNewer(plan.TargetVersion, plan.CurrentVersion) {
		return ApplyPlan{}, errors.New("no update available")
	}

	plan.AssetName = fmt.Sprintf(linuxAMD64ArchiveFmt, plan.TargetVersion)
	asset, ok := findReleaseAsset(release.Assets, plan.AssetName)
	if !ok {
		return ApplyPlan{}, fmt.Errorf("release %s is missing asset %s", plan.TargetVersion, plan.AssetName)
	}
	plan.AssetURL = strings.TrimSpace(asset.BrowserDownloadURL)
	if plan.AssetURL == "" {
		return ApplyPlan{}, fmt.Errorf("release asset %s is missing browser_download_url", plan.AssetName)
	}
	sha256, err := s.resolveAssetSHA256(ctx, asset, release.Assets, plan.AssetName)
	if err != nil {
		return ApplyPlan{}, err
	}
	plan.SHA256 = sha256
	return plan, nil
}

func (s *Service) store(status Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = cachedStatus{status: status, expiresAt: time.Now().Add(s.cacheTTL)}
}

func (s *Service) fetchLatestStableRelease(ctx context.Context) (githubRelease, error) {
	url := releasesAPIURL(s.repoOwner, s.repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("build github releases request: %w", err)
	}
	if ua := strings.TrimSpace(s.userAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("lookup releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("lookup releases: github returned %s", resp.Status)
	}
	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return githubRelease{}, fmt.Errorf("decode releases: %w", err)
	}
	eligible := make([]githubRelease, 0, len(releases))
	for _, release := range releases {
		if release.Draft {
			continue
		}
		tagName := strings.TrimSpace(release.TagName)
		if tagName == "" {
			continue
		}
		if !includeUnstableReleases() && (release.Prerelease || !stableTagPattern.MatchString(tagName)) {
			continue
		}
		if strings.TrimSpace(release.PublishedAt) == "" {
			continue
		}
		eligible = append(eligible, release)
	}
	if len(eligible) == 0 {
		return githubRelease{}, errors.New("no published stable release found")
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].PublishedAt > eligible[j].PublishedAt
	})
	return eligible[0], nil
}

func releasesAPIURL(owner, repo string) string {
	tmpl := strings.TrimSpace(os.Getenv(releasesURLTemplateEnv))
	if tmpl == "" {
		tmpl = releasesAPIFormat
	}
	return fmt.Sprintf(tmpl, owner, repo)
}

func includeUnstableReleases() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(includeUnstableReleasesEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Service) resolveAssetSHA256(ctx context.Context, archiveAsset githubReleaseAsset, assets []githubReleaseAsset, assetName string) (string, error) {
	if digest := normalizeSHA256Digest(archiveAsset.Digest); digest != "" {
		return digest, nil
	}
	checksumName := assetName + ".sha256"
	checksumAsset, ok := findReleaseAsset(assets, checksumName)
	if !ok {
		return "", fmt.Errorf("release asset %s is missing checksum asset %s", assetName, checksumName)
	}
	raw, err := s.fetchAssetText(ctx, checksumAsset.BrowserDownloadURL)
	if err != nil {
		return "", err
	}
	sha256, err := parseSHA256Asset(raw, assetName)
	if err != nil {
		return "", fmt.Errorf("parse checksum asset %s: %w", checksumName, err)
	}
	return sha256, nil
}

func (s *Service) fetchAssetText(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("asset url must not be empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build asset request: %w", err)
	}
	if ua := strings.TrimSpace(s.userAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "text/plain, application/octet-stream;q=0.9, application/vnd.github+json;q=0.8")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download asset metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download asset metadata: github returned %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read asset metadata: %w", err)
	}
	return string(body), nil
}

func findReleaseAsset(assets []githubReleaseAsset, name string) (githubReleaseAsset, bool) {
	name = strings.TrimSpace(name)
	for _, asset := range assets {
		if strings.TrimSpace(asset.Name) == name {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func parseSHA256Asset(raw, assetName string) (string, error) {
	assetBase := path.Base(strings.TrimSpace(assetName))
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		digest := normalizeSHA256Digest(fields[0])
		if digest == "" {
			continue
		}
		if len(fields) == 1 {
			return digest, nil
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if path.Base(name) == assetBase {
			return digest, nil
		}
	}
	return "", errors.New("missing sha256 digest")
}

func normalizeSHA256Digest(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func shouldSuppress(currentVersion, lane string, devMode bool) bool {
	_ = currentVersion
	if devMode {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(lane), "dev")
}

func suppressionReason(currentVersion, lane string, devMode bool) string {
	_ = currentVersion
	switch {
	case devMode:
		return "dev_mode"
	case strings.EqualFold(strings.TrimSpace(lane), "dev"):
		return "dev_lane"
	default:
		return ""
	}
}

func isVersionNewer(latest, current string) bool {
	latestRaw := strings.TrimSpace(latest)
	currentRaw := strings.TrimSpace(current)
	if latestRaw == "" || currentRaw == "" {
		return false
	}
	if strings.EqualFold(latestRaw, currentRaw) {
		return false
	}
	if sharedbuildinfo.IsDevVersionString(latestRaw) && sharedbuildinfo.IsDevVersionString(currentRaw) {
		return true
	}
	latest = normalizeVersion(latestRaw)
	current = normalizeVersion(currentRaw)
	if latest == "" || current == "" {
		return false
	}
	if latest == current {
		return false
	}
	latestParts, latestOK := parseVersionParts(latest)
	currentParts, currentOK := parseVersionParts(current)
	if latestOK && currentOK {
		for i := 0; i < len(latestParts) && i < len(currentParts); i++ {
			if latestParts[i] > currentParts[i] {
				return true
			}
			if latestParts[i] < currentParts[i] {
				return false
			}
		}
		return false
	}
	return latest != current
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "v")
	if idx := strings.IndexAny(value, "+-"); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func parseVersionParts(value string) ([3]int, bool) {
	var parts [3]int
	segments := strings.Split(value, ".")
	if len(segments) != 3 {
		return parts, false
	}
	for i := range segments {
		segment := strings.TrimSpace(segments[i])
		if segment == "" {
			return parts, false
		}
		var n int
		for _, r := range segment {
			if r < '0' || r > '9' {
				return parts, false
			}
			n = n*10 + int(r-'0')
		}
		parts[i] = n
	}
	return parts, true
}
