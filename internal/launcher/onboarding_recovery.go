package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"syscall"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

// Setup recovery is administrator-owned, separate from daemon-owned config.
// It contains identity/stage only, never password bytes or authentication tokens.
type setupRecovery struct {
	Version         int             `json:"version"`
	Account         SelectedAccount `json:"account"`
	Created         bool            `json:"created"`
	Stage           string          `json:"stage"`
	Artifact        string          `json:"artifact"`
	PasswordSkipped bool            `json:"password_skipped,omitempty"`
}

func setupRecoveryDir() (string, error) {
	roots, err := storagecontract.ResolveRoots(storagecontract.Options{})
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(roots.ConfigDir), "swarm-setup"), nil
}

func readSetupRecovery(dir string) (*setupRecovery, error) {
	return readSetupRecoveryWithTrust(dir, trustedFirstInstallPath, trustedFirstInstallMetadata)
}

func readSetupRecoveryWithTrust(dir string, trust func(string) error, metadata func(os.FileInfo) bool) (*setupRecovery, error) {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := trust(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "onboarding.json")
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !metadata(info) || info.Mode().Perm() != 0600 {
		return nil, errors.New("unsafe setup recovery record")
	}
	var r setupRecovery
	dec := json.NewDecoder(io.LimitReader(f, 8193))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if dec.Decode(new(any)) != io.EOF || r.Version != 1 {
		return nil, errors.New("invalid setup recovery record")
	}
	switch r.Stage {
	case "account", "password", "password-started", "ssh", "install", "readiness", "handoff", "done":
	default:
		return nil, errors.New("unknown setup recovery stage")
	}
	return &r, nil
}

func saveSetupRecovery(dir string, r setupRecovery) error {
	return saveSetupRecoveryWithTrust(dir, r, trustedFirstInstallPath)
}

func saveSetupRecoveryWithTrust(dir string, r setupRecovery, trust func(string) error) error {
	if err := trust(dir); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".onboarding-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "onboarding.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// LockOnboarding excludes concurrent privileged account/install transactions.
func LockOnboarding() (func(), error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("setup requires root")
	}
	dir, err := setupRecoveryDir()
	if err != nil {
		return nil, err
	}
	if err := trustedFirstInstallPath(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := trustedFirstInstallPath(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another setup is running")
	}
	return func() { f.Close() }, nil
}

func ResumeOnboardingAccount(artifact string) (*OnboardingAccount, error) {
	dir, err := setupRecoveryDir()
	if err != nil {
		return nil, err
	}
	r, err := readSetupRecovery(dir)
	if err != nil || r == nil {
		return nil, err
	}
	if r.Stage == "done" {
		return nil, errors.New("device setup already completed; use the installed application")
	}
	if r.Artifact != artifact {
		return nil, errors.New("interrupted setup belongs to a different artifact; refusing replacement")
	}
	if r.Stage == "account" {
		return nil, errors.New("account creation was interrupted before identity verification; administrator must inspect the partial account before continuing")
	}
	return resumeSetupAccount(r, defaultInstallAccountOps())
}

func resumeSetupAccount(r *setupRecovery, ops installAccountOps) (*OnboardingAccount, error) {
	if r.Stage == "password" || r.Stage == "ssh" {
		if _, _, found, err := ops.existing(); err != nil || found {
			return nil, errors.New("installation appeared before setup installation stage; refusing resume")
		}
	}
	u, err := r.Account.resolve(ops)
	if err != nil {
		return nil, err
	}
	sshDone := r.Stage == "install" || r.Stage == "readiness" || r.Stage == "handoff" || r.Stage == "done"
	return &OnboardingAccount{account: u, created: r.Created, passwordDone: r.Stage != "password", passwordSkipped: r.PasswordSkipped || r.Stage == "password-started", sshDone: sshDone, recovery: r}, nil
}

func (a *OnboardingAccount) persistStage(stage string) error {
	if a.recovery == nil {
		return nil
	}
	dir, err := setupRecoveryDir()
	if err != nil {
		return err
	}
	next := *a.recovery
	next.Stage = stage
	next.PasswordSkipped = a.passwordSkipped
	if err := saveSetupRecovery(dir, next); err != nil {
		return fmt.Errorf("save setup stage: %w", err)
	}
	a.recovery = &next
	return nil
}

func (a *OnboardingAccount) Stage() string {
	if a.recovery == nil {
		return "password"
	}
	return a.recovery.Stage
}
func (a *OnboardingAccount) PasswordDone() bool           { return a != nil && a.passwordDone }
func (a *OnboardingAccount) MarkStage(stage string) error { return a.persistStage(stage) }

func BeginRecoverableOnboarding(name, artifact string) (*OnboardingAccount, error) {
	dir, err := setupRecoveryDir()
	if err != nil {
		return nil, err
	}
	prior, err := readSetupRecovery(dir)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return nil, errors.New("setup recovery already exists; resume the recorded account")
	}
	// Fail closed on unrelated partial state, even when it happens to share a UID.
	if present, err := firstInstallStatePresent(); err != nil || present {
		return nil, errors.New("existing installation is not a fresh setup target")
	}
	ops := defaultInstallAccountOps()
	_, lookupErr := ops.lookup(name)
	var unknown user.UnknownUserError
	create := errors.As(lookupErr, &unknown)
	if lookupErr != nil && !create {
		return nil, lookupErr
	}
	r := setupRecovery{Version: 1, Stage: "account", Artifact: artifact, Account: SelectedAccount{Username: name}}
	// Write the intent only immediately before useradd, after all nonmutating
	// input/prerequisite checks. Invalid names must not poison future attempts.
	createAccount := ops.create
	ops.create = func(name string) error {
		if err := saveSetupRecovery(dir, r); err != nil {
			return err
		}
		return createAccount(name)
	}
	ops.terminal, ops.password = nil, nil
	u, err := selectInstallationAccount(name, create, ops)
	if err != nil {
		return nil, err
	}
	a := &OnboardingAccount{account: u, created: create}
	r.Account = a.SelectedAccount()
	r.Created = a.Created()
	r.Stage = "password"
	a.recovery = &r
	if err := a.persistStage("password"); err != nil {
		return nil, err
	}
	return a, nil
}
