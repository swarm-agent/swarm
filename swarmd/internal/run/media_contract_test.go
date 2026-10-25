package run

import (
	"context"
	"path/filepath"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/claudecode"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	input.Catalog = nil
	assertAllowed(CompileSessionMediaContract(input), false)
	input.Catalog = &lookup.Record
	input.Adapter = ResolveMediaAdapterDeclaration(identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "fixture-user", AccountScopeID: "other-account"}), "claudecode", runner)
	assertAllowed(CompileSessionMediaContract(input), false)
}
