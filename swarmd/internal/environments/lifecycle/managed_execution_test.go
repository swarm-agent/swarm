package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type managedLeaseStoreFixture struct {
	DeploymentManagerStore
	leases     *pebblestore.LeaseStore
	deployment environments.Deployment
}

func (f *managedLeaseStoreFixture) Leases() *pebblestore.LeaseStore { return f.leases }
func (f *managedLeaseStoreFixture) Get(string, string, string) (environments.Deployment, bool, error) {
	return f.deployment, true, nil
}

type managedConnectionTrap struct {
	ConnectionReader
	calls int
}

func (f *managedConnectionTrap) Get(string, string, string) (environments.Connection, bool, error) {
	f.calls++
	return environments.Connection{}, false, errors.New("unauthorized request reached connection/provider resolution")
}

type managedOperationTrap struct {
	OperationStore
	reads      int
	admissions int
}

func (f *managedOperationTrap) GetActiveOperationForDeployment(string, string, string) (environments.EnvironmentOperation, bool, error) {
	f.reads++
	return environments.EnvironmentOperation{}, false, errors.New("unauthorized operation lookup")
}
func (f *managedOperationTrap) AdmitOperation(environments.EnvironmentOperation) (environments.EnvironmentOperation, bool, error) {
	f.admissions++
	return environments.EnvironmentOperation{}, false, errors.New("unauthorized operation admission")
}

// Requirement: ExecTaskLease rejects stale deployment source and invalid receipt
// ownership before managed operation admission/provider effects. Threat: a
// source replacement, foreign session/account, expiration or changed task attempt
// reaching Submit or provider resolution. ExecTaskLease/ResolveLeaseAccess and
// the durable LeaseStore own this boundary; real isolated Pebble receipt reads
// plus effect traps are its narrowest hermetic proof. This is not provider or
// cancellation qualification; no process/container/daemon/network is started.
func TestManagedExecutionLeaseBoundary(t *testing.T) {
	for _, name := range []string{"source_commit", "container_generation", "expired", "released", "foreign_session", "foreign_account", "wrong_attempt"} {
		t.Run(name, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "receipts"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			build := environments.ImageBuildResult{OperationID: "build", ImageID: "sha256:" + strings.Repeat("a", 64), ConnectionID: "connection", DefinitionDigest: "definition", ContextDigest: "context", Product: environments.CommittedBuildSource{WorkspaceID: "workspace", WorkspaceGeneration: 1, Commit: strings.Repeat("b", 40)}, Recipe: environments.CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("c", 40)}}
			source := environments.PreparedDeploymentSource{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", DeploymentID: "deployment", CreatedAt: 1, ContainerID: "container", Build: build}
			dep := environments.Deployment{ID: source.DeploymentID, AccountScopeID: source.AccountScopeID, WorkspaceID: source.WorkspaceID, EnvironmentID: source.EnvironmentID, ConnectionID: "connection", CreatedAt: source.CreatedAt, Status: environments.DeploymentStatusRunning, Build: &build, Runtime: environments.RuntimeMetadata{ContainerID: source.ContainerID}}
			if !source.Matches(dep) {
				t.Fatal("invalid baseline source fixture")
			}
			lease := environments.DeploymentLease{ID: "receipt", AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: "deployment", EnvironmentID: "environment", ConsumerType: environments.ConsumerTypeSession, ConsumerID: "child", Active: true, Shared: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), PreparedSource: &source, TaskBinding: &environments.TaskLeaseBinding{UserID: "user", ProjectID: "project", TaskID: "task", AttemptID: "attempt", AttachmentID: "attachment", AttachmentRevision: 1}}
			account := "account"
			want := ErrDeploymentLeaseHeld
			switch name {
			case "source_commit":
				changed := build
				changed.Product.Commit = strings.Repeat("d", 40)
				dep.Build = &changed
				want = ErrDeploymentUnusable
			case "container_generation":
				dep.Runtime.ContainerID = "replacement"
				want = ErrDeploymentUnusable
			case "expired":
				lease.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
			case "released":
				lease.Active = false
			case "foreign_session":
				lease.ConsumerID = "other-child"
			case "foreign_account":
				account = "other-account"
			case "wrong_attempt":
				lease.TaskBinding.AttemptID = "previous"
			}
			if err := db.PutJSON(pebblestore.KeyDeploymentLeaseForAccount("account", "workspace", lease.ID), lease); err != nil {
				t.Fatal(err)
			}
			connections, operations := &managedConnectionTrap{}, &managedOperationTrap{}
			manager := NewDeploymentManager(connections, nil, &managedLeaseStoreFixture{leases: pebblestore.NewLeaseStore(db), deployment: dep}, nil, nil)
			defer manager.rootCancel()
			manager.operations = operations
			staleAttempt := errors.New("durable task attempt replaced")
			manager.SetTaskLeaseValidator(func(_ context.Context, receipt environments.DeploymentLease) error {
				if receipt.TaskBinding.AttemptID != "attempt" {
					return staleAttempt
				}
				return nil
			})
			if name == "wrong_attempt" {
				want = staleAttempt
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := manager.ExecTaskLease(ctx, account, "workspace", "receipt", environments.OperationAttribution{Actor: "user", SessionID: "child"}, provider.ExecRequest{Command: []string{"bash", "-c", "exit 0"}})
			if !errors.Is(err, want) || result != nil {
				t.Fatalf("wrong rejection: result=%#v err=%v want=%v", result, err, want)
			}
			if connections.calls != 0 || operations.reads != 0 || operations.admissions != 0 {
				t.Fatalf("unauthorized effect: connections=%d operations=%#v", connections.calls, operations)
			}
			var after environments.DeploymentLease
			if found, err := db.GetJSON(pebblestore.KeyDeploymentLeaseForAccount("account", "workspace", "receipt"), &after); err != nil || !found || after.Active != lease.Active || after.ExpiresAt != lease.ExpiresAt {
				t.Fatalf("rejection mutated receipt: %#v %v", after, err)
			}
		})
	}
}
