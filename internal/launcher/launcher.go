package launcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

type StartupConfig = startupconfig.FileConfig

type Profile struct {
	Root        string
	InstallRoot string
	Lane        string
	Startup     StartupConfig
	Listen      string
	URL         string
	LanePort    int
	DesktopPort int
	StateHome   string
	ConfigHome  string
	DataHome    string
	SwarmState  string
	StateRoot   string
	DataDir     string
	DBPath      string
	LockPath    string
	ManagerFile string
	PIDFile     string
	LogFile     string
	PortsDir    string
	PortRecord  string
	BinDir      string
	ToolBinDir  string
	WebDir      string
	WebDistDir  string
	StartupCWD  string
	Bypass      bool
}

func swarmInstallRoot(dataHome string) string {
	return filepath.Join(dataHome, "swarm")
}

func swarmBinaryRoot(dataHome string) string {
	return filepath.Join(swarmInstallRoot(dataHome), "bin")
}

func swarmToolBinDir(dataHome string) string {
	return filepath.Join(swarmInstallRoot(dataHome), "libexec")
}

func swarmLaneBinDir(dataHome, lane string) string {
	_ = lane
	return swarmBinaryRoot(dataHome)
}

func swarmDesktopDistDir(dataHome string) string {
	return filepath.Join(swarmInstallRoot(dataHome), "share")
}

type ServerStatus struct {
	Status string
	Health string
	PID    string
}

type InstallReport struct {
	BinHome string
	Links   map[string]string
}

type StartBackendOptions struct {
	DesktopPort    int
	ForceRestart   bool
	BuildIfMissing bool
	Bootstrap      startupconfig.BootstrapFlags
}

func ResolveRoot() (string, error) {
	seen := map[string]struct{}{}
	candidates := []string{}
	appendCandidate := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		candidates = append(candidates, abs)
	}

	appendCandidate(os.Getenv("SWARM_ROOT"))
	appendCandidate(os.Getenv("SWARM_GO_ROOT"))
	if exe, err := os.Executable(); err == nil {
		appendCandidate(filepath.Dir(exe))
		appendCandidate(filepath.Join(filepath.Dir(exe), ".."))
		appendCandidate(filepath.Join(filepath.Dir(exe), "../.."))
		appendCandidate(filepath.Join(filepath.Dir(exe), "../../.."))
	}
	if cwd, err := os.Getwd(); err == nil {
		appendCandidate(cwd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		appendCandidate(filepath.Join(home, "swarm-go"))
		appendCandidate(filepath.Join(home, "swarm"))
	}

	for _, candidate := range candidates {
		if root, ok := searchUpForRoot(candidate); ok {
			return root, nil
		}
	}
	return "", errors.New("swarm launcher could not locate project root")
}

func searchUpForRoot(start string) (string, bool) {
	start = filepath.Clean(start)
	for {
		if isRoot(start) {
			return start, true
		}
		next := filepath.Dir(start)
		if next == start {
			return "", false
		}
		start = next
	}
}

func isRoot(dir string) bool {
	checks := []string{
		filepath.Join(dir, "go.mod"),
		filepath.Join(dir, "cmd", "swarmtui", "main.go"),
		filepath.Join(dir, "swarmd", "go.mod"),
	}
	for _, check := range checks {
		if _, err := os.Stat(check); err != nil {
			return false
		}
	}
	return true
}

func DefaultLane(defaultLane string) string {
	lane := strings.TrimSpace(os.Getenv("SWARM_LANE"))
	if lane == "" {
		lane = defaultLane
	}
	lane = strings.ToLower(strings.TrimSpace(lane))
	if lane != "main" && lane != "dev" {
		return "main"
	}
	return lane
}

func LoadProfile(root, lane string, bypassOverride *bool) (Profile, error) {
	if lane != "main" && lane != "dev" {
		return Profile{}, fmt.Errorf("unsupported lane: %s", lane)
	}
	root = strings.TrimSpace(root)
	if root != "" {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return Profile{}, err
		}
		root = filepath.Clean(absRoot)
	}
	cfg, err := LoadStartupConfig()
	if err != nil {
		return Profile{}, err
	}
	port := cfg.Port
	desktopPort := cfg.DesktopPort
	if lane == "dev" {
		if port >= 65535 {
			return Profile{}, fmt.Errorf("invalid startup config %s: dev lane backend port would exceed 65535", cfg.Path)
		}
		if desktopPort >= 65535 {
			return Profile{}, fmt.Errorf("invalid startup config %s: dev lane desktop port would exceed 65535", cfg.Path)
		}
		port++
		desktopPort++
	}
	bypass := cfg.BypassPermissions
	if bypassOverride != nil {
		bypass = *bypassOverride
	}
	stateHome, err := xdgStateHome()
	if err != nil {
		return Profile{}, err
	}
	configHome, err := xdgConfigHome()
	if err != nil {
		return Profile{}, err
	}
	dataHome, err := xdgDataHome()
	if err != nil {
		return Profile{}, err
	}
	startupCWD, _ := os.Getwd()
	startupCWD = filepath.Clean(startupCWD)
	installRoot := swarmInstallRoot(dataHome)
	swarmState := filepath.Join(stateHome, "swarm")
	stateRoot := filepath.Join(swarmState, "swarmd", lane)
	dataDir := filepath.Join(dataHome, "swarmd", lane)
	webDir := ""
	if root != "" {
		webDir = filepath.Join(root, "web")
	}
	return Profile{
		Root:        root,
		InstallRoot: installRoot,
		Lane:        lane,
		Startup:     cfg,
		Listen:      fmt.Sprintf("%s:%d", cfg.Host, port),
		URL:         fmt.Sprintf("http://%s:%d", cfg.Host, port),
		LanePort:    port,
		DesktopPort: desktopPort,
		StateHome:   stateHome,
		ConfigHome:  filepath.Join(configHome, "swarm"),
		DataHome:    dataHome,
		SwarmState:  swarmState,
		StateRoot:   stateRoot,
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "swarmd.pebble"),
		LockPath:    filepath.Join(stateRoot, "swarmd.lock"),
		ManagerFile: filepath.Join(stateRoot, "swarmd.manager.json"),
		PIDFile:     filepath.Join(stateRoot, "swarmd.pid"),
		LogFile:     filepath.Join(stateRoot, "swarmd.log"),
		PortsDir:    filepath.Join(swarmState, "ports"),
		PortRecord:  filepath.Join(swarmState, "ports", fmt.Sprintf("swarmd-%s.env", lane)),
		BinDir:      swarmLaneBinDir(dataHome, lane),
		ToolBinDir:  swarmToolBinDir(dataHome),
		WebDir:      webDir,
		WebDistDir:  swarmDesktopDistDir(dataHome),
		StartupCWD:  startupCWD,
		Bypass:      bypass,
	}, nil
}

func LoadRuntimeProfile(lane string, bypassOverride *bool) (Profile, error) {
	return LoadProfile("", lane, bypassOverride)
}

func LoadBuildProfile(root, lane string, bypassOverride *bool) (Profile, error) {
	return LoadProfile(root, lane, bypassOverride)
}

func LoadStartupConfig() (StartupConfig, error) {
	path, err := startupconfig.ResolvePath()
	if err != nil {
		return StartupConfig{}, err
	}
	return startupconfig.Load(path)
}

func xdgConfigHome() (string, error) {
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return filepath.Clean(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

func xdgStateHome() (string, error) {
	if v := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); v != "" {
		return filepath.Clean(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

func xdgDataHome() (string, error) {
	if v := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); v != "" {
		return filepath.Clean(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

func xdgBinHome() (string, error) {
	if v := strings.TrimSpace(os.Getenv("XDG_BIN_HOME")); v != "" {
		return filepath.Clean(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func (p Profile) EnvMap() map[string]string {
	env := map[string]string{
		"SWARM_LANE":               p.Lane,
		"SWARM_LANE_PORT":          strconv.Itoa(p.LanePort),
		"SWARM_STATE_HOME":         p.SwarmState,
		"SWARM_CONFIG_HOME":        p.ConfigHome,
		"SWARM_STARTUP_CONFIG":     p.Startup.Path,
		"SWARM_STARTUP_MODE":       p.Startup.Mode,
		"SWARM_BYPASS_PERMISSIONS": boolString(p.Bypass),
		"SWARMD_LISTEN":            p.Listen,
		"SWARMD_URL":               p.URL,
		"SWARM_DESKTOP_PORT":       strconv.Itoa(p.DesktopPort),
		"STATE_ROOT":               p.StateRoot,
		"DATA_DIR":                 p.DataDir,
		"DB_PATH":                  p.DBPath,
		"LOCK_PATH":                p.LockPath,
		"PID_FILE":                 p.PIDFile,
		"LOG_FILE":                 p.LogFile,
		"SWARM_BIN_DIR":            p.BinDir,
		"SWARM_TOOL_BIN_DIR":       p.ToolBinDir,
		"SWARM_WEB_DIR":            p.WebDir,
		"SWARM_WEB_DIST_DIR":       p.WebDistDir,
		"SWARM_PORTS_DIR":          p.PortsDir,
		"SWARM_PORT_RECORD":        p.PortRecord,
		"LISTEN":                   p.Listen,
		"ADDR":                     p.URL,
	}
	if strings.TrimSpace(p.Root) != "" {
		if goBin, err := FindGoBin(p.Root); err == nil {
			goBinDir := filepath.Dir(goBin)
			env["GO_BIN"] = goBin
			env["PATH"] = prependPathEntry(os.Getenv("PATH"), goBinDir)
			if isExecutable(filepath.Join(goBinDir, "gofmt")) {
				env["GOFMT_BIN"] = filepath.Join(goBinDir, "gofmt")
			}
			if goRoot := ResolveGoRoot(goBin); goRoot != "" {
				env["GOROOT"] = goRoot
			}
			if strings.TrimSpace(os.Getenv("GOTOOLCHAIN")) == "" {
				env["GOTOOLCHAIN"] = "local"
			}
		}
	}
	return env
}

func prependPathEntry(existing, entry string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return existing
	}
	cleanEntry := filepath.Clean(entry)
	for _, candidate := range filepath.SplitList(existing) {
		if filepath.Clean(strings.TrimSpace(candidate)) == cleanEntry {
			return existing
		}
	}
	if strings.TrimSpace(existing) == "" {
		return cleanEntry
	}
	return cleanEntry + string(os.PathListSeparator) + existing
}

func ResolveGoRoot(goBin string) string {
	goBin = strings.TrimSpace(goBin)
	if goBin == "" {
		return ""
	}
	cmd := exec.Command(goBin, "env", "GOROOT")
	cmd.Env = envWithout(os.Environ(), "GOROOT")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func envWithout(env []string, key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return append([]string(nil), env...)
	}
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (p Profile) EnvList(extra map[string]string) []string {
	merged := map[string]string{}
	for _, raw := range os.Environ() {
		parts := strings.SplitN(raw, "=", 2)
		key := parts[0]
		value := ""
		if len(parts) == 2 {
			value = parts[1]
		}
		merged[key] = value
	}
	for key, value := range p.EnvMap() {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	out := make([]string, 0, len(merged))
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	return out
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func FindGoBin(root string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("GO_BIN")); v != "" {
		if isExecutable(v) {
			return v, nil
		}
	}
	candidates := []string{
		filepath.Join(root, ".tools", "go", "bin", "go"),
		filepath.Join(filepath.Dir(root), ".tools", "go", "bin", "go"),
	}
	for _, candidate := range candidates {
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("go"); err == nil {
		return path, nil
	}
	return "", errors.New("missing Go toolchain")
}

func BuildToolBinaries(root string, skip map[string]bool) error {
	goBin, err := FindGoBin(root)
	if err != nil {
		return err
	}
	dataHome, err := xdgDataHome()
	if err != nil {
		return err
	}
	toolDir := swarmToolBinDir(dataHome)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return err
	}
	commands := []struct {
		Name    string
		Pkg     string
		Args    []string
		WorkDir string
	}{
		{Name: "swarm", Pkg: "./cmd/swarm"},
		{Name: "rebuild", Pkg: "./cmd/rebuild"},
		{Name: "swarmsetup", Pkg: "./cmd/swarmsetup"},
		{Name: "swarmdev", Pkg: "./cmd/swarm", Args: []string{"-X", "main.defaultInvokedName=swarmdev"}},
	}
	for _, command := range commands {
		if skip != nil && skip[command.Name] {
			continue
		}
		outPath := filepath.Join(toolDir, command.Name)
		needsBuild, err := binaryNeedsRebuild(outPath, toolBuildDeps(root, command.Name)...)
		if err != nil {
			return err
		}
		if !needsBuild {
			continue
		}
		workDir := root
		if strings.TrimSpace(command.WorkDir) != "" {
			workDir = filepath.Join(root, command.WorkDir)
		}
		if err := runGoBuildWithArgs(root, workDir, goBin, outPath, command.Pkg, command.Args...); err != nil {
			return err
		}
	}
	return nil
}

func BuildSwarmTUI(profile Profile) error {
	if strings.TrimSpace(profile.Root) == "" {
		return errors.New("building swarmtui requires a source checkout")
	}
	goBin, err := FindGoBin(profile.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile.BinDir, 0o755); err != nil {
		return err
	}
	return runGoBuild(profile.Root, profile.Root, goBin, filepath.Join(profile.BinDir, "swarmtui"), "./cmd/swarmtui")
}

func BuildSwarmdBinaries(profile Profile) error {
	if strings.TrimSpace(profile.Root) == "" {
		return errors.New("building swarmd requires a source checkout")
	}
	goBin, err := FindGoBin(profile.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile.BinDir, 0o755); err != nil {
		return err
	}
	swarmdRoot := filepath.Join(profile.Root, "swarmd")
	if err := runGoBuild(profile.Root, swarmdRoot, goBin, filepath.Join(profile.BinDir, "swarmd"), "./cmd/swarmd"); err != nil {
		return err
	}
	return runGoBuild(profile.Root, swarmdRoot, goBin, filepath.Join(profile.BinDir, "swarmctl"), "./cmd/swarmctl")
}

func runGoBuild(projectRoot, workDir, goBin, outPath, pkg string) error {
	return runGoBuildWithArgs(projectRoot, workDir, goBin, outPath, pkg)
}

func runGoBuildWithArgs(projectRoot, workDir, goBin, outPath, pkg string, extraArgs ...string) error {
	cacheRoot := filepath.Join(projectRoot, ".cache", "go")
	goCache := envOrDefault("GOCACHE_DIR", filepath.Join(cacheRoot, "build"))
	goModCache := envOrDefault("GOMODCACHE_DIR", filepath.Join(cacheRoot, "mod"))
	goPath := envOrDefault("GOPATH_DIR", filepath.Join(cacheRoot, "path"))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(goCache, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(goModCache, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(goPath, 0o755); err != nil {
		return err
	}
	args := []string{"build", "-trimpath"}
	if len(extraArgs) > 0 {
		args = append(args, "-ldflags", strings.Join(extraArgs, " "))
	}
	args = append(args, "-o", outPath, pkg)
	cmd := exec.Command(goBin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"GOCACHE="+goCache,
		"GOMODCACHE="+goModCache,
		"GOPATH="+goPath,
		"GO_BIN="+goBin,
		"GOTOOLCHAIN="+envValueOrDefault("GOTOOLCHAIN", "local"),
		"PATH="+prependPathEntry(os.Getenv("PATH"), filepath.Dir(goBin)),
	)
	if goRoot := ResolveGoRoot(goBin); goRoot != "" {
		cmd.Env = append(cmd.Env, "GOROOT="+goRoot)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("go build %s: %w", pkg, err)
		}
		return fmt.Errorf("go build %s: %w (%s)", pkg, err, trimmed)
	}
	return nil
}

func toolBuildDeps(root, name string) []string {
	deps := []string{
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "go.sum"),
		filepath.Join(root, "internal", "launcher", "launcher.go"),
		filepath.Join(root, "pkg", "startupconfig", "config.go"),
	}
	switch name {
	case "swarm", "swarmdev":
		deps = append(deps, filepath.Join(root, "cmd", "swarm", "main.go"))
	case "rebuild":
		deps = append(deps, filepath.Join(root, "cmd", "rebuild", "main.go"))
	case "swarmsetup":
		deps = append(deps, filepath.Join(root, "cmd", "swarmsetup", "main.go"))
	}
	return deps
}

func binaryNeedsRebuild(outputPath string, deps ...string) (bool, error) {
	info, err := os.Stat(outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	outputTime := info.ModTime()
	for _, dep := range deps {
		if strings.TrimSpace(dep) == "" {
			continue
		}
		depInfo, err := os.Stat(dep)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if depInfo.ModTime().After(outputTime) {
			return true, nil
		}
	}
	return false, nil
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return filepath.Clean(v)
	}
	return fallback
}

func envValueOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func InstallLaunchers(root string) (InstallReport, error) {
	binHome, err := xdgBinHome()
	if err != nil {
		return InstallReport{}, err
	}
	if err := os.MkdirAll(binHome, 0o755); err != nil {
		return InstallReport{}, err
	}
	dataHome, err := xdgDataHome()
	if err != nil {
		return InstallReport{}, err
	}
	toolDir := swarmToolBinDir(dataHome)
	links := map[string]string{
		"swarm":      filepath.Join(toolDir, "swarm"),
		"swarmdev":   filepath.Join(toolDir, "swarmdev"),
		"rebuild":    filepath.Join(toolDir, "rebuild"),
		"swarmsetup": filepath.Join(toolDir, "swarmsetup"),
	}
	for _, target := range links {
		if !isExecutable(target) {
			return InstallReport{}, fmt.Errorf("missing executable launcher: %s", target)
		}
	}
	for name, target := range links {
		linkPath := filepath.Join(binHome, name)
		_ = os.Remove(linkPath)
		if err := os.Symlink(target, linkPath); err != nil {
			return InstallReport{}, fmt.Errorf("link %s -> %s: %w", linkPath, target, err)
		}
	}
	return InstallReport{BinHome: binHome, Links: links}, nil
}

func InstallRuntimeFromArtifact(artifactRoot string) (InstallReport, error) {
	artifactRoot = filepath.Clean(strings.TrimSpace(artifactRoot))
	if artifactRoot == "" {
		return InstallReport{}, errors.New("artifact root must not be empty")
	}
	dataHome, err := xdgDataHome()
	if err != nil {
		return InstallReport{}, err
	}
	platformRoot := filepath.Join(artifactRoot, runtime.GOOS+"-"+runtime.GOARCH)
	rootArtifactDir := filepath.Join(platformRoot, "root")
	swarmdArtifactDir := filepath.Join(platformRoot, "swarmd")
	toolDir := swarmToolBinDir(dataHome)
	binDir := swarmBinaryRoot(dataHome)
	requiredFiles := []struct {
		name   string
		source string
		target string
	}{
		{name: "swarm", source: filepath.Join(rootArtifactDir, "swarm"), target: filepath.Join(toolDir, "swarm")},
		{name: "swarmdev", source: filepath.Join(rootArtifactDir, "swarmdev"), target: filepath.Join(toolDir, "swarmdev")},
		{name: "rebuild", source: filepath.Join(rootArtifactDir, "rebuild"), target: filepath.Join(toolDir, "rebuild")},
		{name: "swarmsetup", source: filepath.Join(rootArtifactDir, "swarmsetup"), target: filepath.Join(toolDir, "swarmsetup")},
		{name: "swarmtui", source: filepath.Join(rootArtifactDir, "swarmtui"), target: filepath.Join(binDir, "swarmtui")},
		{name: "swarmd", source: filepath.Join(swarmdArtifactDir, "swarmd"), target: filepath.Join(binDir, "swarmd")},
		{name: "swarmctl", source: filepath.Join(swarmdArtifactDir, "swarmctl"), target: filepath.Join(binDir, "swarmctl")},
	}
	for _, item := range requiredFiles {
		if !isExecutable(item.source) {
			return InstallReport{}, fmt.Errorf("missing executable artifact for %s: %s", item.name, item.source)
		}
		if err := copyFile(item.source, item.target); err != nil {
			return InstallReport{}, err
		}
	}
	webArtifactDir := filepath.Join(artifactRoot, "web")
	if _, err := os.Stat(filepath.Join(webArtifactDir, "index.html")); err == nil {
		webTargetDir := swarmDesktopDistDir(dataHome)
		if err := os.RemoveAll(webTargetDir); err != nil {
			return InstallReport{}, err
		}
		if err := copyDir(webArtifactDir, webTargetDir); err != nil {
			return InstallReport{}, err
		}
		if err := writeCompressedDesktopAssets(webTargetDir); err != nil {
			return InstallReport{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return InstallReport{}, err
	}
	return InstallLaunchers("")
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func missingInstalledBinaryError(name, path string) error {
	return fmt.Errorf("missing installed %s binary at %s; run rebuild from a source checkout or reinstall Swarm", name, path)
}

func RecordPortFile(profile Profile) error {
	if err := os.MkdirAll(profile.PortsDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("SWARM_LANE=%s\nSWARMD_LISTEN=%s\nSWARMD_URL=%s\nSWARM_PORT=%d\nSTATE_ROOT=%s\nPID_FILE=%s\nLOG_FILE=%s\nUPDATED_AT=%s\n",
		profile.Lane,
		profile.Listen,
		profile.URL,
		profile.LanePort,
		profile.StateRoot,
		profile.PIDFile,
		profile.LogFile,
		time.Now().UTC().Format(time.RFC3339),
	)
	return os.WriteFile(profile.PortRecord, []byte(content), 0o644)
}

func ClearPortFile(profile Profile) {
	_ = os.Remove(profile.PortRecord)
}

func ReadPIDFile(profile Profile) string {
	data, err := os.ReadFile(profile.PIDFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func processRunning(pid string) bool {
	if strings.TrimSpace(pid) == "" {
		return false
	}
	id, err := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || id <= 0 {
		return false
	}
	proc, err := os.FindProcess(id)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func serverHealthJSON(profile Profile) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, status, err := httpRequest(ctx, profile, http.MethodGet, profile.URL+"/healthz", nil, nil)
	if err != nil || status < 200 || status >= 300 {
		return "", false
	}
	return string(body), true
}

func ReadyStatus(profile Profile) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, status, err := httpRequest(ctx, profile, http.MethodGet, profile.URL+"/readyz", nil, nil)
	if err != nil {
		return 0, err
	}
	return status, nil
}

func healthBypassPermissions(body string) (bool, bool) {
	compact := strings.ReplaceAll(strings.ReplaceAll(body, "\n", ""), " ", "")
	switch {
	case strings.Contains(compact, `"bypass_permissions":true`):
		return true, true
	case strings.Contains(compact, `"bypass_permissions":false`):
		return false, true
	default:
		return false, false
	}
}

func StartBackend(profile Profile, opts StartBackendOptions) error {
	if healthBody, ok := serverHealthJSON(profile); ok && !opts.ForceRestart {
		if current, found := healthBypassPermissions(healthBody); found && current == profile.Bypass {
			return RecordPortFile(profile)
		}
		if err := StopBackend(profile); err != nil {
			return err
		}
	} else if pid := ReadPIDFile(profile); pid != "" && processRunning(pid) && (opts.ForceRestart || true) {
		if err := StopBackend(profile); err != nil {
			return err
		}
	}
	backendPath, args, err := backendPathAndArgs(profile, opts)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(profile.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(backendPath, args...)
	cmd.Dir = profile.StartupCWD
	cmd.Env = profile.EnvList(nil)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(profile.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	if err := waitForHealth(profile, 100); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(profile.PIDFile)
		ClearPortFile(profile)
		return err
	}
	if err := recordCurrentLifecycleManager(profile); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(profile.PIDFile)
		ClearPortFile(profile)
		return err
	}
	_ = cmd.Process.Release()
	return RecordPortFile(profile)
}

func RunBackend(profile Profile, opts StartBackendOptions) error {
	if healthBody, ok := serverHealthJSON(profile); ok {
		if current, found := healthBypassPermissions(healthBody); found && current == profile.Bypass {
			return fmt.Errorf("swarmd is already running at %s", profile.URL)
		}
		return fmt.Errorf("swarmd is already running at %s with different runtime flags", profile.URL)
	}
	if pid := ReadPIDFile(profile); pid != "" && processRunning(pid) {
		return fmt.Errorf("swarmd is already running under pid %s", pid)
	}
	backendPath, args, err := backendPathAndArgs(profile, opts)
	if err != nil {
		return err
	}
	_ = os.Remove(profile.PIDFile)
	if err := RecordPortFile(profile); err != nil {
		return err
	}
	if err := recordCurrentLifecycleManager(profile); err != nil {
		ClearPortFile(profile)
		return err
	}
	defer ClearPortFile(profile)
	cmd := exec.Command(backendPath, args...)
	cmd.Dir = profile.StartupCWD
	cmd.Env = profile.EnvList(nil)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func backendPathAndArgs(profile Profile, opts StartBackendOptions) (string, []string, error) {
	if opts.DesktopPort == 0 {
		opts.DesktopPort = profile.DesktopPort
	}
	if opts.Bootstrap.HasAny() && profile.Startup.Exists {
		return "", nil, startupconfig.BootstrapExistingConfigError(profile.Startup.Path)
	}
	if err := os.MkdirAll(profile.StateRoot, 0o755); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(profile.DataDir, 0o755); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(profile.BinDir, 0o755); err != nil {
		return "", nil, err
	}
	backendPath := filepath.Join(profile.BinDir, "swarmd")
	if !isExecutable(backendPath) {
		if !opts.BuildIfMissing {
			return "", nil, missingInstalledBinaryError("swarmd", backendPath)
		}
		if err := BuildSwarmdBinaries(profile); err != nil {
			return "", nil, err
		}
	}
	desktopPort := opts.DesktopPort
	args := []string{
		"--listen", profile.Listen,
		"--desktop-port", strconv.Itoa(desktopPort),
		"--bypass-permissions=" + boolString(profile.Bypass),
		"--data-dir", profile.DataDir,
		"--db-path", profile.DBPath,
		"--lock-path", profile.LockPath,
		"--cwd", profile.StartupCWD,
	}
	if !profile.Startup.Exists {
		if opts.Bootstrap.SwarmNameSet {
			args = append(args, "--swarm-name", strings.TrimSpace(opts.Bootstrap.SwarmName))
		}
		if opts.Bootstrap.ChildSet {
			args = append(args, "--child="+boolString(opts.Bootstrap.Child))
		}
		if opts.Bootstrap.ModeSet {
			args = append(args, "--mode", strings.TrimSpace(opts.Bootstrap.Mode))
		}
		if opts.Bootstrap.AdvertiseHostSet {
			args = append(args, "--advertise-host", strings.TrimSpace(opts.Bootstrap.AdvertiseHost))
		}
		if opts.Bootstrap.AdvertisePortSet {
			args = append(args, "--advertise-port", strconv.Itoa(opts.Bootstrap.AdvertisePort))
		}
		if opts.Bootstrap.TailscaleURLSet {
			args = append(args, "--tailscale-url", strings.TrimSpace(opts.Bootstrap.TailscaleURL))
		}
	}
	return backendPath, args, nil
}

func waitForHealth(profile Profile, attempts int) error {
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, status, err := httpRequest(ctx, profile, http.MethodGet, profile.URL+"/healthz", nil, nil)
		cancel()
		if err == nil && status >= 200 && status < 300 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("swarmd failed to become healthy at %s", profile.URL)
}

func StopBackend(profile Profile) error {
	pid := ReadPIDFile(profile)
	if pid == "" || !processRunning(pid) {
		_ = os.Remove(profile.PIDFile)
		ClearPortFile(profile)
		return nil
	}
	id, err := strconv.Atoi(pid)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(id)
	if err != nil {
		return err
	}
	_ = proc.Signal(syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		if !processRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if processRunning(pid) {
		_ = proc.Signal(syscall.SIGKILL)
	}
	_ = os.Remove(profile.PIDFile)
	ClearPortFile(profile)
	return nil
}

func Status(profile Profile) ServerStatus {
	pid := ReadPIDFile(profile)
	status := ServerStatus{Status: "stopped", Health: "down", PID: pid}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, code, err := httpRequest(ctx, profile, http.MethodGet, profile.URL+"/healthz", nil, nil)
	if err == nil && code >= 200 && code < 300 {
		status.Status = "running"
		status.Health = "healthy"
		return status
	}
	if pid != "" && processRunning(pid) {
		status.Status = "running"
		status.Health = "unhealthy"
	}
	return status
}

const localTransportSocketEnv = "SWARMD_LOCAL_TRANSPORT_SOCKET"

func LocalTransportSocketPath(profile Profile) string {
	return filepath.Join(profile.DataDir, "local-transport", "api.sock")
}

func localTransportRequest(profile Profile, method, rawURL string, headers map[string]string, payload any) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return httpRequest(ctx, profile, method, rawURL, headers, payload)
}

func RunForeground(path string, args []string, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RunCtl(profile Profile, rawArgs []string, auth bool) error {
	if err := StartBackend(profile, StartBackendOptions{BuildIfMissing: false}); err != nil {
		return err
	}
	ctlPath := filepath.Join(profile.BinDir, "swarmctl")
	if !isExecutable(ctlPath) {
		return missingInstalledBinaryError("swarmctl", ctlPath)
	}
	args := append([]string{}, rawArgs...)
	if auth {
		args = append([]string{"auth"}, args...)
	}
	if len(args) == 0 {
		return errors.New("missing swarmctl arguments")
	}
	extraEnv := map[string]string{}
	if os.Getenv("SWARMD_TOKEN") == "" {
		extraEnv[localTransportSocketEnv] = LocalTransportSocketPath(profile)
	}
	hasAddr := false
	for _, arg := range args {
		if arg == "--addr" {
			hasAddr = true
			break
		}
	}
	if args[0] != "sandbox_command" && !hasAddr {
		args = append(args, "--addr", profile.URL)
	}
	return RunForeground(ctlPath, args, profile.EnvList(extraEnv))
}

func RunTUI(profile Profile, args []string) error {
	tuiPath := filepath.Join(profile.BinDir, "swarmtui")
	if !isExecutable(tuiPath) {
		return missingInstalledBinaryError("swarmtui", tuiPath)
	}
	extraEnv := map[string]string{}
	if os.Getenv("SWARMD_TOKEN") == "" {
		extraEnv[localTransportSocketEnv] = LocalTransportSocketPath(profile)
	}
	return RunForeground(tuiPath, args, profile.EnvList(extraEnv))
}

func EnsureWebPrereqs(profile Profile) error {
	if strings.TrimSpace(profile.WebDir) == "" {
		return errors.New("desktop asset build requires a source checkout")
	}
	if _, err := os.Stat(filepath.Join(profile.WebDir, "package.json")); err != nil {
		return fmt.Errorf("missing web app under %s", profile.WebDir)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return errors.New("missing node; required for rebuild f")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return errors.New("missing npm; required for rebuild f")
	}
	if _, err := os.Stat(filepath.Join(profile.WebDir, "node_modules", "vite", "bin", "vite.js")); err != nil {
		return fmt.Errorf("missing web dependencies under %s/node_modules", profile.WebDir)
	}
	return nil
}

func InstallDesktopAssets(profile Profile) error {
	if strings.TrimSpace(profile.WebDir) == "" {
		return errors.New("desktop asset install requires a source checkout")
	}
	sourceDir := filepath.Join(profile.WebDir, "dist")
	if _, err := os.Stat(filepath.Join(sourceDir, "index.html")); err != nil {
		return fmt.Errorf("missing built desktop assets under %s", sourceDir)
	}
	if err := os.RemoveAll(profile.WebDistDir); err != nil {
		return err
	}
	if err := copyDir(sourceDir, profile.WebDistDir); err != nil {
		return err
	}
	return writeCompressedDesktopAssets(profile.WebDistDir)
}

func EnsureWebDistReady(profile Profile) error {
	if _, err := os.Stat(filepath.Join(profile.WebDistDir, "index.html")); err != nil {
		return fmt.Errorf("missing installed desktop assets under %s; run rebuild f or reinstall Swarm with desktop assets", profile.WebDistDir)
	}
	return nil
}

func RunDesktop(profile Profile, port int) error {
	frontendURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := EnsureWebDistReady(profile); err != nil {
		return err
	}
	forceRestart := port != profile.DesktopPort
	if err := StartBackend(profile, StartBackendOptions{BuildIfMissing: false, ForceRestart: forceRestart, DesktopPort: port}); err != nil {
		return err
	}
	_ = RecordPortFile(profile)
	_ = OpenBrowser(frontendURL)
	return nil
}

func OpenBrowser(targetURL string) error {
	commands := [][]string{}
	if isWSL() {
		commands = append(commands,
			[]string{"wslview", targetURL},
			[]string{"rundll32.exe", "url.dll,FileProtocolHandler", targetURL},
			[]string{"cmd.exe", "/c", "start", "", targetURL},
			[]string{"xdg-open", targetURL},
		)
	} else {
		switch runtime.GOOS {
		case "darwin":
			commands = append(commands, []string{"open", targetURL})
		case "windows":
			commands = append(commands,
				[]string{"rundll32", "url.dll,FileProtocolHandler", targetURL},
				[]string{"cmd", "/c", "start", "", targetURL},
			)
		default:
			commands = append(commands,
				[]string{"xdg-open", targetURL},
				[]string{"open", targetURL},
			)
		}
	}
	for _, args := range commands {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			return nil
		}
	}
	return errors.New("failed to open browser")
}

func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	for _, path := range []string{"/proc/sys/kernel/osrelease", "/proc/version"} {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft") {
			return true
		}
	}
	return false
}

func Rebuild(profile Profile, includeWeb, restartSystemd bool) error {
	serviceActive := false
	serviceScope := systemdServiceScope("")
	serviceUnit := ""
	manager, managerKnown, err := resolveLifecycleManager(profile)
	if err != nil {
		return err
	}
	switch manager.Kind {
	case lifecycleKindSystemd:
		if profile.Lane != "main" {
			return errors.New("automatic systemd restart is only supported for the main lane")
		}
		serviceScope = normalizeSystemdScope(manager.Scope)
		serviceUnit = strings.TrimSpace(manager.Unit)
		if serviceScope == "" || serviceUnit == "" {
			return errors.New("systemd lifecycle metadata is incomplete")
		}
		active, installed, err := serviceActiveForScope(serviceScope, serviceUnit)
		if err != nil {
			return err
		}
		if !installed {
			return fmt.Errorf("systemd service %s is not installed", serviceUnit)
		}
		serviceActive = active
		if !serviceActive {
			if err := StopBackend(profile); err != nil {
				return err
			}
		}
	case lifecycleKindDirect:
		fallthrough
	default:
		if restartSystemd && !managerKnown {
			return errors.New("automatic manager restart requires a running daemon or recorded lifecycle metadata")
		}
		if ready, _ := ReadyStatus(profile); ready == http.StatusOK {
			headers := map[string]string{
				"Accept":       "application/json",
				"Content-Type": "application/json",
			}
			payload := map[string]string{"reason": envOrString("SWARM_REBUILD_REASON", "swarmtui-rebuild")}
			body, status, err := localTransportRequest(profile, http.MethodPost, profile.URL+"/v1/system/shutdown", headers, payload)
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return fmt.Errorf("shutdown request failed (%d): %s", status, strings.TrimSpace(string(body)))
			}
			confirmed := false
			for i := 0; i < 30; i++ {
				status, err := ReadyStatus(profile)
				if err != nil || status == http.StatusServiceUnavailable {
					confirmed = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !confirmed {
				return errors.New("shutdown request accepted but daemon stayed ready")
			}
		}
		if err := StopBackend(profile); err != nil {
			return err
		}
	}
	if err := BuildSwarmdBinaries(profile); err != nil {
		return err
	}
	if err := BuildToolBinaries(profile.Root, map[string]bool{
		"rebuild": true,
	}); err != nil {
		return err
	}
	if err := BuildSwarmTUI(profile); err != nil {
		return err
	}
	if includeWeb {
		if err := EnsureWebPrereqs(profile); err != nil {
			return err
		}
		cmd := exec.Command("npm", "run", "build")
		cmd.Dir = profile.WebDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		if err := InstallDesktopAssets(profile); err != nil {
			return err
		}
	}
	if _, err := InstallLaunchers(profile.Root); err != nil {
		return err
	}
	if serviceScope != "" && serviceUnit != "" {
		return restartSystemdService(serviceScope, serviceUnit, serviceActive)
	}
	if err := StartBackend(profile, StartBackendOptions{BuildIfMissing: false}); err != nil {
		return err
	}
	return nil
}

type systemdServiceScope string

const (
	systemdServiceUser   systemdServiceScope = "user"
	systemdServiceSystem systemdServiceScope = "system"
)

func detectSystemdService(unit string) (systemdServiceScope, bool, error) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return "", false, errors.New("systemd unit name must not be empty")
	}
	for _, scope := range []systemdServiceScope{systemdServiceUser, systemdServiceSystem} {
		active, installed, err := serviceActiveForScope(scope, unit)
		if err != nil {
			return "", false, err
		}
		if installed {
			return scope, active, nil
		}
	}
	return "", false, fmt.Errorf("systemd service %s is not installed", unit)
}

func serviceActiveForScope(scope systemdServiceScope, unit string) (bool, bool, error) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return false, false, errors.New("systemd unit name must not be empty")
	}
	args, err := systemctlQueryArgs(scope, "is-active", "--quiet", unit)
	if err != nil {
		return false, false, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 3:
				return false, true, nil
			case 4:
				return false, false, nil
			}
		}
		return false, false, fmt.Errorf("check systemd %s service %s: %w", scope, unit, err)
	}
	return true, true, nil
}

func restartSystemdService(scope systemdServiceScope, unit string, restart bool) error {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return errors.New("systemd unit name must not be empty")
	}
	if scope == "" {
		return errors.New("systemd service scope must not be empty")
	}
	commands := [][]string{{"daemon-reload"}}
	if restart {
		commands = append(commands, []string{"restart", unit})
	} else {
		commands = append(commands, []string{"start", unit})
	}
	for _, commandArgs := range commands {
		args, err := systemctlManageArgs(scope, commandArgs...)
		if err != nil {
			return err
		}
		cmd := exec.Command(args[0], args[1:]...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			trimmed := strings.TrimSpace(string(output))
			if trimmed == "" {
				return fmt.Errorf("%s: %w", strings.Join(args[1:], " "), err)
			}
			return fmt.Errorf("%s: %w (%s)", strings.Join(args[1:], " "), err, trimmed)
		}
	}
	return nil
}

func systemctlQueryArgs(scope systemdServiceScope, args ...string) ([]string, error) {
	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, errors.New("systemctl not found; cannot inspect systemd service")
	}
	switch scope {
	case systemdServiceUser:
		return append([]string{systemctlPath, "--user"}, args...), nil
	case systemdServiceSystem:
		return append([]string{systemctlPath}, args...), nil
	default:
		return nil, fmt.Errorf("unknown systemd service scope %q", scope)
	}
}

func systemctlManageArgs(scope systemdServiceScope, args ...string) ([]string, error) {
	switch scope {
	case systemdServiceUser:
		return systemctlQueryArgs(scope, args...)
	case systemdServiceSystem:
		systemctlPath, err := exec.LookPath("systemctl")
		if err != nil {
			return nil, errors.New("systemctl not found; cannot manage systemd service")
		}
		if os.Geteuid() == 0 {
			return append([]string{systemctlPath}, args...), nil
		}
		sudoPath, err := exec.LookPath("sudo")
		if err != nil {
			return nil, errors.New("sudo not found; cannot manage systemd system service")
		}
		return append([]string{sudoPath, "-n", systemctlPath}, args...), nil
	default:
		return nil, fmt.Errorf("unknown systemd service scope %q", scope)
	}
}

func envOrString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func copyDir(sourceDir, targetDir string) error {
	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		targetPath := targetDir
		if relPath != "." {
			targetPath = filepath.Join(targetDir, relPath)
		}
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(targetFile, sourceFile); err != nil {
			_ = targetFile.Close()
			return err
		}
		return targetFile.Close()
	})
}

func copyFile(sourcePath, targetPath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("copy file %s: source is a directory", sourcePath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		_ = targetFile.Close()
		return err
	}
	return targetFile.Close()
}

func writeCompressedDesktopAssets(distDir string) error {
	assetsDir := filepath.Join(distDir, "assets")
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !shouldCompressDesktopAsset(name) {
			continue
		}
		fullPath := filepath.Join(assetsDir, name)
		if err := writeCompressedDesktopAsset(fullPath); err != nil {
			return err
		}
	}
	return nil
}

func shouldCompressDesktopAsset(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".css", ".html", ".json", ".map", ".svg", ".txt":
		return true
	default:
		return false
	}
}

func writeCompressedDesktopAsset(path string) error {
	sourceInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	compressedPath := path + ".gz"
	if compressedInfo, err := os.Stat(compressedPath); err == nil {
		if !compressedInfo.ModTime().Before(sourceInfo.ModTime()) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	sourceFile, err := os.Open(path)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	tempPath := compressedPath + ".tmp"
	compressedFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, sourceInfo.Mode().Perm())
	if err != nil {
		return err
	}

	gz, err := gzip.NewWriterLevel(compressedFile, gzip.BestSpeed)
	if err != nil {
		_ = compressedFile.Close()
		return err
	}
	gz.Name = filepath.Base(path)
	gz.ModTime = sourceInfo.ModTime()

	copyErr := error(nil)
	if _, err := io.Copy(gz, sourceFile); err != nil {
		copyErr = err
	}
	if err := gz.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if err := compressedFile.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if err := os.Chtimes(tempPath, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, compressedPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func httpRequest(ctx context.Context, profile Profile, rawMethod, rawURL string, headers map[string]string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	method := strings.TrimSpace(rawMethod)
	if method == "" {
		method = http.MethodGet
	}
	requestURL := strings.TrimSpace(rawURL)
	if requestURL == "" {
		return nil, 0, errors.New("request url is required")
	}
	client := http.DefaultClient
	if socketPath := strings.TrimSpace(LocalTransportSocketPath(profile)); socketPath != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}
		client = &http.Client{Transport: transport}
		if parsed, err := url.Parse(requestURL); err == nil && parsed != nil {
			parsed.Scheme = "http"
			parsed.Host = "swarm-local-transport"
			requestURL = parsed.String()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if client == http.DefaultClient {
		if token := strings.TrimSpace(os.Getenv("SWARMD_TOKEN")); token != "" {
			req.Header.Set("X-Swarm-Token", token)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}
