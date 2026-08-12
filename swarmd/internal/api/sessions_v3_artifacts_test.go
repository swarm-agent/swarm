package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
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

func TestBuildSessionV3LegacyArtifactPackageRejectsSymlinkAndPreservesFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery", "assets"), 0o755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("<link href=\"assets/site.css\">"), 0o600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(root, "gallery", "assets", "site.css"), []byte("body{}"), 0o600); err != nil { t.Fatal(err) }
	payload, err := buildSessionV3LegacyArtifactPackage(root, "gallery/index.html")
	if err != nil { t.Fatalf("build package: %v", err) }
	repeated, err := buildSessionV3LegacyArtifactPackage(root, "gallery/index.html")
	if err != nil || !bytes.Equal(payload, repeated) { t.Fatalf("package is not deterministic: err=%v", err) }
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil { t.Fatalf("open package: %v", err) }
	got := make([]string, 0, len(archive.File))
	for _, entry := range archive.File { got = append(got, entry.Name) }
	if strings.Join(got, "|") != "assets/site.css|index.html" {
		t.Fatalf("package entries = %v", got)
	}
	if archive.File[0].Mode().Perm() != 0o600 || archive.File[1].Mode().Perm() != 0o600 {
		t.Fatalf("package modes = %v, %v", archive.File[0].Mode(), archive.File[1].Mode())
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil { t.Fatal(err) }
	if err := os.Symlink(outside, filepath.Join(root, "gallery", "linked.txt")); err != nil { t.Fatal(err) }
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
