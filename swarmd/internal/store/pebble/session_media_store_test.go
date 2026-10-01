package pebblestore

import (
	"bytes"
	"path/filepath"
	"strings"
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

func TestSessionMediaAsset_NormalizeMIMEAndModalities(t *testing.T) {
	// Requirement: normalizeSessionMediaMIME must canonicalize known audio, image, and video
	// MIME aliases (e.g. audio/wave -> audio/wav, image/jpg -> image/jpeg, audio/mp3 -> audio/mpeg),
	// allowing clients to use standard web MIME types without triggering false mismatch rejections.
	// Boundary/authority: normalizeSessionMediaMIME in session_media_store.go.

	cases := []struct {
		input string
		want  string
	}{
		{input: "audio/wave", want: "audio/wav"},
		{input: "audio/x-wav", want: "audio/wav"},
		{input: "audio/wav", want: "audio/wav"},
		{input: "audio/mp3", want: "audio/mpeg"},
		{input: "audio/x-mp3", want: "audio/mpeg"},
		{input: "audio/mpeg", want: "audio/mpeg"},
		{input: "image/jpg", want: "image/jpeg"},
		{input: "image/pjpeg", want: "image/jpeg"},
		{input: "image/jpeg", want: "image/jpeg"},
		{input: "image/png", want: "image/png"},
		{input: "video/x-mp4", want: "video/mp4"},
		{input: "video/mp4", want: "video/mp4"},
		{input: "application/pdf", want: "application/pdf"},
		{input: "text/plain; charset=utf-8", want: "text/plain"},
	}

	for _, tc := range cases {
		if got := normalizeSessionMediaMIME(tc.input); got != tc.want {
			t.Errorf("normalizeSessionMediaMIME(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSessionMediaAsset_GenericAndEmptyMIMESniffing(t *testing.T) {
	// Requirement: Media upload must sniff content magic bytes when declared MIME is empty or generic (application/octet-stream),
	// correctly recognizing valid PNG files (e.g. youtube-thumbnail.png) and inferring modality, MIME, and extension.
	// Threat/regression: Browsers or client tools omitting Content-Type or sending generic octet-stream, causing valid image retention to fail.
	// Boundary/authority: PutSessionMediaAsset in session_media_store.go.
	// Test layer: Pebble store unit test with content sniffing assertions.

	_, sessions := openSessionMediaTestStore(t)

	// 1. Empty declared MIME with filename youtube-thumbnail.png
	asset1, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-1",
		SessionID:        "sess-1",
		FileName:         "youtube-thumbnail.png",
		DeclaredMIMEType: "",
		Reader:           bytes.NewReader(testPNG),
	})
	if err != nil {
		t.Fatalf("put media with empty declared MIME: %v", err)
	}
	if asset1.DetectedMIMEType != "image/png" || asset1.DeclaredMIMEType != "image/png" {
		t.Fatalf("expected MIME image/png, got detected=%q declared=%q", asset1.DetectedMIMEType, asset1.DeclaredMIMEType)
	}
	if asset1.Modality != "image" {
		t.Fatalf("expected modality image, got %q", asset1.Modality)
	}
	if asset1.FileType != "png" {
		t.Fatalf("expected file type png, got %q", asset1.FileType)
	}
	if asset1.FileName != "youtube-thumbnail.png" {
		t.Fatalf("expected filename youtube-thumbnail.png, got %q", asset1.FileName)
	}

	// 2. Generic application/octet-stream declared MIME
	asset2, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-1",
		SessionID:        "sess-2",
		FileName:         "thumbnail.png",
		DeclaredMIMEType: "application/octet-stream",
		Reader:           bytes.NewReader(testPNG),
	})
	if err != nil {
		t.Fatalf("put media with application/octet-stream: %v", err)
	}
	if asset2.DetectedMIMEType != "image/png" || asset2.DeclaredMIMEType != "image/png" {
		t.Fatalf("expected MIME image/png, got detected=%q declared=%q", asset2.DetectedMIMEType, asset2.DeclaredMIMEType)
	}

	// 3. Verify bytes are readable and match original
	readAsset, payload, err := sessions.ReadSessionMediaAsset("account-1", "sess-1", asset1.ID)
	if err != nil {
		t.Fatalf("read session media asset: %v", err)
	}
	if !bytes.Equal(payload, testPNG) {
		t.Fatalf("payload bytes mismatch: got %v want %v", payload, testPNG)
	}
	if readAsset.FileName != "youtube-thumbnail.png" {
		t.Fatalf("read asset filename=%q want youtube-thumbnail.png", readAsset.FileName)
	}
}

func TestSessionMediaAsset_CorruptedAndMismatchedContentRejected(t *testing.T) {
	// Requirement: Media uploads with corrupted bytes or mismatched MIME declarations must be rejected
	// without creating partial or unauthorized state.
	// Threat/regression: Corrupted or spoofed binary payloads polluting durable media storage or bypassing validation.
	// Boundary/authority: PutSessionMediaAsset in session_media_store.go.
	// Test layer: Pebble store negative validation tests.

	_, sessions := openSessionMediaTestStore(t)

	// 1. Declared image/png but body is garbage text
	corruptPayload := []byte("this is definitely not a png image file, just text")
	_, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-1",
		SessionID:        "sess-1",
		FileName:         "corrupt.png",
		DeclaredMIMEType: "image/png",
		Reader:           bytes.NewReader(corruptPayload),
	})
	if err == nil {
		t.Fatal("expected error on corrupt PNG bytes")
	}
	if !strings.Contains(err.Error(), "does not match detected MIME type") {
		t.Fatalf("expected MIME mismatch error, got: %v", err)
	}

	// 2. Declared image/jpeg but body is valid PNG
	_, _, err = sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-1",
		SessionID:        "sess-1",
		FileName:         "mismatch.jpg",
		DeclaredMIMEType: "image/jpeg",
		Reader:           bytes.NewReader(testPNG),
	})
	if err == nil {
		t.Fatal("expected error on MIME mismatch (declared JPEG, body PNG)")
	}

	// 3. Empty body rejected
	_, _, err = sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-1",
		SessionID:        "sess-1",
		DeclaredMIMEType: "image/png",
		Reader:           bytes.NewReader([]byte{}),
	})
	if err == nil {
		t.Fatal("expected error on empty body")
	}
}

func TestSessionMediaAsset_DecoupledRetentionAndRestartDurability(t *testing.T) {
	// Requirement: Media retention must succeed independently of conversational provider contracts,
	// and stored bytes plus metadata must survive store close and restart.
	// Threat/regression: Media assets requiring active conversational provider contracts, or lost across daemon restart.
	// Boundary/authority: PutSessionMediaAsset and ReadSessionMediaAsset across Store Open/Close cycle.
	// Test layer: Pebble store lifecycle and restart durability test.

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "durability.pebble")

	// Phase 1: Open store, put media without contract
	store1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}
	sessions1 := NewSessionStore(store1)
	asset, replayed, err := sessions1.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-restart",
		SessionID:        "sess-restart",
		FileName:         "youtube-thumbnail.png",
		DeclaredMIMEType: "image/png",
		Reader:           bytes.NewReader(testPNG),
		// ContractHash, ProviderID, Model deliberately omitted (independent of conversational model!)
	})
	if err != nil || replayed {
		t.Fatalf("put media without contract: replayed=%v err=%v", replayed, err)
	}
	if asset.ContractHash != "" || asset.ProviderID != "" {
		t.Fatalf("expected empty contract/provider, got hash=%q provider=%q", asset.ContractHash, asset.ProviderID)
	}
	assetID := asset.ID

	// Close store to simulate restart
	if err := store1.Close(); err != nil {
		t.Fatalf("close store 1: %v", err)
	}

	// Phase 2: Reopen store from same path and verify persistence
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store 2: %v", err)
	}
	defer func() { _ = store2.Close() }()
	sessions2 := NewSessionStore(store2)

	reopenedAsset, payload, err := sessions2.ReadSessionMediaAsset("account-restart", "sess-restart", assetID)
	if err != nil {
		t.Fatalf("read session media asset after restart: %v", err)
	}
	if !bytes.Equal(payload, testPNG) {
		t.Fatalf("payload corrupted after restart: got %v want %v", payload, testPNG)
	}
	if reopenedAsset.FileName != "youtube-thumbnail.png" {
		t.Fatalf("filename lost after restart: got %q", reopenedAsset.FileName)
	}
	if reopenedAsset.DigestSHA256 != asset.DigestSHA256 {
		t.Fatalf("digest mismatch after restart: got %q want %q", reopenedAsset.DigestSHA256, asset.DigestSHA256)
	}
	if reopenedAsset.DetectedMIMEType != "image/png" {
		t.Fatalf("MIME type lost after restart: got %q", reopenedAsset.DetectedMIMEType)
	}
}

func TestSessionMediaAsset_SafeFilenamesAndPathContainment(t *testing.T) {
	// Requirement: Filenames must be sanitized to safe basenames, eliminating directory traversal,
	// null bytes, and newlines, ensuring strict path containment.
	// Threat/regression: Path traversal in filenames leaking into Content-Disposition headers or filesystem exports.
	// Boundary/authority: SanitizeMediaFilename and PutSessionMediaAsset in session_media_store.go.
	// Test layer: Pebble store filename sanitization tests.

	cases := []struct {
		input    string
		fileType string
		wantBase string
	}{
		{input: "../../etc/passwd", fileType: "png", wantBase: "passwd"},
		{input: "/var/log/secret.png", fileType: "png", wantBase: "secret.png"},
		{input: "..\\windows\\system32\\calc.exe", fileType: "png", wantBase: "calc.exe"},
		{input: "good-name.png", fileType: "png", wantBase: "good-name.png"},
		{input: "youtube-thumbnail.png", fileType: "png", wantBase: "youtube-thumbnail.png"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := SanitizeMediaFilename(tc.input, "test-asset", tc.fileType)
			if strings.Contains(got, "/") || strings.Contains(got, "\\") || strings.Contains(got, "..") {
				t.Fatalf("SanitizeMediaFilename(%q) = %q contains traversal characters", tc.input, got)
			}
			if got != tc.wantBase {
				t.Fatalf("SanitizeMediaFilename(%q) = %q, want %q", tc.input, got, tc.wantBase)
			}
		})
	}

	// Null bytes and empty fallbacks
	t.Run("null byte sanitized", func(t *testing.T) {
		got := SanitizeMediaFilename("bad\x00file.png", "asset123", "png")
		if strings.Contains(got, "\x00") || got == "" {
			t.Fatalf("expected safe fallback without null bytes, got: %q", got)
		}
		if got != "media-asset123.png" {
			t.Fatalf("expected media-asset123.png, got: %q", got)
		}
	})

	t.Run("empty filename fallback", func(t *testing.T) {
		got := SanitizeMediaFilename("", "asset123", "png")
		if got != "media-asset123.png" {
			t.Fatalf("expected media-asset123.png, got: %q", got)
		}
	})
}

func TestSessionMediaAsset_QuotaAndLimitsEnforced(t *testing.T) {
	// Requirement: Media asset size limits and account quotas must be enforced strictly.
	// Threat/regression: Unbounded uploads causing disk exhaustion or memory pressure.
	// Boundary/authority: PutSessionMediaAsset in session_media_store.go.
	// Test layer: Pebble store quota boundary tests.

	_, sessions := openSessionMediaTestStore(t)

	// 1. MaxBytes limit enforced
	smallMaxBytes := int64(len(testPNG) - 1)
	_, _, err := sessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-quota",
		SessionID:        "sess-quota",
		DeclaredMIMEType: "image/png",
		MaxBytes:         smallMaxBytes,
		Reader:           bytes.NewReader(testPNG),
	})
	if err == nil {
		t.Fatal("expected error on payload exceeding MaxBytes")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected exceeds byte limit error, got: %v", err)
	}

	// 2. Count quota enforced
	quotaSessions := NewSessionStore(sessions.store)
	for i := 0; i < 2; i++ {
		content := append(append([]byte(nil), testPNG...), byte(i))
		_, _, putErr := quotaSessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
			AccountScopeID:   "account-count-limit",
			SessionID:        "sess-count",
			DeclaredMIMEType: "image/png",
			MaxCount:         2,
			QuotaAssets:      2,
			Reader:           bytes.NewReader(content),
		})
		if putErr != nil {
			t.Fatalf("put item %d: %v", i, putErr)
		}
	}
	// 3rd item must fail count quota
	thirdContent := append(append([]byte(nil), testPNG...), 0xFF)
	_, _, err = quotaSessions.PutSessionMediaAsset(PutSessionMediaAssetInput{
		AccountScopeID:   "account-count-limit",
		SessionID:        "sess-count",
		DeclaredMIMEType: "image/png",
		MaxCount:         2,
		QuotaAssets:      2,
		Reader:           bytes.NewReader(thirdContent),
	})
	if err == nil {
		t.Fatal("expected error on count quota exceedance")
	}
	if !strings.Contains(err.Error(), "count quota exceeded") {
		t.Fatalf("expected count quota error, got: %v", err)
	}
}
