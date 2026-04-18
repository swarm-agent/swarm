package deploy

import (
	"testing"
	"time"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func TestPendingSyncVaultPasswordExpires(t *testing.T) {
	svc := &Service{}
	svc.rememberPendingSyncVaultPassword("deploy-1", "vault-password", time.Now().Add(-time.Second).UnixMilli())
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "" {
		t.Fatalf("resolvePendingSyncVaultPassword() = %q, want empty for expired entry", got)
	}
}

func TestPendingSyncVaultPasswordRetainedUntilExpiry(t *testing.T) {
	svc := &Service{}
	svc.rememberPendingSyncVaultPassword("deploy-1", "vault-password", time.Now().Add(time.Minute).UnixMilli())
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "vault-password" {
		t.Fatalf("resolvePendingSyncVaultPassword() = %q, want vault-password", got)
	}
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "vault-password" {
		t.Fatalf("resolvePendingSyncVaultPassword() second read = %q, want vault-password", got)
	}
	svc.clearPendingSyncVaultPassword("deploy-1")
	if got := svc.resolvePendingSyncVaultPassword("deploy-1"); got != "" {
		t.Fatalf("resolvePendingSyncVaultPassword() after clear = %q, want empty", got)
	}
}

func TestCreateResultCanBePersistedRejectsEmptyContainer(t *testing.T) {
	if createResultCanBePersisted(localcontainers.Container{}) {
		t.Fatalf("createResultCanBePersisted(empty) = true, want false")
	}
}

func TestCreateResultCanBePersistedAcceptsRecordedContainer(t *testing.T) {
	container := localcontainers.Container{
		Name:          "child-swarm",
		ContainerName: "child-swarm",
	}
	if !createResultCanBePersisted(container) {
		t.Fatalf("createResultCanBePersisted(container) = false, want true")
	}
}

func TestCreateResultDisplayNameFallsBackToInputName(t *testing.T) {
	got := createResultDisplayName(ContainerCreateInput{Name: "child-swarm"}, localcontainers.Container{})
	if got != "child-swarm" {
		t.Fatalf("createResultDisplayName() = %q, want child-swarm", got)
	}
}
