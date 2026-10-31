package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// A sandbox environment is what one registered folder's sandbox starts from:
// a base image plus setup commands and environment variables. Swarm builds it
// once into a local image named after the definition's hash (so the same
// definition is built once and can be rebuilt anywhere), and the folder's
// sandbox then runs on it with the usual hardening. The folder keeps its last
// ready image while a new definition builds or after one fails.
const (
	KeySandboxEnvironmentPrefix = "sandbox_environment/"

	SandboxEnvironmentBuilding = "building"
	SandboxEnvironmentReady    = "ready"
	SandboxEnvironmentFailed   = "failed"

	MaxSandboxEnvironmentSetup     = 40
	MaxSandboxEnvironmentSetupLen  = 4000
	MaxSandboxEnvironmentEnv       = 40
	MaxSandboxEnvironmentEnvValue  = 4096
	MaxSandboxEnvironmentLogBytes  = 8000
	SandboxEnvironmentImagePrefix  = "swarm-env:"
	sandboxEnvironmentHashVersion  = "v3"
	sandboxEnvironmentMaxImageName = 255
)

var (
	sandboxEnvironmentImagePattern = regexp.MustCompile(`^[a-z0-9][A-Za-z0-9._/:@-]*$`)
	sandboxEnvironmentEnvPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

	// Variables the sandbox itself sets or that would let a definition steer
	// the secret gateway or the shell; secrets go through the secret system.
	sandboxEnvironmentReservedEnv = map[string]bool{
		"HOME": true, "PATH": true, "LANG": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "NODE_EXTRA_CA_CERTS": true,
		"REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true, "GIT_SSL_CAINFO": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
		"BASH_ENV": true, "ENV": true,
	}

	ErrSandboxEnvironmentNotFound = errors.New("no environment is defined for this folder")
)

// SandboxEnvironmentDefinition is the portable part: what to build.
type SandboxEnvironmentDefinition struct {
	BaseImage string            `json:"base_image"`
	Setup     []string          `json:"setup,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// SandboxEnvironment is one folder's environment and its build state.
type SandboxEnvironment struct {
	WorkspacePath string                       `json:"workspace_path"`
	Account       string                       `json:"account"`
	Definition    SandboxEnvironmentDefinition `json:"definition"`
	Hash          string                       `json:"hash"`
	Status        string                       `json:"status"`
	Error         string                       `json:"error,omitempty"`
	Log           string                       `json:"log,omitempty"`
	// ReadyImage is the last image this folder's sandbox may run on, and
	// ReadyHash the definition it was built from; empty until a build succeeds.
	ReadyImage string `json:"ready_image,omitempty"`
	ReadyHash  string `json:"ready_hash,omitempty"`
	// ReadyPath is the PATH the ready image declares, so its toolchains are found.
	ReadyPath string `json:"ready_path,omitempty"`
	// MaxLeases is how many leases of this environment agents may hold at
	// once (sandbox lease.go); 0 means none. It is not part of the build.
	MaxLeases int   `json:"max_leases,omitempty"`
	UpdatedAt int64 `json:"updated_at"`
	BuiltAt   int64 `json:"built_at,omitempty"`
}

// NormalizeSandboxEnvironmentDefinition trims and checks a definition.
func NormalizeSandboxEnvironmentDefinition(def SandboxEnvironmentDefinition) (SandboxEnvironmentDefinition, error) {
	def.BaseImage = strings.TrimSpace(def.BaseImage)
	if def.BaseImage == "" {
		return def, errors.New("base_image is required (for example ubuntu:24.04, node:22 or an image you already use)")
	}
	if len(def.BaseImage) > sandboxEnvironmentMaxImageName || !sandboxEnvironmentImagePattern.MatchString(def.BaseImage) {
		return def, fmt.Errorf("base_image %q is not a valid image reference", def.BaseImage)
	}
	if strings.HasPrefix(def.BaseImage, SandboxEnvironmentImagePrefix) {
		return def, errors.New("base_image cannot be a Swarm-built environment image; give its base image and setup instead")
	}
	setup := make([]string, 0, len(def.Setup))
	for _, command := range def.Setup {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		if len(command) > MaxSandboxEnvironmentSetupLen {
			return def, fmt.Errorf("a setup command is longer than %d characters", MaxSandboxEnvironmentSetupLen)
		}
		setup = append(setup, command)
	}
	if len(setup) > MaxSandboxEnvironmentSetup {
		return def, fmt.Errorf("at most %d setup commands", MaxSandboxEnvironmentSetup)
	}
	def.Setup = setup
	if len(def.Env) > MaxSandboxEnvironmentEnv {
		return def, fmt.Errorf("at most %d environment variables", MaxSandboxEnvironmentEnv)
	}
	env := map[string]string{}
	for name, value := range def.Env {
		name = strings.TrimSpace(name)
		if !sandboxEnvironmentEnvPattern.MatchString(name) {
			return def, fmt.Errorf("environment variable name %q is invalid", name)
		}
		if sandboxEnvironmentReservedEnv[name] {
			return def, fmt.Errorf("environment variable %s is set by Swarm and cannot be defined", name)
		}
		if len(value) > MaxSandboxEnvironmentEnvValue || strings.ContainsAny(value, "\x00\n") {
			return def, fmt.Errorf("environment variable %s: value too long or not a single line", name)
		}
		env[name] = value
	}
	if len(env) == 0 {
		env = nil
	}
	def.Env = env
	return def, nil
}

// SandboxEnvironmentHash identifies a definition: the same definition always
// builds the same image name, on any machine.
func SandboxEnvironmentHash(def SandboxEnvironmentDefinition) string {
	names := make([]string, 0, len(def.Env))
	for name := range def.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	type kv struct{ K, V string }
	env := make([]kv, 0, len(names))
	for _, name := range names {
		env = append(env, kv{name, def.Env[name]})
	}
	encoded, _ := json.Marshal(struct {
		V     string
		Base  string
		Setup []string
		Env   []kv
	}{sandboxEnvironmentHashVersion, def.BaseImage, def.Setup, env})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// SandboxEnvironmentImage is the local image name for a definition hash.
func SandboxEnvironmentImage(hash string) string {
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return SandboxEnvironmentImagePrefix + hash
}

type SandboxEnvironmentStore struct {
	store *Store
	now   func() time.Time
}

// sandboxEnvironmentWriteMu serializes every read-modify-write of an
// environment record. The definition write (Define, SetMaxLeases, Rebuild,
// Clear) and the build's status write (Finish) run on different goroutines;
// without it a writer could store a record it read before another write
// applied and drop that write (a finishing build reverting max_leases or a
// new definition). It is package-wide so stores opened on the same database
// share it; environment writes are rare explicit operations.
var sandboxEnvironmentWriteMu sync.Mutex

// sandboxEnvironmentFinishReadHook, when set by a test, runs in Finish after
// it read the record and before it writes the outcome.
var sandboxEnvironmentFinishReadHook func()

func NewSandboxEnvironmentStore(store *Store) *SandboxEnvironmentStore {
	return &SandboxEnvironmentStore{store: store, now: time.Now}
}

func sandboxEnvironmentKey(workspacePath string) string {
	return KeySandboxEnvironmentPrefix + keyPart(filepath.Clean(workspacePath))
}

// Get reads a folder's environment. A sandbox has one folder root, which
// belongs to one account; callers check the account themselves.
func (s *SandboxEnvironmentStore) Get(workspacePath string) (SandboxEnvironment, bool, error) {
	if s == nil || s.store == nil {
		return SandboxEnvironment{}, false, errors.New("sandbox environment store unavailable")
	}
	var env SandboxEnvironment
	ok, err := s.store.GetJSON(sandboxEnvironmentKey(workspacePath), &env)
	return env, ok, err
}

// Define records a new definition for a folder. It returns the record and
// whether a build must start (false when this definition is already ready).
func (s *SandboxEnvironmentStore) Define(account, workspacePath string, def SandboxEnvironmentDefinition) (SandboxEnvironment, bool, error) {
	return s.DefineWithMaxLeases(account, workspacePath, def, nil)
}

// DefineWithMaxLeases is Define that also sets the lease maximum (when
// maxLeases is given) in the same write, so a build started for this
// definition can never finish onto a record that lacks it.
func (s *SandboxEnvironmentStore) DefineWithMaxLeases(account, workspacePath string, def SandboxEnvironmentDefinition, maxLeases *int) (SandboxEnvironment, bool, error) {
	def, err := NormalizeSandboxEnvironmentDefinition(def)
	if err != nil {
		return SandboxEnvironment{}, false, err
	}
	if maxLeases != nil {
		if err := checkMaxLeases(*maxLeases); err != nil {
			return SandboxEnvironment{}, false, err
		}
	}
	workspacePath = filepath.Clean(workspacePath)
	sandboxEnvironmentWriteMu.Lock()
	defer sandboxEnvironmentWriteMu.Unlock()
	current, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, false, err
	}
	if found && current.Account != "" && current.Account != account {
		return SandboxEnvironment{}, false, errors.New("this folder's environment belongs to another account")
	}
	hash := SandboxEnvironmentHash(def)
	if found && current.Hash == hash && (current.Status == SandboxEnvironmentBuilding || current.Status == SandboxEnvironmentReady) {
		current.Definition = def
		if maxLeases != nil {
			current.MaxLeases = *maxLeases
		}
		current.UpdatedAt = s.now().UnixMilli()
		if err := s.store.PutJSON(sandboxEnvironmentKey(workspacePath), current); err != nil {
			return SandboxEnvironment{}, false, err
		}
		return current, false, nil
	}
	env := current
	env.WorkspacePath, env.Account, env.Definition, env.Hash = workspacePath, account, def, hash
	env.Status, env.Error, env.Log = SandboxEnvironmentBuilding, "", ""
	if maxLeases != nil {
		env.MaxLeases = *maxLeases
	}
	env.UpdatedAt = s.now().UnixMilli()
	if err := s.store.PutJSON(sandboxEnvironmentKey(workspacePath), env); err != nil {
		return SandboxEnvironment{}, false, err
	}
	return env, true, nil
}

// Finish records a build's outcome for the definition hash it built; a
// result for a definition that has since been replaced is ignored. It only
// sets the build-state fields on the current record (read under the write
// lock), so it never replaces the definition or max_leases.
func (s *SandboxEnvironmentStore) Finish(workspacePath, hash, image, imagePath, log string, buildErr error) (SandboxEnvironment, error) {
	sandboxEnvironmentWriteMu.Lock()
	defer sandboxEnvironmentWriteMu.Unlock()
	env, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, err
	}
	if !found || env.Hash != hash {
		return env, nil
	}
	if hook := sandboxEnvironmentFinishReadHook; hook != nil {
		hook()
	}
	if len(log) > MaxSandboxEnvironmentLogBytes {
		log = log[len(log)-MaxSandboxEnvironmentLogBytes:]
	}
	now := s.now().UnixMilli()
	env.Log, env.UpdatedAt = log, now
	if buildErr != nil {
		env.Status, env.Error = SandboxEnvironmentFailed, buildErr.Error()
		if len(env.Error) > 2000 {
			env.Error = env.Error[:2000]
		}
	} else {
		env.Status, env.Error = SandboxEnvironmentReady, ""
		env.ReadyImage, env.ReadyHash, env.ReadyPath, env.BuiltAt = image, hash, imagePath, now
	}
	if err := s.store.PutJSON(sandboxEnvironmentKey(workspacePath), env); err != nil {
		return SandboxEnvironment{}, err
	}
	return env, nil
}

// Clear removes a folder's environment: its sandbox goes back to Swarm's
// default image.
func (s *SandboxEnvironmentStore) Clear(account, workspacePath string) error {
	sandboxEnvironmentWriteMu.Lock()
	defer sandboxEnvironmentWriteMu.Unlock()
	env, found, err := s.Get(workspacePath)
	if err != nil || !found {
		return err
	}
	if env.Account != "" && env.Account != account {
		return errors.New("this folder's environment belongs to another account")
	}
	return s.store.Delete(sandboxEnvironmentKey(workspacePath))
}

// InterruptBuilds marks builds left running by a previous daemon process as
// failed, so a restart never leaves a folder "building" forever.
func (s *SandboxEnvironmentStore) InterruptBuilds() error {
	if s == nil || s.store == nil {
		return nil
	}
	stuck := []SandboxEnvironment{}
	err := s.store.IteratePrefix(KeySandboxEnvironmentPrefix, 10_000, func(_ string, value []byte) error {
		var env SandboxEnvironment
		if err := json.Unmarshal(value, &env); err != nil {
			return err
		}
		if env.Status == SandboxEnvironmentBuilding {
			stuck = append(stuck, env)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, env := range stuck {
		if _, err := s.Finish(env.WorkspacePath, env.Hash, "", "", env.Log, errors.New("the build was interrupted by a Swarm restart; set the environment again to rebuild")); err != nil {
			return err
		}
	}
	return nil
}

// CountBuilding counts the environment builds in progress; a restart of the
// daemon interrupts them.
func (s *SandboxEnvironmentStore) CountBuilding() (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	count := 0
	err := s.store.IteratePrefix(KeySandboxEnvironmentPrefix, 10_000, func(_ string, value []byte) error {
		var env SandboxEnvironment
		if err := json.Unmarshal(value, &env); err != nil {
			return err
		}
		if env.Status == SandboxEnvironmentBuilding {
			count++
		}
		return nil
	})
	return count, err
}

// ErrSandboxEnvironmentBuilding refuses a rebuild while a build runs.
var ErrSandboxEnvironmentBuilding = errors.New("this folder's environment is already building; wait for it, then rebuild if needed")

// List returns the account's folder environments, ordered by folder path.
func (s *SandboxEnvironmentStore) List(account string) ([]SandboxEnvironment, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("sandbox environment store unavailable")
	}
	out := []SandboxEnvironment{}
	err := s.store.IteratePrefix(KeySandboxEnvironmentPrefix, 10_000, func(_ string, value []byte) error {
		var env SandboxEnvironment
		if err := json.Unmarshal(value, &env); err != nil {
			return err
		}
		if env.Account == account {
			out = append(out, env)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkspacePath < out[j].WorkspacePath })
	return out, nil
}

// Rebuild marks a folder's current definition for a new build (after a
// failed or interrupted build, or to check its image again). The build reuses
// an existing complete image for the same definition. It refuses while a
// build is running.
func (s *SandboxEnvironmentStore) Rebuild(account, workspacePath string) (SandboxEnvironment, error) {
	sandboxEnvironmentWriteMu.Lock()
	defer sandboxEnvironmentWriteMu.Unlock()
	env, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, err
	}
	if !found || (env.Account != "" && env.Account != account) {
		return SandboxEnvironment{}, ErrSandboxEnvironmentNotFound
	}
	if env.Status == SandboxEnvironmentBuilding {
		return SandboxEnvironment{}, ErrSandboxEnvironmentBuilding
	}
	env.Status, env.Error, env.Log = SandboxEnvironmentBuilding, "", ""
	env.UpdatedAt = s.now().UnixMilli()
	if err := s.store.PutJSON(sandboxEnvironmentKey(env.WorkspacePath), env); err != nil {
		return SandboxEnvironment{}, err
	}
	return env, nil
}

// MaxLeasesLimit bounds a folder's lease maximum (a typo guard, not a policy).
const MaxLeasesLimit = 64

// SetMaxLeases sets how many leases of the folder's environment agents may
// hold at once (0 turns leasing off). It never changes the build.
func (s *SandboxEnvironmentStore) SetMaxLeases(account, workspacePath string, max int) (SandboxEnvironment, error) {
	if err := checkMaxLeases(max); err != nil {
		return SandboxEnvironment{}, err
	}
	sandboxEnvironmentWriteMu.Lock()
	defer sandboxEnvironmentWriteMu.Unlock()
	env, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, err
	}
	if !found {
		return SandboxEnvironment{}, ErrSandboxEnvironmentNotFound
	}
	if env.Account != "" && env.Account != account {
		return SandboxEnvironment{}, errors.New("this folder's environment belongs to another account")
	}
	env.MaxLeases = max
	env.UpdatedAt = s.now().UnixMilli()
	if err := s.store.PutJSON(sandboxEnvironmentKey(env.WorkspacePath), env); err != nil {
		return SandboxEnvironment{}, err
	}
	return env, nil
}

func checkMaxLeases(max int) error {
	if max < 0 || max > MaxLeasesLimit {
		return fmt.Errorf("max_leases must be between 0 and %d", MaxLeasesLimit)
	}
	return nil
}
