//! A single-line text field, adapted from GPUI's `examples/input.rs` (Zed's gpui
//! 0.2.2, Apache-2.0). Changes: public API, theme colors, no example scaffolding.

use std::ops::Range;

use gpui::{
    App, Bounds, ClipboardItem, Context, CursorStyle, ElementId, ElementInputHandler, Entity,
    EntityInputHandler, FocusHandle, Focusable, GlobalElementId, Hsla, LayoutId, MouseButton,
    MouseDownEvent, MouseMoveEvent, MouseUpEvent, PaintQuad, Pixels, Point, SharedString, Style,
    TextRun, TextStyle, UTF16Selection, UnderlineStyle, Window, WrappedLine, actions, div, fill, point, prelude::*, px,
    relative, size,
};
use unicode_segmentation::*;

actions!(
    text_input,
    [
        Backspace,
        Delete,
        Left,
        Right,
        SelectLeft,
        SelectRight,
        SelectAll,
        Home,
        End,
        ShowCharacterPalette,
        Paste,
        Cut,
        Copy,
        Newline,
        Up,
        Down,
    ]
);

/// Colors the field paints with; set from the theme.
#[derive(Clone, Copy)]
pub struct InputColors {
    pub placeholder: Hsla,
    pub cursor: Hsla,
    pub selection: Hsla,
}

pub struct TextInput {
    focus_handle: FocusHandle,
    colors: InputColors,
    /// Selectable and copyable, never edited (codes, links).
    read_only: bool,
    /// Drawn as asterisks (a key being typed).
    secret: bool,
    /// Never wraps (a name).
    one_line: bool,
    line_height: f32,
    content: SharedString,
    placeholder: SharedString,
    selected_range: Range<usize>,
    selection_reversed: bool,
    marked_range: Option<Range<usize>>,
    last_laid: Option<Laid>,
    last_bounds: Option<Bounds<Pixels>>,
    is_selecting: bool,
}

impl TextInput {
    pub fn new(placeholder: impl Into<SharedString>, colors: InputColors, cx: &mut Context<Self>) -> Self {
        TextInput {
            focus_handle: cx.focus_handle(),
            colors,
            read_only: false,
            secret: false,
            one_line: false,
            line_height: 20.,
            content: "".into(),
            placeholder: placeholder.into(),
            selected_range: 0..0,
            selection_reversed: false,
            marked_range: None,
            last_laid: None,
            last_bounds: None,
            is_selecting: false,
        }
    }

    pub fn text(&self) -> String {
        self.content.to_string()
    }

    pub fn set_text(&mut self, text: impl Into<SharedString>, cx: &mut Context<Self>) {
        self.content = text.into();
        let end = self.content.len();
        self.selected_range = end..end;
        self.marked_range = None;
        cx.notify();
    }

    /// Empties the field and hands over what was typed (a password on its
    /// way to sudo): the field keeps nothing of it.
    pub fn take_text(&mut self, cx: &mut Context<Self>) -> String {
        let taken = std::mem::replace(&mut self.content, "".into());
        self.selected_range = 0..0;
        self.marked_range = None;
        cx.notify();
        taken.to_string()
    }

    pub fn is_empty(&self) -> bool {
        self.content.is_empty()
    }

    pub fn set_colors(&mut self, colors: InputColors) {
        self.colors = colors;
    }

    /// A field that stays on one line, however long its text.
    pub fn one_line(mut self) -> Self {
        self.one_line = true;
        self
    }

    /// A field whose text is drawn as asterisks: what is typed never shows.
    pub fn secret(mut self) -> Self {
        self.secret = true;
        self
    }

    /// A field you can select and copy from but not edit.
    pub fn read_only(mut self, line_height: f32) -> Self {
        self.read_only = true;
        self.line_height = line_height;
        self
    }

    fn left(&mut self, _: &Left, _: &mut Window, cx: &mut Context<Self>) {
        if self.selected_range.is_empty() {
            self.move_to(self.previous_boundary(self.cursor_offset()), cx);
        } else {
            self.move_to(self.selected_range.start, cx)
        }
    }

    fn right(&mut self, _: &Right, _: &mut Window, cx: &mut Context<Self>) {
        if self.selected_range.is_empty() {
            self.move_to(self.next_boundary(self.selected_range.end), cx);
        } else {
            self.move_to(self.selected_range.end, cx)
        }
    }

    fn select_left(&mut self, _: &SelectLeft, _: &mut Window, cx: &mut Context<Self>) {
        self.select_to(self.previous_boundary(self.cursor_offset()), cx);
    }

    fn select_right(&mut self, _: &SelectRight, _: &mut Window, cx: &mut Context<Self>) {
        self.select_to(self.next_boundary(self.cursor_offset()), cx);
    }

    fn select_all(&mut self, _: &SelectAll, _: &mut Window, cx: &mut Context<Self>) {
        self.move_to(0, cx);
        self.select_to(self.content.len(), cx)
    }

    fn home(&mut self, _: &Home, _: &mut Window, cx: &mut Context<Self>) {
        self.move_to(0, cx);
    }

    fn end(&mut self, _: &End, _: &mut Window, cx: &mut Context<Self>) {
        self.move_to(self.content.len(), cx);
    }

    fn backspace(&mut self, _: &Backspace, window: &mut Window, cx: &mut Context<Self>) {
        if self.selected_range.is_empty() {
            self.select_to(self.previous_boundary(self.cursor_offset()), cx)
        }
        self.replace_text_in_range(None, "", window, cx)
    }

    fn delete(&mut self, _: &Delete, window: &mut Window, cx: &mut Context<Self>) {
        if self.selected_range.is_empty() {
            self.select_to(self.next_boundary(self.cursor_offset()), cx)
        }
        self.replace_text_in_range(None, "", window, cx)
    }

    fn on_mouse_down(
        &mut self,
        event: &MouseDownEvent,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.is_selecting = true;

        // Double-click selects the whole field (a code or a link in one go).
        if event.click_count >= 2 {
            self.selected_range = 0..self.content.len();
            self.selection_reversed = false;
            self.publish_primary(cx);
            cx.notify();
            return;
        }
        if event.modifiers.shift {
            self.select_to(self.index_for_mouse_position(event.position), cx);
        } else {
            self.move_to(self.index_for_mouse_position(event.position), cx)
        }
    }

    fn on_mouse_up(&mut self, _: &MouseUpEvent, _window: &mut Window, cx: &mut Context<Self>) {
        self.is_selecting = false;
        self.publish_primary(cx);
    }

    /// On Linux, selected text is what middle-click pastes.
    fn publish_primary(&self, cx: &mut Context<Self>) {
        // A secret (a key, a password) never reaches a clipboard.
        if !self.selected_range.is_empty() && !self.secret {
            cx.write_to_primary(ClipboardItem::new_string(self.content[self.selected_range.clone()].to_string()));
        }
    }

    fn on_mouse_move(&mut self, event: &MouseMoveEvent, _: &mut Window, cx: &mut Context<Self>) {
        if self.is_selecting {
            self.select_to(self.index_for_mouse_position(event.position), cx);
        }
    }

    fn show_character_palette(
        &mut self,
        _: &ShowCharacterPalette,
        window: &mut Window,
        _: &mut Context<Self>,
    ) {
        window.show_character_palette();
    }

    fn paste(&mut self, _: &Paste, window: &mut Window, cx: &mut Context<Self>) {
        let Some(item) = cx.read_from_clipboard() else { return };
        // An image, or files copied in a file manager, are for whoever owns the
        // field (the composer attaches them), not text.
        if let Some(image) = item.entries().iter().find_map(|e| match e {
            gpui::ClipboardEntry::Image(image) => Some(image.clone()),
            _ => None,
        }) {
            cx.emit(Pasted::Image(image));
            return;
        }
        if let Some(text) = item.text() {
            if let Some(files) = file_uris(&text) {
                cx.emit(Pasted::Files(files));
                return;
            }
            self.replace_text_in_range(None, &text.replace("\n", " "), window, cx);
        }
    }

    fn copy(&mut self, _: &Copy, _: &mut Window, cx: &mut Context<Self>) {
        if !self.selected_range.is_empty() && !self.secret {
            cx.write_to_clipboard(ClipboardItem::new_string(
                self.content[self.selected_range.clone()].to_string(),
            ));
        }
    }
    fn cut(&mut self, _: &Cut, window: &mut Window, cx: &mut Context<Self>) {
        if !self.selected_range.is_empty() && !self.secret {
            cx.write_to_clipboard(ClipboardItem::new_string(
                self.content[self.selected_range.clone()].to_string(),
            ));
            self.replace_text_in_range(None, "", window, cx)
        }
    }

    fn move_to(&mut self, offset: usize, cx: &mut Context<Self>) {
        self.selected_range = offset..offset;
        cx.notify()
    }

    fn cursor_offset(&self) -> usize {
        if self.selection_reversed {
            self.selected_range.start
        } else {
            self.selected_range.end
        }
    }

    fn index_for_mouse_position(&self, position: Point<Pixels>) -> usize {
        if self.content.is_empty() {
            return 0;
        }
        let (Some(bounds), Some(laid)) = (self.last_bounds.as_ref(), self.last_laid.as_ref()) else {
            return 0;
        };
        laid.index(point(position.x - bounds.left(), position.y - bounds.top()), self.content.len())
    }

    /// A line break in the text (shift-enter); enter alone is the composer's.
    fn newline(&mut self, _: &Newline, window: &mut Window, cx: &mut Context<Self>) {
        if self.read_only {
            return;
        }
        let range = self.selected_range.clone();
        self.replace_text_in_range(Some(self.range_to_utf16(&range)), "\n", window, cx);
    }

    /// The cursor one row up or down, keeping its x.
    fn vertical(&mut self, rows: f32, cx: &mut Context<Self>) {
        let Some(laid) = self.last_laid.as_ref() else { return };
        let p = laid.position(self.cursor_offset());
        let target = point(p.x, p.y + laid.line_height * rows + laid.line_height / 2.);
        let at = if target.y < px(0.) { 0 } else { laid.index(target, self.content.len()) };
        self.move_to(at, cx);
    }

    fn up(&mut self, _: &Up, _: &mut Window, cx: &mut Context<Self>) {
        self.vertical(-1., cx);
    }

    fn down(&mut self, _: &Down, _: &mut Window, cx: &mut Context<Self>) {
        self.vertical(1., cx);
    }

    fn select_to(&mut self, offset: usize, cx: &mut Context<Self>) {
        if self.selection_reversed {
            self.selected_range.start = offset
        } else {
            self.selected_range.end = offset
        };
        if self.selected_range.end < self.selected_range.start {
            self.selection_reversed = !self.selection_reversed;
            self.selected_range = self.selected_range.end..self.selected_range.start;
        }
        cx.notify()
    }

    fn offset_from_utf16(&self, offset: usize) -> usize {
        let mut utf8_offset = 0;
        let mut utf16_count = 0;

        for ch in self.content.chars() {
            if utf16_count >= offset {
                break;
            }
            utf16_count += ch.len_utf16();
            utf8_offset += ch.len_utf8();
        }

        utf8_offset
    }

    fn offset_to_utf16(&self, offset: usize) -> usize {
        let mut utf16_offset = 0;
        let mut utf8_count = 0;

        for ch in self.content.chars() {
            if utf8_count >= offset {
                break;
            }
            utf8_count += ch.len_utf8();
            utf16_offset += ch.len_utf16();
        }

        utf16_offset
    }

    fn range_to_utf16(&self, range: &Range<usize>) -> Range<usize> {
        self.offset_to_utf16(range.start)..self.offset_to_utf16(range.end)
    }

    fn range_from_utf16(&self, range_utf16: &Range<usize>) -> Range<usize> {
        self.offset_from_utf16(range_utf16.start)..self.offset_from_utf16(range_utf16.end)
    }

    fn previous_boundary(&self, offset: usize) -> usize {
        self.content
            .grapheme_indices(true)
            .rev()
            .find_map(|(idx, _)| (idx < offset).then_some(idx))
            .unwrap_or(0)
    }

    fn next_boundary(&self, offset: usize) -> usize {
        self.content
            .grapheme_indices(true)
            .find_map(|(idx, _)| (idx > offset).then_some(idx))
            .unwrap_or(self.content.len())
    }

}

impl EntityInputHandler for TextInput {
    fn text_for_range(
        &mut self,
        range_utf16: Range<usize>,
        actual_range: &mut Option<Range<usize>>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<String> {
        let range = self.range_from_utf16(&range_utf16);
        actual_range.replace(self.range_to_utf16(&range));
        Some(self.content[range].to_string())
    }

    fn selected_text_range(
        &mut self,
        _ignore_disabled_input: bool,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<UTF16Selection> {
        Some(UTF16Selection {
            range: self.range_to_utf16(&self.selected_range),
            reversed: self.selection_reversed,
        })
    }

    fn marked_text_range(
        &self,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<Range<usize>> {
        self.marked_range
            .as_ref()
            .map(|range| self.range_to_utf16(range))
    }

    fn unmark_text(&mut self, _window: &mut Window, _cx: &mut Context<Self>) {
        self.marked_range = None;
    }

    fn replace_text_in_range(
        &mut self,
        range_utf16: Option<Range<usize>>,
        new_text: &str,
        _: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.read_only {
            return;
        }
        let range = range_utf16
            .as_ref()
            .map(|range_utf16| self.range_from_utf16(range_utf16))
            .or(self.marked_range.clone())
            .unwrap_or(self.selected_range.clone());

        self.content =
            (self.content[0..range.start].to_owned() + new_text + &self.content[range.end..])
                .into();
        self.selected_range = range.start + new_text.len()..range.start + new_text.len();
        self.marked_range.take();
        cx.notify();
    }

    fn replace_and_mark_text_in_range(
        &mut self,
        range_utf16: Option<Range<usize>>,
        new_text: &str,
        new_selected_range_utf16: Option<Range<usize>>,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.read_only {
            return;
        }
        let range = range_utf16
            .as_ref()
            .map(|range_utf16| self.range_from_utf16(range_utf16))
            .or(self.marked_range.clone())
            .unwrap_or(self.selected_range.clone());

        self.content =
            (self.content[0..range.start].to_owned() + new_text + &self.content[range.end..])
                .into();
        if !new_text.is_empty() {
            self.marked_range = Some(range.start..range.start + new_text.len());
        } else {
            self.marked_range = None;
        }
        self.selected_range = new_selected_range_utf16
            .as_ref()
            .map(|range_utf16| self.range_from_utf16(range_utf16))
            .map(|new_range| new_range.start + range.start..new_range.end + range.end)
            .unwrap_or_else(|| range.start + new_text.len()..range.start + new_text.len());

        cx.notify();
    }

    fn bounds_for_range(
        &mut self,
        range_utf16: Range<usize>,
        bounds: Bounds<Pixels>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<Bounds<Pixels>> {
        let laid = self.last_laid.as_ref()?;
        let range = self.range_from_utf16(&range_utf16);
        let (a, b) = (laid.position(range.start), laid.position(range.end));
        Some(Bounds::from_corners(point(bounds.left() + a.x, bounds.top() + a.y), point(bounds.left() + b.x, bounds.top() + b.y + laid.line_height)))
    }

    fn character_index_for_point(
        &mut self,
        point: gpui::Point<Pixels>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<usize> {
        let bounds = self.last_bounds?;
        let laid = self.last_laid.as_ref()?;
        let utf8_index = laid.index(gpui::point(point.x - bounds.left(), point.y - bounds.top()), self.content.len());
        Some(self.offset_to_utf16(utf8_index))
    }
}

struct TextElement {
    input: Entity<TextInput>,
}

/// The field's text as laid out at the last paint: one wrapped line per
/// paragraph (the text split on '\n'), stacked.
struct Laid {
    lines: Vec<WrappedLine>,
    line_height: Pixels,
}

impl Laid {
    /// Each paragraph's byte range in the text (the '\n' after it excluded).
    fn paragraphs(&self) -> Vec<(usize, usize)> {
        let mut out = Vec::with_capacity(self.lines.len());
        let mut start = 0;
        for l in &self.lines {
            out.push((start, start + l.len()));
            start += l.len() + 1;
        }
        out
    }

    fn height_of(&self, line: &WrappedLine) -> Pixels {
        line.size(self.line_height).height
    }

    /// Rows on screen: each paragraph's wrapped rows, at least one.
    pub fn rows(&self) -> usize {
        rows(self.lines.iter().map(|l| l.wrap_boundaries().len()))
    }

    pub fn height(&self) -> Pixels {
        field_height(self.rows(), self.line_height)
    }

    /// Where a byte index sits: x, y (top of its row) relative to the field.
    fn position(&self, index: usize) -> Point<Pixels> {
        let mut y = px(0.);
        for (l, (start, end)) in self.lines.iter().zip(self.paragraphs()) {
            if index <= end {
                let p = l.position_for_index(index - start, self.line_height).unwrap_or(point(px(0.), px(0.)));
                return point(p.x, y + p.y);
            }
            y += self.height_of(l);
        }
        point(px(0.), y - self.line_height.max(px(0.)))
    }

    /// The byte index nearest a point relative to the field.
    fn index(&self, position: Point<Pixels>, len: usize) -> usize {
        if position.y < px(0.) {
            return 0;
        }
        let mut y = px(0.);
        for (l, (start, end)) in self.lines.iter().zip(self.paragraphs()) {
            let h = self.height_of(l);
            if position.y < y + h {
                let local = point(position.x.max(px(0.)), position.y - y);
                return match l.closest_index_for_position(local, self.line_height) {
                    Ok(i) | Err(i) => (start + i).min(end),
                };
            }
            y += h;
        }
        len
    }
}

/// Rows of text from each paragraph's wrap count (a paragraph wrapped once
/// is two rows); an empty field is one row.
fn rows(wraps: impl IntoIterator<Item = usize>) -> usize {
    wraps.into_iter().map(|w| w + 1).sum::<usize>().max(1)
}

/// How tall the field is for `rows` rows: whole rows, never less than one.
fn field_height(rows: usize, line_height: Pixels) -> Pixels {
    line_height * rows.max(1) as f32
}

/// The font size and row height the field is shaped and drawn with.
#[derive(Clone, Copy, Debug, PartialEq)]
struct Metrics {
    font_size: Pixels,
    line_height: Pixels,
}

impl Metrics {
    /// From the field's own text style. Read it in `request_layout`, where the
    /// field's style (its line height, the console's font) is in effect: GPUI
    /// runs the measure closure later, under the window's default style, so
    /// reading the style there sized the field with another font and row
    /// height than it is drawn with (the first wrap grew it by part of a row).
    fn of(style: &TextStyle, rem_size: Pixels) -> Self {
        Metrics { font_size: style.font_size.to_pixels(rem_size), line_height: style.line_height_in_pixels(rem_size) }
    }
}

struct PrepaintState {
    laid: Option<Laid>,
    cursor: Option<PaintQuad>,
    selection: Vec<PaintQuad>,
}

impl IntoElement for TextElement {
    type Element = Self;

    fn into_element(self) -> Self::Element {
        self
    }
}

impl TextElement {
    /// Shapes the field's text (or its placeholder) wrapped at `width`, in
    /// the field's own text style (`style`, read where it is in effect).
    fn lay_out(&self, width: Pixels, style: &TextStyle, window: &mut Window, cx: &mut App) -> (Laid, bool) {
        let input = self.input.read(cx);
        let content = input.content.clone();
        let placeholder = content.is_empty();
        let (display_text, text_color) = if placeholder {
            (input.placeholder.clone(), input.colors.placeholder)
        } else if input.secret {
            // One asterisk per byte keeps every index into the drawn text valid.
            (SharedString::from(content.chars().map(|c| "*".repeat(c.len_utf8())).collect::<String>()), style.color)
        } else {
            (content, style.color)
        };
        let run = TextRun { len: display_text.len(), font: style.font(), color: text_color, background_color: None, underline: None, strikethrough: None };
        let runs = if let Some(marked_range) = input.marked_range.as_ref().filter(|_| !placeholder) {
            vec![
                TextRun { len: marked_range.start, ..run.clone() },
                TextRun {
                    len: marked_range.end - marked_range.start,
                    underline: Some(UnderlineStyle { color: Some(run.color), thickness: px(1.0), wavy: false }),
                    ..run.clone()
                },
                TextRun { len: display_text.len() - marked_range.end, ..run },
            ]
            .into_iter()
            .filter(|run| run.len > 0)
            .collect()
        } else {
            vec![run]
        };
        let Metrics { font_size, line_height } = Metrics::of(style, window.rem_size());
        let lines = window
            .text_system()
            // A read-only field (a sign-in code, a link) stays on one line.
            .shape_text(display_text, font_size, &runs, { let input = self.input.read(cx); (!input.read_only && !input.one_line).then(|| width.max(px(1.))) }, None)
            .map(|l| l.into_vec())
            .unwrap_or_default();
        (Laid { lines, line_height }, placeholder)
    }
}

impl Element for TextElement {
    type RequestLayoutState = ();
    type PrepaintState = PrepaintState;

    fn id(&self) -> Option<ElementId> {
        None
    }

    fn source_location(&self) -> Option<&'static core::panic::Location<'static>> {
        None
    }

    fn request_layout(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (LayoutId, Self::RequestLayoutState) {
        let mut style = Style::default();
        style.size.width = relative(1.).into();
        // As tall as the wrapped text: the field grows as you type. The text
        // style is read here, not in the closure (see `Metrics::of`).
        let element = TextElement { input: self.input.clone() };
        let text_style = window.text_style();
        let line_height = Metrics::of(&text_style, window.rem_size()).line_height;
        let id = window.request_measured_layout(style, move |known, available, window, cx| {
            let width = known.width.or(match available.width {
                gpui::AvailableSpace::Definite(w) => Some(w),
                _ => None,
            });
            let height = match width {
                Some(w) => element.lay_out(w, &text_style, window, cx).0.height(),
                None => field_height(1, line_height),
            };
            size(width.unwrap_or(px(0.)), height)
        });
        (id, ())
    }

    fn prepaint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        bounds: Bounds<Pixels>,
        _request_layout: &mut Self::RequestLayoutState,
        window: &mut Window,
        cx: &mut App,
    ) -> Self::PrepaintState {
        let text_style = window.text_style();
        let (laid, placeholder) = self.lay_out(bounds.size.width, &text_style, window, cx);
        let input = self.input.read(cx);
        let selected_range = input.selected_range.clone();
        let cursor_at = input.cursor_offset();
        let line_height = laid.line_height;
        let at = |index: usize| {
            let p = if placeholder { point(px(0.), px(0.)) } else { laid.position(index) };
            point(bounds.left() + p.x, bounds.top() + p.y)
        };
        let (selection, cursor) = if selected_range.is_empty() {
            let p = at(cursor_at);
            (Vec::new(), Some(fill(Bounds::new(p, size(px(2.), line_height)), input.colors.cursor)))
        } else {
            let (a, b) = (at(selected_range.start), at(selected_range.end));
            let mut quads = Vec::new();
            if a.y == b.y {
                quads.push(fill(Bounds::from_corners(a, point(b.x, b.y + line_height)), input.colors.selection));
            } else {
                // The first row to its end, the rows between whole, the last row from its start.
                quads.push(fill(Bounds::from_corners(a, point(bounds.right(), a.y + line_height)), input.colors.selection));
                if b.y > a.y + line_height {
                    quads.push(fill(Bounds::from_corners(point(bounds.left(), a.y + line_height), point(bounds.right(), b.y)), input.colors.selection));
                }
                quads.push(fill(Bounds::from_corners(point(bounds.left(), b.y), point(b.x, b.y + line_height)), input.colors.selection));
            }
            (quads, None)
        };
        PrepaintState { laid: Some(laid), cursor, selection }
    }

    fn paint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        bounds: Bounds<Pixels>,
        _request_layout: &mut Self::RequestLayoutState,
        prepaint: &mut Self::PrepaintState,
        window: &mut Window,
        cx: &mut App,
    ) {
        let focus_handle = self.input.read(cx).focus_handle.clone();
        window.handle_input(&focus_handle, ElementInputHandler::new(bounds, self.input.clone()), cx);
        for quad in prepaint.selection.drain(..) {
            window.paint_quad(quad);
        }
        let laid = prepaint.laid.take().unwrap();
        let mut y = bounds.top();
        for line in &laid.lines {
            let _ = line.paint(point(bounds.left(), y), laid.line_height, gpui::TextAlign::Left, None, window, cx);
            y += laid.height_of(line);
        }
        if focus_handle.is_focused(window)
            && let Some(cursor) = prepaint.cursor.take()
        {
            window.paint_quad(cursor);
        }
        self.input.update(cx, |input, _cx| {
            input.last_laid = Some(laid);
            input.last_bounds = Some(bounds);
        });
    }
}

impl Render for TextInput {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .flex()
            .key_context("TextInput")
            .track_focus(&self.focus_handle(cx))
            .cursor(CursorStyle::IBeam)
            .on_action(cx.listener(Self::backspace))
            .on_action(cx.listener(Self::delete))
            .on_action(cx.listener(Self::left))
            .on_action(cx.listener(Self::right))
            .on_action(cx.listener(Self::select_left))
            .on_action(cx.listener(Self::select_right))
            .on_action(cx.listener(Self::select_all))
            .on_action(cx.listener(Self::home))
            .on_action(cx.listener(Self::end))
            .on_action(cx.listener(Self::show_character_palette))
            .on_action(cx.listener(Self::paste))
            .on_action(cx.listener(Self::cut))
            .on_action(cx.listener(Self::copy))
            .on_action(cx.listener(Self::newline))
            .on_action(cx.listener(Self::up))
            .on_action(cx.listener(Self::down))
            .on_mouse_down(MouseButton::Left, cx.listener(Self::on_mouse_down))
            .on_mouse_up(MouseButton::Left, cx.listener(Self::on_mouse_up))
            .on_mouse_up_out(MouseButton::Left, cx.listener(Self::on_mouse_up))
            .on_mouse_move(cx.listener(Self::on_mouse_move))
            .w_full()
            .line_height(px(self.line_height))
            .child(div().w_full().child(TextElement { input: cx.entity() }))
    }
}

impl Focusable for TextInput {
    fn focus_handle(&self, _: &App) -> FocusHandle {
        self.focus_handle.clone()
    }
}

/// Something pasted that is not text.
pub enum Pasted {
    Image(gpui::Image),
    /// Files copied in a file manager (file:// lines).
    Files(Vec<std::path::PathBuf>),
}

impl gpui::EventEmitter<Pasted> for TextInput {}

/// The files of a text/uri-list paste: every non-empty line a file:// URI.
fn file_uris(text: &str) -> Option<Vec<std::path::PathBuf>> {
    let lines: Vec<&str> = text.lines().map(str::trim).filter(|l| !l.is_empty() && !l.starts_with('#')).collect();
    if lines.is_empty() {
        return None;
    }
    lines.iter().map(|l| l.strip_prefix("file://").map(|p| std::path::PathBuf::from(percent_decode(p)))).collect()
}

/// %XX escapes in a file URI's path.
fn percent_decode(path: &str) -> String {
    let bytes = path.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(b) = u8::from_str_radix(&path[i + 1..i + 3], 16) {
                out.push(b);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

#[cfg(test)]
mod paste_tests {
    use super::*;

    #[test]
    fn copied_files_are_files_and_text_is_text() {
        assert_eq!(file_uris("file:///srv/share/My%20Pics/a.png\nfile:///tmp/b.mp4\n"), Some(vec!["/srv/share/My Pics/a.png".into(), "/tmp/b.mp4".into()]));
        assert_eq!(file_uris("look at file:///tmp/a.png"), None);
        assert_eq!(file_uris("/etc/hosts"), None);
    }
}

#[cfg(test)]
mod height_tests {
    use super::*;

    // Purpose: the composer grows by whole rows from the very first wrap.
    // Requirement: text wrapped once (two rows) is exactly two full row
    // heights on the first computation, one row is one row height, and the
    // height comes from the field's own text style (the console's 12.5px font
    // and the field's 20px rows), not the window default GPUI applies when it
    // runs the measure closure. Threat: the first wrap growing the box by only
    // part of a row (the second row hidden), or not shrinking back to one row.
    // `rows`, `field_height` and `Metrics::of` are what `TextElement`'s
    // measured layout and `Laid::height` compute from; shaping itself needs a
    // window, so these pure functions are the narrowest layer.
    #[test]
    fn first_wrap_is_two_full_rows_in_the_fields_own_style() {
        let rem = px(16.);
        // The composer: the console's root text size, the field's row height.
        let field = TextStyle { font_size: px(12.5).into(), line_height: px(20.).into(), ..Default::default() };
        let m = Metrics::of(&field, rem);
        assert_eq!(m, Metrics { font_size: px(12.5), line_height: px(20.) });
        // The window default is another row height: measuring with it was the bug.
        assert_ne!(Metrics::of(&TextStyle::default(), rem).line_height, m.line_height);

        // One paragraph, not wrapped: one row.
        assert_eq!(rows([0]), 1);
        assert_eq!(field_height(rows([0]), m.line_height), px(20.));
        // The same paragraph after its first wrap: two full rows, at once.
        assert_eq!(rows([1]), 2);
        assert_eq!(field_height(rows([1]), m.line_height), px(40.));
        // Back to one row when the wrap goes away.
        assert_eq!(field_height(rows([0]), m.line_height), px(20.));
        // Empty, several paragraphs, later wraps.
        assert_eq!(field_height(rows([]), m.line_height), px(20.));
        assert_eq!(field_height(0, m.line_height), px(20.));
        assert_eq!(rows([0, 0]), 2);
        assert_eq!(field_height(rows([1, 2]), m.line_height), px(100.));
    }
}
