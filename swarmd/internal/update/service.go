package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sharedbuildinfo "swarm-refactor/swarmtui/pkg/buildinfo"
)

const (
	defaultRepoOwner       = "swarm-agent"
	defaultRepoName        = "swarm"
	defaultCacheTTL        = 30 * time.Minute
	defaultLookupTimeout   = 5 * time.Second
	defaultUserAgent       = "swarmd-update-status"
	latestReleaseAPIFormat = "https://api.github.com/repos/%s/%s/releases/latest"
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

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func NewService(lane string, devMode bool) *Service {
	return &Service{
		client: &http.Client{Timeout: defaultLookupTimeout},
		repoOwner: defaultRepoOwner,
		repoName: defaultRepoName,
		cacheTTL: defaultCacheTTL,
		timeout: defaultLookupTimeout,
		userAgent: defaultUserAgent,
		lane: strings.ToLower(strings.TrimSpace(lane)),
		devMode: devMode,
		current: sharedbuildinfo.DisplayVersion(),
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
	status.ComparisonSource = "github_releases"
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	release, err := s.fetchLatestRelease(ctx)
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

func (s *Service) store(status Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = cachedStatus{status: status, expiresAt: time.Now().Add(s.cacheTTL)}
}

func (s *Service) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	url := fmt.Sprintf(latestReleaseAPIFormat, s.repoOwner, s.repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("build github latest-release request: %w", err)
	}
	if ua := strings.TrimSpace(s.userAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("lookup latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("lookup latest release: github returned %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return githubRelease{}, errors.New("latest release missing tag_name")
	}
	return release, nil
}

func shouldSuppress(currentVersion, lane string, devMode bool) bool {
	if devMode {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(lane), "dev") {
		return true
	}
	return sharedbuildinfo.IsDevVersionString(currentVersion)
}

func suppressionReason(currentVersion, lane string, devMode bool) string {
	switch {
	case devMode:
		return "dev_mode"
	case strings.EqualFold(strings.TrimSpace(lane), "dev"):
		return "dev_lane"
	case sharedbuildinfo.IsDevVersionString(currentVersion):
		return "dev_version"
	default:
		return ""
	}
}

func isVersionNewer(latest, current string) bool {
	latest = normalizeVersion(latest)
	current = normalizeVersion(current)
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
