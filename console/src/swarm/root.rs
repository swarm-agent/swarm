//! Root steps on this computer go through sudo. `sudo -n` first: a cached
//! sudo or a NOPASSWD rule needs nothing from the person. When sudo needs a
//! password, the app's own prompt asks for it (desktops without a polkit
//! agent, such as a bare i3, cannot show pkexec's) and it is written to
//! sudo's stdin only: `sudo -S -k -p '' -- ARGS`.
//!
//! The password is passed through, nothing more: it is never stored, logged,
//! written to disk, put in argv or the environment, or included in an error;
//! its bytes are overwritten as soon as sudo has them. This module holds the
//! pure parts (argv, what sudo's answer means, what to tell the person);
//! `target::Target::run_root` runs the processes.

use std::fmt;

/// A password on its way to sudo. Not Clone; its bytes are wiped on drop.
pub struct Password(Vec<u8>);

impl Password {
    pub fn from_string(typed: String) -> Password {
        Password(typed.into_bytes())
    }

    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Overwrites every byte (volatile, so the compiler cannot skip it).
    fn wipe(&mut self) {
        for b in self.0.iter_mut() {
            // SAFETY: `b` is a valid, aligned, exclusive reference into our own buffer.
            unsafe { std::ptr::write_volatile(b, 0) };
        }
        std::sync::atomic::compiler_fence(std::sync::atomic::Ordering::SeqCst);
    }
}

impl Drop for Password {
    fn drop(&mut self) {
        self.wipe();
    }
}

impl fmt::Debug for Password {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Password(…)")
    }
}

/// Asks the person for their password for one root step and waits for the
/// answer. Called from the background thread that runs the step.
pub trait AskPassword: Send + Sync {
    /// `summary` names what will run, `command` is the full command line,
    /// `retry` says why it asks again. None: the person cancelled.
    fn ask(&self, summary: &str, command: &str, retry: Option<&str>) -> Option<Password>;
}

/// No one to ask: a server over SSH, where sudo must work without a password.
pub struct NoPassword;

impl AskPassword for NoPassword {
    fn ask(&self, _: &str, _: &str, _: Option<&str>) -> Option<Password> {
        None
    }
}

pub const CANCELLED: &str = "Cancelled; nothing changed.";
pub const WRONG_PASSWORD: &str = "That password was not right. Try again.";
pub const NOT_ALLOWED: &str = "Your account may not run commands as root on this computer (sudo refused it), so nothing changed.";

/// sudo's arguments for the first try: never prompts.
pub fn sudo_without_password(args: &[String]) -> Vec<String> {
    let mut v: Vec<String> = ["-n", "--"].map(String::from).to_vec();
    v.extend(args.iter().cloned());
    v
}

/// sudo's arguments when the password comes on stdin: no prompt text, and
/// any cached sudo ignored so the typed password is the one checked.
pub fn sudo_with_password(args: &[String]) -> Vec<String> {
    let mut v: Vec<String> = ["-S", "-k", "-p", "", "--"].map(String::from).to_vec();
    v.extend(args.iter().cloned());
    v
}

/// What one sudo run came to.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Outcome {
    Done,
    /// `sudo -n` stopped before running anything: it wants a password.
    NeedsPassword,
    /// The typed password was refused.
    WrongPassword,
    /// The account may not use sudo at all.
    NotAllowed,
    /// The command ran (or sudo stopped) with this error.
    Failed(String),
}

/// Reads sudo's exit code and stderr. `printed`: the command wrote to stdout,
/// so it ran and any failure is its own. `with_password`: this was the
/// `-S` run.
pub fn outcome(code: Option<i32>, stderr: &str, printed: bool, with_password: bool) -> Outcome {
    if code == Some(0) {
        return Outcome::Done;
    }
    let lines: Vec<&str> = stderr.lines().map(str::trim).filter(|l| !l.is_empty()).collect();
    let any = |needle: &str| lines.iter().any(|l| l.to_ascii_lowercase().contains(needle));
    // sudo itself exits 1 before running anything; its lines start "sudo:".
    let from_sudo = !printed && code == Some(1) && lines.iter().any(|l| l.starts_with("sudo:"));
    if !printed && (any("not in the sudoers") || any("is not allowed to execute")) {
        return Outcome::NotAllowed;
    }
    if from_sudo && !with_password {
        return Outcome::NeedsPassword;
    }
    if with_password && !printed && code == Some(1) && (any("incorrect password") || any("sorry, try again") || any("no password was provided") || any("authentication failure")) {
        return Outcome::WrongPassword;
    }
    let last = lines.last().copied().unwrap_or("it stopped with an error");
    Outcome::Failed(last.trim_start_matches("error: ").to_string())
}

/// What the person reads when a root step did not get done.
pub fn message(outcome: &Outcome) -> String {
    match outcome {
        Outcome::Done => String::new(),
        Outcome::NeedsPassword => "sudo needs your password".into(),
        Outcome::WrongPassword => WRONG_PASSWORD.into(),
        Outcome::NotAllowed => NOT_ALLOWED.into(),
        Outcome::Failed(e) => e.clone(),
    }
}

/// Quotes an argument for a shell only when it needs it.
pub fn shell_quote(s: &str) -> String {
    let plain = !s.is_empty() && s.chars().all(|c| c.is_ascii_alphanumeric() || "_./=:,@+-%".contains(c));
    if plain { s.to_string() } else { format!("'{}'", s.replace('\'', "'\\''")) }
}

/// The full command as it runs: `sudo ARGS`, quoted for a terminal.
pub fn command_line(args: &[String]) -> String {
    let mut out = vec!["sudo".to_string()];
    out.extend(args.iter().map(|a| shell_quote(a)));
    out.join(" ")
}

/// A short name for what runs: the program's file name and its arguments
/// (`swarmctl workspace add /srv/a /srv/b`); the installer is `install.sh …`.
pub fn summary(args: &[String]) -> String {
    let base = |p: &str| p.rsplit('/').next().unwrap_or(p).to_string();
    let rest: &[String] = match args {
        // `bash -c SCRIPT install.sh ARGS…`: the installed installer.
        [bash, c, _, name, rest @ ..] if base(bash) == "bash" && c == "-c" => return std::iter::once(name.clone()).chain(rest.iter().map(|a| shell_quote(a))).collect::<Vec<_>>().join(" "),
        // `bash PATH/install.sh ARGS…`: a downloaded installer.
        [bash, script, rest @ ..] if base(bash) == "bash" => return std::iter::once(base(script)).chain(rest.iter().map(|a| shell_quote(a))).collect::<Vec<_>>().join(" "),
        [_, rest @ ..] => rest,
        [] => return String::new(),
    };
    std::iter::once(base(&args[0])).chain(rest.iter().map(|a| shell_quote(a))).collect::<Vec<_>>().join(" ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(s: &[&str]) -> Vec<String> {
        s.iter().map(|x| x.to_string()).collect()
    }

    #[test]
    fn sudo_argv_keeps_the_password_out_of_it() {
        let args = v(&["/usr/local/bin/swarmctl", "workspace", "add", "/srv/a"]);
        assert_eq!(sudo_without_password(&args), v(&["-n", "--", "/usr/local/bin/swarmctl", "workspace", "add", "/srv/a"]));
        assert_eq!(sudo_with_password(&args), v(&["-S", "-k", "-p", "", "--", "/usr/local/bin/swarmctl", "workspace", "add", "/srv/a"]));
    }

    #[test]
    fn decides_when_to_ask_for_a_password() {
        // A cached or NOPASSWD sudo just runs it.
        assert_eq!(outcome(Some(0), "", true, false), Outcome::Done);
        // sudo -n, any language: sudo's own line, exit 1, nothing ran.
        assert_eq!(outcome(Some(1), "sudo: a password is required\n", false, false), Outcome::NeedsPassword);
        assert_eq!(outcome(Some(1), "sudo: es wird ein Passwort benötigt\n", false, false), Outcome::NeedsPassword);
        // The command itself failed: its error, no password prompt.
        assert_eq!(outcome(Some(1), "error: /srv/a is a system folder\n", false, false), Outcome::Failed("/srv/a is a system folder".into()));
        assert_eq!(outcome(Some(1), "sudo: looks like sudo\n", true, false), Outcome::Failed("sudo: looks like sudo".into()));
        assert_eq!(outcome(Some(2), "", false, false), Outcome::Failed("it stopped with an error".into()));
    }

    #[test]
    fn maps_sudo_answers_to_messages() {
        let wrong = "Sorry, try again.\nsudo: no password was provided\nsudo: 1 incorrect password attempt\n";
        assert_eq!(outcome(Some(1), wrong, false, true), Outcome::WrongPassword);
        assert_eq!(message(&Outcome::WrongPassword), WRONG_PASSWORD);
        let refused = "dev is not in the sudoers file.  This incident will be reported.\n";
        assert_eq!(outcome(Some(1), refused, false, true), Outcome::NotAllowed);
        assert_eq!(message(&outcome(Some(1), refused, false, true)), NOT_ALLOWED);
        // With the password accepted, a failing command reports its own error,
        // and a sudo problem that is not the password is never a retry loop.
        assert_eq!(outcome(Some(1), "error: setfacl is missing (apt-get install acl)\n", false, true), Outcome::Failed("setfacl is missing (apt-get install acl)".into()));
        assert_eq!(outcome(Some(1), "sudo: sorry, you must have a tty to run sudo\n", false, true), Outcome::Failed("sudo: sorry, you must have a tty to run sudo".into()));
        assert_eq!(outcome(Some(0), "", false, true), Outcome::Done);
    }

    #[test]
    fn names_what_will_run() {
        let add = v(&["/usr/local/bin/swarmctl", "workspace", "add", "/srv/a", "/srv/my b"]);
        assert_eq!(summary(&add), "swarmctl workspace add /srv/a '/srv/my b'");
        assert_eq!(command_line(&add), "sudo /usr/local/bin/swarmctl workspace add /srv/a '/srv/my b'");
        let installed = v(&["bash", "-c", "i=/x; exec bash \"$i\" \"$@\"", "install.sh", "update"]);
        assert_eq!(summary(&installed), "install.sh update");
        let staged = v(&["bash", "/srv/cache/install.sh", "--for-user", "dev"]);
        assert_eq!(summary(&staged), "install.sh --for-user dev");
        assert_eq!(shell_quote("it's"), "'it'\\''s'");
        assert_eq!(shell_quote(""), "''");
    }

    #[test]
    fn password_is_wiped_and_never_printed() {
        let mut p = Password::from_string("hunter2".into());
        assert_eq!(p.as_bytes(), b"hunter2");
        assert_eq!(format!("{p:?}"), "Password(…)");
        p.wipe();
        assert!(p.as_bytes().iter().all(|b| *b == 0));
        assert!(!p.is_empty());
        assert!(NoPassword.ask("x", "sudo x", None).is_none());
    }
}
