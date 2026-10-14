// Package outbox exports a machine's signal feed and the results of its jobs
// to storage the owner chooses (a folder, Google Cloud Storage, S3), in the
// layout described in docs/checkpoints/outbox.md. Swarm only appends; every
// object is created once and never changed.
package outbox

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Version is the format version of log records and result manifests.
	Version = 1

	defaultMaxFileMB = 512
	maxMaxFileMB     = 4096
)

var (
	// Object keys and prefixes use only characters that need no escaping on
	// any storage, so a key is the same string everywhere.
	keyPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	machinePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	bucketPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,221}[a-z0-9]$`)
)

// Config is where the outbox writes. It holds credentials; only Describe is
// ever shown.
type Config struct {
	Scheme          string // file, gs or s3
	Dir             string // file
	Bucket          string // gs, s3
	Prefix          string // gs, s3: no leading or trailing slash
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Machine         string
	MaxFileBytes    int64
}

// ConfigFromEnv reads the outbox settings. It reports false when the outbox
// is not configured (SWARM_OUTBOX_URL unset), and an error when it is
// configured wrongly.
func ConfigFromEnv(getenv func(string) string, defaultMachine string) (Config, bool, error) {
	raw := strings.TrimSpace(getenv("SWARM_OUTBOX_URL"))
	if raw == "" {
		return Config{}, false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Config{}, true, errors.New("SWARM_OUTBOX_URL must be file:///DIR, gs://BUCKET/PREFIX or s3://BUCKET/PREFIX")
	}
	cfg := Config{Scheme: parsed.Scheme}
	switch parsed.Scheme {
	case "file":
		if parsed.Host != "" || !filepath.IsAbs(parsed.Path) {
			return Config{}, true, errors.New("SWARM_OUTBOX_URL file:// needs an absolute folder (file:///var/lib/swarmd/outbox)")
		}
		cfg.Dir = filepath.Clean(parsed.Path)
	case "gs", "s3":
		cfg.Bucket = parsed.Host
		if !bucketPattern.MatchString(cfg.Bucket) {
			return Config{}, true, fmt.Errorf("SWARM_OUTBOX_URL: %q is not a bucket name", cfg.Bucket)
		}
		cfg.Prefix = strings.Trim(parsed.Path, "/")
		if cfg.Prefix != "" && !keyPattern.MatchString(cfg.Prefix) {
			return Config{}, true, errors.New("SWARM_OUTBOX_URL: the prefix may use only letters, digits and ._-/")
		}
		cfg.AccessKeyID = strings.TrimSpace(getenv("SWARM_OUTBOX_ACCESS_KEY_ID"))
		cfg.SecretAccessKey = strings.TrimSpace(getenv("SWARM_OUTBOX_SECRET_ACCESS_KEY"))
		if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
			return Config{}, true, errors.New("SWARM_OUTBOX_ACCESS_KEY_ID and SWARM_OUTBOX_SECRET_ACCESS_KEY are required for a bucket")
		}
		if parsed.Scheme == "gs" {
			cfg.Endpoint, cfg.Region = "https://storage.googleapis.com", "auto"
		} else {
			cfg.Region = strings.TrimSpace(getenv("SWARM_OUTBOX_S3_REGION"))
			if cfg.Region == "" {
				cfg.Region = "us-east-1"
			}
			cfg.Endpoint = strings.TrimRight(strings.TrimSpace(getenv("SWARM_OUTBOX_S3_ENDPOINT")), "/")
			if cfg.Endpoint == "" {
				cfg.Endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
			}
			if endpoint, err := url.Parse(cfg.Endpoint); err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
				return Config{}, true, errors.New("SWARM_OUTBOX_S3_ENDPOINT must be an http(s) URL")
			}
		}
	default:
		return Config{}, true, errors.New("SWARM_OUTBOX_URL must be file:///DIR, gs://BUCKET/PREFIX or s3://BUCKET/PREFIX")
	}
	cfg.Machine = strings.ToLower(strings.TrimSpace(getenv("SWARM_OUTBOX_MACHINE")))
	if cfg.Machine == "" {
		cfg.Machine = machineName(defaultMachine)
	}
	if !machinePattern.MatchString(cfg.Machine) {
		return Config{}, true, fmt.Errorf("outbox machine name %q must be lowercase letters, digits and - (set SWARM_OUTBOX_MACHINE)", cfg.Machine)
	}
	maxMB := defaultMaxFileMB
	if rawMB := strings.TrimSpace(getenv("SWARM_OUTBOX_MAX_FILE_MB")); rawMB != "" {
		parsedMB, err := strconv.Atoi(rawMB)
		if err != nil || parsedMB < 1 || parsedMB > maxMaxFileMB {
			return Config{}, true, fmt.Errorf("SWARM_OUTBOX_MAX_FILE_MB must be 1 to %d", maxMaxFileMB)
		}
		maxMB = parsedMB
	}
	cfg.MaxFileBytes = int64(maxMB) << 20
	return cfg, true, nil
}

// machineName turns a configured Swarm name or host name into a layout name.
func machineName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name, _ = os.Hostname()
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

// Describe names the destination without credentials.
func (c Config) Describe() string {
	switch c.Scheme {
	case "file":
		return "file://" + c.Dir
	case "gs", "s3":
		if c.Prefix == "" {
			return c.Scheme + "://" + c.Bucket
		}
		return c.Scheme + "://" + c.Bucket + "/" + c.Prefix
	}
	return ""
}

// key joins the destination prefix and a layout path.
func (c Config) key(path string) string {
	if c.Prefix == "" {
		return path
	}
	return c.Prefix + "/" + path
}

// Open returns the destination the config names.
func (c Config) Open() (Destination, error) {
	switch c.Scheme {
	case "file":
		return newFolder(c.Dir)
	case "gs", "s3":
		return newBucket(c), nil
	}
	return nil, errors.New("outbox is not configured")
}
