package pebblestore

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const localProductJWTSigningKeyLength = 32

var ErrIdentitySigningKeyNotConfigured = errors.New("identity signing key store is not configured")

type IdentitySessionStore struct {
	store *Store
}

type IdentitySessionSigningKeyRecord struct {
	Version   int       `json:"version"`
	Algorithm string    `json:"algorithm"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewIdentitySessionStore(store *Store) *IdentitySessionStore {
	return &IdentitySessionStore{store: store}
}

func (s *IdentitySessionStore) EnsureLocalProductJWTSigningKey() ([]byte, IdentitySessionSigningKeyRecord, error) {
	if err := s.configured(); err != nil {
		return nil, IdentitySessionSigningKeyRecord{}, err
	}
	if existing, ok, err := s.getLocalProductJWTSigningKeyRecord(); err != nil {
		return nil, IdentitySessionSigningKeyRecord{}, err
	} else if ok {
		key, err := decodeIdentitySessionSigningKey(existing)
		if err != nil {
			return nil, IdentitySessionSigningKeyRecord{}, err
		}
		return key, existing, nil
	}

	key := make([]byte, localProductJWTSigningKeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, IdentitySessionSigningKeyRecord{}, fmt.Errorf("generate local product jwt signing key: %w", err)
	}
	now := time.Now().UTC()
	record := IdentitySessionSigningKeyRecord{
		Version:   1,
		Algorithm: "HS256",
		Key:       base64.RawURLEncoding.EncodeToString(key),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.PutJSON(KeyIdentityLocalProductJWTSigningKey(), record); err != nil {
		return nil, IdentitySessionSigningKeyRecord{}, err
	}
	return append([]byte(nil), key...), record, nil
}

func (s *IdentitySessionStore) getLocalProductJWTSigningKeyRecord() (IdentitySessionSigningKeyRecord, bool, error) {
	var record IdentitySessionSigningKeyRecord
	ok, err := s.store.GetJSON(KeyIdentityLocalProductJWTSigningKey(), &record)
	if err != nil || !ok {
		return IdentitySessionSigningKeyRecord{}, ok, err
	}
	return record, true, nil
}

func decodeIdentitySessionSigningKey(record IdentitySessionSigningKeyRecord) ([]byte, error) {
	if record.Algorithm != "HS256" {
		return nil, fmt.Errorf("unsupported local product jwt signing key algorithm %q", record.Algorithm)
	}
	key, err := base64.RawURLEncoding.DecodeString(record.Key)
	if err != nil {
		return nil, fmt.Errorf("decode local product jwt signing key: %w", err)
	}
	if len(key) != localProductJWTSigningKeyLength {
		return nil, fmt.Errorf("local product jwt signing key has %d bytes, want %d", len(key), localProductJWTSigningKeyLength)
	}
	return key, nil
}

func (s *IdentitySessionStore) configured() error {
	if s == nil || s.store == nil {
		return ErrIdentitySigningKeyNotConfigured
	}
	return nil
}
