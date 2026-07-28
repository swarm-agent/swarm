package pebblestore

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

// TailscaleServeAllowlistRecord is the machine-global set of origins approved
// for desktop access. It intentionally lives outside account and UI settings.
type TailscaleServeAllowlistRecord struct {
	Origins   []string `json:"origins"`
	Revision  uint64   `json:"revision"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

type TailscaleServeAllowlistStore struct {
	store *Store
}

func NewTailscaleServeAllowlistStore(store *Store) *TailscaleServeAllowlistStore {
	return &TailscaleServeAllowlistStore{store: store}
}

// NormalizeTailscaleServeOrigin accepts only an HTTPS origin on a concrete
// .ts.net DNS name. Paths, credentials, ports, queries, and fragments are not
// part of an origin approved by this store and are rejected.
func NormalizeTailscaleServeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("tailscale serve origin is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse tailscale serve origin: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", errors.New("tailscale serve origin must use https")
	}
	if parsed.User != nil {
		return "", errors.New("tailscale serve origin must not include credentials")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("tailscale serve origin must include a hostname")
	}
	if parsed.Port() != "" {
		return "", errors.New("tailscale serve origin must not include a port")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("tailscale serve origin must not include a path")
	}
	if parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("tailscale serve origin must not include a query or fragment")
	}

	host := strings.ToLower(parsed.Hostname())
	if parsed.Host != parsed.Hostname() && !strings.EqualFold(parsed.Host, parsed.Hostname()) {
		return "", errors.New("tailscale serve origin contains an invalid authority")
	}
	if !strings.HasSuffix(host, ".ts.net") || host == "ts.net" {
		return "", errors.New("tailscale serve origin hostname must end in .ts.net")
	}
	if err := validateTailscaleDNSName(host); err != nil {
		return "", err
	}
	return "https://" + host, nil
}

func validateTailscaleDNSName(host string) error {
	if len(host) > 253 {
		return errors.New("tailscale serve origin hostname is too long")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("tailscale serve origin contains an invalid DNS label")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("tailscale serve origin contains an invalid DNS label")
			}
		}
	}
	return nil
}

func (s *TailscaleServeAllowlistStore) Get() (TailscaleServeAllowlistRecord, bool, error) {
	if s == nil || s.store == nil {
		return TailscaleServeAllowlistRecord{}, false, nil
	}
	var record TailscaleServeAllowlistRecord
	ok, err := s.store.GetJSON(KeyTailscaleServeAllowlistDefault, &record)
	if err != nil || !ok {
		return TailscaleServeAllowlistRecord{}, ok, err
	}
	record, err = normalizeTailscaleServeAllowlistRecord(record)
	if err != nil {
		return TailscaleServeAllowlistRecord{}, false, fmt.Errorf("validate tailscale serve allowlist: %w", err)
	}
	return record, true, nil
}

func (s *TailscaleServeAllowlistStore) Add(origin string) (TailscaleServeAllowlistRecord, bool, error) {
	normalized, err := NormalizeTailscaleServeOrigin(origin)
	if err != nil {
		return TailscaleServeAllowlistRecord{}, false, err
	}
	return s.mutate(func(origins []string) ([]string, bool) {
		index := sort.SearchStrings(origins, normalized)
		if index < len(origins) && origins[index] == normalized {
			return origins, false
		}
		origins = append(origins, "")
		copy(origins[index+1:], origins[index:])
		origins[index] = normalized
		return origins, true
	})
}

func (s *TailscaleServeAllowlistStore) Remove(origin string) (TailscaleServeAllowlistRecord, bool, error) {
	normalized, err := NormalizeTailscaleServeOrigin(origin)
	if err != nil {
		return TailscaleServeAllowlistRecord{}, false, err
	}
	return s.mutate(func(origins []string) ([]string, bool) {
		index := sort.SearchStrings(origins, normalized)
		if index == len(origins) || origins[index] != normalized {
			return origins, false
		}
		return append(origins[:index:index], origins[index+1:]...), true
	})
}

func (s *TailscaleServeAllowlistStore) mutate(change func([]string) ([]string, bool)) (TailscaleServeAllowlistRecord, bool, error) {
	if s == nil || s.store == nil {
		return TailscaleServeAllowlistRecord{}, false, errors.New("tailscale serve allowlist store is not configured")
	}
	s.store.tailscaleAllowlistMu.Lock()
	defer s.store.tailscaleAllowlistMu.Unlock()

	record, ok, err := s.Get()
	if err != nil {
		return TailscaleServeAllowlistRecord{}, false, err
	}
	if !ok {
		record.Origins = []string{}
	}
	record.Origins, ok = change(record.Origins)
	if !ok {
		return record, false, nil
	}
	if record.Revision == math.MaxUint64 {
		return TailscaleServeAllowlistRecord{}, false, errors.New("tailscale serve allowlist revision overflow")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt == 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	record.Revision++
	if err := s.store.PutJSON(KeyTailscaleServeAllowlistDefault, record); err != nil {
		return TailscaleServeAllowlistRecord{}, false, err
	}
	return record, true, nil
}

func normalizeTailscaleServeAllowlistRecord(record TailscaleServeAllowlistRecord) (TailscaleServeAllowlistRecord, error) {
	origins := make([]string, 0, len(record.Origins))
	seen := make(map[string]struct{}, len(record.Origins))
	for _, origin := range record.Origins {
		normalized, err := NormalizeTailscaleServeOrigin(origin)
		if err != nil {
			return TailscaleServeAllowlistRecord{}, err
		}
		if _, exists := seen[normalized]; exists {
			return TailscaleServeAllowlistRecord{}, fmt.Errorf("duplicate tailscale serve origin %q", normalized)
		}
		seen[normalized] = struct{}{}
		origins = append(origins, normalized)
	}
	sort.Strings(origins)
	record.Origins = origins
	if record.CreatedAt < 0 || record.UpdatedAt < 0 {
		return TailscaleServeAllowlistRecord{}, errors.New("tailscale serve allowlist timestamps must not be negative")
	}
	if record.UpdatedAt != 0 && record.CreatedAt > record.UpdatedAt {
		return TailscaleServeAllowlistRecord{}, errors.New("tailscale serve allowlist created_at is after updated_at")
	}
	return record, nil
}
