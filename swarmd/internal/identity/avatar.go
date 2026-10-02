package identity

import (
	"bytes"
	"errors"
	"image/png"
)

const MaxAvatarBytes = 2 << 20
const MaxAvatarDimension = 1024

var ErrAvatarUnauthorized = errors.New("profile picture requires the current account identity")
var ErrInvalidAvatar = errors.New("choose a valid PNG up to 2 MiB and 1024 by 1024 pixels")

func (s *Service) authorizeAvatar(actor ActorContext) error {
	if err := s.configured(); err != nil {
		return err
	}
	if actor.UserID == "" || actor.AccountScopeID == "" || actor.UserID != actor.User.ID {
		return ErrAvatarUnauthorized
	}
	membership, ok, err := s.store.GetAccountUser(actor.AccountScopeID, actor.UserID)
	if err != nil {
		return err
	}
	if !ok || membership.Status != "active" {
		return ErrAvatarUnauthorized
	}
	return nil
}

func (s *Service) CurrentUserAvatar(actor ActorContext) ([]byte, error) {
	if err := s.authorizeAvatar(actor); err != nil {
		return nil, err
	}
	data, _, err := s.store.GetUserAvatar(actor.AccountScopeID, actor.UserID)
	return data, err
}

func (s *Service) SaveCurrentUserAvatar(actor ActorContext, data []byte) ([]byte, error) {
	if err := s.authorizeAvatar(actor); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxAvatarBytes {
		return nil, ErrInvalidAvatar
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxAvatarDimension || cfg.Height > MaxAvatarDimension {
		return nil, ErrInvalidAvatar
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalidAvatar
	}
	// Re-encode decoded pixels: never serve user-supplied metadata or trailing data.
	var clean bytes.Buffer
	if err := png.Encode(&clean, img); err != nil {
		return nil, err
	}
	if clean.Len() > MaxAvatarBytes {
		return nil, ErrInvalidAvatar
	}
	if err := s.store.PutUserAvatar(actor.AccountScopeID, actor.UserID, clean.Bytes()); err != nil {
		return nil, err
	}
	return clean.Bytes(), nil
}
