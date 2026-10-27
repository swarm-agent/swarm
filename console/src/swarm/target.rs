//! The machine Swarm runs on: this computer, or a server reached over SSH.
//!
//! Root steps (the installer, connecting this app, making folders visible)
//! go through pkexec, the desktop password prompt, on this computer, and through
//! sudo over SSH on a server. The app's client session (its token) is read
//! from the person's private files and checked first.

use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use super::APP_NAME;

/// Swarm's loopback API on the machine.
pub const API_PORT: u16 = 7781;
/// Where an installed Swarm keeps its installer (every install since the
/// release downloads; the docs and Swarm's own answers name this path).
pub const INSTALLED_INSTALLER: &str = "/var/lib/swarm-headless/install.sh";
/// Where installs from before the release downloads keep it.
pub const LEGACY_INSTALLER: &str = "/var/lib/swarm-headless/src/containers/headless/app/install.sh";
/// The public installer.
pub const INSTALLER_URL: &str = "https://swarmagent.dev/install";
/// Swarm's CLI, as the installer puts it on root's PATH.
pub const SWARMCTL: &str = "/usr/local/bin/swarmctl";

/// SWARM_CONSOLE_CHANNEL=BRANCH (development): install that branch of Swarm,
/// built from source with the branch's own installer, instead of the newest
/// release.
pub fn channel() -> Option<String> {
    std::env::var("SWARM_CONSOLE_CHANNEL").ok().filter(|b| !b.is_empty())
}

fn installer_url() -> String {
    match channel() {
        Some(branch) => format!(
            "https://raw.githubusercontent.com/swarm-agent/swarm/{branch}/containers/headless/app/install.sh"
        ),
        None => INSTALLER_URL.to_string(),
    }
}

/// A root command that runs the machine's installed installer with `rest`,
/// wherever this install keeps it.
pub fn installed_installer(rest: &[&str]) -> Vec<String> {
    let script = format!(r#"i={INSTALLED_INSTALLER}; [ -f "$i" ] || i={LEGACY_INSTALLER}; exec bash "$i" "$@""#);
    let mut args: Vec<String> = vec!["bash".into(), "-c".into(), script, "install.sh".into()];
    args.extend(rest.iter().map(|s| s.to_string()));
    args
}

#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    Local,
    Ssh {
        user: String,
        host: String,
        port: u16,
        identity: Option<PathBuf>,
    },
}

fn home() -> PathBuf {
    PathBuf::from(std::env::var("HOME").unwrap_or_else(|_| "/".into()))
}

/// Single-quotes an argument for a remote shell.
fn quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

impl Target {
    /// "user@host[:port]" for a server.
    pub fn parse_server(input: &str, identity: Option<PathBuf>) -> Result<Target, String> {
        let input = input.trim();
        let (user, rest) = input.split_once('@').ok_or("Use the form user@host, for example dev@my-server")?;
        let (host, port) = match rest.rsplit_once(':') {
            Some((h, p)) => (h, p.parse::<u16>().map_err(|_| "The port after : must be a number")?),
            None => (rest, 22),
        };
        let ok = |s: &str| !s.is_empty() && s.chars().all(|c| c.is_ascii_alphanumeric() || "._-".contains(c));
        if !ok(user) || !ok(host) {
            return Err("Use the form user@host, for example dev@my-server".into());
        }
        Ok(Target::Ssh { user: user.into(), host: host.into(), port, identity })
    }

    /// A server picked by environment, for development and dogfooding:
    /// SWARM_CONSOLE_SSH=user@host:port, SWARM_CONSOLE_SSH_KEY.
    pub fn from_env() -> Option<Target> {
        let server = std::env::var("SWARM_CONSOLE_SSH").ok()?;
        let identity = std::env::var("SWARM_CONSOLE_SSH_KEY").ok().map(PathBuf::from);
        Target::parse_server(&server, identity).ok()
    }

    pub fn label(&self) -> String {
        match self {
            Target::Local => "this computer".into(),
            Target::Ssh { host, .. } => host.clone(),
        }
    }

    /// Swarm's loopback API as this PC reaches it: directly here, through the
    /// app's own SSH tunnel for a server.
    pub fn api(&self) -> String {
        match self {
            Target::Local => format!("http://127.0.0.1:{API_PORT}"),
            Target::Ssh { .. } => self.tunnel().unwrap_or_else(|_| "http://127.0.0.1:1".into()),
        }
    }

    /// Opens (once per server) `ssh -N -L 127.0.0.1:PORT:127.0.0.1:7781` and
    /// returns its local address. The tunnel lives as long as this app.
    fn tunnel(&self) -> Result<String, String> {
        use std::collections::HashMap;
        use std::sync::{Mutex, OnceLock};
        static TUNNELS: OnceLock<Mutex<HashMap<String, (u16, std::process::Child)>>> = OnceLock::new();
        let Target::Ssh { user, host, port, .. } = self else { return Err("not a server".into()) };
        let id = format!("{user}@{host}:{port}");
        let mut tunnels = TUNNELS.get_or_init(Default::default).lock().map_err(|_| "tunnel lock")?;
        if let Some((local, child)) = tunnels.get_mut(&id) {
            if matches!(child.try_wait(), Ok(None)) {
                return Ok(format!("http://127.0.0.1:{local}"));
            }
        }
        let local = std::net::TcpListener::bind("127.0.0.1:0").and_then(|l| l.local_addr()).map_err(|e| e.to_string())?.port();
        let child = self
            .ssh()
            .args(["-N", "-o", "ExitOnForwardFailure=yes", "-L", &format!("127.0.0.1:{local}:127.0.0.1:{API_PORT}")])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|e| format!("ssh tunnel: {e}"))?;
        // Give ssh a moment to listen before the first request.
        for _ in 0..30 {
            if std::net::TcpStream::connect(("127.0.0.1", local)).is_ok() {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(100));
        }
        tunnels.insert(id, (local, child));
        Ok(format!("http://127.0.0.1:{local}"))
    }

    /// The machine's own host name: a name to suggest for its Swarm.
    pub fn hostname(&self) -> String {
        let found = match self {
            Target::Local => this_computer(),
            Target::Ssh { host, .. } => self
                .ssh()
                .arg("hostname")
                .output()
                .ok()
                .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
                .filter(|h| !h.is_empty())
                .unwrap_or_else(|| host.clone()),
        };
        found.split('.').next().unwrap_or("").to_string()
    }

    /// The account Swarm is set up for.
    pub fn person(&self) -> String {
        match self {
            Target::Local => std::env::var("USER").unwrap_or_default(),
            Target::Ssh { user, .. } => user.clone(),
        }
    }

    fn ssh(&self) -> Command {
        let Target::Ssh { user, host, port, identity, .. } = self else { unreachable!() };
        let mut c = Command::new("ssh");
        c.args(["-p", &port.to_string(), "-o", "BatchMode=yes", "-o", "ConnectTimeout=8"]);
        if let Some(id) = identity {
            c.arg("-i").arg(id).args(["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR"]);
        }
        c.arg(format!("{user}@{host}"));
        c
    }

    /// Checks that the server answers over SSH and may use sudo without a prompt.
    pub fn check_server(&self) -> Result<(), String> {
        if matches!(self, Target::Local) {
            return Ok(());
        }
        let out = self.ssh().arg("sudo -n true && echo ok").output().map_err(|e| format!("ssh: {e}"))?;
        if String::from_utf8_lossy(&out.stdout).trim() == "ok" {
            Ok(())
        } else if out.status.code() == Some(255) {
            Err("Could not sign in over SSH. Check the address and that your SSH key is accepted.".into())
        } else {
            Err("Signed in, but this account cannot use sudo without a password there.".into())
        }
    }

    /// Runs a root command, passing each stdout line to `on_line`.
    pub fn run_root(&self, args: &[String], mut on_line: impl FnMut(String)) -> Result<(), String> {
        let mut cmd = match self {
            Target::Local => {
                let mut c = Command::new("pkexec");
                c.args(args);
                c
            }
            Target::Ssh { .. } => {
                let mut c = self.ssh();
                c.arg(format!("sudo -n -- {}", args.iter().map(|a| quote(a)).collect::<Vec<_>>().join(" ")));
                c
            }
        };
        let mut child = cmd
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|e| format!("could not start {}: {e}", args.first().map(String::as_str).unwrap_or("")))?;
        let stderr = child.stderr.take();
        let errs = std::thread::spawn(move || {
            let mut s = String::new();
            if let Some(mut e) = stderr {
                let _ = e.read_to_string(&mut s);
            }
            s
        });
        if let Some(out) = child.stdout.take() {
            for line in BufReader::new(out).lines().map_while(Result::ok) {
                on_line(line);
            }
        }
        let status = child.wait().map_err(|e| e.to_string())?;
        let errs = errs.join().unwrap_or_default();
        if status.success() {
            return Ok(());
        }
        if matches!(self, Target::Local) && status.code() == Some(126) {
            return Err("The password prompt was closed, so nothing changed.".into());
        }
        if matches!(self, Target::Local) && status.code() == Some(127) {
            return Err(format!(
                "Your desktop could not show a password prompt for this app, so nothing changed. Open Swarm Console from your desktop's app menu, or run this in a terminal and press Connect again: sudo {}",
                args.join(" ")
            ));
        }
        let last = errs.lines().rev().find(|l| !l.trim().is_empty()).unwrap_or("it stopped with an error");
        Err(last.trim_start_matches("error: ").to_string())
    }

    /// Puts the installer on the machine and returns its path there.
    /// SWARM_CONSOLE_INSTALLER points at a local copy (development); otherwise
    /// the public installer (or the SWARM_CONSOLE_CHANNEL branch's) is downloaded.
    pub fn stage_installer(&self) -> Result<String, String> {
        let local = match std::env::var("SWARM_CONSOLE_INSTALLER") {
            Ok(path) => PathBuf::from(path),
            Err(_) => {
                let dir = home().join(".cache/swarm-console");
                std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
                let path = dir.join("install.sh");
                let body = ureq::get(&installer_url())
                    .call()
                    .map_err(|e| format!("could not download the installer: {e}"))?
                    .into_string()
                    .map_err(|e| e.to_string())?;
                std::fs::write(&path, body).map_err(|e| e.to_string())?;
                path
            }
        };
        let script = std::fs::read(&local).map_err(|e| format!("installer {}: {e}", local.display()))?;
        if !script.starts_with(b"#!") {
            return Err("the installer does not look like a script".into());
        }
        match self {
            Target::Local => Ok(local.display().to_string()),
            Target::Ssh { user, .. } => {
                let remote = format!("/home/{user}/.cache/swarm-install.sh");
                let mut child = self
                    .ssh()
                    .arg(format!("mkdir -p ~/.cache && cat > {}", quote(&remote)))
                    .stdin(Stdio::piped())
                    .stdout(Stdio::null())
                    .spawn()
                    .map_err(|e| e.to_string())?;
                child.stdin.take().ok_or("ssh stdin")?.write_all(&script).map_err(|e| e.to_string())?;
                if !child.wait().map_err(|e| e.to_string())?.success() {
                    return Err("could not copy the installer to the server".into());
                }
                Ok(remote)
            }
        }
    }

    /// Where this PC keeps the app's client session for the target: here,
    /// where `swarmctl client connect` leaves it; for a server, this app's
    /// own private folder.
    pub fn token_path(&self) -> PathBuf {
        match self {
            Target::Local => home().join(format!(".config/swarm/clients/{APP_NAME}.token")),
            Target::Ssh { host, port: 22, .. } => home().join(format!(".config/swarm-console/machines/{host}.token")),
            // Two Swarms on one host (VMs, containers) differ by their SSH port.
            Target::Ssh { host, port, .. } => home().join(format!(".config/swarm-console/machines/{host}_{port}.token")),
        }
    }

    /// The app's session token, if this PC already holds one for the target.
    pub fn stored_token(&self) -> Option<String> {
        read_private_token(&self.token_path()).ok()
    }

    /// Makes this app a client of the machine's Swarm: as root, Swarm mints an
    /// owner session named for this app. Here it lands in the person's config;
    /// from a server it is printed over SSH and kept in this PC's private folder.
    pub fn connect(&self) -> Result<String, String> {
        let person = self.person();
        let device = this_computer();
        match self {
            Target::Local => {
                let args: Vec<String> = [SWARMCTL, "client", "connect", APP_NAME, "--device", &device, "--for-user", &person].map(String::from).to_vec();
                let mut usage = false;
                self.run_root(&args, |line| usage |= line.trim() == "Usage:").map_err(|e| older(usage, e))?;
            }
            Target::Ssh { .. } => {
                let args: Vec<String> = [SWARMCTL, "client", "connect", APP_NAME, "--device", &device, "--print"].map(String::from).to_vec();
                let mut token = String::new();
                let mut usage = false;
                self.run_root(&args, |line| {
                    usage |= line.trim() == "Usage:";
                    if token.is_empty() && line.trim().len() > 20 && !line.contains(' ') {
                        token = line.trim().to_string();
                    }
                }).map_err(|e| older(usage, e))?;
                if token.is_empty() {
                    return Err("Swarm printed no session for this app".into());
                }
                let path = self.token_path();
                let dir = path.parent().ok_or("token folder")?;
                std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
                std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700)).map_err(|e| e.to_string())?;
                let mut f = std::fs::OpenOptions::new()
                    .write(true)
                    .create(true)
                    .truncate(true)
                    .mode(0o600)
                    .open(&path)
                    .map_err(|e| e.to_string())?;
                f.write_all(token.as_bytes()).map_err(|e| e.to_string())?;
            }
        }
        read_private_token(&self.token_path())
    }

    /// The person's home folder on the machine.
    pub fn home_dir(&self) -> String {
        match self {
            Target::Local => home().display().to_string(),
            Target::Ssh { user, .. } => format!("/home/{user}"),
        }
    }

    /// Folders in the person's home to pick a project from: every folder at
    /// the top of the home (dotfolders aside), plus git repositories a few
    /// levels deeper. Sorted, each once.
    pub fn home_folders(&self) -> Vec<String> {
        let mut found = match self {
            Target::Local => {
                let mut found = Vec::new();
                if let Ok(entries) = std::fs::read_dir(home()) {
                    for e in entries.flatten() {
                        let name = e.file_name().to_string_lossy().to_string();
                        if !name.starts_with('.') && name != "snap" && e.file_type().map(|t| t.is_dir()).unwrap_or(false) {
                            found.push(e.path().display().to_string());
                        }
                    }
                }
                walk_repos(&home(), 0, &mut found);
                found
            }
            Target::Ssh { .. } => {
                let out = self
                    .ssh()
                    .arg("find ~ -mindepth 1 -maxdepth 1 -type d -not -name '.*' -not -name snap 2>/dev/null; find ~ -maxdepth 3 -name .git -type d -not -path '*/.*/.git' -not -path '*/node_modules/*' -printf '%h\\n' 2>/dev/null")
                    .output();
                out.map(|o| String::from_utf8_lossy(&o.stdout).lines().map(String::from).collect()).unwrap_or_default()
            }
        };
        found.sort();
        found.dedup();
        found
    }

    /// Makes sure `path` exists as the person's folder: creates it, empty,
    /// when it does not (Swarm gives an empty folder its first commit by
    /// itself). Ok(true) when it was created.
    pub fn ensure_folder(&self, path: &str) -> Result<bool, String> {
        match self {
            Target::Local => {
                let p = Path::new(path);
                if p.is_dir() {
                    return Ok(false);
                }
                if p.exists() {
                    return Err(format!("{path} is a file, not a folder"));
                }
                std::fs::create_dir_all(p).map_err(|e| format!("could not make {path}: {e}"))?;
                Ok(true)
            }
            Target::Ssh { .. } => {
                let script = format!(
                    "p={}; if [ -d \"$p\" ]; then echo exists; elif [ -e \"$p\" ]; then echo file; else mkdir -p \"$p\" && echo made; fi",
                    quote(path)
                );
                let out = self.ssh().arg(script).output().map_err(|e| format!("ssh: {e}"))?;
                match String::from_utf8_lossy(&out.stdout).trim() {
                    "exists" => Ok(false),
                    "made" => Ok(true),
                    "file" => Err(format!("{path} is a file, not a folder")),
                    _ => Err(format!("could not make {path} on the server")),
                }
            }
        }
    }
}

fn walk_repos(dir: &Path, depth: usize, found: &mut Vec<String>) {
    if depth > 3 || found.len() >= 40 {
        return;
    }
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name.starts_with('.') || matches!(name.as_ref(), "node_modules" | "target" | "snap" | "go") {
            continue;
        }
        let path = entry.path();
        if !entry.file_type().map(|t| t.is_dir()).unwrap_or(false) {
            continue;
        }
        if path.join(".git").exists() {
            found.push(path.display().to_string());
        } else {
            walk_repos(&path, depth + 1, found);
        }
    }
}

/// This computer's host name (where the app runs).
pub fn this_computer() -> String {
    std::fs::read_to_string("/proc/sys/kernel/hostname").map(|h| h.trim().to_string()).ok().filter(|h| !h.is_empty()).unwrap_or_else(|| "this computer".into())
}

/// Reads a token file only when it is private to this account (0600, owned by us).
pub fn read_private_token(path: &Path) -> Result<String, String> {
    let meta = std::fs::metadata(path).map_err(|_| "no session file".to_string())?;
    let uid = unsafe { libc::getuid() };
    if meta.uid() != uid || meta.mode() & 0o077 != 0 {
        return Err(format!("{} must be private to you (mode 0600); this app will not use it", path.display()));
    }
    let token = std::fs::read_to_string(path).map_err(|e| e.to_string())?.trim().to_string();
    // A signed session: three base64url parts.
    if token.split('.').count() != 3 || token.contains(char::is_whitespace) {
        return Err("the session file does not hold a Swarm session".into());
    }
    Ok(token)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_servers() {
        let t = Target::parse_server("dev@127.0.0.1:4223", None).unwrap();
        assert_eq!(t.person(), "dev");
        assert!(matches!(Target::parse_server("me@box", None).unwrap(), Target::Ssh { port: 22, .. }));
        for bad in ["box", "me@", "me@box:x", "me;rm@box", "@box"] {
            assert!(Target::parse_server(bad, None).is_err(), "{bad}");
        }
    }

    #[test]
    fn quotes_for_remote_shell() {
        assert_eq!(quote("a b"), "'a b'");
        assert_eq!(quote("it's"), "'it'\\''s'");
    }

    #[test]
    fn refuses_tokens_others_can_read() {
        let dir = std::env::temp_dir().join(format!("swarm-console-token-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("t");
        std::fs::write(&path, "aaa.bbb.ccc\n").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
        assert!(read_private_token(&path).is_err());
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).unwrap();
        assert_eq!(read_private_token(&path).unwrap(), "aaa.bbb.ccc");
        std::fs::write(&path, "nope").unwrap();
        assert!(read_private_token(&path).is_err());
        std::fs::remove_dir_all(&dir).unwrap();
    }
}

/// A swarmctl from before client sessions has no `client` command: it prints
/// its usage (on stdout) and stops. Say so, so onboarding asks for an update.
fn older(usage: bool, error: String) -> String {
    if usage { "usage: this Swarm has no client sessions yet (it is older than this app)".into() } else { error }
}
