package webpush

import (
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	pushlib "github.com/SherClockHolmes/webpush-go"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	vapidKeyPairKey       = "secret/webpush/vapid/v1"
	subscriptionKeyPrefix = "secret/webpush/subscription/v1/"
	defaultListLimit      = 200
	maximumListLimit      = 1000
)

var ErrAccountScopeRequired = errors.New("web push account scope id is required")

// VAPIDKeyPair is deliberately safe to return through an API: PrivateKey is
// never JSON encoded. The pair itself is persisted only in the secret store.
type VAPIDKeyPair struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"-"`
}

// SubscriptionInput is the browser PushSubscription capability supplied by a
// client. Auth and P256DH are capability secrets and must not be logged or
// returned from API responses.
type SubscriptionInput struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
}

type SubscriptionKeys struct {
	Auth   string `json:"auth"`
	P256DH string `json:"p256dh"`
}

// Subscription is non-sensitive subscription metadata suitable for an API.
type Subscription struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// StoredSubscription is available to delivery code. Its capability fields are
// explicitly excluded from JSON to prevent accidental API exposure.
type StoredSubscription struct {
	Subscription
	AccountScopeID string `json:"-"`
	Endpoint       string `json:"-"`
	Auth           string `json:"-"`
	P256DH         string `json:"-"`
}

// Repository owns VAPID and browser capability persistence.
type Repository interface {
	EnsureVAPIDKeyPair(context.Context) (VAPIDKeyPair, error)
	UpsertSubscription(context.Context, string, SubscriptionInput) (Subscription, bool, error)
	ListSubscriptions(context.Context, string, int) ([]Subscription, error)
	ListStoredSubscriptions(context.Context, string, int) ([]StoredSubscription, error)
	DeleteSubscription(context.Context, string, string) (bool, error)
}

type PebbleRepository struct {
	store *pebblestore.Store
	mu    sync.Mutex
	now   func() time.Time
}

func NewPebbleRepository(secretStore *pebblestore.Store) (*PebbleRepository, error) {
	if secretStore == nil {
		return nil, errors.New("web push secret store is required")
	}
	return &PebbleRepository{store: secretStore, now: time.Now}, nil
}

func (r *PebbleRepository) EnsureVAPIDKeyPair(ctx context.Context) (VAPIDKeyPair, error) {
	if err := contextErr(ctx); err != nil {
		return VAPIDKeyPair{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var persisted persistedVAPIDKeyPair
	ok, err := r.store.GetJSON(vapidKeyPairKey, &persisted)
	pair := VAPIDKeyPair{PublicKey: persisted.PublicKey, PrivateKey: persisted.PrivateKey}
	if err != nil {
		return VAPIDKeyPair{}, fmt.Errorf("read web push VAPID key pair: %w", err)
	}
	if ok {
		if err := validateVAPIDKeyPair(pair); err != nil {
			return VAPIDKeyPair{}, fmt.Errorf("stored web push VAPID key pair is invalid: %w", err)
		}
		return pair, nil
	}
	privateKey, publicKey, err := pushlib.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeyPair{}, fmt.Errorf("generate web push VAPID key pair: %w", err)
	}
	pair = VAPIDKeyPair{PublicKey: publicKey, PrivateKey: privateKey}
	if err := r.store.PutJSON(vapidKeyPairKey, persistedVAPIDKeyPair{PublicKey: pair.PublicKey, PrivateKey: pair.PrivateKey}); err != nil {
		return VAPIDKeyPair{}, fmt.Errorf("persist web push VAPID key pair: %w", err)
	}
	return pair, nil
}

func (r *PebbleRepository) UpsertSubscription(ctx context.Context, accountScopeID string, input SubscriptionInput) (Subscription, bool, error) {
	if err := contextErr(ctx); err != nil {
		return Subscription{}, false, err
	}
	accountScopeID, err := requireAccountScope(accountScopeID)
	if err != nil {
		return Subscription{}, false, err
	}
	input, err = normalizeAndValidateSubscription(input)
	if err != nil {
		return Subscription{}, false, err
	}
	now := r.now().UnixMilli()
	id := subscriptionID(input.Endpoint)
	key := subscriptionStoreKey(accountScopeID, id)

	r.mu.Lock()
	defer r.mu.Unlock()
	var current storedSubscriptionRecord
	exists, err := r.store.GetJSON(key, &current)
	if err != nil {
		return Subscription{}, false, fmt.Errorf("read web push subscription: %w", err)
	}
	next := storedSubscriptionRecord{
		ID: id, AccountScopeID: accountScopeID, Endpoint: input.Endpoint,
		Auth: input.Keys.Auth, P256DH: input.Keys.P256DH, UpdatedAt: now,
	}
	if exists {
		if current.AccountScopeID != accountScopeID || current.ID != id {
			return Subscription{}, false, errors.New("stored web push subscription identity mismatch")
		}
		next.CreatedAt = current.CreatedAt
	} else {
		next.CreatedAt = now
	}
	changed := !exists || current.Endpoint != next.Endpoint || current.Auth != next.Auth || current.P256DH != next.P256DH
	if !changed {
		return current.public(), false, nil
	}
	if err := r.store.PutJSON(key, next); err != nil {
		return Subscription{}, false, fmt.Errorf("persist web push subscription: %w", err)
	}
	return next.public(), true, nil
}

func (r *PebbleRepository) ListSubscriptions(ctx context.Context, accountScopeID string, limit int) ([]Subscription, error) {
	records, err := r.ListStoredSubscriptions(ctx, accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, 0, len(records))
	for _, record := range records {
		out = append(out, record.Subscription)
	}
	return out, nil
}

func (r *PebbleRepository) ListStoredSubscriptions(ctx context.Context, accountScopeID string, limit int) ([]StoredSubscription, error) {
	accountScopeID, err := requireAccountScope(accountScopeID)
	if err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	prefix := subscriptionAccountPrefix(accountScopeID)
	out := make([]StoredSubscription, 0, limit)
	err = r.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		var record storedSubscriptionRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode web push subscription: %w", err)
		}
		if record.AccountScopeID != accountScopeID {
			return errors.New("stored web push subscription account mismatch")
		}
		if err := record.validate(); err != nil {
			return fmt.Errorf("stored web push subscription is invalid: %w", err)
		}
		out = append(out, record.private())
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list web push subscriptions: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (r *PebbleRepository) DeleteSubscription(ctx context.Context, accountScopeID, id string) (bool, error) {
	accountScopeID, err := requireAccountScope(accountScopeID)
	if err != nil {
		return false, err
	}
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	id = strings.TrimSpace(id)
	if !validSubscriptionID(id) {
		return false, errors.New("valid web push subscription id is required")
	}
	key := subscriptionStoreKey(accountScopeID, id)
	_, exists, err := r.store.GetBytes(key)
	if err != nil {
		return false, fmt.Errorf("read web push subscription before delete: %w", err)
	}
	if !exists {
		return false, nil
	}
	if err := r.store.Delete(key); err != nil {
		return false, fmt.Errorf("delete web push subscription: %w", err)
	}
	return true, nil
}

type persistedVAPIDKeyPair struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

type storedSubscriptionRecord struct {
	ID             string `json:"id"`
	AccountScopeID string `json:"account_scope_id"`
	Endpoint       string `json:"endpoint"`
	Auth           string `json:"auth"`
	P256DH         string `json:"p256dh"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (r storedSubscriptionRecord) public() Subscription {
	return Subscription{ID: r.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (r storedSubscriptionRecord) private() StoredSubscription {
	return StoredSubscription{Subscription: r.public(), AccountScopeID: r.AccountScopeID, Endpoint: r.Endpoint, Auth: r.Auth, P256DH: r.P256DH}
}

func (r storedSubscriptionRecord) validate() error {
	if _, err := requireAccountScope(r.AccountScopeID); err != nil {
		return err
	}
	input, err := normalizeAndValidateSubscription(SubscriptionInput{Endpoint: r.Endpoint, Keys: SubscriptionKeys{Auth: r.Auth, P256DH: r.P256DH}})
	if err != nil {
		return err
	}
	if r.ID != subscriptionID(input.Endpoint) || r.CreatedAt <= 0 || r.UpdatedAt <= 0 {
		return errors.New("subscription identity or timestamps are invalid")
	}
	return nil
}

func normalizeAndValidateSubscription(input SubscriptionInput) (SubscriptionInput, error) {
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	input.Keys.Auth = strings.TrimSpace(input.Keys.Auth)
	input.Keys.P256DH = strings.TrimSpace(input.Keys.P256DH)
	u, err := url.Parse(input.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return SubscriptionInput{}, errors.New("web push endpoint must be an absolute HTTPS URL without credentials or fragment")
	}
	auth, err := decodeRawURLKey(input.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return SubscriptionInput{}, errors.New("web push auth key must be 16 bytes of base64url data")
	}
	publicKey, err := decodeRawURLKey(input.Keys.P256DH)
	if err != nil || len(publicKey) != 65 {
		return SubscriptionInput{}, errors.New("web push p256dh key must be an uncompressed P-256 public key")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return SubscriptionInput{}, errors.New("web push p256dh key is not a valid P-256 point")
	}
	input.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)
	input.Keys.P256DH = base64.RawURLEncoding.EncodeToString(publicKey)
	input.Endpoint = u.String()
	return input, nil
}

func validateVAPIDKeyPair(pair VAPIDKeyPair) error {
	privateKey, err := decodeRawURLKey(pair.PrivateKey)
	if err != nil || len(privateKey) != 32 {
		return errors.New("private key must be 32 bytes of base64url data")
	}
	publicKey, err := decodeRawURLKey(pair.PublicKey)
	if err != nil || len(publicKey) != 65 {
		return errors.New("public key must be an uncompressed P-256 point")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return errors.New("public key is not on P-256")
	}
	expectedX, expectedY := elliptic.P256().ScalarBaseMult(privateKey)
	if expectedX.Cmp(x) != 0 || expectedY.Cmp(y) != 0 {
		return errors.New("public and private keys do not match")
	}
	return nil
}

func decodeRawURLKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("key is empty")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func subscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "wps_" + hex.EncodeToString(sum[:16])
}

func validSubscriptionID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "wps_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "wps_"))
	return err == nil
}

func subscriptionAccountPrefix(accountScopeID string) string {
	return subscriptionKeyPrefix + encodeKeyPart(accountScopeID) + "/"
}

func subscriptionStoreKey(accountScopeID, id string) string {
	return subscriptionAccountPrefix(accountScopeID) + id
}

func encodeKeyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func requireAccountScope(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrAccountScopeRequired
	}
	return value, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maximumListLimit {
		return maximumListLimit
	}
	return limit
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
