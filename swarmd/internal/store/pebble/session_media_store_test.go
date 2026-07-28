package pebblestore

import (
	"bytes"
	"path/filepath"
	"testing"
)

var testPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}

func openSessionMediaTestStore(t *testing.T) (*Store, *SessionStore) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewSessionStore(store)
}

func putTestMedia(t *testing.T, sessions *SessionStore, account, session string) SessionMediaAsset {
	t.Helper()
	asset, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID: account, SessionID: session, Modality: "image", DeclaredMIMEType: "image/png",
		ContractHash: "contract", ProviderID: "openai", Model: "gpt", Reader: bytes.NewReader(testPNG),
	})
	if err != nil {
		t.Fatalf("put media: %v", err)
	}
	return asset
}

func TestDetectSessionMediaMIMERecognizesHEIFBrands(t *testing.T) {
	for _, test := range []struct {
		brand string
		want  string
	}{
		{brand: "heic", want: "image/heic"},
		{brand: "heix", want: "image/heic"},
		{brand: "mif1", want: "image/heif"},
		{brand: "msf1", want: "image/heif"},
	} {
		payload := append([]byte{0, 0, 0, 24}, []byte("ftyp")...)
		payload = append(payload, []byte(test.brand)...)
		payload = append(payload, make([]byte, 12)...)
		if got := detectSessionMediaMIME(payload); got != test.want {
			t.Fatalf("brand %q MIME = %q, want %q", test.brand, got, test.want)
		}
	}
}

func TestSessionMediaAssetProviderAllowlistIsExplicit(t *testing.T) {
	for _, providerID := range []string{"openai", "codex", "google", "anthropic", "fireworks", "openrouter"} {
		if !sessionMediaAssetProviderEnabled(providerID) {
			t.Fatalf("reviewed provider %q is not enabled", providerID)
		}
	}
	for _, providerID := range []string{"exa", "copilot", "ollama", "unknown"} {
		if sessionMediaAssetProviderEnabled(providerID) {
			t.Fatalf("unreviewed provider %q is enabled", providerID)
		}
	}
}

func TestSessionMediaAssetDedupOwnershipSpoofingQuotaAndGC(t *testing.T) {
	_, sessions := openSessionMediaTestStore(t)
	asset := putTestMedia(t, sessions, "account-a", "session-a")
	replayed, dedup, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID: "account-a", SessionID: "session-a", Modality: "image", DeclaredMIMEType: "image/png",
		ContractHash: "contract", ProviderID: "openai", Model: "gpt", Reader: bytes.NewReader(testPNG),
	})
	if err != nil || !dedup || replayed.ID != asset.ID {
		t.Fatalf("dedup asset=%+v replayed=%v err=%v", replayed, dedup, err)
	}
	if _, ok, err := sessions.GetSessionMediaAsset("account-b", "session-a", asset.ID); err != nil || ok {
		t.Fatalf("cross-account lookup ok=%v err=%v", ok, err)
	}
	if _, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID: "account-a", SessionID: "session-b", Modality: "image", DeclaredMIMEType: "image/jpeg",
		ContractHash: "contract", ProviderID: "openai", Model: "gpt", Reader: bytes.NewReader(testPNG),
	}); err == nil {
		t.Fatal("expected MIME spoofing rejection")
	}
	if _, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID: "account-a", SessionID: "session-b", Modality: "image", DeclaredMIMEType: "image/png",
		ContractHash: "contract", ProviderID: "openai", Model: "gpt", MaxBytes: 4, Reader: bytes.NewReader(testPNG),
	}); err == nil {
		t.Fatal("expected oversize rejection")
	}
	if deleted, err := sessions.DeleteUnreferencedSessionMediaAsset("account-a", "session-a", asset.ID); err != nil || !deleted {
		t.Fatalf("delete unreferenced deleted=%v err=%v", deleted, err)
	}
	if _, _, err := sessions.ReadSessionMediaAsset("account-a", "session-a", asset.ID); err == nil {
		t.Fatal("expected deleted bytes to be inaccessible")
	}
}

func TestV3MessageMediaReferenceReplayIdempotencyAndTampering(t *testing.T) {
	_, sessions := openSessionMediaTestStore(t)
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "session", UserID: "user", AccountScopeID: "account"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	asset := putTestMedia(t, sessions, "account", "session")
	ref := SessionMediaReference{AssetID: asset.ID, Modality: asset.Modality, MIMEType: asset.DetectedMIMEType, FileType: asset.FileType, Size: asset.Size, DigestSHA256: asset.DigestSHA256, ContractHash: asset.ContractHash}
	input := V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "message", PayloadHash: "message-with-media", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "inspect", Media: []SessionMediaReference{ref}}}
	first, err := sessions.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	second, err := sessions.ApplyV3SessionMutation(input)
	if err != nil || !second.Replayed || second.Message == nil || len(second.Message.Media) != 1 {
		t.Fatalf("idempotent replay=%v message=%+v err=%v", second.Replayed, second.Message, err)
	}
	messages, err := sessions.ListV3SessionMessages("session", 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Media[0].AssetID != asset.ID || first.Message == nil {
		t.Fatalf("durable replay messages=%+v first=%+v err=%v", messages, first.Message, err)
	}
	stored, ok, err := sessions.GetSessionMediaAsset("account", "session", asset.ID)
	if err != nil || !ok || stored.ReferenceCount != 1 {
		t.Fatalf("reference count asset=%+v ok=%v err=%v", stored, ok, err)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "session", UserID: "user", AccountScopeID: "account", ClientRequestID: "tamper", PayloadHash: "tamper", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "bad", Media: []SessionMediaReference{{AssetID: asset.ID, Modality: "image", MIMEType: "image/png", Size: asset.Size, DigestSHA256: "forged", ContractHash: asset.ContractHash}}}}); err == nil {
		t.Fatal("expected tampered reference rejection")
	}
	if deleted, err := sessions.DeleteUnreferencedSessionMediaAsset("account", "session", asset.ID); err == nil || deleted {
		t.Fatal("expected referenced immutable asset deletion rejection")
	}
}

func TestSessionDeletePurgesMediaBytesAndSearchNeverIndexesMediaIdentity(t *testing.T) {
	_, sessions := openSessionMediaTestStore(t)
	session := SessionSnapshot{ID: "media-private", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", Title: "media privacy"}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "create-private", PayloadHash: "create-private", Kind: V3SessionMutationCreateSession, Session: &session}); err != nil {
		t.Fatalf("create: %v", err)
	}
	asset := putTestMedia(t, sessions, session.AccountScopeID, session.ID)
	ref := SessionMediaReference{AssetID: asset.ID, Modality: asset.Modality, MIMEType: asset.DetectedMIMEType, Size: asset.Size, DigestSHA256: asset.DigestSHA256, ContractHash: asset.ContractHash}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "message-private", PayloadHash: "message-private", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "ordinary searchable text", Media: []SessionMediaReference{ref}}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	for _, secret := range []string{asset.ID, asset.DigestSHA256} {
		result, err := sessions.SearchV3Sessions(V3SessionSearchOptions{AccountScopeID: session.AccountScopeID, UserID: session.UserID, Global: true, Query: secret, Limit: 10})
		if err != nil {
			t.Fatalf("search media identity: %v", err)
		}
		if len(result.Items) != 0 {
			t.Fatalf("media identity %q leaked into search: %+v", secret, result.Items)
		}
	}
	if err := sessions.DeleteSession(session.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := sessions.ReadSessionMediaAsset(session.AccountScopeID, session.ID, asset.ID); err == nil {
		t.Fatal("deleted session retained media bytes")
	}
}

func TestV3LegacyTextOnlyMessageRemainsValid(t *testing.T) {
	_, sessions := openSessionMediaTestStore(t)
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "legacy", UserID: "user", AccountScopeID: "account", ClientRequestID: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "legacy"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "legacy", UserID: "user", AccountScopeID: "account", ClientRequestID: "message", PayloadHash: "message", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "text only"}}); err != nil {
		t.Fatalf("legacy text append: %v", err)
	}
}
