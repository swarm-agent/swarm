//! What the Orchestrator made: its media tasks (image, video, sound) and their
//! files, downloaded once into this app's cache so the Media tab and page
//! show them and open them locally. Reading the files over Swarm's HTTP API
//! is not wired yet: settled tasks show nothing until it is.

use std::collections::HashMap;
use std::path::PathBuf;

use serde_json::Value;

use super::client::Client;
use crate::data::{Media, MediaKind};

/// The agents whose tasks make media.
pub fn is_media_agent(agent: &str) -> bool {
    matches!(agent, "image" | "video" | "sound")
}

/// One media task of a session, as the session's children list it.
#[derive(Clone, Debug, PartialEq)]
pub struct MediaTask {
    pub task_id: String,
    pub agent: String,
    pub status: String,
    pub title: String,
}

/// A media task's files as last read, and the status they were read at.
pub struct Read {
    status: String,
    files: Vec<Media>,
}

/// Where downloaded files live.
fn cache_dir() -> PathBuf {
    let base = std::env::var_os("XDG_CACHE_HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".cache")))
        .unwrap_or_else(std::env::temp_dir);
    base.join("swarm-console").join("media")
}

fn kind_of(media_type: &str, agent: &str) -> MediaKind {
    match media_type.split('/').next().unwrap_or("") {
        "image" => MediaKind::Image,
        "video" => MediaKind::Video,
        "audio" => MediaKind::Sound,
        _ => match agent {
            "video" => MediaKind::Video,
            "sound" => MediaKind::Sound,
            _ => MediaKind::Image,
        },
    }
}

fn ext_of(media_type: &str) -> &'static str {
    match media_type {
        "image/png" => "png",
        "image/jpeg" => "jpg",
        "image/webp" => "webp",
        "image/gif" => "gif",
        "video/mp4" => "mp4",
        "video/webm" => "webm",
        "video/quicktime" => "mov",
        "audio/mpeg" | "audio/mp3" => "mp3",
        "audio/wav" | "audio/x-wav" => "wav",
        "audio/ogg" => "ogg",
        "audio/mp4" => "m4a",
        _ => "bin",
    }
}

/// Whether a task is still making its files.
fn settled(status: &str) -> bool {
    matches!(status, "completed" | "needs_review" | "failed" | "rejected" | "integrated" | "cancelled")
}

/// Writes a downloaded file into `path`: beside it and renamed, so a
/// half-written file is never shown.
fn store(path: &PathBuf, data: &[u8]) -> Result<(), String> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    }
    let part = path.with_extension("part");
    std::fs::write(&part, data).map_err(|e| e.to_string())?;
    std::fs::rename(&part, path).map_err(|e| e.to_string())
}

/// A media task's files as Swarm lists them. Not read over HTTP yet: none.
fn files_of(_client: &Client, _project_id: &str, _task_id: &str) -> Option<Value> {
    None
}

/// The media of the sessions' media tasks: settled tasks are read once (and
/// their files downloaded once); running ones show as being made.
pub fn collect(client: &Client, reads: &mut HashMap<String, Read>, tasks: &[(String, String, MediaTask)]) -> Vec<Media> {
    let mut out = Vec::new();
    for (session_id, project_id, task) in tasks {
        if !settled(&task.status) {
            out.push(Media {
                id: task.task_id.clone().into(),
                session_id: session_id.clone().into(),
                session: None,
                file: None,
                dims: None,
                title: task.title.clone().into(),
                kind: kind_of("", &task.agent),
                project: 0,
                machine: 0,
                model: "".into(),
                meta: "being made".into(),
                age: "".into(),
                hue: 0.6,
                pending: true,
            });
            continue;
        }
        let fresh = reads.get(&task.task_id).is_none_or(|r| r.status != task.status);
        if fresh {
            let Some(job) = files_of(client, project_id, &task.task_id) else { continue };
            let mut files = Vec::new();
            for f in job.get("files").and_then(Value::as_array).cloned().unwrap_or_default() {
                let index = f.get("file").and_then(Value::as_u64).unwrap_or(1) as usize;
                let media_type = f.get("media_type").and_then(Value::as_str).unwrap_or("").to_string();
                let size = f.get("size_bytes").and_then(Value::as_u64).unwrap_or(0);
                let sha = f.get("sha256").and_then(Value::as_str).unwrap_or("");
                let failed = f.get("status").and_then(Value::as_str) == Some("failed");
                if failed {
                    // A failed file has nothing to show; the task says why.
                    continue;
                }
                let path = cache_dir().join(format!("{}-{index}-{}.{}", task.task_id, &sha[..sha.len().min(12)], ext_of(&media_type)));
                let file = if size > 0 && !failed && (path.exists() || client.get_bytes(&format!("/v3/projects/{project_id}/media/{}", s(&f, "id"))).ok().is_some_and(|(data, _)| store(&path, &data).is_ok())) {
                    Some(path)
                } else {
                    None
                };
                let model = f.get("model").and_then(Value::as_str).unwrap_or("");
                let mut meta = vec![];
                if !model.is_empty() {
                    meta.push(model.to_string());
                }
                if failed {
                    meta.push(f.get("error").and_then(Value::as_str).unwrap_or("failed").to_string());
                } else if size > 0 {
                    meta.push(format!("{:.1} MB", size as f64 / (1 << 20) as f64));
                }
                let title = f.get("title").and_then(Value::as_str).filter(|t| !t.is_empty()).unwrap_or(&task.title);
                files.push(Media {
                    id: format!("{}#{index}", task.task_id).into(),
                    session_id: session_id.clone().into(),
                    session: None,
                    dims: file.as_ref().and_then(|p| image_dims(p)),
                    file,
                    title: title.to_string().into(),
                    kind: kind_of(&media_type, &task.agent),
                    project: 0,
                    machine: 0,
                    model: model.to_string().into(),
                    meta: meta.join(" · ").into(),
                    age: "".into(),
                    hue: 0.6,
                    pending: false,
                });
            }
            reads.insert(task.task_id.clone(), Read { status: task.status.clone(), files });
        }
        if let Some(read) = reads.get(&task.task_id) {
            out.extend(read.files.iter().cloned());
        }
    }
    out
}

fn s<'a>(v: &'a Value, key: &str) -> &'a str {
    v.get(key).and_then(Value::as_str).unwrap_or("")
}

/// Standard base64 with padding; None when it is not.
#[allow(dead_code)]
fn decode_base64(text: &str) -> Option<Vec<u8>> {
    let value = |c: u8| -> Option<u32> {
        Some(match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            _ => return None,
        } as u32)
    };
    let bytes: Vec<u8> = text.bytes().filter(|b| !b.is_ascii_whitespace()).collect();
    if bytes.len() % 4 != 0 {
        return None;
    }
    let mut out = Vec::with_capacity(bytes.len() / 4 * 3);
    for chunk in bytes.chunks(4) {
        let pad = chunk.iter().rev().take_while(|b| **b == b'=').count();
        let mut n = 0u32;
        for (i, b) in chunk.iter().enumerate() {
            n |= if *b == b'=' { 0 } else { value(*b)? } << (18 - 6 * i);
        }
        out.push((n >> 16) as u8);
        if pad < 2 {
            out.push((n >> 8) as u8);
        }
        if pad < 1 {
            out.push(n as u8);
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64_decodes() {
        assert_eq!(decode_base64("Zm9vYmFy"), Some(b"foobar".to_vec()));
        assert_eq!(decode_base64("Zg=="), Some(b"f".to_vec()));
        assert_eq!(decode_base64("/+4B"), Some(vec![0xff, 0xee, 0x01]));
        assert_eq!(decode_base64("abc"), None);
    }

    #[test]
    fn kinds_and_names() {
        assert_eq!(kind_of("video/mp4", "image"), MediaKind::Video);
        assert_eq!(kind_of("", "sound"), MediaKind::Sound);
        assert_eq!(ext_of("image/jpeg"), "jpg");
        assert!(settled("completed") && !settled("in_progress"));
        assert!(is_media_agent("video") && !is_media_agent("coder"));
    }
}

/// An image's width and height from its header (PNG, GIF, JPEG, WebP).
pub fn image_dims(path: &std::path::Path) -> Option<(u32, u32)> {
    use std::io::Read;
    let mut head = vec![0u8; 64 * 1024];
    let n = std::fs::File::open(path).ok()?.read(&mut head).ok()?;
    let b = &head[..n];
    let be16 = |i: usize| Some(u16::from_be_bytes([*b.get(i)?, *b.get(i + 1)?]) as u32);
    let le16 = |i: usize| Some(u16::from_le_bytes([*b.get(i)?, *b.get(i + 1)?]) as u32);
    if b.starts_with(b"\x89PNG\r\n\x1a\n") {
        let w = u32::from_be_bytes(b.get(16..20)?.try_into().ok()?);
        let h = u32::from_be_bytes(b.get(20..24)?.try_into().ok()?);
        return Some((w, h));
    }
    if b.starts_with(b"GIF8") {
        return Some((le16(6)?, le16(8)?));
    }
    if b.starts_with(b"RIFF") && b.get(8..12) == Some(b"WEBP") {
        return match b.get(12..16)? {
            b"VP8X" => Some((1 + (le16(24)? | (*b.get(26)? as u32) << 16), 1 + (le16(27)? | (*b.get(29)? as u32) << 16))),
            b"VP8L" => {
                let bits = u32::from_le_bytes(b.get(21..25)?.try_into().ok()?);
                Some((1 + (bits & 0x3fff), 1 + ((bits >> 14) & 0x3fff)))
            }
            _ => Some((le16(26)? & 0x3fff, le16(28)? & 0x3fff)),
        };
    }
    if b.starts_with(&[0xff, 0xd8]) {
        // Walk the JPEG markers to the frame header (SOFn).
        let mut i = 2;
        while i + 9 < b.len() {
            if b[i] != 0xff {
                return None;
            }
            let marker = b[i + 1];
            let len = be16(i + 2)? as usize;
            if (0xc0..=0xcf).contains(&marker) && !matches!(marker, 0xc4 | 0xc8 | 0xcc) {
                return Some((be16(i + 7)?, be16(i + 5)?));
            }
            i += 2 + len;
        }
    }
    None
}

/// The largest size within `max` that keeps `dims`' proportions.
pub fn fit(dims: (u32, u32), max: (f32, f32)) -> (f32, f32) {
    let (w, h) = (dims.0.max(1) as f32, dims.1.max(1) as f32);
    let scale = (max.0 / w).min(max.1 / h).min(1.0);
    (w * scale, h * scale)
}

#[cfg(test)]
mod dims_tests {
    use super::*;

    #[test]
    fn reads_png_size_and_fits_it() {
        let dir = std::env::temp_dir().join(format!("swarm-console-dims-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let png = dir.join("a.png");
        let mut bytes = b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR".to_vec();
        bytes.extend(1254u32.to_be_bytes());
        bytes.extend(836u32.to_be_bytes());
        std::fs::write(&png, &bytes).unwrap();
        assert_eq!(image_dims(&png), Some((1254, 836)));
        std::fs::remove_dir_all(&dir).unwrap();
        assert_eq!(fit((1254, 1254), (860., 560.)), (560., 560.));
        assert_eq!(fit((100, 50), (860., 560.)), (100., 50.));
    }
}
