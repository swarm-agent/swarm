package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type AttachAuthRecord struct {
	Token     string `json:"token"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ClientAuthStore struct {
	store       *Store
	secretStore *Store
}

func NewClientAuthStore(store *Store) *ClientAuthStore {
	return NewClientAuthStoreWithSecretStore(store, store)
}

func NewClientAuthStoreWithSecretStore(store, secretStore *Store) *ClientAuthStore {
	return &ClientAuthStore{store: store, secretStore: secretStore}
}

func (s *ClientAuthStore) EnsureAttachToken() (AttachAuthRecord, error) {
	record, ok, err := s.GetAttachAuth()
	if err != nil {
		return AttachAuthRecord{}, err
	}
	if ok && record.Token != "" {
		return record, nil
	}
	return s.RotateAttachToken(0)
}

func (s *ClientAuthStore) RotateAttachToken(createdAt int64) (AttachAuthRecord, error) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	token, err := generateToken(32)
	if err != nil {
		return AttachAuthRecord{}, err
	}
	record := AttachAuthRecord{
		Token:     token,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	if err := s.secretStore.PutJSON(KeyAuthAttachDefault, record); err != nil {
		return AttachAuthRecord{}, err
	}
	return record, nil
}

func (s *ClientAuthStore) GetAttachAuth() (AttachAuthRecord, bool, error) {
	var record AttachAuthRecord
	ok, err := s.secretStore.GetJSON(KeyAuthAttachDefault, &record)
	if err != nil {
		return AttachAuthRecord{}, false, err
	}
	if ok {
		if s.store != nil && s.store != s.secretStore {
			if err := s.store.Delete(KeyAuthAttachDefault); err != nil {
				return AttachAuthRecord{}, false, fmt.Errorf("remove migrated attach token: %w", err)
			}
		}
		return record, true, nil
	}
	if s.store == nil || s.store == s.secretStore {
		return AttachAuthRecord{}, false, nil
	}
	ok, err = s.store.GetJSON(KeyAuthAttachDefault, &record)
	if err != nil || !ok {
		return AttachAuthRecord{}, false, err
	}
	if err := s.secretStore.PutJSON(KeyAuthAttachDefault, record); err != nil {
		return AttachAuthRecord{}, false, fmt.Errorf("migrate attach token to secret store: %w", err)
	}
	if err := s.store.Delete(KeyAuthAttachDefault); err != nil {
		return AttachAuthRecord{}, false, fmt.Errorf("remove migrated attach token: %w", err)
	}
	return record, true, nil
}

func generateToken(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("token size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
