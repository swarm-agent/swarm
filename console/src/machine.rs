//! Your machine, for the Orchestrator: the machine_run, machine_read and
//! machine_write calls Swarm hands this app, carried out here, as you. Swarm
//! only lets them through when the session's shield allows access; whether
//! each one waits for you (ask) or runs at once (full) is decided by the
//! console before it calls `answer`.

use std::io::Read;
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use serde_json::{Value, json};

/// A command may run this long; Swarm waits five minutes for the whole call.
const RUN_LIMIT: Duration = Duration::from_secs(240);
/// Output kept per stream, and of a file: the answer must stay under Swarm's
/// 64KB client result limit.
const KEEP: usize = 24 * 1024;

/// Carries out one call and returns the result Swarm passes to the agent.
pub fn answer(tool: &str, args: &Value) -> Value {
    let text = |key: &str| args.get(key).and_then(Value::as_str).unwrap_or("").to_string();
    match tool {
        "machine_run" => run(&text("command"), &text("cwd")),
        "machine_read" => read(&text("path")),
        "machine_write" => write(&text("path"), &text("content")),
        other => json!({"error": format!("this app does not run {other}")}),
    }
}

fn home() -> PathBuf {
    std::env::var_os("HOME").map(PathBuf::from).unwrap_or_else(|| PathBuf::from("/"))
}

/// "~/x" and "~" are under your home folder; anything else is taken as is.
fn expand(path: &str) -> PathBuf {
    match path.strip_prefix("~") {
        Some(rest) if rest.is_empty() || rest.starts_with('/') => home().join(rest.trim_start_matches('/')),
        _ => PathBuf::from(path),
    }
}

/// Cuts text to `KEEP` bytes on a character boundary.
fn cut(mut text: String) -> (String, bool) {
    if text.len() <= KEEP {
        return (text, false);
    }
    let mut end = KEEP;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    text.truncate(end);
    (text, true)
}

fn run(command: &str, cwd: &str) -> Value {
    if command.trim().is_empty() {
        return json!({"error": "no command"});
    }
    let dir = if cwd.trim().is_empty() { home() } else { expand(cwd) };
    let shell = std::env::var("SHELL").ok().filter(|s| !s.is_empty()).unwrap_or_else(|| "/bin/sh".into());
    let child = Command::new(&shell)
        .arg("-c")
        .arg(command)
        .current_dir(&dir)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    let mut child = match child {
        Ok(child) => child,
        Err(e) => return json!({"error": format!("could not start {shell} in {}: {e}", dir.display())}),
    };
    // Read both streams while it runs, so a full pipe never stalls it.
    let drain = |stream: Option<Box<dyn Read + Send>>| {
        std::thread::spawn(move || {
            let mut buf = Vec::new();
            if let Some(mut s) = stream {
                let _ = s.read_to_end(&mut buf);
            }
            String::from_utf8_lossy(&buf).into_owned()
        })
    };
    let out = drain(child.stdout.take().map(|s| Box::new(s) as Box<dyn Read + Send>));
    let err = drain(child.stderr.take().map(|s| Box::new(s) as Box<dyn Read + Send>));
    let start = Instant::now();
    let mut timed_out = false;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) if start.elapsed() > RUN_LIMIT => {
                timed_out = true;
                let _ = child.kill();
                break child.wait().ok();
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(50)),
            Err(_) => break None,
        }
    };
    let (stdout, stdout_cut) = cut(out.join().unwrap_or_default());
    let (stderr, stderr_cut) = cut(err.join().unwrap_or_default());
    json!({
        "exit_code": status.and_then(|s| s.code()),
        "stdout": stdout,
        "stderr": stderr,
        "truncated": stdout_cut || stderr_cut,
        "timed_out": timed_out,
        "cwd": dir.display().to_string(),
    })
}

fn read(path: &str) -> Value {
    let path = expand(path);
    match std::fs::read(&path) {
        Ok(bytes) => {
            let (content, truncated) = cut(String::from_utf8_lossy(&bytes).into_owned());
            json!({"path": path.display().to_string(), "content": content, "truncated": truncated, "bytes": bytes.len()})
        }
        Err(e) => json!({"path": path.display().to_string(), "error": e.to_string()}),
    }
}

fn write(path: &str, content: &str) -> Value {
    let path = expand(path);
    match std::fs::write(&path, content) {
        Ok(()) => json!({"path": path.display().to_string(), "written_bytes": content.len()}),
        Err(e) => json!({"path": path.display().to_string(), "error": e.to_string()}),
    }
}

/// One line saying what a call does, for its card and for the notice.
pub fn describe(tool: &str, args: &Value) -> String {
    let text = |key: &str| args.get(key).and_then(Value::as_str).unwrap_or("").to_string();
    match tool {
        "machine_run" => {
            let cwd = text("cwd");
            if cwd.is_empty() { format!("run: {}", text("command")) } else { format!("run in {cwd}: {}", text("command")) }
        }
        "machine_read" => format!("read {}", text("path")),
        "machine_write" => format!("write {} ({} characters)", text("path"), text("content").chars().count()),
        "request_machine_access" => text("reason"),
        "request_workspace_setup" => format!("Ready {} for agents: {}", text("workspace_path"), text("reason")),
        "request_browser_access" => text("reason"),
        "manage_browser" => crate::browser::describe(args),
        other => other.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn runs_reads_and_writes() {
        let dir = std::env::temp_dir().join(format!("swarm-console-machine-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("note.txt");
        let wrote = answer("machine_write", &json!({"path": file.display().to_string(), "content": "hello"}));
        assert_eq!(wrote["written_bytes"], 5);
        let read = answer("machine_read", &json!({"path": file.display().to_string()}));
        assert_eq!(read["content"], "hello");
        let ran = answer("machine_run", &json!({"command": "cat note.txt; echo oops >&2; exit 3", "cwd": dir.display().to_string()}));
        assert_eq!((ran["stdout"].as_str(), ran["stderr"].as_str(), ran["exit_code"].as_i64()), (Some("hello"), Some("oops\n"), Some(3)));
        assert_eq!(answer("machine_read", &json!({"path": dir.join("missing").display().to_string()}))["error"].is_string(), true);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn long_output_is_cut_on_a_character() {
        let (text, cut_it) = cut("é".repeat(KEEP));
        assert!(cut_it && text.len() <= KEEP && text.chars().all(|c| c == 'é'));
        assert_eq!(expand("~/x"), home().join("x"));
        assert_eq!(expand("/etc/hosts"), PathBuf::from("/etc/hosts"));
    }
}
