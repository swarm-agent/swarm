package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

// A signal sink is where this machine forwards its signal feed: an HTTPS
// endpoint (an alerting service, a fleet monitor) that the owner configures.
// The machine only sends; nothing is received. Each delivery is signed with
// the sink's secret, and the sink's cursor advances only after the endpoint
// accepts a batch, so an outage delays signals instead of losing them.
const (
	KeySignalSinkPrefix         = "signal_sink/"
	MaxSignalSinksPerAccount    = 8
	DefaultSinkHeartbeatSeconds = 60
	MinSinkHeartbeatSeconds     = 30
	MaxSinkHeartbeatSeconds     = 3600
)

var ErrSignalSinkNotFound = errors.New("signal sink not found")

type SignalSink struct {
	ID               string   `json:"id"`
	Account          string   `json:"account"`
	Name             string   `json:"name"`
	URL              string   `json:"url"`
	KindPrefixes     []string `json:"kinds,omitempty"`
	MinSeverity      string   `json:"min_severity,omitempty"`
	HeartbeatSeconds int      `json:"heartbeat_seconds"`
	// Secret signs deliveries. It is returned once, when the sink is created.
	Secret          string `json:"secret,omitempty"`
	Cursor          uint64 `json:"cursor"`
	CreatedAt       int64  `json:"created_at"`
	LastDeliveredAt int64  `json:"last_delivered_at,omitempty"`
	LastAttemptAt   int64  `json:"last_attempt_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	Failures        int    `json:"failures,omitempty"`
}

// Public returns the sink without its secret.
func (s SignalSink) Public() SignalSink {
	s.Secret = ""
	return s
}

type SignalSinkStore struct {
	store *Store
	now   func() time.Time
}

func NewSignalSinkStore(store *Store) *SignalSinkStore {
	return &SignalSinkStore{store: store, now: time.Now}
}

func signalSinkKey(account, id string) string {
	return KeySignalSinkPrefix + keyPart(account) + "/" + keyPart(id)
}

// ValidateSignalSinkURL accepts https URLs, and http only to this machine's
// loopback (a local receiver or test). User info, fragments and non-default
// schemes are refused.
func ValidateSignalSinkURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("sink url must be an absolute https URL")
	}
	if u.User != nil || u.Fragment != "" {
		return "", errors.New("sink url must not contain credentials or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("sink url must use https (http is allowed only to this machine's loopback)")
		}
	default:
		return "", errors.New("sink url must use https")
	}
	return u.String(), nil
}

// Create validates and stores a new sink. The feed is forwarded from its
// current end: a new sink receives signals raised after it was created.
func (s *SignalSinkStore) Create(account, name, rawURL string, kinds []string, minSeverity string, heartbeatSeconds int, startAfter uint64) (SignalSink, error) {
	account = strings.TrimSpace(account)
	name = boundRunes(oneLine(name), 64)
	if name == "" {
		return SignalSink{}, errors.New("sink name is required")
	}
	sinkURL, err := ValidateSignalSinkURL(rawURL)
	if err != nil {
		return SignalSink{}, err
	}
	minSeverity = strings.ToLower(strings.TrimSpace(minSeverity))
	if minSeverity != "" && !ValidSignalSeverity(minSeverity) {
		return SignalSink{}, errors.New("min_severity must be info, warning or critical")
	}
	cleanKinds := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if !signalKindPrefixPattern.MatchString(kind) {
			return SignalSink{}, fmt.Errorf("kind %q must be a dotted lowercase kind or prefix, such as agent or run.failed", kind)
		}
		cleanKinds = append(cleanKinds, kind)
	}
	if heartbeatSeconds == 0 {
		heartbeatSeconds = DefaultSinkHeartbeatSeconds
	}
	if heartbeatSeconds < MinSinkHeartbeatSeconds || heartbeatSeconds > MaxSinkHeartbeatSeconds {
		return SignalSink{}, fmt.Errorf("heartbeat_seconds must be %d to %d", MinSinkHeartbeatSeconds, MaxSinkHeartbeatSeconds)
	}
	existing, err := s.List(account)
	if err != nil {
		return SignalSink{}, err
	}
	if len(existing) >= MaxSignalSinksPerAccount {
		return SignalSink{}, fmt.Errorf("at most %d sinks per account", MaxSignalSinksPerAccount)
	}
	var idBytes, secretBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return SignalSink{}, err
	}
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return SignalSink{}, err
	}
	sink := SignalSink{
		ID:               "sink_" + hex.EncodeToString(idBytes[:8]),
		Account:          account,
		Name:             name,
		URL:              sinkURL,
		KindPrefixes:     cleanKinds,
		MinSeverity:      minSeverity,
		HeartbeatSeconds: heartbeatSeconds,
		Secret:           "sss_" + hex.EncodeToString(secretBytes[:]),
		Cursor:           startAfter,
		CreatedAt:        s.now().UnixMilli(),
	}
	return sink, s.store.PutJSON(signalSinkKey(account, sink.ID), sink)
}

func (s *SignalSinkStore) Get(account, id string) (SignalSink, bool, error) {
	var sink SignalSink
	ok, err := s.store.GetJSON(signalSinkKey(account, id), &sink)
	return sink, ok, err
}

func (s *SignalSinkStore) List(account string) ([]SignalSink, error) {
	return s.listPrefix(KeySignalSinkPrefix + keyPart(account) + "/")
}

// ListAll returns every account's sinks, for the forwarder.
func (s *SignalSinkStore) ListAll() ([]SignalSink, error) {
	return s.listPrefix(KeySignalSinkPrefix)
}

func (s *SignalSinkStore) listPrefix(prefix string) ([]SignalSink, error) {
	out := []SignalSink{}
	err := s.store.IteratePrefix(prefix, 10_000, func(_ string, value []byte) error {
		var sink SignalSink
		if err := json.Unmarshal(value, &sink); err != nil {
			return err
		}
		out = append(out, sink)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, err
}

func (s *SignalSinkStore) Delete(account, id string) error {
	if _, ok, err := s.Get(account, id); err != nil {
		return err
	} else if !ok {
		return ErrSignalSinkNotFound
	}
	return s.store.Delete(signalSinkKey(account, id))
}

// RecordDelivery stores the outcome of one delivery attempt. On success the
// cursor moves to cursor; on failure it stays and the error is kept. A sink
// deleted meanwhile is not recreated.
func (s *SignalSinkStore) RecordDelivery(account, id string, cursor uint64, deliveryErr error) error {
	sink, ok, err := s.Get(account, id)
	if err != nil || !ok {
		return err
	}
	now := s.now().UnixMilli()
	sink.LastAttemptAt = now
	if deliveryErr != nil {
		sink.Failures++
		sink.LastError = boundRunes(oneLine(deliveryErr.Error()), MaxSignalValueRunes)
	} else {
		if cursor > sink.Cursor {
			sink.Cursor = cursor
		}
		sink.Failures = 0
		sink.LastError = ""
		sink.LastDeliveredAt = now
	}
	return s.store.PutJSON(signalSinkKey(account, id), sink)
}
