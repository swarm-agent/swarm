package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionV3ArtifactRequiresBundleOnlyForPackages(t *testing.T) {
	tests := []struct {
		name     string
		artifact sessionsV3ResolvedArtifact
		want     bool
	}{
		{name: "native image", artifact: sessionsV3ResolvedArtifact{Descriptor: pebblestore.PlanFinalHandoffArtifact{MediaType: "image/png", Kind: "image"}}, want: false},
		{name: "native video", artifact: sessionsV3ResolvedArtifact{Descriptor: pebblestore.PlanFinalHandoffArtifact{MediaType: "video/mp4", Kind: "video"}}, want: false},
		{name: "zip package", artifact: sessionsV3ResolvedArtifact{Managed: &pebblestore.SessionArtifactVariant{MediaType: "application/zip", Presentation: pebblestore.SessionArtifactPresentation{Kind: "package"}}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionV3ArtifactRequiresBundle(tt.artifact); got != tt.want {
				t.Fatalf("sessionV3ArtifactRequiresBundle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionsV3ArtifactCatalogItemPreservesAnimationProfile(t *testing.T) {
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneSessionsV3ArtifactAnimationProfile(profile)
	profile.ProfileID = "mutated"
	if cloned == nil || cloned.ProfileID != "motion_ui" || cloned.RuntimeKind != "native_css_waapi_svg" || cloned.RuntimePackage != "" || cloned.Budgets.NetworkAllowed {
		t.Fatalf("cloned animation profile = %#v", cloned)
	}
	item := sessionsV3ArtifactCatalogItem{AnimationProfile: cloned}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"animation_profile"`) || !strings.Contains(string(encoded), `"registry_version"`) {
		t.Fatalf("catalog item = %s", encoded)
	}
}

func TestSessionV3ArtifactPresentationInfersReadyHTMLPreview(t *testing.T) {
	tests := []struct {
		name            string
		variant         pebblestore.SessionArtifactVariant
		wantKind        string
		wantPreviewable bool
	}{
		{
			name: "ready html repairs omitted previewable flag",
			variant: pebblestore.SessionArtifactVariant{
				Status:    pebblestore.SessionArtifactStatusReady,
				MediaType: "text/html",
				Presentation: pebblestore.SessionArtifactPresentation{
					Kind: "html",
				},
			},
			wantKind:        "html",
			wantPreviewable: true,
		},
		{
			name: "staging html preserves declared presentation",
			variant: pebblestore.SessionArtifactVariant{
				Status:    pebblestore.SessionArtifactStatusStaging,
				MediaType: "text/html",
				Presentation: pebblestore.SessionArtifactPresentation{
					Kind: "html",
				},
			},
			wantKind:        "html",
			wantPreviewable: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, previewable := sessionsV3ArtifactPresentation(test.variant)
			if kind != test.wantKind || previewable != test.wantPreviewable {
				t.Fatalf("presentation = (%q, %t), want (%q, %t)", kind, previewable, test.wantKind, test.wantPreviewable)
			}
		})
	}
}

func TestOpenSessionV3ArtifactFileRejectsSymlinkAndEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("<h1>preview</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := openSessionV3ArtifactFile(root, "gallery/index.html")
	if err != nil {
		t.Fatalf("open declared artifact: %v", err)
	}
	file.Close()
	if !info.Mode().IsRegular() {
		t.Fatalf("artifact mode = %v", info.Mode())
	}
	if _, _, err := openSessionV3ArtifactFile(root, "../outside.html"); err == nil {
		t.Fatal("workspace escape was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "gallery", "linked.html")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSessionV3ArtifactFile(root, "gallery/linked.html"); err == nil {
		t.Fatal("symlink artifact was accepted")
	}
	if _, _, err := openSessionV3ArtifactFile(root, "gallery"); err == nil {
		t.Fatal("directory artifact was accepted")
	}
}

func TestSessionV3ArtifactPreviewTokenIsScopedAndExpires(t *testing.T) {
	server := &Server{artifactPreviewKey: []byte("0123456789abcdef0123456789abcdef")}
	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "user_preview",
		AccountScopeID: "acct_preview",
	}
	token, err := server.issueSessionV3ArtifactPreviewToken(principal, "session_preview", "artifact_preview", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("issue preview token: %v", err)
	}
	if strings.Contains(token, principal.UserID) || strings.Contains(token, principal.AccountScopeID) {
		t.Fatal("preview token exposes identity claims")
	}
	request := httptest.NewRequest("GET", "/v3/sessions/session_preview/artifacts/artifact_preview/content/access/"+token+"/variant/index.html", nil)
	resolved, ok := server.validateSessionV3ArtifactPreviewRequest(request)
	if !ok || resolved.UserID != principal.UserID || resolved.AccountScopeID != principal.AccountScopeID {
		t.Fatalf("validate scoped preview token = (%+v, %v)", resolved, ok)
	}
	wrongArtifact := httptest.NewRequest("GET", "/v3/sessions/session_preview/artifacts/other/content/access/"+token+"/variant/index.html", nil)
	if _, ok := server.validateSessionV3ArtifactPreviewRequest(wrongArtifact); ok {
		t.Fatal("preview token was accepted for another artifact")
	}
	queryToken := httptest.NewRequest("GET", "/v3/sessions/session_preview/artifacts/artifact_preview/content/variant/index.html?artifact_preview="+token, nil)
	if _, ok := server.validateSessionV3ArtifactPreviewRequest(queryToken); ok {
		t.Fatal("preview token was accepted from a query string")
	}
	request = httptest.NewRequest("GET", "/v3/sessions/session_preview/artifacts/artifact_preview/content/access/"+token+"/variant/index.html", nil)
	server.artifactPreviewKey = []byte("abcdef0123456789abcdef0123456789")
	if _, ok := server.validateSessionV3ArtifactPreviewRequest(request); ok {
		t.Fatal("preview token was accepted with another server key")
	}
}

func TestSessionV3ArtifactPackageHTMLCSPKeepsOpaqueAncestorEmbeddable(t *testing.T) {
	if strings.Contains(sessionsV3ArtifactPackageHTMLCSP, "frame-ancestors") {
		t.Fatal("package HTML cannot restrict frame ancestors while nested below an opaque sandboxed srcdoc ancestor")
	}
	for _, directive := range []string{
		"sandbox allow-scripts",
		"default-src 'none'",
		"connect-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
	} {
		if !strings.Contains(sessionsV3ArtifactPackageHTMLCSP, directive) {
			t.Fatalf("package HTML CSP is missing %q", directive)
		}
	}
}

func TestOpenSessionV3ArtifactPackageFileConfinesContentToHTMLDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery", "variant-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("<iframe src=\"variant-1/index.html\"></iframe>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "variant-1", "index.html"), []byte("<script>requestAnimationFrame(()=>{})</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, mediaType, err := openSessionV3ArtifactPackageFile(root, "gallery/index.html", "variant-1/index.html")
	if err != nil {
		t.Fatalf("open packaged html: %v", err)
	}
	file.Close()
	if !info.Mode().IsRegular() || mediaType != "text/html; charset=utf-8" {
		t.Fatalf("package file = mode %v media %q", info.Mode(), mediaType)
	}
	file, _, mediaType, err = openSessionV3ArtifactPackageFile(root, "gallery/index.html", sessionsV3ArtifactPackageEntryPath)
	if err != nil {
		t.Fatalf("open package entry alias: %v", err)
	}
	entry, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatalf("read package entry alias: %v", err)
	}
	if string(entry) != "<iframe src=\"variant-1/index.html\"></iframe>" || mediaType != "text/html; charset=utf-8" {
		t.Fatalf("package entry alias = %q media %q", string(entry), mediaType)
	}
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "gallery/index.html", "../outside.html"); err == nil {
		t.Fatal("package directory escape was accepted")
	}
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "index.html", "../outside.html"); err == nil {
		t.Fatal("root-level package escape was accepted")
	}
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "gallery/index.html", "variant-1/program.exe"); err == nil {
		t.Fatal("unsupported package media type was accepted")
	}
}

func TestOpenSessionV3ArtifactPackageFileUsesExactRootAndNestedLocations(t *testing.T) {
	workspaceRoot := t.TempDir()
	locations := map[string]string{
		"index.html":                                  "<iframe src=\"variants/swarm-14/index.html\"></iframe>",
		"variants/swarm-14/index.html":                "<link rel=\"stylesheet\" href=\"assets/css/site.css\"><script src=\"assets/js/app.js\"></script>",
		"variants/swarm-14/assets/css/site.css":       "body { color: #b7ff32; }",
		"variants/swarm-14/assets/js/app.js":          "document.body.dataset.ready = 'true'",
		"variants/swarm-14/assets/images/cluster.svg": "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>",
	}
	for relative, content := range locations {
		absolute := filepath.Join(workspaceRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatalf("mkdir exact artifact location %q: %v", absolute, err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
			t.Fatalf("write exact artifact location %q: %v", absolute, err)
		}
	}

	tests := []struct {
		name         string
		artifactPath string
		contentPath  string
		wantPath     string
		wantType     string
	}{
		{name: "root entry alias", artifactPath: "index.html", contentPath: sessionsV3ArtifactPackageEntryPath, wantPath: "index.html", wantType: "text/html; charset=utf-8"},
		{name: "root nested html", artifactPath: "index.html", contentPath: "variants/swarm-14/index.html", wantPath: "variants/swarm-14/index.html", wantType: "text/html; charset=utf-8"},
		{name: "root nested css", artifactPath: "index.html", contentPath: "variants/swarm-14/assets/css/site.css", wantPath: "variants/swarm-14/assets/css/site.css", wantType: "text/css; charset=utf-8"},
		{name: "nested entry alias", artifactPath: "variants/swarm-14/index.html", contentPath: sessionsV3ArtifactPackageEntryPath, wantPath: "variants/swarm-14/index.html", wantType: "text/html; charset=utf-8"},
		{name: "nested sibling script", artifactPath: "variants/swarm-14/index.html", contentPath: "assets/js/app.js", wantPath: "variants/swarm-14/assets/js/app.js", wantType: "text/javascript; charset=utf-8"},
		{name: "nested sibling image", artifactPath: "variants/swarm-14/index.html", contentPath: "assets/images/cluster.svg", wantPath: "variants/swarm-14/assets/images/cluster.svg", wantType: "image/svg+xml"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, _, mediaType, err := openSessionV3ArtifactPackageFile(workspaceRoot, test.artifactPath, test.contentPath)
			if err != nil {
				t.Fatalf("open workspace_root=%q artifact_path=%q content_path=%q: %v", workspaceRoot, test.artifactPath, test.contentPath, err)
			}
			got, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("read workspace_root=%q artifact_path=%q content_path=%q: read=%v close=%v", workspaceRoot, test.artifactPath, test.contentPath, readErr, closeErr)
			}
			if string(got) != locations[test.wantPath] || mediaType != test.wantType {
				t.Fatalf("workspace_root=%q artifact_path=%q content_path=%q resolved=%q media=%q, want content from %q media=%q", workspaceRoot, test.artifactPath, test.contentPath, string(got), mediaType, test.wantPath, test.wantType)
			}
		})
	}
}

func TestCollectSessionV3ArtifactBundleIncludesHTMLPackageTree(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"gallery/index.html":             "<iframe src=\"variant/index.html\"></iframe>",
		"gallery/variant/index.html":     "<script src=\"../assets/app.js\"></script>",
		"gallery/assets/app.js":          "document.body.dataset.ready = 'true'",
		"gallery/assets/styles/site.css": "body { color: red; }",
	} {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	packageRoot, files, err := collectSessionV3ArtifactBundle(root, "gallery/index.html", true)
	if err != nil {
		t.Fatalf("collect bundle: %v", err)
	}
	if packageRoot != "gallery" {
		t.Fatalf("package root = %q", packageRoot)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.RelativePath)
	}
	want := []string{"assets/app.js", "assets/styles/site.css", "index.html", "variant/index.html"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("bundle files = %v, want %v", got, want)
	}
}

func TestResolveSessionV3NativeManagedArtifactUsesOpaqueMetadata(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "unused.txt", "unused")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	created, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, TaskCallID: "call-1", ChildSessionID: "child-1"}, artifact.CreateInput{
		RequestID: "native-create", CollectionID: "native-collection", CollectionName: "Designer alternatives", VariantID: "native-variant", Filename: "design.txt", MediaType: "text/plain", Presentation: pebblestore.SessionArtifactPresentation{Kind: "text", Label: "Design", Previewable: true}, Body: []byte("native managed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, found, err := server.resolveSessionV3Artifact(context.Background(), principal, created.SessionID, created.ID)
	if err != nil || !found || resolved.Managed == nil || resolved.Descriptor.ID != created.ID || resolved.Reference.Path != "design.txt" {
		t.Fatalf("resolve native managed = found=%t resolved=%+v err=%v", found, resolved, err)
	}
	witched := testPrincipal()
	witched.AccountScopeID = "account-2"
	if _, found, err := server.resolveSessionV3Artifact(context.Background(), witched, created.SessionID, created.ID); err != nil || found {
		t.Fatalf("cross-account native resolve = found=%t err=%v", found, err)
	}
	file, _, err := server.openManagedSessionV3Artifact(context.Background(), mustSession(t, server, created.SessionID), resolved)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	file.Close()
	if readErr != nil || string(data) != "native managed" {
		t.Fatalf("native managed bytes = %q err=%v", data, readErr)
	}
}

func TestManagedArtifactCatalogShowsPrivateReadyArtifactWithoutRepositoryOutput(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "existing.txt", "existing workspace file")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "catalog-private-create", CollectionID: "catalog-private-collection", CollectionName: "Private catalog", VariantID: "catalog-private-variant", Filename: "preview.txt", MediaType: "text/plain", Presentation: pebblestore.SessionArtifactPresentation{Kind: "text", Label: "Private preview", Previewable: true}, Body: []byte("managed bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := mustSession(t, server, variant.SessionID).WorkspacePath
	if _, err := os.Stat(filepath.Join(workspace, "artifacts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed create wrote repository artifacts/: %v", err)
	}
	req := httptest.NewRequest("GET", "/v3/artifacts?limit=2000", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), variant.ID) || !strings.Contains(rec.Body.String(), "Private preview") {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagedArtifactCatalogProjectsStagingIterationGroupProgress(t *testing.T) {
	server, sessionSvc, _, _, _, _, _ := newLegacyArtifactImportFixture(t, "unused.txt", "unused")
	principal := testPrincipal()
	sessionID := "legacy-artifact-session"
	collection := pebblestore.SessionArtifactCollection{ID: "iteration-collection", Name: "Navigation iterations", Description: "Iteration Swarm group · 2 iterations", Lineage: pebblestore.SessionArtifactLineage{ParentSessionID: sessionID, TaskCallID: "call-swarm", IterationGroupID: "group-1"}}
	for index, theme := range []string{"compact", "spacious"} {
		label := strings.ToUpper(theme[:1]) + theme[1:]
		variant := pebblestore.SessionArtifactVariant{ID: fmt.Sprintf("iteration-variant-%d", index+1), CollectionID: collection.ID, Lineage: pebblestore.SessionArtifactLineage{ParentSessionID: sessionID, SourceSessionID: fmt.Sprintf("child-%d", index+1), TaskCallID: "call-swarm", ChildSessionID: fmt.Sprintf("child-%d", index+1), IterationGroupID: "group-1", IterationGroup: "navigation", IterationID: fmt.Sprintf("iteration-%d", index+1), IterationIndex: index + 1, IterationLabel: label, IterationTheme: theme}, Presentation: pebblestore.SessionArtifactPresentation{Label: label}}
		payload, err := json.Marshal(variant)
		if err != nil {
			t.Fatal(err)
		}
		result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "catalog-stage-" + variant.ID, IdempotencyKey: "catalog-stage-" + variant.ID, PayloadHash: fmt.Sprintf("%x", payload), RequestHash: fmt.Sprintf("%x", payload), Kind: sessionruntime.SessionMutationCreateArtifact, Artifact: &sessionruntime.ArtifactMutation{Collection: collection, Variant: &variant}, NowUnixMs: time.Now().UnixMilli()})
		if err != nil || result.Artifact == nil || result.Artifact.Variant == nil {
			t.Fatalf("stage iteration %d: result=%+v err=%v", index+1, result, err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v3/artifacts?limit=2000", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Artifacts []sessionsV3ArtifactCatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, item := range payload.Artifacts {
		if item.CollectionID != collection.ID {
			continue
		}
		matched++
		if item.Status != pebblestore.SessionArtifactStatusStaging || item.Progress == nil || item.Progress.Total != 2 || item.Progress.Staging != 2 || item.Progress.Ready != 0 || item.Lineage == nil || item.Lineage.IterationGroupID != "group-1" || item.Lineage.IterationIndex < 1 {
			t.Fatalf("staging catalog item = %+v", item)
		}
	}
	if matched != 2 {
		t.Fatalf("staging catalog matched=%d artifacts=%+v", matched, payload.Artifacts)
	}
}

func TestManagedSVGArtifactCatalogAndEndpointExposeInlinePreview(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "unused.svg", "unused")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "svg-preview-create", CollectionID: "svg-preview-collection", CollectionName: "SVG preview", VariantID: "svg-preview-variant", Filename: "preview.svg", MediaType: "image/svg+xml", Presentation: pebblestore.SessionArtifactPresentation{Kind: "image", Label: "SVG preview", Previewable: true}, Body: []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`),
	})
	if err != nil {
		t.Fatal(err)
	}

	catalogReq := httptest.NewRequest(http.MethodGet, "/v3/artifacts?limit=2000", nil)
	catalogRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(catalogRec, withTestPrincipal(catalogReq))
	if catalogRec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogRec.Code, catalogRec.Body.String())
	}
	var payload struct {
		Artifacts []sessionsV3ArtifactCatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(catalogRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var cataloged *sessionsV3ArtifactCatalogItem
	for i := range payload.Artifacts {
		if payload.Artifacts[i].ArtifactID == variant.ID {
			cataloged = &payload.Artifacts[i]
			break
		}
	}
	if cataloged == nil || cataloged.MediaType != "image/svg+xml" || cataloged.Kind != "image" || !cataloged.Previewable || cataloged.Category != "visual" {
		t.Fatalf("SVG catalog entry = %+v", cataloged)
	}

	previewReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+variant.SessionID+"/artifacts/"+variant.ID, nil)
	previewRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewRec, withTestPrincipal(previewReq))
	if previewRec.Code != http.StatusOK || previewRec.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(previewRec.Header().Get("Content-Disposition"), "inline") || !strings.Contains(previewRec.Body.String(), "<svg") {
		t.Fatalf("SVG preview status=%d type=%q disposition=%q body=%s", previewRec.Code, previewRec.Header().Get("Content-Type"), previewRec.Header().Get("Content-Disposition"), previewRec.Body.String())
	}
}

func TestManagedArtifactCatalogSuppressesUnavailableLegacyDuplicateForNativeHandoff(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "unused.html", "unused")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, RunID: "run-native", PlanID: "plan-native", CheckpointID: "cp-native", AttemptID: "attempt-native"}, artifact.CreateInput{
		RequestID: "native-handoff-create", CollectionID: "native-handoff-collection", CollectionName: "Native handoff", VariantID: "native-handoff-variant", Filename: "managed.html", MediaType: "text/html", Presentation: pebblestore.SessionArtifactPresentation{Kind: "html", Label: "Managed handoff", Previewable: true}, Body: []byte("<!doctype html><title>managed</title>"),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := &pebblestore.SessionPlanDocument{ID: "plan-native", Title: "Native plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-native", Status: sessionruntime.PlanCheckpointStatusCompleted, RunID: "run-native", AttemptID: "attempt-native", CompletedAt: time.Now().UnixMilli(), Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "managed.html", Role: "deliverable", Description: "Managed handoff", MediaType: "text/html"}}, Handoff: &pebblestore.SessionPlanCheckpointHandoff{Overview: "done"}}}}
	if _, _, err := sessionSvc.SavePlanWithMetadata(variant.SessionID, doc.ID, doc.Title, "", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: doc}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v3/artifacts?limit=2000", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Artifacts []sessionsV3ArtifactCatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	matching := make([]sessionsV3ArtifactCatalogItem, 0, 2)
	for _, item := range payload.Artifacts {
		if item.SessionID == variant.SessionID && item.Filename == variant.Filename {
			matching = append(matching, item)
		}
	}
	if len(matching) != 1 || matching[0].ArtifactID != variant.ID || matching[0].Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("managed handoff catalog entries = %+v", matching)
	}
	previewReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+variant.SessionID+"/artifacts/"+variant.ID, nil)
	previewRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewRec, withTestPrincipal(previewReq))
	if previewRec.Code != http.StatusOK || !strings.Contains(previewRec.Body.String(), "<title>managed</title>") {
		t.Fatalf("managed handoff preview status=%d body=%s", previewRec.Code, previewRec.Body.String())
	}
	accessBody := bytes.NewBufferString(`{"artifact_id":"` + variant.ID + `"}`)
	accessReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+variant.SessionID+"/artifacts/preview-access", accessBody)
	accessReq.Header.Set("Content-Type", "application/json")
	accessRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(accessRec, withTestPrincipal(accessReq))
	if accessRec.Code != http.StatusOK || !strings.Contains(accessRec.Body.String(), `"token"`) {
		t.Fatalf("managed HTML preview access status=%d body=%s", accessRec.Code, accessRec.Body.String())
	}
}

func TestManagedSessionV3ArtifactPackageEntryPrefersRootIndexAndFallsBackToHTML(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		want    string
	}{
		{name: "root index", entries: []string{"variant.html", "index.html"}, want: "index.html"},
		{name: "fallback html", entries: []string{"assets/site.css", "preview/main.html"}, want: "preview/main.html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload bytes.Buffer
			writer := zip.NewWriter(&payload)
			for _, name := range tc.entries {
				entry, err := writer.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte(name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive, err := zip.NewReader(bytes.NewReader(payload.Bytes()), int64(payload.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if got := managedSessionV3ArtifactPackageEntry(archive); got != tc.want {
				t.Fatalf("entry = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyManagedArtifactIDsAreStableAndOpaque(t *testing.T) {
	collectionA, variantA := sessionsV3LegacyManagedArtifactIDs("session", "plan", "checkpoint", "art_legacy")
	collectionB, variantB := sessionsV3LegacyManagedArtifactIDs("session", "plan", "checkpoint", "art_legacy")
	if collectionA != collectionB || variantA != variantB {
		t.Fatalf("legacy managed IDs are not stable: %q/%q vs %q/%q", collectionA, variantA, collectionB, variantB)
	}
	for _, id := range []string{collectionA, variantA} {
		if strings.Contains(id, "session") || strings.Contains(id, "plan") || strings.ContainsAny(id, `/\\`) {
			t.Fatalf("managed ID exposes source identity or path syntax: %q", id)
		}
	}
	_, other := sessionsV3LegacyManagedArtifactIDs("other-session", "plan", "checkpoint", "art_legacy")
	if other == variantA {
		t.Fatal("managed variant ID was reused across sessions")
	}
}

func TestImportLegacySessionV3ArtifactOrdinaryFileIsConcurrentAndIdempotent(t *testing.T) {
	server, sessionSvc, registry, plan, checkpoint, reference, descriptor := newLegacyArtifactImportFixture(t, "note.txt", "legacy file")
	collectionID, variantID := sessionsV3LegacyManagedArtifactIDs(plan.SessionID, plan.ID, checkpoint.ID, descriptor.ID)
	principal := testPrincipal()

	const imports = 8
	start := make(chan struct{})
	results := make(chan pebblestore.SessionArtifactVariant, imports)
	errs := make(chan error, imports)
	var wg sync.WaitGroup
	for i := 0; i < imports; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			managed, err := server.importLegacySessionV3Artifact(context.Background(), principal, plan, checkpoint, reference, descriptor, collectionID, variantID)
			results <- managed
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent legacy import: %v", err)
		}
	}
	var digest string
	for managed := range results {
		if managed.Status != pebblestore.SessionArtifactStatusReady || managed.ID != variantID || managed.DigestSHA256 == "" {
			t.Fatalf("managed artifact = %+v", managed)
		}
		if digest == "" {
			digest = managed.DigestSHA256
		} else if managed.DigestSHA256 != digest {
			t.Fatalf("legacy import digest = %q, want %q", managed.DigestSHA256, digest)
		}
	}
	managed, ok, err := sessionSvc.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID)
	if err != nil || !ok {
		t.Fatalf("managed variant missing: ok=%t variant=%+v err=%v", ok, managed, err)
	}
	service, err := registry.ServiceForSession(plan.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := service.Read(context.Background(), managed, 1024)
	if err != nil || string(data) != "legacy file" {
		t.Fatalf("managed legacy bytes = %q err=%v", data, err)
	}
}

func TestImportLegacySessionV3ArtifactPackageAndMissingSource(t *testing.T) {
	t.Run("html package", func(t *testing.T) {
		server, _, registry, plan, checkpoint, reference, descriptor := newLegacyArtifactImportFixture(t, "gallery/index.html", "<h1>legacy</h1>")
		asset := filepath.Join(sessionV3ArtifactWorkspaceRoot(mustSession(t, server, plan.SessionID)), "gallery", "site.css")
		if err := os.WriteFile(asset, []byte("body{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		collectionID, variantID := sessionsV3LegacyManagedArtifactIDs(plan.SessionID, plan.ID, checkpoint.ID, descriptor.ID)
		managed, err := server.importLegacySessionV3Artifact(context.Background(), testPrincipal(), plan, checkpoint, reference, descriptor, collectionID, variantID)
		if err != nil {
			t.Fatal(err)
		}
		if managed.Status != pebblestore.SessionArtifactStatusReady || managed.MediaType != "application/zip" || managed.Presentation.Kind != "package" {
			t.Fatalf("managed package = %+v", managed)
		}
		service, err := registry.ServiceForSession(plan.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		data, _, err := service.Read(context.Background(), managed, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if len(archive.File) != 2 || archive.File[0].Name != "index.html" || archive.File[1].Name != "site.css" {
			t.Fatalf("managed package entries = %+v", archive.File)
		}
	})

	t.Run("missing source unavailable", func(t *testing.T) {
		server, _, _, plan, checkpoint, reference, descriptor := newLegacyArtifactImportFixture(t, "missing.txt", "")
		if err := os.Remove(filepath.Join(sessionV3ArtifactWorkspaceRoot(mustSession(t, server, plan.SessionID)), reference.Path)); err != nil {
			t.Fatal(err)
		}
		collectionID, variantID := sessionsV3LegacyManagedArtifactIDs(plan.SessionID, plan.ID, checkpoint.ID, descriptor.ID)
		managed, err := server.importLegacySessionV3Artifact(context.Background(), testPrincipal(), plan, checkpoint, reference, descriptor, collectionID, variantID)
		if err != nil {
			t.Fatal(err)
		}
		if managed.Status != pebblestore.SessionArtifactStatusUnavailable || managed.FailureCode != "legacy_source_unavailable" || managed.DigestSHA256 != "" || managed.Size != 0 {
			t.Fatalf("missing legacy artifact = %+v", managed)
		}
		repeated, err := server.importLegacySessionV3Artifact(context.Background(), testPrincipal(), plan, checkpoint, reference, descriptor, collectionID, variantID)
		if err != nil || repeated.Status != pebblestore.SessionArtifactStatusUnavailable {
			t.Fatalf("repeated missing import = %+v err=%v", repeated, err)
		}
	})
}

func TestImportLegacySessionV3ArtifactRejectsOwnershipMismatch(t *testing.T) {
	server, _, _, plan, checkpoint, reference, descriptor := newLegacyArtifactImportFixture(t, "note.txt", "legacy file")
	collectionID, variantID := sessionsV3LegacyManagedArtifactIDs(plan.SessionID, plan.ID, checkpoint.ID, descriptor.ID)
	wrong := testPrincipal()
	wrong.AccountScopeID = "account-2"
	if _, err := server.importLegacySessionV3Artifact(context.Background(), wrong, plan, checkpoint, reference, descriptor, collectionID, variantID); err == nil {
		t.Fatal("legacy import accepted mismatched account ownership")
	}
}

func newLegacyArtifactImportFixture(t *testing.T, relativePath, content string) (*Server, *sessionruntime.Service, *artifact.Registry, pebblestore.SessionPlanSnapshot, pebblestore.SessionPlanCheckpoint, pebblestore.SessionPlanArtifactReference, pebblestore.PlanFinalHandoffArtifact) {
	t.Helper()
	workspace := t.TempDir()
	t.Setenv("STATE_DIRECTORY", filepath.Join(workspace, "..", "private-data"))
	absolute := filepath.Join(workspace, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "legacy-artifact-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID,
		Title: "Legacy artifact", WorkspacePath: workspace, WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := artifact.NewRegistry(sessionSvc, artifact.Limits{})
	server.SetArtifactRegistry(registry)
	checkpoint := pebblestore.SessionPlanCheckpoint{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusCompleted, RunID: "run-1", AttemptID: "attempt-1", Handoff: &pebblestore.SessionPlanCheckpointHandoff{Overview: "done"}}
	reference := pebblestore.SessionPlanArtifactReference{Path: filepath.ToSlash(relativePath), Role: "deliverable", Description: "Legacy deliverable"}
	descriptors := sessionruntime.ProjectPlanFinalHandoffArtifacts("plan-1", checkpoint.ID, []pebblestore.SessionPlanArtifactReference{reference})
	if len(descriptors) != 1 {
		t.Fatalf("project legacy descriptor = %+v", descriptors)
	}
	plan := pebblestore.SessionPlanSnapshot{ID: "plan-1", SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID}
	return server, sessionSvc, registry, plan, checkpoint, reference, descriptors[0]
}

func mustSession(t *testing.T, server *Server, sessionID string) pebblestore.SessionSnapshot {
	t.Helper()
	session, ok, err := server.sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%t err=%v", ok, err)
	}
	return session
}

func TestSessionsV3ArtifactMessageSelectionContract(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "note.txt", "legacy file")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "message-ref-create", CollectionID: "message-ref-collection", CollectionName: "Review variants", VariantID: "message-ref-variant", Filename: "review.txt", MediaType: "text/plain", Presentation: pebblestore.SessionArtifactPresentation{Kind: "text", Label: "Review choice", Description: "Chosen design"}, Body: []byte("private artifact bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := pebblestore.SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq, Action: "use"}
	accepted, job, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-accepted", Role: "user", Content: "Use this design", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{selection}})
	if err != nil || job != nil || accepted.Message == nil || len(accepted.Message.ArtifactSelections) != 1 {
		t.Fatalf("accept artifact message: result=%+v job=%+v err=%v", accepted, job, err)
	}
	stored := accepted.Message.ArtifactSelections[0]
	if stored.Label != "Review choice" || stored.Description != "Chosen design" || stored.EventSeq != variant.EventSeq {
		t.Fatalf("stored normalized selection = %+v", stored)
	}
	encoded, err := json.Marshal(accepted.Message)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private artifact bytes")) || bytes.Contains(encoded, []byte("private-data")) {
		t.Fatalf("message leaked artifact bytes or private path: %s", encoded)
	}

	replayed, _, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-accepted", Role: "user", Content: "Use this design", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{selection}})
	if err != nil || !replayed.Replayed {
		t.Fatalf("idempotent replay = replayed=%t err=%v", replayed.Replayed, err)
	}
	changed := selection
	changed.Action = "select"
	if _, _, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-accepted", Role: "user", Content: "Use this design", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{changed}}); err == nil || !errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
		t.Fatalf("changed selection idempotency error = %v", err)
	}
	stale := selection
	stale.EventSeq++
	if _, _, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-stale", Role: "user", Content: "Use stale", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{stale}}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale selection error = %v", err)
	}
	other, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "other-owned-session", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		Title: "Other", WorkspacePath: t.TempDir(),
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	crossSession := selection
	crossSession.SessionID = other.ID
	if _, _, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-cross-session", Role: "user", Content: "Use mismatched session", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{crossSession}}); err == nil {
		t.Fatal("cross-session artifact selection was accepted")
	}
	wrong := principal
	wrong.AccountScopeID = "account-2"
	if _, _, err := server.acceptSessionsV3Message(wrong, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-cross-account", Role: "user", Content: "Use foreign", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{selection}}); err == nil {
		t.Fatal("cross-account artifact selection was accepted")
	}
	tooMany := make([]pebblestore.SessionArtifactSelectionReference, pebblestore.SessionArtifactMaxMessageSelections+1)
	for i := range tooMany {
		tooMany[i] = selection
	}
	if _, _, err := server.acceptSessionsV3Message(principal, variant.SessionID, sessionsV3MessageRequest{ClientRequestID: "message-ref-bounds", Role: "user", Content: "Too many", ArtifactSelections: tooMany}); err == nil || !strings.Contains(err.Error(), "count limit") {
		t.Fatalf("selection bounds error = %v", err)
	}
}

func TestSessionV3ArtifactSelectionActionPersistsThroughMutation(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newLegacyArtifactImportFixture(t, "note.txt", "legacy file")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "legacy-artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{RequestID: "action-create", CollectionID: "action-collection", CollectionName: "Actions", VariantID: "action-variant", Filename: "action.txt", MediaType: "text/plain", Body: []byte("ready")})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(`{"client_request_id":"select-action","event_seq":%d,"action":"use"}`, variant.EventSeq))
	req := httptest.NewRequest("POST", "/v3/sessions/"+variant.SessionID+"/artifacts/"+variant.ID+"/selection", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != 200 {
		t.Fatalf("selection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Selection pebblestore.SessionArtifactSelectionReference `json:"selection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Selection.VariantID != variant.ID || response.Selection.EventSeq <= variant.EventSeq || response.Selection.Action != "use" {
		t.Fatalf("selection response = %+v", response.Selection)
	}
	if !strings.Contains(response.Selection.Label, "Actions") && !strings.Contains(response.Selection.Label, "action.txt") {
		t.Fatalf("selection label = %q", response.Selection.Label)
	}
	collection, ok, err := sessionSvc.GetSessionArtifactCollection(principal.AccountScopeID, variant.SessionID, variant.CollectionID)
	if err != nil || !ok || collection.SelectedVariantID != variant.ID || collection.EventSeq != response.Selection.EventSeq {
		t.Fatalf("selected collection = %+v ok=%t err=%v", collection, ok, err)
	}
	body = bytes.NewBufferString(`{"client_request_id":"select-action-stale","event_seq":0,"action":"select"}`)
	req = httptest.NewRequest("POST", "/v3/sessions/"+variant.SessionID+"/artifacts/"+variant.ID+"/selection", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "event sequence") {
		t.Fatalf("stale selection status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBuildSessionV3LegacyArtifactPackageRejectsSymlinkAndPreservesFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("<link href=\"assets/site.css\">"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "assets", "site.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := buildSessionV3LegacyArtifactPackage(root, "gallery/index.html")
	if err != nil {
		t.Fatalf("build package: %v", err)
	}
	repeated, err := buildSessionV3LegacyArtifactPackage(root, "gallery/index.html")
	if err != nil || !bytes.Equal(payload, repeated) {
		t.Fatalf("package is not deterministic: err=%v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("open package: %v", err)
	}
	got := make([]string, 0, len(archive.File))
	for _, entry := range archive.File {
		got = append(got, entry.Name)
	}
	if strings.Join(got, "|") != "assets/site.css|index.html" {
		t.Fatalf("package entries = %v", got)
	}
	if archive.File[0].Mode().Perm() != 0o600 || archive.File[1].Mode().Perm() != 0o600 {
		t.Fatalf("package modes = %v, %v", archive.File[0].Mode(), archive.File[1].Mode())
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "gallery", "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := buildSessionV3LegacyArtifactPackage(root, "gallery/index.html"); err == nil {
		t.Fatal("legacy package import accepted a symlink")
	}
}

func TestCollectSessionV3ArtifactBundleRejectsPackageSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "gallery", "private.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectSessionV3ArtifactBundle(root, "gallery/index.html", true); err == nil {
		t.Fatal("bundle accepted a package symlink")
	}
}

func TestSessionsV3ArtifactOutputRequirementsProjectionClonesSnapshot(t *testing.T) {
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	projected := cloneSessionsV3ArtifactOutputRequirements(requirements)
	if projected == nil || *projected != *requirements {
		t.Fatalf("projected = %#v", projected)
	}
	requirements.Width = 1
	if projected.Width != 1500 {
		t.Fatalf("projection aliases stored requirements: %#v", projected)
	}
}

func TestSessionsV3VideoArtifactRangeServingAndVisualCategory(t *testing.T) {
	server, sessionSvc, registry, plan, checkpoint, _, _ := newLegacyArtifactImportFixture(t, "note.txt", "fixture")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)

	mp4Header := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42")
	mp4Content := append(mp4Header, bytes.Repeat([]byte{0x42}, 128)...)

	created, err := authority.Create(context.Background(), artifact.Principal{
		SessionID:      plan.SessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		RunID:          checkpoint.RunID,
		PlanID:         plan.ID,
		CheckpointID:   checkpoint.ID,
		AttemptID:      checkpoint.AttemptID,
	}, artifact.CreateInput{
		RequestID:      "req-video-1",
		CollectionID:   "video-col-1",
		CollectionName: "Rendered Clip",
		VariantID:      "video-var-1",
		Filename:       "output.mp4",
		MediaType:      "video/mp4",
		Presentation:   pebblestore.SessionArtifactPresentation{Kind: "video", Label: "Output Clip"},
		Body:           mp4Content,
	})
	if err != nil {
		t.Fatalf("create video artifact: %v", err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("video status = %s", created.Status)
	}

	// 1. Check artifact catalog projection has Category == "visual" and Kind == "video"
	catalogReq := httptest.NewRequest("GET", "/v3/artifacts", nil)
	catalogRec := httptest.NewRecorder()
	server.handleSessionsV3Artifacts(catalogRec, catalogReq.WithContext(identity.ContextWithPrincipal(catalogReq.Context(), principal)))
	if catalogRec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", catalogRec.Code, catalogRec.Body.String())
	}
	var catalogResp struct {
		OK        bool                            `json:"ok"`
		Artifacts []sessionsV3ArtifactCatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(catalogRec.Body.Bytes(), &catalogResp); err != nil {
		t.Fatal(err)
	}
	var foundVideo bool
	for _, item := range catalogResp.Artifacts {
		if item.ArtifactID == created.ID {
			foundVideo = true
			if item.Category != "visual" {
				t.Fatalf("catalog category = %q, want visual", item.Category)
			}
			if item.Kind != "video" {
				t.Fatalf("catalog kind = %q, want video", item.Kind)
			}
			if item.MediaType != "video/mp4" {
				t.Fatalf("catalog media_type = %q, want video/mp4", item.MediaType)
			}
			if !item.Previewable {
				t.Fatalf("catalog previewable = false, want true")
			}
		}
	}
	if !foundVideo {
		t.Fatal("video artifact not found in catalog")
	}

	// 2. Full GET request checks headers (Accept-Ranges, Content-Type, CSP media-src)
	getReq := httptest.NewRequest("GET", "/v3/sessions/"+plan.SessionID+"/artifacts/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	server.handleSessionV3Artifact(getRec, getReq, principal, plan.SessionID, created.ID)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get artifact status = %d: %s", getRec.Code, getRec.Body.String())
	}
	if getRec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", getRec.Header().Get("Accept-Ranges"))
	}
	if getRec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", getRec.Header().Get("Content-Type"))
	}
	csp := getRec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "media-src") {
		t.Fatalf("CSP missing media-src directive: %q", csp)
	}
	if !bytes.Equal(getRec.Body.Bytes(), mp4Content) {
		t.Fatalf("body length = %d, want %d", getRec.Body.Len(), len(mp4Content))
	}

	// 3. Range request returns 206 Partial Content
	rangeReq := httptest.NewRequest("GET", "/v3/sessions/"+plan.SessionID+"/artifacts/"+created.ID, nil)
	rangeReq.Header.Set("Range", "bytes=0-15")
	rangeRec := httptest.NewRecorder()
	server.handleSessionV3Artifact(rangeRec, rangeReq, principal, plan.SessionID, created.ID)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("range request status = %d, want 206", rangeRec.Code)
	}
	if rangeRec.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-15/%d", len(mp4Content)) {
		t.Fatalf("Content-Range = %q, want bytes 0-15/%d", rangeRec.Header().Get("Content-Range"), len(mp4Content))
	}
	if !bytes.Equal(rangeRec.Body.Bytes(), mp4Content[0:16]) {
		t.Fatalf("range body mismatch")
	}
}
