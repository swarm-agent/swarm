//! Files sent with a message: paste an image (ctrl-v), paste files copied in
//! a file manager, or drop files on the message box. They wait as chips above
//! it and go with the next message. They are copied to the Swarm (the chips
//! say which): each is uploaded to the session there, where the Orchestrator
//! sees images and can use any of them in manage_media (to edit, animate or
//! transcribe); a text or code file goes into the message itself.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use gpui::{ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::data::Attachment;
use crate::icons::Icon;

/// Swarm's limits: 8 files a message, 20 MiB each.
const MAX_FILES: usize = 8;
const MAX_BYTES: u64 = 20 << 20;

/// A file's type by its extension (Swarm also checks the bytes).
pub(super) fn mime_for(path: &Path) -> &'static str {
    match path.extension().and_then(|e| e.to_str()).map(str::to_ascii_lowercase).as_deref() {
        Some("png") => "image/png",
        Some("jpg" | "jpeg") => "image/jpeg",
        Some("gif") => "image/gif",
        Some("webp") => "image/webp",
        Some("mp4") => "video/mp4",
        Some("mov") => "video/quicktime",
        Some("webm") => "video/webm",
        Some("mp3") => "audio/mpeg",
        Some("wav") => "audio/wav",
        Some("m4a") => "audio/mp4",
        Some("ogg") => "audio/ogg",
        Some("pdf") => "application/pdf",
        Some("txt" | "md") => "text/plain",
        _ => "application/octet-stream",
    }
}

fn size(bytes: usize) -> String {
    match bytes {
        b if b < 1024 => format!("{b} B"),
        b if b < 1 << 20 => format!("{} KB", b / 1024),
        b => format!("{:.1} MB", b as f64 / (1 << 20) as f64),
    }
}

impl Console {
    fn add_attachment(&mut self, pid: usize, attachment: Attachment) -> bool {
        let files = self.attachments.entry(pid).or_default();
        if files.len() >= MAX_FILES {
            self.notice = Some((format!("At most {MAX_FILES} files per message.").into(), true));
            return false;
        }
        files.push(attachment);
        true
    }

    /// A pasted image.
    pub(super) fn attach_image(&mut self, pid: usize, image: &gpui::Image, cx: &mut Context<Self>) {
        let mime = image.format().mime_type();
        let ext = mime.rsplit('/').next().unwrap_or("png").replace("jpeg", "jpg");
        let n = self.attachments.get(&pid).map_or(0, Vec::len) + 1;
        if image.bytes().len() as u64 > MAX_BYTES {
            self.notice = Some(("That image is over 20 MB.".into(), true));
        } else {
            self.add_attachment(pid, Attachment { name: format!("pasted-{n}.{ext}"), mime: mime.to_string(), data: Arc::new(image.bytes().to_vec()) });
        }
        cx.notify();
    }

    /// Files pasted from a file manager or dropped on the message box.
    pub(super) fn attach_files(&mut self, pid: usize, paths: &[PathBuf], cx: &mut Context<Self>) {
        for path in paths {
            let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| path.display().to_string());
            match std::fs::metadata(path) {
                Ok(meta) if meta.is_dir() => self.notice = Some((format!("{name} is a folder; attach files.").into(), true)),
                Ok(meta) if meta.len() > MAX_BYTES => self.notice = Some((format!("{name} is over 20 MB.").into(), true)),
                Ok(_) => match std::fs::read(path) {
                    Ok(data) => {
                        if !self.add_attachment(pid, Attachment { name, mime: mime_for(path).to_string(), data: Arc::new(data) }) {
                            break;
                        }
                    }
                    Err(e) => self.notice = Some((format!("Could not read {name}: {e}").into(), true)),
                },
                Err(e) => self.notice = Some((format!("Could not read {name}: {e}").into(), true)),
            }
        }
        cx.notify();
    }

    /// The files waiting in a message box, each with its remove button.
    pub(super) fn render_attachments(&self, pid: usize, cx: &mut Context<Self>) -> Option<Div> {
        let files = self.attachments.get(&pid).filter(|f| !f.is_empty())?;
        let t = &self.theme;
        let hover = t.hover;
        Some(
            div()
                .flex()
                .flex_wrap()
                .items_center()
                .gap_1()
                .pb_1()
                // They are copied to the Swarm with the message; agents never read your disk.
                .child(div().text_size(px(11.)).text_color(t.faint).child(format!("→ {}", self.machines.first().map_or("Swarm".into(), |m| m.name.to_string()))))
                .children(files.iter().enumerate().map(|(i, f)| {
                div()
                    .flex()
                    .items_center()
                    .gap_1()
                    .pl_2()
                    .pr_1()
                    .py(px(1.))
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(alpha(t.accent, 0.4))
                    .bg(t.raised)
                    .text_size(px(11.))
                    .child(div().max_w(px(220.)).truncate().text_color(t.text).child(f.name.clone()))
                    .child(div().text_color(t.faint).child(size(f.data.len())))
                    .child(
                        div()
                            .id(SharedString::from(format!("unattach-{pid}-{i}")))
                            .px_1()
                            .rounded(px(t.radius))
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .text_color(t.faint)
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                if let Some(files) = this.attachments.get_mut(&pid) {
                                    if i < files.len() {
                                        files.remove(i);
                                    }
                                }
                                cx.notify();
                            }))
                            .child(self.icon(Icon::Close, 10., t.faint)),
                    )
            })),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn types_and_sizes() {
        assert_eq!(mime_for(Path::new("/a/B.PNG")), "image/png");
        assert_eq!(mime_for(Path::new("clip.mov")), "video/quicktime");
        assert_eq!(mime_for(Path::new("x")), "application/octet-stream");
        assert_eq!((size(512), size(2048), size(3 << 20)), ("512 B".into(), "2 KB".into(), "3.0 MB".into()));
    }
}
