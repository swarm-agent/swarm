# Container Sandbox Implementation

> **Status**: IN PROGRESS
> **Branch**: main (unstaged changes)
> **Revert**: `git restore packages/opencode/src/` to undo all changes

## Current Progress (2025-12-14)

### ✅ WORKING
- `swarm profile create test-sandbox --image ubuntu:22.04` - creates profile
- `swarm profile list` - shows profiles with status
- `swarm profile start <name>` - starts container with security flags
- `swarm profile stop <name>` - stops container
- `swarm profile status <name>` - shows running state
- `swarm profile exec <name> <cmd>` - executes commands in container
- **Security flags applied**: `no-new-privileges:true`, ports bound to `127.0.0.1` only
- **User-friendly error**: Shows install instructions when podman/docker missing
- **Docker tested**: Working with docker runtime (daemon must be running)
- `swarm run --profile <name>` - ✅ WORKS! Commands execute in container
- **Bash tool routing** - ✅ WORKS! Container IS sandbox, bwrap skipped
- **Volume mounts** - ✅ WORKS! Host files visible at /workspace (read+write)
- **Dual mode**: Host sandbox (bwrap) works without profile, container sandbox works with profile

### ⏳ NOT YET TESTED
- `swarm serve --profile <name>` - server with profile
- Podman runtime (need to install: `sudo pacman -S podman`)
- Server reconnection to existing container

### 📝 KNOWN ISSUES
- Docker requires daemon running (`sudo systemctl start docker`)
- Build can fail on npm pack issue - may need retry

### 🔧 CONFIG FOR TESTING
Current test config in `/home/roy/swarm-cli/opencode.json`:
```json
{
  "container": {
    "runtime": "docker"
  }
}
```

Active test profile: `test-sandbox` (ubuntu:22.04, container ID: 6bfc28e2eb9c)

## Runtime Requirements

### Podman (RECOMMENDED)
Podman is **rootless** and **daemonless** - it just works without sudo or background services.

```bash
# Install podman
Arch:   sudo pacman -S podman
Ubuntu: sudo apt install podman
Fedora: sudo dnf install podman
macOS:  brew install podman

# That's it! No daemon to start, no sudo needed to run containers
podman run hello-world
```

### Docker (Alternative)
Docker requires a **daemon running as root**. More friction but widely used.

```bash
# Install docker
Arch:   sudo pacman -S docker
Ubuntu: sudo apt install docker.io
Fedora: sudo dnf install docker
macOS:  brew install --cask docker

# IMPORTANT: Docker requires the daemon to be running
sudo systemctl start docker    # Start daemon (required every boot)
sudo systemctl enable docker   # Auto-start on boot

# Add yourself to docker group (to avoid sudo for every command)
sudo usermod -aG docker $USER
# Then log out and back in

# Set docker as runtime in opencode.json:
# { "container": { "runtime": "docker" } }
```

## Security Model

Containers are configured with these safety measures:
- **Ports bound to localhost only** (`127.0.0.1:port`) - not accessible from network
- **No privilege escalation** (`--security-opt no-new-privileges:true`)
- **Bridge network by default** - container can reach out, nothing can reach in unless ports exposed
- **Rootless with podman** - container runs as your user, not root

## Goals

1. **Isolated execution environment** - Run all bash commands inside a container (podman/docker)
2. **Profile-based configuration** - Named profiles with different images, volumes, network settings
3. **Persistent containers** - Keep containers running between commands (keep-alive mode)
4. **Server reconnection** - SDK/server can reconnect to existing running containers
5. **Dual runtime support** - Support both podman (preferred) and docker as fallback

## Files Changed

```
packages/opencode/src/
├── cli/cmd/
│   ├── profile.ts      (NEW)  - Profile CLI commands
│   ├── run.ts          (MOD)  - Added --profile flag
│   └── serve.ts        (MOD)  - Added --profile flag
├── config/
│   └── config.ts       (MOD)  - Added ContainerConfig, ContainerProfileConfig schemas
├── container/
│   └── runtime.ts      (NEW)  - Container runtime abstraction (podman/docker)
├── profile/
│   └── index.ts        (NEW)  - Profile management (CRUD, lifecycle)
├── session/
│   └── index.ts        (MOD)  - Added containerProfile field
├── tool/
│   └── bash.ts         (MOD)  - Route commands through container if profile active
├── server/
│   └── server.ts       (MOD)  - Export for profile access
└── index.ts            (MOD)  - Export Profile namespace
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  swarm run --profile dev "do something"                     │
│  swarm serve --profile dev                                  │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  Profile.start("dev")                                       │
│  - Loads profile config from ~/.local/share/opencode/       │
│  - Creates container if not exists                          │
│  - Starts container if not running                          │
│  - Stores containerID in profile state                      │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  Session.create({ containerProfile: "dev" })                │
│  - Session stores which profile to use                      │
│  - All bash commands in session route to container          │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  BashTool.execute()                                         │
│  - Checks session.containerProfile                          │
│  - If set, runs: podman exec <containerID> bash -c "..."    │
│  - Otherwise, runs on host (original behavior)              │
└─────────────────────────────────────────────────────────────┘
```

## Test Checklist

### Prerequisites
- [ ] Podman installed and working (`podman --version`)
- [ ] Docker installed and working (`docker --version`)
- [ ] App rebuilt (`bun run build` in packages/opencode)

### Profile CLI Commands

#### With Podman (default)
- [ ] `swarm profile create test-podman --image ubuntu:22.04`
- [ ] `swarm profile list` - shows profile with stopped status
- [ ] `swarm profile show test-podman` - shows JSON config
- [ ] `swarm profile start test-podman` - starts container
- [ ] `swarm profile status test-podman` - shows running
- [ ] `swarm profile exec test-podman echo "hello"` - outputs "hello"
- [ ] `swarm profile exec test-podman pwd` - outputs "/workspace"
- [ ] `swarm profile logs test-podman` - shows container logs
- [ ] `swarm profile stop test-podman` - stops container
- [ ] `swarm profile restart test-podman` - restarts container
- [ ] `swarm profile delete test-podman` - removes profile and container

#### With Docker (fallback)
- [ ] Create config with `runtime: "docker"` in opencode.json
- [ ] `swarm profile create test-docker --image ubuntu:22.04`
- [ ] `swarm profile start test-docker`
- [ ] `swarm profile exec test-docker echo "hello"`
- [ ] `swarm profile stop test-docker`
- [ ] `swarm profile delete test-docker`

### Profile Persistence
- [ ] Profile saved to `~/.local/share/opencode/profiles/<name>.json`
- [ ] Profile survives app restart
- [ ] Container ID persisted when running
- [ ] Status correctly shows "stopped" after container dies

### Server Integration
- [ ] `swarm serve --profile test` - starts server with profile
- [ ] Container auto-starts if not running
- [ ] Server logs show "Using container profile: test"
- [ ] Creating session via SDK uses the profile
- [ ] Bash commands execute inside container

### Run Command Integration
- [ ] `swarm run --profile test "echo hello"` - works
- [ ] Container auto-starts if profile stopped
- [ ] Commands execute inside container
- [ ] Output streams correctly

### Bash Tool in Container
- [ ] Simple commands work: `ls`, `pwd`, `echo`
- [ ] Environment variables passed correctly
- [ ] Working directory is /workspace (or configured workdir)
- [ ] Exit codes propagate correctly
- [ ] Stdout captured correctly
- [ ] Stderr captured correctly
- [ ] Timeout works
- [ ] Large output truncated correctly

### Volume Mounts
- [ ] Default mount: current dir -> /workspace
- [ ] Custom mount: `--volume /host/path:/container/path`
- [ ] Read-only mount: `--volume /path:/path:ro`
- [ ] Changes in container visible on host
- [ ] Changes on host visible in container

### Network Modes
- [ ] Bridge mode (default): container has network
- [ ] Host mode: `--network host`
- [ ] None mode: `--network none` - no network access

### Keep-Alive Behavior
- [ ] Container stays running after command completes
- [ ] Multiple commands reuse same container
- [ ] Idle timeout works (if implemented)

### Reconnection
- [ ] Start profile, stop swarm, restart swarm
- [ ] Profile reconnects to existing container
- [ ] Commands continue to work
- [ ] No duplicate containers created

### Error Handling
- [ ] Missing runtime: clear error message
- [ ] Invalid image: clear error message
- [ ] Container crash: status updates to "error"
- [ ] Permission denied: proper error

### Sandbox Integration (if enabled inside container)
- [ ] Sandbox config applies inside container
- [ ] Bubblewrap works inside container (if applicable)

## Config Schema

```json
{
  "container": {
    "enabled": true,
    "runtime": "podman",
    "defaultProfile": "dev",
    "profiles": {
      "dev": {
        "name": "dev",
        "image": "ubuntu:22.04",
        "workdir": "/workspace",
        "keepAlive": true,
        "idleTimeoutMinutes": 30,
        "volumes": [
          { "host": ".", "container": "/workspace", "readonly": false }
        ],
        "environment": {
          "TERM": "xterm-256color"
        },
        "network": {
          "mode": "bridge"
        }
      }
    }
  }
}
```

## CLI Reference

```bash
# Profile management
swarm profile list
swarm profile create <name> --image <image> [options]
swarm profile show <name>
swarm profile delete <name>

# Container lifecycle
swarm profile start <name> [--pull]
swarm profile stop <name>
swarm profile restart <name>
swarm profile status [name]
swarm profile logs <name> [-f] [--tail N]
swarm profile exec <name> [command...]

# Using profiles
swarm run --profile <name> "message"
swarm serve --profile <name>
```

## Known Issues / TODOs

- [ ] No auto-install of podman/docker - need setup command
- [ ] Idle timeout not implemented (container runs forever)
- [ ] Socket bridges not tested
- [ ] No container resource limits (CPU, memory)
- [ ] No image auto-pull on first start
- [ ] Session.containerProfile field needs SDK update

## Rollback Instructions

If something breaks badly:

```bash
# Revert all changes to src/
git restore packages/opencode/src/

# Or revert specific files
git restore packages/opencode/src/cli/cmd/run.ts
git restore packages/opencode/src/cli/cmd/serve.ts
git restore packages/opencode/src/config/config.ts
git restore packages/opencode/src/session/index.ts
git restore packages/opencode/src/tool/bash.ts
git restore packages/opencode/src/index.ts
git restore packages/opencode/src/server/server.ts

# Remove new files
rm -rf packages/opencode/src/container/
rm -rf packages/opencode/src/profile/
rm packages/opencode/src/cli/cmd/profile.ts
rm packages/opencode/src/server/profile.ts
```

## Next Steps

1. Install podman if not available
2. Run through test checklist
3. Fix any issues found
4. Add setup/doctor command for missing runtime
5. Commit working version
