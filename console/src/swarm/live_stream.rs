//! The fleet's live activity (console mode): one SSE feed on the home,
//! `GET /v3/fleet/activity/stream?cursors=home:12,box:40`, which merges the
//! home's own activity with each linked machine's (the server side is
//! swarmd/internal/api/fleet_activity.go; the frames are described in
//! docs/activity-stream.md, "The fleet's activity").
//!
//! Frames carry metadata only (kind, ids, worker name, state, tool name and
//! file path). Every line here is built from those fields and nothing else.
//!
//! Parsing, mapping, cursors, filters and the bounded buffer are pure and
//! tested below. [`Connection`] holds the one HTTP connection on its own
//! thread and, after a drop, reconnects with the per-machine cursors after a
//! short delay (a stream, never polling).

use std::collections::{BTreeMap, BTreeSet, HashMap, VecDeque};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{Shutdown, TcpStream, ToSocketAddrs};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender, TryRecvError};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::Value;

use super::tool_summary;

/// The most lines the page keeps; older ones fall off the top.
pub const BUFFER: usize = 2000;

pub const STREAM_PATH: &str = "/v3/fleet/activity/stream";

// SSE

/// One thing an SSE stream said: a frame (its `event:` name and joined
/// `data:` lines) or a comment (Swarm's `: connected` and `: heartbeat`).
#[derive(Clone, Debug, PartialEq)]
pub enum Sse {
    Frame { event: String, data: String },
    Comment(String),
}

/// Reads SSE line by line; a blank line ends a frame.
#[derive(Default)]
pub struct SseParser {
    event: String,
    data: String,
    has_data: bool,
}

impl SseParser {
    pub fn line(&mut self, line: &str) -> Option<Sse> {
        let line = line.strip_suffix('\n').unwrap_or(line);
        let line = line.strip_suffix('\r').unwrap_or(line);
        if line.is_empty() {
            if self.event.is_empty() && !self.has_data {
                return None;
            }
            let event = std::mem::take(&mut self.event);
            let data = std::mem::take(&mut self.data);
            self.has_data = false;
            return Some(Sse::Frame { event: if event.is_empty() { "message".into() } else { event }, data });
        }
        if let Some(text) = line.strip_prefix(':') {
            return Some(Sse::Comment(text.trim().to_string()));
        }
        let (field, value) = match line.split_once(':') {
            Some((field, value)) => (field, value.strip_prefix(' ').unwrap_or(value)),
            None => (line, ""),
        };
        match field {
            "event" => self.event = value.trim().to_string(),
            "data" => {
                if self.has_data {
                    self.data.push('\n');
                }
                self.data.push_str(value);
                self.has_data = true;
            }
            _ => {}
        }
        None
    }
}

// Frames

/// An `activity` frame: one machine's record, as the home re-encoded it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Activity {
    pub machine: String,
    pub seq: u64,
    /// Unix ms.
    pub at: u64,
    pub kind: String,
    pub session_id: String,
    pub job_id: String,
    pub worker_id: String,
    pub worker_name: String,
    pub project_id: String,
    pub state: String,
    pub tool_name: String,
    pub file_path: String,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Frame {
    Activity(Activity),
    /// Where a machine's feed stands; reconnect with it.
    Cursor { machine: String, cursor: String },
    /// Records the machine could not send; its feed resumes from `cursor`.
    Gap { machine: String, from: u64, to: u64, cursor: String },
    /// `online`, `offline` (with `error`) or `not_shown` (with `reason`).
    Machine { machine: String, state: String, error: String, reason: String },
}

fn field(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

fn number(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn decimal(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit())
}

/// The frame an SSE `event:` and `data:` carry; None for anything else (or
/// a frame missing what makes it one).
pub fn frame(event: &str, data: &str) -> Option<Frame> {
    let v: Value = serde_json::from_str(data).ok()?;
    let machine = field(&v, "machine");
    if machine.is_empty() {
        return None;
    }
    match event {
        "activity" => {
            let a = Activity {
                machine,
                seq: number(&v, "seq"),
                at: number(&v, "at"),
                kind: field(&v, "kind"),
                session_id: field(&v, "session_id"),
                job_id: field(&v, "job_id"),
                worker_id: field(&v, "worker_id"),
                worker_name: field(&v, "worker_name"),
                project_id: field(&v, "project_id"),
                state: field(&v, "state"),
                tool_name: field(&v, "tool_name"),
                file_path: field(&v, "file_path"),
            };
            (!a.kind.is_empty()).then_some(Frame::Activity(a))
        }
        "cursor" => {
            let cursor = field(&v, "cursor");
            decimal(&cursor).then_some(Frame::Cursor { machine, cursor })
        }
        "gap" => {
            let cursor = field(&v, "cursor");
            decimal(&cursor).then(|| Frame::Gap { machine, from: number(&v, "from"), to: number(&v, "to"), cursor })
        }
        "machine" => {
            let state = field(&v, "state");
            (!state.is_empty()).then(|| Frame::Machine { machine, state, error: field(&v, "error"), reason: field(&v, "reason") })
        }
        _ => None,
    }
}

// Cursors

/// The latest cursor per machine, as `cursor` and `gap` frames move them.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Cursors(BTreeMap<String, String>);

impl Cursors {
    pub fn apply(&mut self, frame: &Frame) {
        match frame {
            Frame::Cursor { machine, cursor } | Frame::Gap { machine, cursor, .. } => {
                self.0.insert(machine.clone(), cursor.clone());
            }
            _ => {}
        }
    }

    #[cfg(test)]
    pub fn get(&self, machine: &str) -> Option<&str> {
        self.0.get(machine).map(String::as_str)
    }

    /// `cursors=` for the stream: `box:40,home:12`.
    pub fn query(&self) -> String {
        self.0.iter().map(|(m, c)| format!("{}:{c}", encode(m))).collect::<Vec<_>>().join(",")
    }

    /// The stream's path from these cursors. A machine without one starts at
    /// its present (start=now): a live view never replays a machine's history.
    pub fn path(&self) -> String {
        match self.query() {
            q if q.is_empty() => format!("{STREAM_PATH}?start=now"),
            q => format!("{STREAM_PATH}?start=now&cursors={q}"),
        }
    }
}

fn encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

// Lines

/// The kind toggles at the top of the page.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Kind {
    Sessions,
    Tools,
    Tasks,
    Workers,
    Problems,
}

impl Kind {
    pub const ALL: [Kind; 5] = [Kind::Sessions, Kind::Tools, Kind::Tasks, Kind::Workers, Kind::Problems];

    pub fn label(self) -> &'static str {
        match self {
            Kind::Sessions => "sessions",
            Kind::Tools => "tools",
            Kind::Tasks => "tasks",
            Kind::Workers => "workers/workflows",
            Kind::Problems => "problems",
        }
    }
}

/// What a line is about. Fleet lines are gaps and machine state changes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Category {
    Session,
    Tool,
    Task,
    Worker,
    Fleet,
}

impl Category {
    fn kind(self) -> Option<Kind> {
        match self {
            Category::Session => Some(Kind::Sessions),
            Category::Tool => Some(Kind::Tools),
            Category::Task => Some(Kind::Tasks),
            Category::Worker => Some(Kind::Workers),
            Category::Fleet => None,
        }
    }
}

/// Where a line opens, when its frame carries the ids for it.
#[derive(Clone, Debug, PartialEq)]
pub enum Open {
    Session(String),
    Run { worker_id: String, run_id: String, session_id: String },
}

#[derive(Debug, PartialEq)]
pub struct Line {
    /// Unix ms: the record's own time, else when the console received it.
    pub at: u64,
    pub machine: String,
    pub category: Category,
    pub problem: bool,
    /// The kind in words: session, run, tool, task, worker run, review,
    /// delivery, gap, machine.
    pub word: &'static str,
    /// Drawn with the tool stream's "└" mark.
    pub tool: bool,
    /// The rest of the line in the tool stream's parts (name, detail,
    /// summary).
    pub text: tool_summary::Line,
    pub open: Option<Open>,
}

/// The state Swarm gives a kind by default (activity.go); a different state
/// is news and is shown, the default is already said by the kind.
fn default_state(kind: &str) -> &'static str {
    match kind {
        "session.created" | "session.tool.started" => "started",
        "session.assistant.started" => "running",
        "session.assistant.completed" | "session.run.completed" | "session.tool.completed" => "finished",
        "session.run.failed" | "session.tool.failed" => "failed",
        "session.run.cancelled" | "session.run.interrupted" => "stopped",
        "session.review.recorded" => "recorded",
        _ => "changed",
    }
}

/// A state in words: `job.publish_failed` is "publish failed".
fn state_words(state: &str) -> String {
    let state = state.strip_prefix("job.").unwrap_or(state);
    match state {
        "waiting_tasks" => "waiting on tasks".into(),
        s => s.replace(['_', '.'], " "),
    }
}

fn is_problem(kind: &str, state: &str) -> bool {
    matches!(kind, "session.run.failed" | "session.tool.failed" | "job.failed" | "session.run.cancelled" | "session.run.interrupted")
        || matches!(state, "failed" | "blocked" | "cancelled" | "canceled" | "interrupted" | "stopped" | "job.publish_failed")
}

/// What a kind is, in words: (category, the kind's word, what happened).
fn describe(kind: &str) -> (Category, &'static str, String) {
    let (category, word, what) = match kind {
        "session.created" => (Category::Session, "session", "created"),
        "session.assistant.started" => (Category::Session, "session", "assistant started"),
        "session.assistant.completed" => (Category::Session, "session", "assistant finished"),
        "session.lifecycle.updated" => (Category::Session, "session", "lifecycle changed"),
        "session.run.completed" => (Category::Session, "run", "completed"),
        "session.run.failed" => (Category::Session, "run", "failed"),
        "session.run.cancelled" => (Category::Session, "run", "cancelled"),
        "session.run.interrupted" => (Category::Session, "run", "interrupted"),
        "session.run_intent.updated" => (Category::Session, "run", "intent updated"),
        "session.run_intent.recorded" => (Category::Session, "run", "intent recorded"),
        "session.run.waiting_tasks" => (Category::Task, "run", "waiting on tasks"),
        "session.run.tasks_ready" => (Category::Task, "run", "tasks ready"),
        "session.task.reported" => (Category::Task, "task", "reported"),
        "session.task.delivered" => (Category::Task, "task", "delivered"),
        "session.review.recorded" => (Category::Worker, "review", "recorded"),
        "session.delivery.updated" => (Category::Worker, "delivery", "updated"),
        "job.started" => (Category::Worker, "worker run", "started"),
        "job.state_changed" => (Category::Worker, "worker run", "state changed"),
        "job.finished" => (Category::Worker, "worker run", "finished"),
        "job.failed" => (Category::Worker, "worker run", "failed"),
        "review.recorded" => (Category::Worker, "review", "recorded"),
        // A kind this console does not know yet: shown as Swarm names it.
        other => {
            let category = if other.starts_with("job.") { Category::Worker } else { Category::Session };
            return (category, "event", other.to_string());
        }
    };
    (category, word, what.to_string())
}

/// `worker/{worker_id}/{run_id}`, the job id of a worker run.
fn worker_run(job_id: &str) -> Option<(String, String)> {
    let mut parts = job_id.splitn(3, '/');
    match (parts.next(), parts.next(), parts.next()) {
        (Some("worker"), Some(w), Some(r)) if !w.is_empty() && !r.is_empty() => Some((w.to_string(), r.to_string())),
        _ => None,
    }
}

/// Where an activity line opens: a worker's run when the frame names one,
/// else its conversation; else nowhere.
fn open_of(a: &Activity) -> Option<Open> {
    let run = worker_run(&a.job_id).filter(|_| a.kind.starts_with("job.") || a.kind == "review.recorded");
    match run {
        Some((worker_id, run_id)) => Some(Open::Run { worker_id, run_id, session_id: a.session_id.clone() }),
        None if !a.session_id.is_empty() => Some(Open::Session(a.session_id.clone())),
        None => None,
    }
}

pub fn activity_line(a: &Activity, now: u64) -> Line {
    let at = if a.at > 0 { a.at } else { now };
    let problem = is_problem(&a.kind, &a.state);
    if a.kind.starts_with("session.tool.") {
        let what = a.kind.trim_start_matches("session.tool.");
        let what = if what == "completed" { "finished" } else { what };
        let summary = if a.state.is_empty() || a.state == default_state(&a.kind) { what.to_string() } else { state_words(&a.state) };
        let name = if a.tool_name.is_empty() { "tool" } else { &a.tool_name };
        return Line {
            at,
            machine: a.machine.clone(),
            category: Category::Tool,
            problem,
            word: "tool",
            tool: true,
            text: tool_summary::line(name, &a.file_path, &summary, ""),
            open: open_of(a),
        };
    }
    let (category, word, what) = describe(&a.kind);
    let state = if a.state.is_empty() || a.state == default_state(&a.kind) { String::new() } else { state_words(&a.state) };
    // Said once: "worker run deploy-bot failed", not "failed · failed".
    let state = if state == what { String::new() } else { state };
    Line {
        at,
        machine: a.machine.clone(),
        category,
        problem,
        word,
        tool: false,
        text: tool_summary::line(&a.worker_name, &what, &state, ""),
        open: open_of(a),
    }
}

pub fn gap_line(machine: &str, from: u64, to: u64, now: u64) -> Line {
    // from..to are outbox positions: some of them were never this person's
    // records, so the count is an upper bound.
    let what = if to >= from && from > 0 {
        let n = to - from + 1;
        format!("up to {n} event{} not shown", if n == 1 { "" } else { "s" })
    } else {
        "events not shown".to_string()
    };
    Line {
        at: now,
        machine: machine.to_string(),
        category: Category::Fleet,
        problem: true,
        word: "gap",
        tool: false,
        text: tool_summary::line("", &what, "gap", ""),
        open: None,
    }
}

/// A machine's new state as a line; None when nothing changed.
pub fn machine_line(machine: &str, state: &str, error: &str, reason: &str, was: Option<&str>, now: u64) -> Option<Line> {
    if was == Some(state) {
        return None;
    }
    let (what, problem) = match state {
        "online" if matches!(was, Some("offline") | Some("not_shown")) => ("back online".to_string(), false),
        "online" => ("online".to_string(), false),
        "offline" if error.is_empty() => ("offline".to_string(), true),
        "offline" => (format!("offline: {error}"), true),
        "not_shown" if reason.is_empty() => ("not shown".to_string(), false),
        "not_shown" => (format!("not shown: {reason}"), false),
        other => (state_words(other), false),
    };
    Some(Line {
        at: now,
        machine: machine.to_string(),
        category: Category::Fleet,
        problem,
        word: "machine",
        tool: false,
        text: tool_summary::line("", &what, "", ""),
        open: None,
    })
}

// The feed

/// What the page shows: the bounded lines, the cursors to reconnect with,
/// the machines seen and their last state, and which one is the home.
pub struct Feed {
    lines: VecDeque<Line>,
    cap: usize,
    pub cursors: Cursors,
    home: Option<String>,
    /// Since connecting, no machine was announced online yet: the first one
    /// is the home (Swarm announces the home online at the start).
    awaiting_home: bool,
    states: HashMap<String, String>,
    machines: Vec<String>,
    hues: HashMap<String, f32>,
}

impl Default for Feed {
    fn default() -> Self {
        Feed::new(BUFFER)
    }
}

impl Feed {
    pub fn new(cap: usize) -> Feed {
        Feed { lines: VecDeque::new(), cap: cap.max(1), cursors: Cursors::default(), home: None, awaiting_home: false, states: HashMap::new(), machines: Vec::new(), hues: HashMap::new() }
    }

    pub fn lines(&self) -> &VecDeque<Line> {
        &self.lines
    }

    /// The home's name in the stream, once it announced itself.
    pub fn home(&self) -> Option<&str> {
        self.home.as_deref()
    }

    /// Every machine the stream named, the home first, then in order of
    /// first appearance.
    pub fn machines(&self) -> Vec<String> {
        let mut out: Vec<String> = self.home.iter().cloned().collect();
        out.extend(self.machines.iter().filter(|m| Some(*m) != self.home.as_ref()).cloned());
        out
    }

    /// The machine's color, as a hue (0..1).
    pub fn hue(&self, machine: &str) -> f32 {
        self.hues.get(machine).copied().unwrap_or(HUES[0] / 360.)
    }

    pub fn state(&self, machine: &str) -> Option<&str> {
        self.states.get(machine).map(String::as_str)
    }

    /// A new connection opened.
    pub fn connected(&mut self) {
        self.awaiting_home = true;
    }

    fn seen(&mut self, machine: &str) {
        if !self.machines.iter().any(|m| m == machine) {
            self.machines.push(machine.to_string());
            self.hues = machine_hues(&self.machines);
        }
    }

    /// Adds a line; returns the line that fell off the top, if one did.
    fn push(&mut self, line: Line) -> Option<Line> {
        self.lines.push_back(line);
        (self.lines.len() > self.cap).then(|| self.lines.pop_front()).flatten()
    }

    /// Takes one frame; returns the lines that fell off the top.
    pub fn apply(&mut self, frame: Frame, now: u64) -> Vec<Line> {
        self.cursors.apply(&frame);
        let line = match frame {
            Frame::Activity(a) => {
                self.seen(&a.machine);
                Some(activity_line(&a, now))
            }
            Frame::Cursor { machine, .. } => {
                self.seen(&machine);
                None
            }
            Frame::Gap { machine, from, to, .. } => {
                self.seen(&machine);
                Some(gap_line(&machine, from, to, now))
            }
            Frame::Machine { machine, state, error, reason } => {
                self.seen(&machine);
                if state == "online" && self.awaiting_home {
                    self.awaiting_home = false;
                    self.home = Some(machine.clone());
                }
                let was = self.states.insert(machine.clone(), state.clone());
                machine_line(&machine, &state, &error, &reason, was.as_deref(), now)
            }
        };
        line.and_then(|l| self.push(l)).into_iter().collect()
    }
}

// Filters

/// The page's filters; everything shows by default.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Filter {
    pub hidden_machines: BTreeSet<String>,
    pub hidden_kinds: BTreeSet<Kind>,
    pub only_problems: bool,
}

impl Filter {
    pub fn kind_on(&self, kind: Kind) -> bool {
        !self.hidden_kinds.contains(&kind)
    }

    pub fn machine_on(&self, machine: &str) -> bool {
        !self.hidden_machines.contains(machine)
    }

    pub fn toggle_kind(&mut self, kind: Kind) {
        if !self.hidden_kinds.remove(&kind) {
            self.hidden_kinds.insert(kind);
        }
    }

    pub fn toggle_machine(&mut self, machine: &str) {
        if !self.hidden_machines.remove(machine) {
            self.hidden_machines.insert(machine.to_string());
        }
    }

    /// A line shows when its machine is on and its kind is on. A problem
    /// (a failure, a gap, a machine offline) also shows under "problems"
    /// when its own kind is off; machines coming online or not shown always
    /// show, unless only problems are asked for.
    pub fn shows(&self, line: &Line) -> bool {
        if !self.machine_on(&line.machine) || (self.only_problems && !line.problem) {
            return false;
        }
        let own = line.category.kind().map(|k| self.kind_on(k));
        if line.problem {
            own.unwrap_or(false) || self.kind_on(Kind::Problems)
        } else {
            own.unwrap_or(true)
        }
    }
}

// Presentation helpers

/// Hues for machines: well apart, and none red, which means a problem here.
const HUES: [f32; 9] = [40., 75., 120., 160., 188., 212., 245., 280., 312.];

fn fnv(name: &str) -> u32 {
    let mut h: u32 = 0x811c9dc5;
    for b in name.bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(0x01000193);
    }
    h
}

/// A hue (0..1) for each machine: the one its name hashes to, or the next
/// free one, taken in name order, so the same fleet gets the same colors on
/// every run and up to nine machines never share one.
pub fn machine_hues(names: &[String]) -> HashMap<String, f32> {
    let mut sorted: Vec<&String> = names.iter().collect();
    sorted.sort();
    sorted.dedup();
    let mut taken = [false; HUES.len()];
    let mut out = HashMap::new();
    for name in sorted {
        let start = fnv(name) as usize % HUES.len();
        let slot = (0..HUES.len()).map(|i| (start + i) % HUES.len()).find(|s| !taken[*s]).unwrap_or(start);
        taken[slot] = true;
        out.insert(name.clone(), HUES[slot] / 360.);
    }
    out
}

/// HH:MM:SS of `ms` at `offset_secs` from UTC.
pub fn hms(ms: u64, offset_secs: i64) -> String {
    let secs = (ms / 1000) as i64 + offset_secs;
    let day = secs.rem_euclid(86_400);
    format!("{:02}:{:02}:{:02}", day / 3600, day % 3600 / 60, day % 60)
}

/// This computer's offset from UTC at `ms`, in seconds.
pub fn local_offset(ms: u64) -> i64 {
    let t = (ms / 1000) as libc::time_t;
    let mut tm: libc::tm = unsafe { std::mem::zeroed() };
    // SAFETY: localtime_r writes only into `tm`.
    let ok = unsafe { !libc::localtime_r(&t, &mut tm).is_null() };
    if ok { tm.tm_gmtoff as i64 } else { 0 }
}

// The connection

/// How long a silent stream is trusted: Swarm sends a heartbeat every 15 s.
const IDLE: Duration = Duration::from_secs(45);
const CONNECT: Duration = Duration::from_secs(3);
/// The first wait before reconnecting after a drop, and the longest.
const RETRY_FIRST: Duration = Duration::from_secs(1);
const RETRY_MAX: Duration = Duration::from_secs(30);
/// The longest line read from the stream (Swarm's own frame limit is 64 KiB).
const LINE_LIMIT: u64 = 256 << 10;

/// What the connection thread tells the page.
#[derive(Debug)]
pub enum Event {
    Connected,
    Frame(Frame),
    /// The stream dropped; it reconnects after `retry_in`.
    Dropped { reason: String, retry_in: Duration },
    /// Swarm refused the stream; it is not tried again.
    Refused(String),
}

/// The one connection to the home's fleet stream, on its own thread.
/// Dropping it closes the connection and ends the thread.
pub struct Connection {
    stop: Option<Sender<()>>,
    socket: Arc<Mutex<Option<TcpStream>>>,
}

impl Drop for Connection {
    fn drop(&mut self) {
        self.stop.take();
        if let Some(socket) = self.socket.lock().ok().and_then(|mut s| s.take()) {
            let _ = socket.shutdown(Shutdown::Both);
        }
    }
}

impl Connection {
    /// Opens the stream on the home at `base` (its loopback API) as this
    /// app's session, from `cursors`.
    pub fn open(base: &str, token: &str, cursors: Cursors) -> (Connection, Receiver<Event>) {
        let (tx, rx) = mpsc::channel();
        let (stop_tx, stop_rx) = mpsc::channel();
        let socket = Arc::new(Mutex::new(None));
        let (base, token, shared) = (base.to_string(), token.to_string(), socket.clone());
        std::thread::spawn(move || run(base, token, cursors, stop_rx, shared, tx));
        (Connection { stop: Some(stop_tx), socket }, rx)
    }
}

enum End {
    Dropped(String),
    Refused(String),
    Stopped,
}

fn stopped(stop: &Receiver<()>) -> bool {
    !matches!(stop.try_recv(), Err(TryRecvError::Empty))
}

fn run(base: String, token: String, mut cursors: Cursors, stop: Receiver<()>, socket: Arc<Mutex<Option<TcpStream>>>, tx: Sender<Event>) {
    let mut delay = RETRY_FIRST;
    loop {
        let mut connected = false;
        let end = once(&base, &token, &mut cursors, &stop, &socket, &tx, &mut connected);
        if let Ok(mut s) = socket.lock() {
            s.take();
        }
        if stopped(&stop) {
            return;
        }
        match end {
            End::Stopped => return,
            End::Refused(reason) => {
                let _ = tx.send(Event::Refused(reason));
                return;
            }
            End::Dropped(reason) => {
                if connected {
                    delay = RETRY_FIRST;
                }
                if tx.send(Event::Dropped { reason, retry_in: delay }).is_err() {
                    return;
                }
                match stop.recv_timeout(delay) {
                    Err(RecvTimeoutError::Timeout) => {}
                    _ => return,
                }
                delay = (delay * 2).min(RETRY_MAX);
            }
        }
    }
}

/// `http://host:port/prefix` as (host:port, /prefix).
fn split_base(base: &str) -> Option<(String, String)> {
    let rest = base.trim_end_matches('/').strip_prefix("http://")?;
    let (host, prefix) = match rest.find('/') {
        Some(i) => (&rest[..i], &rest[i..]),
        None => (rest, ""),
    };
    (!host.is_empty()).then(|| (host.to_string(), prefix.to_string()))
}

/// What a refusal says: Swarm's `error` (or `error.message`, `message`).
fn refusal_text(body: &str) -> String {
    let v: Value = serde_json::from_str(body).unwrap_or(Value::Null);
    v.get("error")
        .and_then(|e| e.as_str().map(String::from).or_else(|| e.get("message").and_then(Value::as_str).map(String::from)))
        .or_else(|| v.get("message").and_then(Value::as_str).map(String::from))
        .unwrap_or_else(|| body.trim().chars().take(300).collect())
}

/// One HTTP response head: status and headers (names lower case).
fn read_head(reader: &mut impl BufRead) -> io::Result<(u16, HashMap<String, String>)> {
    let mut line = String::new();
    (&mut *reader).take(8 << 10).read_line(&mut line)?;
    let status = line.split_whitespace().nth(1).and_then(|s| s.parse::<u16>().ok()).ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "not an HTTP answer"))?;
    let mut headers = HashMap::new();
    for _ in 0..200 {
        line.clear();
        if (&mut *reader).take(8 << 10).read_line(&mut line)? == 0 {
            break;
        }
        let l = line.trim_end();
        if l.is_empty() {
            return Ok((status, headers));
        }
        if let Some((name, value)) = l.split_once(':') {
            headers.insert(name.trim().to_ascii_lowercase(), value.trim().to_string());
        }
    }
    Err(io::Error::new(io::ErrorKind::InvalidData, "unreadable HTTP headers"))
}

fn once(base: &str, token: &str, cursors: &mut Cursors, stop: &Receiver<()>, socket: &Mutex<Option<TcpStream>>, tx: &Sender<Event>, connected: &mut bool) -> End {
    let Some((host, prefix)) = split_base(base) else {
        return End::Refused("The live stream reaches Swarm over its loopback API (http) only.".into());
    };
    let addr = match host.to_socket_addrs().ok().and_then(|mut a| a.next()) {
        Some(a) => a,
        None => return End::Dropped(format!("cannot resolve {host}")),
    };
    let stream = match TcpStream::connect_timeout(&addr, CONNECT) {
        Ok(s) => s,
        Err(_) => return End::Dropped("Swarm is not answering".into()),
    };
    let _ = stream.set_read_timeout(Some(IDLE));
    let _ = stream.set_write_timeout(Some(Duration::from_secs(5)));
    if let (Ok(clone), Ok(mut shared)) = (stream.try_clone(), socket.lock()) {
        *shared = Some(clone);
    }
    // Closed meanwhile: the close may have missed the socket just stored.
    if stopped(stop) {
        return End::Stopped;
    }
    let request = format!(
        "GET {prefix}{} HTTP/1.1\r\nHost: {host}\r\nX-Swarm-Token: {token}\r\nAccept: text/event-stream\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n",
        cursors.path()
    );
    let mut writer = &stream;
    if writer.write_all(request.as_bytes()).and_then(|_| writer.flush()).is_err() {
        return End::Dropped("could not ask Swarm for the stream".into());
    }
    let mut reader = BufReader::new(&stream);
    let (status, headers) = match read_head(&mut reader) {
        Ok(h) => h,
        Err(e) if stopped(stop) => {
            let _ = e;
            return End::Stopped;
        }
        Err(e) => return End::Dropped(format!("stream: {e}")),
    };
    let chunked = headers.get("transfer-encoding").is_some_and(|v| v.to_ascii_lowercase().contains("chunked"));
    let mut body: Box<dyn BufRead + '_> = if chunked { Box::new(BufReader::new(Chunked::new(reader))) } else { Box::new(reader) };
    if status != 200 {
        let mut text = String::new();
        let _ = (&mut body).take(64 << 10).read_to_string(&mut text);
        let said = refusal_text(&text);
        let said = if said.is_empty() { format!("Swarm answered HTTP {status}") } else { said };
        return match status {
            401 => End::Refused("Swarm no longer accepts this app's session. Connect the app again.".into()),
            404 => End::Refused("This Swarm has no fleet activity stream (GET /v3/fleet/activity/stream): it needs a newer Swarm.".into()),
            400 | 403 | 409 => End::Refused(said),
            _ => End::Dropped(format!("HTTP {status}: {said}")),
        };
    }
    if !headers.get("content-type").is_some_and(|t| t.starts_with("text/event-stream")) {
        return End::Refused("Swarm's answer to the stream was not an event stream.".into());
    }
    *connected = true;
    if tx.send(Event::Connected).is_err() {
        return End::Stopped;
    }
    let mut parser = SseParser::default();
    let mut buf = Vec::new();
    loop {
        buf.clear();
        let read = (&mut body).take(LINE_LIMIT).read_until(b'\n', &mut buf);
        if stopped(stop) {
            return End::Stopped;
        }
        match read {
            Ok(0) => return End::Dropped("Swarm closed the stream".into()),
            Ok(_) if buf.last() != Some(&b'\n') => {
                return End::Dropped(if buf.len() as u64 >= LINE_LIMIT { "Swarm sent a line too long".into() } else { "Swarm closed the stream".into() });
            }
            Ok(_) => {}
            Err(e) if matches!(e.kind(), io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut) => return End::Dropped("the stream went silent".into()),
            Err(e) => return End::Dropped(format!("stream: {e}")),
        }
        let line = String::from_utf8_lossy(&buf);
        if let Some(Sse::Frame { event, data }) = parser.line(&line) {
            if let Some(f) = frame(&event, &data) {
                cursors.apply(&f);
                if tx.send(Event::Frame(f)).is_err() {
                    return End::Stopped;
                }
            }
        }
    }
}

/// An HTTP/1.1 chunked body as plain bytes.
pub struct Chunked<R: BufRead> {
    inner: R,
    left: usize,
    /// A chunk's data ended; its CRLF comes before the next size.
    crlf: bool,
    done: bool,
}

impl<R: BufRead> Chunked<R> {
    pub fn new(inner: R) -> Chunked<R> {
        Chunked { inner, left: 0, crlf: false, done: false }
    }
}

impl<R: BufRead> Read for Chunked<R> {
    fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
        if self.done || out.is_empty() {
            return Ok(0);
        }
        if self.left == 0 {
            let mut line = String::new();
            if self.crlf {
                (&mut self.inner).take(1024).read_line(&mut line)?;
                self.crlf = false;
                line.clear();
            }
            if (&mut self.inner).take(1024).read_line(&mut line)? == 0 {
                return Ok(0);
            }
            let size = line.trim().split(';').next().unwrap_or("").trim();
            let size = usize::from_str_radix(size, 16).map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "bad chunk size"))?;
            if size == 0 {
                self.done = true;
                return Ok(0);
            }
            self.left = size;
        }
        let want = out.len().min(self.left);
        let n = self.inner.read(&mut out[..want])?;
        if n == 0 {
            return Err(io::Error::new(io::ErrorKind::UnexpectedEof, "chunk cut short"));
        }
        self.left -= n;
        if self.left == 0 {
            self.crlf = true;
        }
        Ok(n)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed_lines(text: &str) -> Vec<Sse> {
        let mut p = SseParser::default();
        text.split_inclusive('\n').filter_map(|l| p.line(l)).collect()
    }

    #[test]
    fn sse_lines_become_frames_and_comments() {
        let got = feed_lines(": connected\n\nevent: machine\ndata: {\"kind\":\"machine\"}\n\n: heartbeat\n\nevent: x\r\ndata: a\r\ndata:b\r\n\r\n");
        assert_eq!(
            got,
            vec![
                Sse::Comment("connected".into()),
                Sse::Frame { event: "machine".into(), data: "{\"kind\":\"machine\"}".into() },
                Sse::Comment("heartbeat".into()),
                Sse::Frame { event: "x".into(), data: "a\nb".into() },
            ]
        );
        // Blank lines alone and unknown fields say nothing.
        assert!(feed_lines("\n\nid: 4\nretry: 10\n\n").is_empty());
    }

    #[test]
    fn frames_parse_and_reject_what_is_missing() {
        let a = frame("activity", r#"{"machine":"box","seq":41,"at":1793000000000,"kind":"session.tool.started","session_id":"s1","job_id":"","worker_id":"","worker_name":"","project_id":"p1","state":"started","summary":"session.tool.started","tool_name":"read","file_path":"main.go"}"#);
        let Some(Frame::Activity(a)) = a else { panic!("{a:?}") };
        assert_eq!((a.machine.as_str(), a.seq, a.at, a.tool_name.as_str(), a.file_path.as_str()), ("box", 41, 1793000000000, "read", "main.go"));
        assert_eq!(frame("cursor", r#"{"kind":"cursor","machine":"box","cursor":"41"}"#), Some(Frame::Cursor { machine: "box".into(), cursor: "41".into() }));
        assert_eq!(frame("gap", r#"{"kind":"gap","machine":"box","from":12,"to":40,"cursor":"40"}"#), Some(Frame::Gap { machine: "box".into(), from: 12, to: 40, cursor: "40".into() }));
        assert_eq!(
            frame("machine", r#"{"kind":"machine","machine":"forge","state":"offline","error":"HTTP 429"}"#),
            Some(Frame::Machine { machine: "forge".into(), state: "offline".into(), error: "HTTP 429".into(), reason: "".into() })
        );
        assert_eq!(frame("cursor", r#"{"machine":"box","cursor":"4x"}"#), None);
        assert_eq!(frame("activity", r#"{"machine":"box","kind":""}"#), None);
        assert_eq!(frame("activity", r#"{"kind":"session.created"}"#), None, "no machine");
        assert_eq!(frame("message", r#"{"machine":"box"}"#), None);
        assert_eq!(frame("cursor", "not json"), None);
    }

    #[test]
    fn cursors_follow_cursor_and_gap_frames() {
        let mut c = Cursors::default();
        assert_eq!(c.path(), "/v3/fleet/activity/stream?start=now");
        c.apply(&Frame::Cursor { machine: "home".into(), cursor: "12".into() });
        c.apply(&Frame::Cursor { machine: "box".into(), cursor: "41".into() });
        c.apply(&Frame::Gap { machine: "box".into(), from: 42, to: 90, cursor: "90".into() });
        c.apply(&Frame::Machine { machine: "lab".into(), state: "online".into(), error: "".into(), reason: "".into() });
        assert_eq!(c.get("box"), Some("90"));
        assert_eq!(c.get("lab"), None);
        assert_eq!(c.query(), "box:90,home:12");
        assert_eq!(c.path(), "/v3/fleet/activity/stream?start=now&cursors=box:90,home:12");
        let mut odd = Cursors::default();
        odd.apply(&Frame::Cursor { machine: "a b".into(), cursor: "1".into() });
        assert_eq!(odd.query(), "a%20b:1");
    }

    fn act(machine: &str, kind: &str, state: &str) -> Activity {
        Activity { machine: machine.into(), at: 1_000, kind: kind.into(), state: state.into(), ..Default::default() }
    }

    #[test]
    fn each_frame_kind_becomes_a_line() {
        let mut a = act("box", "session.tool.started", "started");
        a.tool_name = "read".into();
        a.file_path = "main.go".into();
        a.session_id = "s1".into();
        let l = activity_line(&a, 9);
        assert_eq!((l.word, l.tool, l.category, l.problem, l.at), ("tool", true, Category::Tool, false, 1_000));
        assert_eq!(l.text.text, "read main.go · started");
        assert_eq!(l.open, Some(Open::Session("s1".into())));

        let l = activity_line(&act("box", "session.tool.failed", "failed"), 9);
        assert_eq!((l.text.text.as_str(), l.problem), ("tool · failed", true));

        let mut w = act("social", "job.finished", "finished");
        w.worker_name = "deploy-bot".into();
        w.job_id = "worker/w1/r9".into();
        w.session_id = "s2".into();
        let l = activity_line(&w, 9);
        assert_eq!((l.word, l.text.text.as_str(), l.category), ("worker run", "deploy-bot finished", Category::Worker));
        assert_eq!(l.open, Some(Open::Run { worker_id: "w1".into(), run_id: "r9".into(), session_id: "s2".into() }));

        w.kind = "job.state_changed".into();
        w.state = "blocked".into();
        let l = activity_line(&w, 9);
        assert_eq!((l.text.text.as_str(), l.problem), ("deploy-bot state changed · blocked", true));

        // The kind's own default state is not repeated; another state is news.
        let l = activity_line(&act("home", "session.run.completed", "finished"), 9);
        assert_eq!((l.word, l.text.text.as_str(), l.open.clone()), ("run", "completed", None));
        let l = activity_line(&act("home", "session.lifecycle.updated", "waiting_tasks"), 9);
        assert_eq!(l.text.text, "lifecycle changed · waiting on tasks");
        let l = activity_line(&act("home", "session.run.cancelled", "cancelled"), 9);
        assert_eq!((l.text.text.as_str(), l.problem), ("cancelled", true));
        let l = activity_line(&act("home", "session.delivery.updated", "job.publish_failed"), 9);
        assert_eq!((l.word, l.text.text.as_str(), l.problem), ("delivery", "updated · publish failed", true));
        let l = activity_line(&act("home", "session.task.reported", "changed"), 9);
        assert_eq!((l.word, l.category), ("task", Category::Task));
        // An unknown kind is shown as Swarm named it.
        let l = activity_line(&act("home", "session.new_thing", "changed"), 9);
        assert_eq!((l.word, l.text.text.as_str()), ("event", "session.new_thing"));
        // No time: when it was received.
        let mut z = act("home", "session.created", "started");
        z.at = 0;
        assert_eq!(activity_line(&z, 77).at, 77);

        let g = gap_line("box", 12, 40, 5);
        assert_eq!((g.word, g.text.text.as_str(), g.problem, g.category), ("gap", "up to 29 events not shown · gap", true, Category::Fleet));
        assert_eq!(gap_line("box", 91, 40, 5).text.text, "events not shown · gap");

        let m = machine_line("forge", "offline", "HTTP 429", "", Some("online"), 5).unwrap();
        assert_eq!((m.word, m.text.text.as_str(), m.problem), ("machine", "offline: HTTP 429", true));
        assert_eq!(machine_line("social", "online", "", "", Some("offline"), 5).unwrap().text.text, "back online");
        assert_eq!(machine_line("social", "online", "", "", None, 5).unwrap().text.text, "online");
        assert_eq!(machine_line("social", "online", "", "", Some("online"), 5), None);
        let n = machine_line("swarm-deploy", "not_shown", "", "linked work rule is off", None, 5).unwrap();
        assert_eq!((n.text.text.as_str(), n.problem), ("not shown: linked work rule is off", false));
    }

    fn machine(name: &str, state: &str) -> Frame {
        Frame::Machine { machine: name.into(), state: state.into(), error: "".into(), reason: "".into() }
    }

    #[test]
    fn the_feed_learns_the_home_and_machine_states() {
        let mut f = Feed::default();
        f.connected();
        // A not-shown machine may come before the home's own announcement.
        f.apply(machine("lab", "not_shown"), 1);
        f.apply(machine("pc", "online"), 1);
        f.apply(machine("box", "online"), 1);
        assert_eq!(f.home(), Some("pc"));
        assert_eq!(f.machines(), vec!["pc", "lab", "box"]);
        f.apply(machine("box", "offline"), 2);
        f.apply(machine("box", "online"), 3);
        assert_eq!(f.state("box"), Some("online"));
        let texts: Vec<&str> = f.lines().iter().map(|l| l.text.text.as_str()).collect();
        assert_eq!(texts, vec!["not shown", "online", "online", "offline", "back online"]);
        // Reconnected: Swarm announces them again; unchanged says nothing.
        f.connected();
        f.apply(machine("pc", "online"), 4);
        assert_eq!(f.lines().len(), 5);
        f.apply(Frame::Cursor { machine: "pc".into(), cursor: "7".into() }, 4);
        assert_eq!(f.cursors.get("pc"), Some("7"));
        assert_eq!(f.lines().len(), 5, "a cursor is not a line");
    }

    #[test]
    fn the_buffer_is_bounded() {
        let mut f = Feed::new(3);
        let mut dropped = 0;
        for i in 0..5u64 {
            let mut a = act("home", "session.created", "started");
            a.at = i + 1;
            dropped += f.apply(Frame::Activity(a), 0).len();
        }
        assert_eq!((f.lines().len(), dropped), (3, 2));
        assert_eq!(f.lines().iter().map(|l| l.at).collect::<Vec<_>>(), vec![3, 4, 5], "newest at the bottom");
        assert_eq!(Feed::default().cap, BUFFER);
    }

    #[test]
    fn filters() {
        let tool = activity_line(&act("box", "session.tool.completed", "finished"), 0);
        let failed_tool = activity_line(&act("box", "session.tool.failed", "failed"), 0);
        let session = activity_line(&act("pc", "session.created", "started"), 0);
        let worker = activity_line(&act("pc", "job.started", "started"), 0);
        let gap = gap_line("box", 1, 2, 0);
        let online = machine_line("box", "online", "", "", None, 0).unwrap();
        let offline = machine_line("box", "offline", "", "", None, 0).unwrap();

        let mut f = Filter::default();
        assert!([&tool, &failed_tool, &session, &worker, &gap, &online, &offline].iter().all(|l| f.shows(l)));

        f.toggle_kind(Kind::Tools);
        assert!(!f.shows(&tool));
        assert!(f.shows(&failed_tool), "a problem still shows under problems");
        f.toggle_kind(Kind::Problems);
        assert!(!f.shows(&failed_tool) && !f.shows(&gap) && !f.shows(&offline));
        assert!(f.shows(&online) && f.shows(&session));
        f.toggle_kind(Kind::Tools);
        assert!(f.shows(&failed_tool), "its own kind is on again");
        f.toggle_kind(Kind::Problems);

        f.only_problems = true;
        assert!(f.shows(&failed_tool) && f.shows(&gap) && f.shows(&offline));
        assert!(!f.shows(&tool) && !f.shows(&session) && !f.shows(&online) && !f.shows(&worker));
        f.only_problems = false;

        f.toggle_machine("box");
        assert!(!f.shows(&tool) && !f.shows(&gap) && !f.shows(&offline));
        assert!(f.shows(&session));
        f.toggle_machine("box");
        assert!(f.shows(&tool));
        f.toggle_kind(Kind::Workers);
        assert!(!f.shows(&worker));
    }

    #[test]
    fn times_and_colors() {
        assert_eq!(hms(0, 0), "00:00:00");
        assert_eq!(hms(1_793_000_000_000, 0), "07:33:20");
        assert_eq!(hms(1_793_000_000_000, 3 * 3600), "10:33:20");
        assert_eq!(hms(1_000, -3600), "23:00:01");
        let names: Vec<String> = ["pc", "forge", "social", "swarm-deploy", "box", "lab", "a", "b", "c"].map(String::from).to_vec();
        let hues = machine_hues(&names);
        let distinct: BTreeSet<u32> = hues.values().map(|h| (h * 360.) as u32).collect();
        assert_eq!(distinct.len(), 9, "nine machines, nine colors");
        let mut reversed = names.clone();
        reversed.reverse();
        assert_eq!(machine_hues(&reversed), hues, "the same fleet, the same colors");
        assert!(hues.values().all(|h| (0.0..1.0).contains(h)));
    }

    #[test]
    fn bases_and_refusals() {
        assert_eq!(split_base("http://127.0.0.1:7781"), Some(("127.0.0.1:7781".into(), "".into())));
        assert_eq!(split_base("http://127.0.0.1:7781/x/"), Some(("127.0.0.1:7781".into(), "/x".into())));
        assert_eq!(split_base("https://a"), None);
        assert_eq!(refusal_text(r#"{"error":"activity stream limit"}"#), "activity stream limit");
        assert_eq!(refusal_text(r#"{"error":{"message":"no"}}"#), "no");
        assert_eq!(refusal_text("404 page not found\n"), "404 page not found");
    }

    #[test]
    fn chunked_bodies_decode() {
        let body = "8\r\n: hello\n\r\n13\r\nevent: x\ndata: {}\n\n\r\n0\r\n\r\n";
        let mut out = String::new();
        Chunked::new(std::io::Cursor::new(body.as_bytes())).read_to_string(&mut out).unwrap();
        assert_eq!(out, ": hello\nevent: x\ndata: {}\n\n");
        assert!(Chunked::new(std::io::Cursor::new(&b"zz\r\n"[..])).read_to_string(&mut String::new()).is_err());
    }

    /// A fake home: answers one stream request with a chunked SSE body,
    /// then closes; the connection reports it and comes back with cursors.
    #[test]
    fn the_connection_streams_and_reconnects_with_cursors() {
        use std::io::Write as _;
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let (seen_tx, seen_rx) = mpsc::channel::<String>();
        std::thread::spawn(move || {
            for (i, conn) in listener.incoming().take(2).enumerate() {
                let mut conn = conn.unwrap();
                let mut reader = BufReader::new(conn.try_clone().unwrap());
                let mut first = String::new();
                reader.read_line(&mut first).unwrap();
                let mut token = String::new();
                loop {
                    let mut h = String::new();
                    reader.read_line(&mut h).unwrap();
                    if h.to_ascii_lowercase().starts_with("x-swarm-token:") {
                        token = h.trim().to_string();
                    }
                    if h.trim().is_empty() {
                        break;
                    }
                }
                seen_tx.send(format!("{} {token}", first.trim())).unwrap();
                let body = if i == 0 {
                    ": connected\n\nevent: machine\ndata: {\"kind\":\"machine\",\"machine\":\"pc\",\"state\":\"online\"}\n\nevent: activity\ndata: {\"machine\":\"pc\",\"seq\":3,\"at\":1,\"kind\":\"session.created\",\"state\":\"started\"}\n\nevent: cursor\ndata: {\"kind\":\"cursor\",\"machine\":\"pc\",\"cursor\":\"3\"}\n\n"
                } else {
                    ": connected\n\n"
                };
                let head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n";
                let chunk = format!("{:x}\r\n{body}\r\n0\r\n\r\n", body.len());
                conn.write_all(head.as_bytes()).unwrap();
                conn.write_all(chunk.as_bytes()).unwrap();
            }
        });
        let (conn, rx) = Connection::open(&base, "tok", Cursors::default());
        let next = || rx.recv_timeout(Duration::from_secs(5)).unwrap();
        assert!(matches!(next(), Event::Connected));
        assert!(matches!(next(), Event::Frame(Frame::Machine { .. })));
        assert!(matches!(next(), Event::Frame(Frame::Activity(_))));
        assert!(matches!(next(), Event::Frame(Frame::Cursor { .. })));
        assert!(matches!(next(), Event::Dropped { retry_in, .. } if retry_in == RETRY_FIRST));
        assert!(matches!(next(), Event::Connected));
        assert_eq!(seen_rx.recv().unwrap(), "GET /v3/fleet/activity/stream?start=now HTTP/1.1 X-Swarm-Token: tok");
        assert_eq!(seen_rx.recv().unwrap(), "GET /v3/fleet/activity/stream?start=now&cursors=pc:3 HTTP/1.1 X-Swarm-Token: tok");
        drop(conn);
    }

    #[test]
    fn a_refusal_stops_the_connection() {
        use std::io::Write as _;
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        std::thread::spawn(move || {
            let (mut conn, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(conn.try_clone().unwrap());
            loop {
                let mut h = String::new();
                reader.read_line(&mut h).unwrap();
                if h.trim().is_empty() {
                    break;
                }
            }
            let body = r#"{"error":"this Swarm has no name to show its activity under"}"#;
            conn.write_all(format!("HTTP/1.1 409 Conflict\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}", body.len()).as_bytes()).unwrap();
        });
        let (_conn, rx) = Connection::open(&base, "tok", Cursors::default());
        match rx.recv_timeout(Duration::from_secs(5)).unwrap() {
            Event::Refused(text) => assert_eq!(text, "this Swarm has no name to show its activity under"),
            other => panic!("{other:?}"),
        }
        assert!(rx.recv_timeout(Duration::from_secs(2)).is_err(), "nothing after a refusal");
    }

    #[test]
    fn closing_ends_a_quiet_stream_at_once() {
        use std::io::Write as _;
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let (done_tx, done_rx) = mpsc::channel();
        std::thread::spawn(move || {
            let (mut conn, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(conn.try_clone().unwrap());
            loop {
                let mut h = String::new();
                reader.read_line(&mut h).unwrap();
                if h.trim().is_empty() {
                    break;
                }
            }
            conn.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n: connected\n\n").unwrap();
            // Waits until the console closes its end.
            let mut rest = Vec::new();
            let _ = reader.read_to_end(&mut rest);
            done_tx.send(()).unwrap();
        });
        let (conn, rx) = Connection::open(&base, "tok", Cursors::default());
        assert!(matches!(rx.recv_timeout(Duration::from_secs(5)).unwrap(), Event::Connected));
        let started = std::time::Instant::now();
        drop(conn);
        done_rx.recv_timeout(Duration::from_secs(5)).unwrap();
        assert!(started.elapsed() < Duration::from_secs(3), "{:?}", started.elapsed());
        // The thread ends: its sender goes.
        let end = std::time::Instant::now() + Duration::from_secs(5);
        loop {
            match rx.recv_timeout(end.saturating_duration_since(std::time::Instant::now())) {
                Ok(_) => continue,
                Err(RecvTimeoutError::Disconnected) => break,
                Err(RecvTimeoutError::Timeout) => panic!("the thread did not end"),
            }
        }
    }
}
