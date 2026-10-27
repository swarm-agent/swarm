//! Swarm's realtime stream: one WebSocket at /v3/realtime/stream, as the web
//! UI opens it. Frames arrive as JSON objects; `live.patch` carries the
//! agent's text as it is produced (asked for with the `live_patch_v1`
//! capability in the `resume` frame). This module only moves frames; what
//! they mean is `live`'s business.

use std::net::TcpStream;
use std::sync::mpsc::Sender;
use std::time::Duration;

use serde_json::{Value, json};
use tungstenite::client::IntoClientRequest;
use tungstenite::stream::MaybeTlsStream;
use tungstenite::{Message, WebSocket};

/// How long a silent socket is trusted: Swarm sends keepalives well within it.
const IDLE: Duration = Duration::from_secs(45);
/// How long one read waits before queued commands get a turn.
const POLL: Duration = Duration::from_millis(100);

pub struct Stream {
    socket: WebSocket<MaybeTlsStream<TcpStream>>,
}

impl Stream {
    /// Opens the stream from `cursor` (the endpoint cursor bootstrap gave) and
    /// sends the `resume` frame asking for live text.
    pub fn open(base: &str, token: &str, cursor: &str) -> Result<Stream, String> {
        let ws = base.replacen("http://", "ws://", 1).replacen("https://", "wss://", 1);
        let url = format!("{ws}/v3/realtime/stream?endpoint_cursor={}", urlencode(cursor));
        let mut request = url.into_client_request().map_err(|e| format!("stream request: {e}"))?;
        request.headers_mut().insert("X-Swarm-Token", token.parse().map_err(|_| "token header")?);
        let (socket, _) = tungstenite::connect(request).map_err(|e| match e {
            tungstenite::Error::Http(r) if r.status().as_u16() == 401 => "Swarm no longer accepts this app's session. Connect the app again.".to_string(),
            tungstenite::Error::Http(r) => format!("Swarm answered HTTP {} to the stream", r.status()),
            e => format!("stream: {e}"),
        })?;
        // Short reads: between them, queued commands (subscribe, resume) go
        // out at once instead of waiting for Swarm's next frame.
        if let MaybeTlsStream::Plain(tcp) = socket.get_ref() {
            let _ = tcp.set_read_timeout(Some(POLL));
        }
        // The caller sends `resume` first: Swarm hands it back with the
        // snapshot, scoped to it.
        Ok(Stream { socket })
    }

    /// Sends one client frame; every frame names the protocol.
    pub fn send(&mut self, frame: Value) -> Result<(), String> {
        let mut frame = frame;
        frame["protocol"] = json!("v3.realtime");
        frame["protocol_version"] = json!(1);
        self.socket.send(Message::Text(frame.to_string().into())).map_err(|e| format!("stream send: {e}"))
    }

    /// Subscribes to a session's live text and events.
    pub fn subscribe(&mut self, session_id: &str) -> Result<(), String> {
        self.send(json!({"kind": "subscribe.session", "session_id": session_id}))
    }

    pub fn unsubscribe(&mut self, session_id: &str) -> Result<(), String> {
        self.send(json!({"kind": "unsubscribe.session", "session_id": session_id}))
    }

    /// One read: the next frame, None when nothing arrived within POLL.
    /// Err when the stream ended.
    fn read(&mut self) -> Result<Option<Value>, String> {
        match self.socket.read() {
            Ok(Message::Text(text)) => Ok(serde_json::from_str::<Value>(&text).ok()),
            Ok(Message::Binary(_)) | Ok(Message::Pong(_)) | Ok(Message::Frame(_)) => Ok(None),
            Ok(Message::Ping(data)) => {
                let _ = self.socket.send(Message::Pong(data));
                Ok(None)
            }
            Ok(Message::Close(_)) => Err("Swarm closed the stream".into()),
            Err(tungstenite::Error::Io(e)) if matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut) => Ok(None),
            Err(e) => Err(format!("stream: {e}")),
        }
    }

    /// The next frame. Err when the stream ended or went silent for IDLE.
    pub fn next(&mut self) -> Result<Value, String> {
        let started = std::time::Instant::now();
        loop {
            if let Some(frame) = self.read()? {
                return Ok(frame);
            }
            if started.elapsed() > IDLE {
                return Err("the stream went silent".into());
            }
        }
    }

    /// Runs the stream on this thread, handing every frame to `tx`, until it
    /// ends. `commands` are frames to send (subscribe, unsubscribe) that arrive
    /// meanwhile; they go out within POLL.
    pub fn pump(mut self, tx: Sender<Value>, commands: std::sync::mpsc::Receiver<Value>) -> String {
        let mut last = std::time::Instant::now();
        loop {
            while let Ok(cmd) = commands.try_recv() {
                if let Err(e) = self.send(cmd) {
                    return e;
                }
            }
            match self.read() {
                Ok(Some(frame)) => {
                    last = std::time::Instant::now();
                    if tx.send(frame).is_err() {
                        return "the console is gone".into();
                    }
                }
                Ok(None) => {
                    if last.elapsed() > IDLE {
                        return "the stream went silent".into();
                    }
                }
                Err(e) => return e,
            }
        }
    }
}

pub(crate) fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    #[test]
    fn encodes_cursors() {
        assert_eq!(super::urlencode("abc-_.~"), "abc-_.~");
        assert_eq!(super::urlencode("a b/c="), "a%20b%2Fc%3D");
    }

    #[test]
    fn nothing_listening_is_an_error() {
        assert!(super::Stream::open("http://127.0.0.1:1", "t", "c").is_err());
    }

    /// Against a real Swarm with a session: SWARM_CONSOLE_TEST_SESSION names
    /// it. Subscribes, sends a message over HTTP and prints every frame kind
    /// for 12 seconds, to see what the stream carries.
    #[test]
    #[ignore]
    fn prints_frames_for_a_message() {
        let api = std::env::var("SWARM_CONSOLE_TEST_API").unwrap();
        let token = std::env::var("SWARM_CONSOLE_TEST_TOKEN").unwrap();
        let session = std::env::var("SWARM_CONSOLE_TEST_SESSION").unwrap();
        let client = crate::swarm::client::Client::new(&api, &token);
        let boot = client
            .post("/v3/sync/bootstrap", serde_json::json!({"surface": "desktop", "selector": {"kind": "recent", "global": true, "recent": {"limit": 5}}, "history": {"mode": "none"}, "resources": {"current_run_state": true}, "include_active": true}))
            .unwrap();
        let cursor = boot.get("snapshot_endpoint_cursor").and_then(|c| c.as_str()).unwrap().to_string();
        let mut stream = super::Stream::open(&api, &token, &cursor).unwrap();
        let mut resume = boot.get("realtime").and_then(|r| r.get("resume")).cloned().unwrap();
        resume["capabilities"] = serde_json::json!(["live_patch_v1"]);
        stream.send(resume).unwrap();
        stream.send(serde_json::json!({"kind": "subscribe.session", "session_id": session, "subscription_id": format!("test:session:{session}")})).unwrap();
        let (tx, rx) = std::sync::mpsc::channel();
        let (_ctx, crx) = std::sync::mpsc::channel();
        std::thread::spawn(move || stream.pump(tx, crx));
        client
            .post(&format!("/v3/sessions/{session}/messages"), serde_json::json!({"client_request_id": format!("test:{}", std::process::id()), "role": "user", "content": "Reply with the single word: pong"}))
            .unwrap();
        let until = std::time::Instant::now() + std::time::Duration::from_secs(12);
        while let Ok(frame) = rx.recv_timeout(until.saturating_duration_since(std::time::Instant::now())) {
            let kind = frame.get("kind").and_then(|k| k.as_str()).unwrap_or("?");
            let event = frame.get("event_type").and_then(|k| k.as_str()).unwrap_or("");
            let role = frame.pointer("/event/payload/message/role").and_then(|r| r.as_str()).unwrap_or("");
            let sid = frame.get("session_id").and_then(|k| k.as_str()).map(|s| &s[..8.min(s.len())]).unwrap_or("");
            eprintln!("FRAME {kind} {event} {role} {sid}");
            if role == "user" {
                eprintln!("USER MESSAGE {}", frame.pointer("/event/payload/message").map(|m| m.to_string()).unwrap_or_default());
            }
            if event == "session.assistant.completed" {
                break;
            }
        }
    }

    /// Against a real Swarm: SWARM_CONSOLE_TEST_API (its loopback API) and
    /// SWARM_CONSOLE_TEST_TOKEN (a client session). Bootstraps, opens the
    /// stream and expects Swarm's hello.
    #[test]
    #[ignore]
    fn streams_from_a_real_swarm() {
        let api = std::env::var("SWARM_CONSOLE_TEST_API").unwrap();
        let token = std::env::var("SWARM_CONSOLE_TEST_TOKEN").unwrap();
        let client = crate::swarm::client::Client::new(&api, &token);
        let boot = client
            .post("/v3/sync/bootstrap", serde_json::json!({"surface": "desktop", "selector": {"kind": "recent", "global": true, "recent": {"limit": 5}}, "history": {"mode": "none"}, "resources": {"current_run_state": true}, "include_active": true}))
            .unwrap();
        let cursor = boot.get("snapshot_endpoint_cursor").and_then(|c| c.as_str()).unwrap().to_string();
        let mut stream = super::Stream::open(&api, &token, &cursor).unwrap();
        let mut resume = boot.get("realtime").and_then(|r| r.get("resume")).cloned().unwrap();
        resume["capabilities"] = serde_json::json!(["live_patch_v1"]);
        stream.send(resume).unwrap();
        let first = stream.next().unwrap();
        assert_eq!(first.get("kind").and_then(|k| k.as_str()), Some("hello"), "{first}");
        // The resume is accepted: no auth.denied follows within a keepalive.
        for _ in 0..3 {
            let frame = stream.next().unwrap();
            assert_ne!(frame.get("kind").and_then(|k| k.as_str()), Some("auth.denied"), "{frame}");
            if frame.get("kind").and_then(|k| k.as_str()) == Some("keepalive") {
                break;
            }
        }
    }
}
