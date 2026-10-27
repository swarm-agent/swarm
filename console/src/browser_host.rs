//! `swarm-console --browser-host`: the native messaging host Chrome starts
//! for the Swarm extension. It carries messages between Chrome (stdin and
//! stdout, each message a 4-byte length then JSON, Chrome's framing) and the
//! running console (its socket, one JSON object per line), and does nothing
//! else. Without a console running it tells the extension so and exits.

use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::UnixStream;

use serde_json::json;

pub fn run() -> i32 {
    let path = crate::browser::socket_path();
    let socket = match UnixStream::connect(&path) {
        Ok(s) => s,
        Err(e) => {
            let _ = write_frame(&mut std::io::stdout(), &json!({"event": "host_error", "error": format!("Swarm Console is not running ({}: {e}).", path.display())}).to_string());
            return 1;
        }
    };
    let Ok(mut to_console) = socket.try_clone() else { return 1 };
    // Chrome → console.
    let up = std::thread::spawn(move || {
        let mut stdin = std::io::stdin().lock();
        loop {
            let Some(text) = read_frame(&mut stdin) else { break };
            let mut line = text;
            line.push('\n');
            if to_console.write_all(line.as_bytes()).is_err() {
                break;
            }
        }
        let _ = to_console.shutdown(std::net::Shutdown::Both);
    });
    // Console → Chrome.
    let reader = BufReader::new(socket);
    let mut stdout = std::io::stdout().lock();
    for line in reader.lines() {
        let Ok(line) = line else { break };
        if line.trim().is_empty() {
            continue;
        }
        if write_frame(&mut stdout, line.trim()).is_err() {
            break;
        }
    }
    drop(stdout);
    let _ = up.join();
    0
}

/// One message from Chrome: a native-endian u32 length, then that many
/// bytes of JSON.
fn read_frame(input: &mut impl Read) -> Option<String> {
    let mut len = [0u8; 4];
    input.read_exact(&mut len).ok()?;
    let len = u32::from_ne_bytes(len) as usize;
    let mut body = vec![0u8; len];
    input.read_exact(&mut body).ok()?;
    String::from_utf8(body).ok()
}

/// One message to Chrome, framed the same way (Chrome takes up to 1MB).
fn write_frame(output: &mut impl Write, text: &str) -> std::io::Result<()> {
    output.write_all(&(text.len() as u32).to_ne_bytes())?;
    output.write_all(text.as_bytes())?;
    output.flush()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frames_round_trip() {
        let mut buf = Vec::new();
        write_frame(&mut buf, r#"{"a":1}"#).unwrap();
        assert_eq!(&buf[..4], &7u32.to_ne_bytes());
        let mut input = std::io::Cursor::new(buf);
        assert_eq!(read_frame(&mut input).unwrap(), r#"{"a":1}"#);
        assert!(read_frame(&mut input).is_none());
    }
}
