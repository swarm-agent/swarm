//! Media: images, video, sound and designs agents made across the fleet.

use gpui::{ClickEvent, Context, SharedString, div, hsla, prelude::*, px};

use super::{Console, alpha};
use crate::data::MediaKind;

impl Console {
    pub(super) fn render_media(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let hover = t.hover;
        let dark = t.bg.l < 0.5;

        let filters = [None, Some(MediaKind::Image), Some(MediaKind::Video), Some(MediaKind::Sound), Some(MediaKind::Design)]
            .into_iter()
            .map(|kind| {
                let on = self.media_filter == kind;
                let count = self.media.iter().filter(|m| kind.is_none_or(|k| m.kind == k)).count();
                div()
                    .id(SharedString::from(format!("media-filter-{}", kind.map_or("all", |k| k.label()))))
                    .px_2()
                    .py(px(2.))
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(if on { alpha(t.accent, 0.6) } else { t.border })
                    .when(on, |d| d.bg(t.raised))
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .text_color(if on { t.text } else { t.muted })
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.media_filter = kind;
                        cx.notify();
                    }))
                    .child(format!("{} {count}", kind.map_or("all", |k| k.label())))
            });

        let tiles = self
            .media
            .iter()
            .enumerate()
            .filter(|(_, m)| self.media_filter.is_none_or(|k| m.kind == k))
            .map(|(i, m)| {
                let glyph = match m.kind {
                    MediaKind::Image => "▨",
                    MediaKind::Video => "▶",
                    MediaKind::Sound => "♪",
                    MediaKind::Design => "◰",
                };
                let (l1, l2) = if dark { (0.20, 0.30) } else { (0.82, 0.72) };
                let border = t.border_strong;
                div()
                    .id(SharedString::from(format!("media-{i}")))
                    .w(px(228.))
                    .flex_none()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .overflow_hidden()
                    .cursor_pointer()
                    .hover(move |s| s.border_color(border))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.preview = Some(i);
                        cx.notify();
                    }))
                    .child(
                        div()
                            .h(px(132.))
                            .relative()
                            .flex()
                            .items_center()
                            .justify_center()
                            .bg(hsla(m.hue, 0.35, l1, 1.0))
                            .when_some(m.file.clone().filter(|_| m.kind == MediaKind::Image), |d, path| {
                                d.child(gpui::img(path).absolute().top_0().left_0().size_full().object_fit(gpui::ObjectFit::Cover))
                            })
                            .child(
                                div()
                                    .absolute()
                                    .top_0()
                                    .left_0()
                                    .right_0()
                                    .h(px(66.))
                                    .bg(hsla(m.hue, 0.40, l2, 0.6)),
                            )
                            .child(
                                div()
                                    .text_size(px(26.))
                                    .text_color(hsla(m.hue, 0.25, if dark { 0.75 } else { 0.30 }, 0.9))
                                    .child(glyph),
                            )
                            .child(
                                div()
                                    .absolute()
                                    .top(px(8.))
                                    .left(px(8.))
                                    .px(px(5.))
                                    .rounded(px(t.radius))
                                    .bg(alpha(t.bg, 0.7))
                                    .text_size(px(10.5))
                                    .text_color(t.muted)
                                    .child(m.kind.label()),
                            )
                            .when(m.pending, |d| {
                                d.child(
                                    div()
                                        .absolute()
                                        .bottom(px(8.))
                                        .left(px(8.))
                                        .right(px(8.))
                                        .flex()
                                        .items_center()
                                        .gap_2()
                                        .text_size(px(10.5))
                                        .text_color(t.text)
                                        .child("rendering")
                                        .child(self.bar(0.6, t.accent, 120.)),
                                )
                            }),
                    )
                    .child(
                        div()
                            .p_2()
                            .flex()
                            .flex_col()
                            .gap(px(3.))
                            .child(div().truncate().text_color(t.text).child(m.title.clone()))
                            .child(
                                div()
                                    .truncate()
                                    .text_size(px(10.5))
                                    .text_color(t.faint)
                                    .child(m.meta.clone()),
                            )
                            .child(
                                div()
                                    .flex()
                                    .gap_1()
                                    .text_size(px(10.5))
                                    .text_color(t.faint)
                                    .child(self.projects[m.project].name.clone())
                                    .child("·")
                                    .child(self.machines[m.machine].name.clone())
                                    .child("·")
                                    .child(m.model.clone())
                                    .child(div().flex_1())
                                    .child(m.age.clone()),
                            ),
                    )
            });

        div()
            .id("media-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_4()
            .child(
                div()
                    .flex()
                    .items_end()
                    .justify_between()
                    .child(
                        div()
                            .flex()
                            .flex_col()
                            .gap_1()
                            .child(div().text_size(px(16.)).text_color(t.text).child("Media"))
                            .child(div().text_color(t.faint).child("What agents made on every machine, newest first.")),
                    )
                    .child(self.button("media-new", "+ ask for media", true)),
            )
            .child(div().flex().gap_1().children(filters))
            .child(div().flex().flex_wrap().gap_3().children(tiles))
    }
}

impl Console {
    /// One piece of media as a tile: the image itself once downloaded, else a
    /// tinted placeholder with its kind; clicking opens the preview.
    pub(super) fn media_thumb(&self, i: usize, w: f32, h: f32, key: String, cx: &mut Context<Self>) -> gpui::AnyElement {
        let t = &self.theme;
        let m = &self.media[i];
        let dark = t.bg.l < 0.5;
        let border = t.border_strong;
        let glyph = match m.kind {
            MediaKind::Image => "▣",
            MediaKind::Video => "▶",
            MediaKind::Sound => "♪",
            MediaKind::Design => "◰",
        };
        let picture = match (&m.file, m.kind) {
            (Some(path), MediaKind::Image) => gpui::img(path.clone()).w(px(w)).h(px(h)).object_fit(gpui::ObjectFit::Cover).into_any_element(),
            _ => div()
                .size_full()
                .flex()
                .items_center()
                .justify_center()
                .bg(hsla(m.hue, 0.35, if dark { 0.22 } else { 0.80 }, 1.0))
                .text_size(px(22.))
                .text_color(hsla(m.hue, 0.25, if dark { 0.75 } else { 0.30 }, 0.9))
                .child(if m.pending { "…" } else { glyph })
                .into_any_element(),
        };
        div()
            .id(SharedString::from(format!("thumb-{key}-{}", if m.id.is_empty() { i.to_string() } else { m.id.to_string() })))
            .w(px(w))
            .flex_none()
            .flex()
            .flex_col()
            .rounded(px(t.radius + 1.))
            .border_1()
            .border_color(t.border)
            .bg(t.surface)
            .overflow_hidden()
            .cursor_pointer()
            .hover(move |s| s.border_color(border))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.preview = Some(i);
                cx.notify();
            }))
            .child(div().h(px(h)).w_full().overflow_hidden().child(picture))
            .child(div().px_2().py(px(3.)).text_size(px(10.5)).truncate().text_color(t.muted).child(m.title.clone()))
            .into_any_element()
    }

    /// The preview over the window: the image large, or the kind of a video or
    /// sound, with open (your viewer or player), save and close.
    pub(super) fn render_preview(&self, cx: &mut Context<Self>) -> Option<gpui::AnyElement> {
        let i = self.preview?;
        let m = self.media.get(i)?;
        let t = &self.theme;
        let body = match (&m.file, m.kind) {
            (Some(path), MediaKind::Image) => {
                // Fitted by hand: the image keeps its proportions within 860×560.
                let (w, h) = crate::swarm::media::fit(m.dims.unwrap_or((560, 560)), (860., 560.));
                gpui::img(path.clone()).w(px(w)).h(px(h)).into_any_element()
            }
            _ => div()
                .w(px(420.))
                .h(px(240.))
                .flex()
                .flex_col()
                .items_center()
                .justify_center()
                .gap_2()
                .rounded(px(t.radius + 2.))
                .bg(hsla(m.hue, 0.35, 0.22, 1.0))
                .text_color(t.text)
                .child(div().text_size(px(40.)).child(match m.kind {
                    MediaKind::Video => "▶",
                    MediaKind::Sound => "♪",
                    _ => "▣",
                }))
                .child(div().text_size(px(12.)).text_color(t.muted).child(if m.file.is_some() {
                    "opens in your player"
                } else if m.pending {
                    "still being made"
                } else {
                    "downloading…"
                }))
                .into_any_element(),
        };
        let ready = m.file.is_some();
        Some(
            div()
                .id("media-preview")
                .absolute()
                .inset_0()
                .flex()
                .items_center()
                .justify_center()
                .bg(gpui::black().opacity(0.72))
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.preview = None;
                    cx.notify();
                }))
                .child(
                    div()
                        .id("media-preview-card")
                        .flex()
                        .flex_col()
                        .items_center()
                        .gap_3()
                        .p_4()
                        .rounded(px(t.radius + 4.))
                        .bg(t.panel)
                        .border_1()
                        .border_color(t.border_strong)
                        // Clicks on the card stay on the card.
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(body)
                        .child(div().text_color(t.text).child(m.title.clone()))
                        .child(div().text_size(px(11.)).text_color(t.faint).child(m.meta.clone()))
                        .child(
                            div()
                                .flex()
                                .gap_2()
                                .when(ready, |d| {
                                    d.child(self.button("preview-open", "open", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_media(i, cx))))
                                        .child(self.button("preview-save", "save to Downloads", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.save_media(i, cx))))
                                })
                                .child(self.button("preview-close", "close", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.preview = None;
                                    cx.notify();
                                }))),
                        ),
                )
                .into_any_element(),
        )
    }

    /// Opens a downloaded file in the program your desktop picks for it.
    fn open_media(&mut self, i: usize, cx: &mut Context<Self>) {
        let Some(path) = self.media.get(i).and_then(|m| m.file.clone()) else { return };
        self.notice = Some(match std::process::Command::new("xdg-open").arg(&path).spawn() {
            Ok(_) => ("Opened.".into(), false),
            Err(e) => (format!("Could not open it: {e}").into(), true),
        });
        cx.notify();
    }

    /// Copies a downloaded file into ~/Downloads under its title.
    fn save_media(&mut self, i: usize, cx: &mut Context<Self>) {
        let Some(m) = self.media.get(i) else { return };
        let Some(path) = m.file.clone() else { return };
        let home = std::env::var_os("HOME").map(std::path::PathBuf::from).unwrap_or_default();
        let dir = home.join("Downloads");
        let ext = path.extension().and_then(|e| e.to_str()).unwrap_or("bin");
        let stem: String = m.title.chars().map(|c| if c.is_alphanumeric() || c == '-' || c == '_' { c } else { '-' }).collect();
        let stem = if stem.trim_matches('-').is_empty() { "swarm-media".to_string() } else { stem.trim_matches('-').to_string() };
        let mut target = dir.join(format!("{stem}.{ext}"));
        let mut n = 2;
        while target.exists() {
            target = dir.join(format!("{stem}-{n}.{ext}"));
            n += 1;
        }
        self.notice = Some(match std::fs::create_dir_all(&dir).and_then(|_| std::fs::copy(&path, &target)) {
            Ok(_) => (format!("Saved to {}", target.display()).into(), false),
            Err(e) => (format!("Could not save it: {e}").into(), true),
        });
        cx.notify();
    }
}
