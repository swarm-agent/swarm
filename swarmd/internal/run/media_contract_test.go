package run

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/claudecode"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: prove ResolveMediaAdapterDeclaration and CompileSessionMediaContract
// admit Claude Code images only with real catalog and account credential facts.
// A temporary catalog/auth integration is the narrowest layer that catches
// source-provider rekeying errors as well as missing declarations, without a CLI.
func TestClaudeCodeCatalogMediaContract(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "media.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(db))
	catalog.UseEmbeddedOnly()
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	records, err := catalog.List("claudecode", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var modelID string
	for _, record := range records {
		if record.Media != nil {
			for _, input := range record.Media.Inputs {
				if input.Modality == "image" && input.State == pebblestore.ModelCatalogMediaStateSupported {
					modelID = record.Model
					break
				}
			}
		}
		if modelID != "" {
			break
		}
	}
	if modelID == "" {
		t.Fatal("embedded Claude catalog has no image model")
	}
	lookup, err := catalog.Get("claudecode", modelID)
	if err != nil {
		t.Fatal(err)
	}
	auth := pebblestore.NewAuthStore(db)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{ID: "fixture-cli", AccountScopeID: "media-account", Provider: "claudecode", Type: pebblestore.AuthTypeCLI, SetActive: true}); err != nil {
		t.Fatal(err)
	}
	runner := claudecode.NewRunner(auth, nil)
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "fixture-user", AccountScopeID: "media-account"})
	declaration := ResolveMediaAdapterDeclaration(ctx, "claudecode", runner)
	input := SessionMediaContractInput{ProviderID: "claudecode", Model: modelID, Catalog: &lookup.Record, CatalogMeta: lookup.Meta, Adapter: declaration, AgentAuthorized: true, ExecutionMode: "auto", WorkspaceScope: "fixture-workspace", SessionScope: "fixture-session"}
	assertAllowed := func(contract provideriface.SessionMediaContract, want bool) {
		t.Helper()
		allowed := false
		for _, cap := range contract.Capabilities {
			if cap.Modality == "image" && cap.State == provideriface.MediaCapabilityStateAllowed {
				allowed = true
				if cap.MaxBytes <= 0 || cap.MaxCount <= 0 || len(cap.MIMETypes) != 4 {
					t.Fatalf("invalid limits: %+v", cap)
				}
			}
		}
		if allowed != want {
			t.Fatalf("allowed=%v want=%v: %+v", allowed, want, contract)
		}
	}
	assertAllowed(CompileSessionMediaContract(input), true)
	// Purpose: the authority comparator must ignore provenance only, preserving
	// adapter, credential, session/project scope and transport limits. Direct
	// compiled inputs are the narrowest layer for each independent mutation.
	start := CompileSessionMediaContract(input)
	for _, change := range []struct {
		name   string
		mutate func(*SessionMediaContractInput)
	}{
		{"adapter", func(i *SessionMediaContractInput) { i.Adapter.AdapterID = "other" }},
		{"credential surface", func(i *SessionMediaContractInput) { i.Adapter.CredentialSurface = "other" }},
		{"credential", func(i *SessionMediaContractInput) { i.Adapter.CredentialFingerprint = "other" }},
		{"session", func(i *SessionMediaContractInput) { i.SessionScope = "other" }},
		{"project", func(i *SessionMediaContractInput) { i.ProjectScope = "other" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := input
			change.mutate(&changed)
			if sameSessionMediaAuthority(start, CompileSessionMediaContract(changed)) {
				t.Fatal("changed authority accepted")
			}
		})
	}
	t.Run("catalog refresh during worker inspection", func(t *testing.T) {
		testMediaInspectCatalogRefresh(t, db, catalog, runner, ctx, input)
	})
	input.Catalog = nil
	assertAllowed(CompileSessionMediaContract(input), false)
	input.Catalog = &lookup.Record
	input.Adapter = ResolveMediaAdapterDeclaration(identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "fixture-user", AccountScopeID: "other-account"}), "claudecode", runner)
	assertAllowed(CompileSessionMediaContract(input), false)
}

// Purpose: executeProviderManagedMediaInspect must survive a catalog publication
// without accepting changed authority or returning media on rejection. A real
// temporary catalog/session store and local PNG exercise the invocation boundary
// without a provider process; sessionMediaContractInput is the shared step/call owner.
func testMediaInspectCatalogRefresh(t *testing.T, db *pebblestore.Store, catalog *model.CatalogService, runner provideriface.Runner, ctx context.Context, input SessionMediaContractInput) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "fixture-user", AccountScopeID: "media-account"}
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	workspace := t.TempDir()
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "fixture-worker", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: workspace, Preference: &pebblestore.ModelPreference{Provider: input.ProviderID, Model: input.Model, Thinking: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	// Worker project membership is not project-conversation authority.
	session.Metadata = map[string]any{"project_id": "fixture-project", "agent_name": "system-coder"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	s := &Service{sessions: sessions, providers: providers, model: model.NewService(pebblestore.NewModelStore(db), nil, catalog)}
	config := providerToolInvokerConfig{sessionID: session.ID, workspacePath: workspace, workspaceRoots: []string{workspace}, principal: principal, providerID: input.ProviderID, model: input.Model, mediaExecutionMode: "auto", agentProfile: pebblestore.AgentProfile{ToolScope: &pebblestore.AgentToolScope{AllowTools: []string{mediaInspectToolName}}}}
	startInput, err := s.sessionMediaContractInput(config, session, input.Catalog, input.CatalogMeta, input.Adapter)
	if err != nil {
		t.Fatal(err)
	}
	config.mediaContract = CompileSessionMediaContract(startInput)
	callInput, err := s.sessionMediaContractInput(config, session, input.Catalog, input.CatalogMeta, input.Adapter)
	if err != nil {
		t.Fatal(err)
	}
	if got := CompileSessionMediaContract(callInput); got.Hash != config.mediaContract.Hash || !SessionMediaContractAllows(got, "image", "image/png", "png") {
		t.Fatalf("worker step/call contract mismatch: %+v", got)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "image.png"), pngBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{CallID: "inspect", Name: mediaInspectToolName, Arguments: `{"path":"image.png"}`}
	inspect := func(config providerToolInvokerConfig, allowed bool) {
		t.Helper()
		result, err := s.executeProviderManagedMediaInspect(ctx, config, call, principal)
		if allowed {
			if err != nil || result.Media == nil || !bytes.Equal(result.Media.Bytes, pngBytes.Bytes()) {
				t.Fatalf("inspection failed: %v %+v", err, result)
			}
		} else if err == nil || result.Media != nil || result.Output != "" {
			t.Fatalf("rejected inspection leaked output: %v %+v", err, result)
		}
	}
	inspect(config, true)
	source, err := catalog.Get("anthropic", input.Model)
	if err != nil {
		t.Fatal(err)
	}
	record, meta := source.Record, *source.Meta
	record.SourceSnapshotID, meta.SnapshotID = "fixture-refreshed", "fixture-refreshed"
	record.SourceSnapshotVersion, meta.SnapshotVersion = "fixture-v2", "fixture-v2"
	if err := pebblestore.NewModelCatalogStore(db).ReplaceSnapshot([]pebblestore.ModelCatalogRecord{record}, meta); err != nil {
		t.Fatal(err)
	}
	inspect(config, true)
	for _, change := range []struct {
		name   string
		mutate func(*providerToolInvokerConfig)
	}{
		{"provider", func(c *providerToolInvokerConfig) { c.providerID = "codex" }},
		{"model", func(c *providerToolInvokerConfig) { c.model = "other-model" }},
		{"workspace", func(c *providerToolInvokerConfig) { c.workspacePath = filepath.Join(workspace, "other") }},
		{"disabled", func(c *providerToolInvokerConfig) { c.mediaInspectDisabled = true }},
		{"authorization", func(c *providerToolInvokerConfig) { c.agentProfile = pebblestore.AgentProfile{} }},
		{"forged", func(c *providerToolInvokerConfig) { c.mediaContract.Hash = "forged" }},
	} {
		t.Run(change.name, func(t *testing.T) { changed := config; change.mutate(&changed); inspect(changed, false) })
	}
	media := *record.Media
	media.State = "unsupported"
	record.Media = &media
	if err := pebblestore.NewModelCatalogStore(db).ReplaceSnapshot([]pebblestore.ModelCatalogRecord{record}, meta); err != nil {
		t.Fatal(err)
	}
	inspect(config, false)
}
