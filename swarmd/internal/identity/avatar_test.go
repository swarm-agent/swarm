package identity

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"
)

// Requirement: SaveCurrentUserAvatar decodes bounded PNG pixels, isolates owners,
// and cannot destroy a saved photo or username on rejection. The identity service
// plus temporary Pebble store is the narrowest layer proving those postconditions.
func TestCurrentUserAvatarValidationAndIsolation(t *testing.T) {
	svc, store := newTestService(t, "user_avatar", "acct_avatar")
	created, err := svc.BootstrapFirstIdentity("alice")
	if err != nil {
		t.Fatal(err)
	}
	actor := ActorContext{UserID: created.User.ID, AccountScopeID: created.AccountScope.ID, User: created.User}
	encode := func(width int) []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, width, 1))); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	valid := encode(2)
	saved, err := svc.SaveCurrentUserAvatar(actor, append(valid, []byte("untrusted trailing metadata")...))
	if err != nil || !bytes.Equal(saved, valid) {
		t.Fatalf("save/canonicalize: %v", err)
	}
	for _, data := range [][]byte{nil, []byte("not png"), valid[:len(valid)-12], encode(MaxAvatarDimension + 1), make([]byte, MaxAvatarBytes+1)} {
		if _, err := svc.SaveCurrentUserAvatar(actor, data); !errors.Is(err, ErrInvalidAvatar) {
			t.Fatalf("invalid image accepted: %v", err)
		}
		got, err := svc.CurrentUserAvatar(actor)
		if err != nil || !bytes.Equal(got, saved) {
			t.Fatalf("rejection changed image: %v", err)
		}
	}
	for _, bad := range []ActorContext{{}, {UserID: actor.UserID, User: actor.User, AccountScopeID: "other_account"}, {UserID: "other_user", User: actor.User, AccountScopeID: actor.AccountScopeID}} {
		if _, err := svc.SaveCurrentUserAvatar(bad, valid); !errors.Is(err, ErrAvatarUnauthorized) {
			t.Fatalf("unauthorized save: %v", err)
		}
		if data, err := svc.CurrentUserAvatar(bad); !errors.Is(err, ErrAvatarUnauthorized) || len(data) != 0 {
			t.Fatalf("unauthorized read: %v", err)
		}
	}
	if _, ok, err := store.GetUserAvatar("other_account", actor.UserID); err != nil || ok {
		t.Fatalf("cross-account mutation: %v", err)
	}
	if _, err := svc.RenameCurrentUser(actor, "renamed"); err != nil {
		t.Fatal(err)
	}
	// A fresh service reads persisted bytes, not an in-memory profile cache.
	got, err := NewService(store).CurrentUserAvatar(actor)
	if err != nil || !bytes.Equal(got, saved) {
		t.Fatalf("rehydrate after rename: %v", err)
	}
}
