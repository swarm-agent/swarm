//! What the Orchestrator made: its media tasks (image, video, sound) and their
//! files, downloaded once into this app's cache so the Media tab and page
//! show them and open them locally, from each task's deliverables on Swarm's
//! HTTP API (`media_url`).

use std::collections::HashMap;
use std::path::PathBuf;

use serde_json::Value;

use super::client::Client;
use crate::data::{Media, MediaKind};

/// The agents whose tasks make media.
pub fn is_media_agent(agent: &str) -> bool {
    matches!(agent, "image" | "video" | "sound")
}

/// One media task of a project (agent image, video or sound).
#[derive(Clone, Debug, PartialEq)]
pub struct MediaTask {
    pub task_id: String,
    pub agent: String,
    pub status: String,
    pub title: String,
    pub created_at: u64,
    /// Its deliverables as the task list gives them (`media_url` per file).
    pub deliverables: Vec<Value>,
}

/// A media task's files as last read, and the status they were read at.
pub struct Read {
    status: String,
    files: Vec<Media>,
    /// A file is still being downloaded: read the task again next time.
    missing: bool,
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


/// How long ago, shortly: "now", "4m", "3h", "2d".
fn age(created_at: u64) -> String {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0);
    match now.saturating_sub(created_at) / 60_000 {
        0 => "now".into(),
        m if m < 60 => format!("{m}m"),
        m if m < 60 * 24 => format!("{}h", m / 60),
        m => format!("{}d", m / (60 * 24)),
    }
}

/// The sha256 a deliverable's media_url names, so a changed file gets a new
/// cache name.
fn sha_of(url: &str) -> &str {
    url.split(['?', '&']).find_map(|part| part.strip_prefix("sha256=")).unwrap_or("")
}

/// Downloads one file `collect` asked for into the cache as STEM.EXT (EXT from
/// the type Swarm serves it as). True when the file is there.
pub fn download(client: &Client, url: &str, stem: &str) -> bool {
    match client.get_bytes(url) {
        Ok((data, mime)) => {
            let mime = mime.split(';').next().unwrap_or("").trim().to_string();
            store(&cache_dir().join(format!("{stem}.{}", ext_of(&mime))), &data).is_ok()
        }
        Err(_) => false,
    }
}

/// The media of the media tasks, newest first: settled tasks are read once
/// and the files they lack go into `wanted` for `download`; running ones show
/// as being made.
pub fn collect(reads: &mut HashMap<String, Read>, tasks: &[(String, String, MediaTask)], wanted: &mut Vec<(String, String)>) -> Vec<Media> {
    let mut out = Vec::new();
    for (session_id, _project_id, task) in tasks {
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
                age: age(task.created_at).into(),
                hue: 0.6,
                pending: true,
            });
            continue;
        }
        let fresh = reads.get(&task.task_id).is_none_or(|r| r.status != task.status || r.missing);
        if fresh {
            let deliverables = &task.deliverables;
            let mut files = Vec::new();
            let mut missing = false;
            for (n, d) in deliverables.iter().enumerate() {
                let url = s(d, "media_url");
                if s(d, "status") != "ready" || !url.starts_with("/v3/") {
                    // A failed file has nothing to show; the task says why.
                    continue;
                }
                let sha = sha_of(url);
                let key = if sha.is_empty() { s(d, "id") } else { &sha[..sha.len().min(12)] };
                let cached = std::fs::read_dir(cache_dir()).ok().and_then(|dir| {
                    let prefix = format!("{}-{}-{key}.", task.task_id, n + 1);
                    dir.filter_map(Result::ok).map(|e| e.path()).find(|p| p.file_name().and_then(|f| f.to_str()).is_some_and(|f| f.starts_with(&prefix) && !f.ends_with(".part")))
                });
                let (file, media_type) = match cached {
                    Some(path) => (Some(path), String::new()),
                    None => {
                        // Fetched off this thread; the next read finds it cached.
                        wanted.push((url.to_string(), format!("{}-{}-{key}", task.task_id, n + 1)));
                        missing = true;
                        (None, String::new())
                    }
                };
                let media_type = if media_type.is_empty() {
                    match s(d, "kind") {
                        "video" => "video/".to_string(),
                        "audio" | "sound" | "music" | "speech" => "audio/".to_string(),
                        _ => "image/".to_string(),
                    }
                } else {
                    media_type
                };
                let model = s(d, "model");
                let mut meta = vec![];
                if !model.is_empty() {
                    meta.push(model.to_string());
                }
                if let Some(cost) = d.get("cost_usd").and_then(Value::as_f64) {
                    meta.push(format!("${cost:.2}"));
                }
                if let Some(size) = file.as_ref().and_then(|p| std::fs::metadata(p).ok()).map(|m| m.len()) {
                    meta.push(format!("{:.1} MB", size as f64 / (1 << 20) as f64));
                }
                files.push(Media {
                    id: format!("{}#{}", task.task_id, n + 1).into(),
                    session_id: session_id.clone().into(),
                    session: None,
                    dims: file.as_ref().and_then(|p| image_dims(p)),
                    file,
                    title: task.title.clone().into(),
                    kind: kind_of(&media_type, &task.agent),
                    project: 0,
                    machine: 0,
                    model: model.to_string().into(),
                    meta: meta.join(" · ").into(),
                    age: age(task.created_at).into(),
                    hue: 0.6,
                    pending: false,
                });
            }
            reads.insert(task.task_id.clone(), Read { status: task.status.clone(), files, missing });
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

    // A settled task's ready deliverable is listed at once and its file is
    // asked of the download thread; a failed one is left out; a running task
    // shows as being made.
    #[test]
    fn deliverables_become_media() {
        let url = "/v3/projects/p/tasks/t/deliverables/d1?field=media&sha256=0a5b47adfba7802c16f2";
        assert_eq!(sha_of(url), "0a5b47adfba7802c16f2");
        let task = |id: &str, status: &str| MediaTask {
            task_id: id.into(),
            agent: "image".into(),
            status: status.into(),
            title: "bee".into(),
            created_at: 0,
            deliverables: vec![
                serde_json::json!({"status": "ready", "media_url": url, "kind": "image", "model": "nano", "cost_usd": 0.0415}),
                serde_json::json!({"status": "failed", "media_url": "", "kind": "image"}),
            ],
        };
        let tasks = vec![
            ("s".to_string(), "p".to_string(), task("task_test-not-cached", "completed")),
            ("s".to_string(), "p".to_string(), task("task_test-running", "in_progress")),
        ];
        let (mut reads, mut wanted) = (HashMap::new(), Vec::new());
        let media = collect(&mut reads, &tasks, &mut wanted);
        assert_eq!(media.len(), 2);
        assert!(media[0].file.is_none() && !media[0].pending);
        assert_eq!(media[0].meta.as_ref(), "nano · $0.04");
        assert!(media[1].pending);
        assert_eq!(wanted, vec![(url.to_string(), "task_test-not-cached-1-0a5b47adfba7".to_string())]);
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
