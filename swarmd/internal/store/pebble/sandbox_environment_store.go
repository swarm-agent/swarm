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
	sandboxEnvironmentHashVersion  = "v1"
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
	UpdatedAt int64  `json:"updated_at"`
	BuiltAt   int64  `json:"built_at,omitempty"`
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
	def, err := NormalizeSandboxEnvironmentDefinition(def)
	if err != nil {
		return SandboxEnvironment{}, false, err
	}
	workspacePath = filepath.Clean(workspacePath)
	current, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, false, err
	}
	if found && current.Account != "" && current.Account != account {
		return SandboxEnvironment{}, false, errors.New("this folder's environment belongs to another account")
	}
	hash := SandboxEnvironmentHash(def)
	if found && current.Hash == hash && (current.Status == SandboxEnvironmentBuilding || current.Status == SandboxEnvironmentReady) {
		return current, false, nil
	}
	env := current
	env.WorkspacePath, env.Account, env.Definition, env.Hash = workspacePath, account, def, hash
	env.Status, env.Error, env.Log = SandboxEnvironmentBuilding, "", ""
	env.UpdatedAt = s.now().UnixMilli()
	if err := s.store.PutJSON(sandboxEnvironmentKey(workspacePath), env); err != nil {
		return SandboxEnvironment{}, false, err
	}
	return env, true, nil
}

// Finish records a build's outcome for the definition hash it built; a
// result for a definition that has since been replaced is ignored.
func (s *SandboxEnvironmentStore) Finish(workspacePath, hash, image, imagePath, log string, buildErr error) (SandboxEnvironment, error) {
	env, found, err := s.Get(workspacePath)
	if err != nil {
		return SandboxEnvironment{}, err
	}
	if !found || env.Hash != hash {
		return env, nil
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
