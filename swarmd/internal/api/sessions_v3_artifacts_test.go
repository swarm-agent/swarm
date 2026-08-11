package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

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
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "gallery/index.html", "../outside.html"); err == nil {
		t.Fatal("package directory escape was accepted")
	}
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "index.html", "variant-1/index.html"); err == nil {
		t.Fatal("root-level html artifact was accepted as a package")
	}
	if _, _, _, err := openSessionV3ArtifactPackageFile(root, "gallery/index.html", "variant-1/program.exe"); err == nil {
		t.Fatal("unsupported package media type was accepted")
	}
}
