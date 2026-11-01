//! Split layout for session panes: a tree of rows and columns. Each split
//! keeps its children's shares of the space (summing to 1), so dividers can
//! resize them.

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Axis {
    /// Children side by side.
    Row,
    /// Children stacked top to bottom.
    Column,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct PaneId(pub usize);

/// Where a pane goes relative to another: its four edges, or in its place.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Zone {
    Left,
    Right,
    Top,
    Bottom,
    Center,
}

impl Zone {
    /// The split a drop on this edge makes, and whether the new pane comes first.
    pub fn split(self) -> Option<(Axis, bool)> {
        match self {
            Zone::Left => Some((Axis::Row, true)),
            Zone::Right => Some((Axis::Row, false)),
            Zone::Top => Some((Axis::Column, true)),
            Zone::Bottom => Some((Axis::Column, false)),
            Zone::Center => None,
        }
    }
}

/// The smallest share a resize leaves a pane.
const MIN_SHARE: f32 = 0.12;

#[derive(Clone, Debug)]
pub enum Node {
    Leaf(PaneId),
    Split { axis: Axis, children: Vec<Node>, sizes: Vec<f32> },
}

impl Node {
    /// Puts `new` after `target` along `axis`. Returns false if `target` is absent.
    #[cfg(test)]
    pub fn split(&mut self, target: PaneId, axis: Axis, new: PaneId) -> bool {
        self.split_at(target, axis, new, false)
    }

    /// Puts `new` beside `target` along `axis`, before it (left, above) or
    /// after it; the two share what `target` had.
    pub fn split_at(&mut self, target: PaneId, axis: Axis, new: PaneId, before: bool) -> bool {
        let pair = |a: PaneId, b: PaneId| if before { vec![Node::Leaf(b), Node::Leaf(a)] } else { vec![Node::Leaf(a), Node::Leaf(b)] };
        match self {
            Node::Leaf(id) if *id == target => {
                *self = Node::Split { axis, children: pair(target, new), sizes: vec![0.5, 0.5] };
                true
            }
            Node::Leaf(_) => false,
            Node::Split { axis: own, children, sizes } => {
                for i in 0..children.len() {
                    if matches!(children[i], Node::Leaf(id) if id == target) {
                        if *own == axis {
                            let half = sizes[i] / 2.;
                            sizes[i] = half;
                            let at = if before { i } else { i + 1 };
                            children.insert(at, Node::Leaf(new));
                            sizes.insert(at, half);
                        } else {
                            children[i] = Node::Split { axis, children: pair(target, new), sizes: vec![0.5, 0.5] };
                        }
                        return true;
                    }
                    if children[i].split_at(target, axis, new, before) {
                        return true;
                    }
                }
                false
            }
        }
    }

    /// Removes `target` and collapses splits left with one child.
    /// A tree is never empty, so a lone leaf is not removed here; `Layout`
    /// closes the last pane.
    pub fn close(&mut self, target: PaneId) -> bool {
        if matches!(self, Node::Leaf(_)) {
            return false;
        }
        let removed = self.remove(target);
        self.collapse();
        removed
    }

    fn remove(&mut self, target: PaneId) -> bool {
        let Node::Split { children, sizes, .. } = self else {
            return false;
        };
        if let Some(i) = children.iter().position(|child| matches!(child, Node::Leaf(id) if *id == target)) {
            children.remove(i);
            let gone = sizes.remove(i);
            // The neighbours share the space it leaves.
            let rest: f32 = sizes.iter().sum();
            if rest > 0. {
                sizes.iter_mut().for_each(|s| *s += gone * *s / rest);
            }
            return true;
        }
        children.iter_mut().any(|child| child.remove(target))
    }

    fn collapse(&mut self) {
        if let Node::Split { children, .. } = self {
            for child in children.iter_mut() {
                child.collapse();
            }
            if children.len() == 1 {
                *self = children.remove(0);
            }
        }
    }

    /// Moves pane `moved` next to `target` (an edge zone). Center is not a move.
    pub fn move_pane(&mut self, moved: PaneId, target: PaneId, zone: Zone) -> bool {
        let Some((axis, before)) = zone.split() else { return false };
        if moved == target || !self.contains(target) || !self.contains(moved) {
            return false;
        }
        let mut next = self.clone();
        if !next.close(moved) || !next.split_at(target, axis, moved, before) {
            return false;
        }
        *self = next;
        true
    }

    pub fn contains(&self, id: PaneId) -> bool {
        match self {
            Node::Leaf(own) => *own == id,
            Node::Split { children, .. } => children.iter().any(|c| c.contains(id)),
        }
    }

    /// Moves the divider after child `index` of the split at `path` (child
    /// indexes from the root) to `at`, a fraction of that split's length.
    pub fn resize(&mut self, path: &[usize], index: usize, at: f32) {
        let Node::Split { children, sizes, .. } = self else { return };
        if let Some((first, rest)) = path.split_first() {
            if let Some(child) = children.get_mut(*first) {
                child.resize(rest, index, at);
            }
            return;
        }
        if index + 1 >= sizes.len() {
            return;
        }
        let start: f32 = sizes[..index].iter().sum();
        let end = start + sizes[index] + sizes[index + 1];
        let at = at.clamp(start + MIN_SHARE, (end - MIN_SHARE).max(start + MIN_SHARE));
        sizes[index] = at - start;
        sizes[index + 1] = end - at;
    }

    pub fn leaves(&self, out: &mut Vec<PaneId>) {
        match self {
            Node::Leaf(id) => out.push(*id),
            Node::Split { children, .. } => children.iter().for_each(|c| c.leaves(out)),
        }
    }
}

/// The panes of one window: the split tree (none when every pane is closed),
/// the focused pane, and the next pane id to hand out.
#[derive(Clone, Debug, Default)]
pub struct Layout {
    root: Option<Node>,
    active: Option<PaneId>,
    next: usize,
}

impl Layout {
    /// One pane, focused.
    pub fn single() -> Self {
        let mut layout = Layout::default();
        let id = layout.alloc();
        layout.place(id, None, Axis::Row, false);
        layout
    }

    /// A new pane id, never handed out before.
    pub fn alloc(&mut self) -> PaneId {
        let id = PaneId(self.next);
        self.next += 1;
        id
    }

    pub fn root(&self) -> Option<&Node> {
        self.root.as_ref()
    }

    pub fn active(&self) -> Option<PaneId> {
        self.active
    }

    pub fn contains(&self, id: PaneId) -> bool {
        self.root.as_ref().is_some_and(|r| r.contains(id))
    }

    pub fn leaves(&self) -> Vec<PaneId> {
        let mut out = Vec::new();
        if let Some(root) = &self.root {
            root.leaves(&mut out);
        }
        out
    }

    /// Focuses `id` if it is one of the panes.
    pub fn focus(&mut self, id: PaneId) -> bool {
        if self.contains(id) {
            self.active = Some(id);
            true
        } else {
            false
        }
    }

    /// Places pane `id` beside `target` (the focused pane when None) along
    /// `axis`, before or after it, and focuses it. With no pane open it
    /// becomes the only one. False if `id` is already placed or the target
    /// is not a pane here.
    pub fn place(&mut self, id: PaneId, target: Option<PaneId>, axis: Axis, before: bool) -> bool {
        if self.contains(id) {
            return false;
        }
        match self.root.as_mut() {
            None => self.root = Some(Node::Leaf(id)),
            Some(root) => {
                let Some(target) = target.or(self.active) else { return false };
                if !root.split_at(target, axis, id, before) {
                    return false;
                }
            }
        }
        self.next = self.next.max(id.0 + 1);
        self.active = Some(id);
        true
    }

    /// Removes pane `id`; the last pane leaves the layout empty. The focus
    /// moves to the first pane left, if any.
    pub fn close(&mut self, id: PaneId) -> bool {
        let removed = match self.root.as_mut() {
            None => false,
            Some(Node::Leaf(own)) if *own == id => {
                self.root = None;
                true
            }
            Some(root) => root.close(id),
        };
        if removed && self.active.is_none_or(|a| a == id || !self.contains(a)) {
            self.active = self.leaves().first().copied();
        }
        removed
    }

    /// Focuses the pane after the focused one, wrapping; None with no panes.
    pub fn cycle(&mut self) -> Option<PaneId> {
        let leaves = self.leaves();
        if leaves.is_empty() {
            self.active = None;
            return None;
        }
        let next = match self.active.and_then(|a| leaves.iter().position(|p| *p == a)) {
            Some(i) => leaves[(i + 1) % leaves.len()],
            None => leaves[0],
        };
        self.active = Some(next);
        Some(next)
    }

    /// See `Node::move_pane`.
    pub fn move_pane(&mut self, moved: PaneId, target: PaneId, zone: Zone) -> bool {
        let moved_ok = self.root.as_mut().is_some_and(|r| r.move_pane(moved, target, zone));
        if moved_ok {
            self.active = Some(moved);
        }
        moved_ok
    }

    /// See `Node::resize`.
    pub fn resize(&mut self, path: &[usize], index: usize, at: f32) {
        if let Some(root) = self.root.as_mut() {
            root.resize(path, index, at);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ids(node: &Node) -> Vec<usize> {
        let mut out = Vec::new();
        node.leaves(&mut out);
        out.into_iter().map(|p| p.0).collect()
    }

    fn sizes(node: &Node) -> Vec<f32> {
        match node {
            Node::Split { sizes, .. } => sizes.clone(),
            Node::Leaf(_) => vec![],
        }
    }

    #[test]
    fn split_same_axis_appends_sibling() {
        let mut root = Node::Leaf(PaneId(0));
        assert!(root.split(PaneId(0), Axis::Row, PaneId(1)));
        assert!(root.split(PaneId(1), Axis::Row, PaneId(2)));
        match &root {
            Node::Split { axis: Axis::Row, children, .. } => assert_eq!(children.len(), 3),
            other => panic!("unexpected {other:?}"),
        }
        assert_eq!(ids(&root), vec![0, 1, 2]);
        assert_eq!(sizes(&root), vec![0.5, 0.25, 0.25]);
    }

    #[test]
    fn split_other_axis_nests() {
        let mut root = Node::Leaf(PaneId(0));
        root.split(PaneId(0), Axis::Row, PaneId(1));
        root.split(PaneId(1), Axis::Column, PaneId(2));
        assert_eq!(ids(&root), vec![0, 1, 2]);
        let Node::Split { children, .. } = &root else { panic!() };
        assert!(matches!(children[1], Node::Split { axis: Axis::Column, .. }));
    }

    #[test]
    fn split_before_puts_the_new_pane_first() {
        let mut root = Node::Leaf(PaneId(0));
        root.split_at(PaneId(0), Axis::Column, PaneId(1), true);
        assert_eq!(ids(&root), vec![1, 0]);
        root.split_at(PaneId(0), Axis::Column, PaneId(2), true);
        assert_eq!(ids(&root), vec![1, 2, 0]);
    }

    // Purpose of the `Layout` tests below. Requirement: the console's main
    // window can have no pane at all (the last pane closes), and every pane
    // action works from there. Regression they prevent: the X / ctrl-w doing
    // nothing on the last pane, and the old `leaves[0]` / `% leaves.len()`
    // indexing panicking once zero panes is possible. Owner: `Layout` (used
    // by `Console::open`, `close`, `next_pane`, `new_pane`, `pop_out`). The
    // narrowest layer: the state is plain data, so these run without GPUI.
    #[test]
    fn close_collapses_and_the_last_close_empties() {
        let mut layout = Layout::single();
        let (a, b, c) = (PaneId(0), layout.alloc(), layout.alloc());
        assert!(layout.place(b, Some(a), Axis::Row, false));
        assert!(layout.place(c, Some(b), Axis::Column, false));
        assert!(layout.close(c));
        assert_eq!(layout.leaves(), vec![a, b]);
        assert!(layout.close(b));
        assert!(matches!(layout.root(), Some(Node::Leaf(PaneId(0)))));
        assert_eq!(layout.active(), Some(a), "focus falls back to the pane left");
        // The last pane closes too: nothing open, nothing focused.
        assert!(layout.close(a));
        assert!(layout.root().is_none());
        assert_eq!(layout.active(), None);
        assert!(layout.leaves().is_empty());
        // Closing again, or a pane that never was, changes nothing.
        assert!(!layout.close(a));
        assert!(!layout.close(PaneId(42)));
        assert!(layout.root().is_none());
    }

    #[test]
    fn the_tree_alone_keeps_its_last_leaf() {
        // `Node` cannot be empty; `Layout` is what closes the last pane.
        let mut root = Node::Leaf(PaneId(0));
        assert!(!root.close(PaneId(0)));
        assert!(matches!(root, Node::Leaf(PaneId(0))));
    }

    #[test]
    fn closing_another_pane_keeps_the_focus() {
        let mut layout = Layout::single();
        let b = layout.alloc();
        layout.place(b, None, Axis::Row, false);
        layout.focus(PaneId(0));
        assert!(layout.close(b));
        assert_eq!(layout.active(), Some(PaneId(0)));
    }

    #[test]
    fn opening_or_splitting_from_empty_makes_the_first_pane() {
        for axis in [Axis::Row, Axis::Column] {
            for before in [false, true] {
                let mut layout = Layout::single();
                assert!(layout.close(PaneId(0)));
                let id = layout.alloc();
                assert_ne!(id, PaneId(0), "ids are not reused");
                // No target: the first pane takes the whole space.
                assert!(layout.place(id, None, axis, before));
                assert!(matches!(layout.root(), Some(Node::Leaf(p)) if *p == id));
                assert_eq!(layout.active(), Some(id));
                // A split after that splits the new pane as usual.
                let next = layout.alloc();
                assert!(layout.place(next, None, axis, before));
                assert_eq!(layout.leaves().len(), 2);
                assert_eq!(layout.active(), Some(next));
            }
        }
    }

    #[test]
    fn place_refuses_a_missing_target_or_a_placed_pane() {
        let mut layout = Layout::single();
        let id = layout.alloc();
        assert!(!layout.place(id, Some(PaneId(9)), Axis::Row, false));
        assert_eq!(layout.leaves(), vec![PaneId(0)]);
        assert_eq!(layout.active(), Some(PaneId(0)));
        assert!(!layout.place(PaneId(0), None, Axis::Row, false));
        assert_eq!(layout.leaves(), vec![PaneId(0)]);
    }

    #[test]
    fn focus_only_takes_a_pane_in_the_layout() {
        let mut layout = Layout::single();
        assert!(!layout.focus(PaneId(7)));
        assert_eq!(layout.active(), Some(PaneId(0)));
        layout.close(PaneId(0));
        assert!(!layout.focus(PaneId(0)));
        assert_eq!(layout.active(), None);
    }

    #[test]
    fn cycle_with_zero_one_and_many_panes() {
        let mut layout = Layout::single();
        // One pane: cycling stays on it.
        assert_eq!(layout.cycle(), Some(PaneId(0)));
        assert_eq!(layout.cycle(), Some(PaneId(0)));
        // Many: in order, wrapping.
        let b = layout.alloc();
        let c = layout.alloc();
        layout.place(b, None, Axis::Row, false);
        layout.place(c, None, Axis::Column, false);
        layout.focus(PaneId(0));
        assert_eq!(layout.cycle(), Some(b));
        assert_eq!(layout.cycle(), Some(c));
        assert_eq!(layout.cycle(), Some(PaneId(0)));
        // Zero: nothing to focus, no panic.
        for id in layout.leaves() {
            layout.close(id);
        }
        assert!(layout.root().is_none());
        assert_eq!(layout.cycle(), None);
        assert_eq!(layout.active(), None);
    }

    #[test]
    fn empty_layout_ignores_moves_and_resizes() {
        let mut layout = Layout::single();
        layout.close(PaneId(0));
        assert!(!layout.move_pane(PaneId(0), PaneId(1), Zone::Left));
        layout.resize(&[], 0, 0.5);
        assert!(layout.root().is_none());
    }

    #[test]
    fn close_gives_the_space_to_neighbours() {
        let mut root = Node::Leaf(PaneId(0));
        root.split(PaneId(0), Axis::Row, PaneId(1));
        root.split(PaneId(1), Axis::Row, PaneId(2));
        root.close(PaneId(2));
        let s = sizes(&root);
        assert!((s.iter().sum::<f32>() - 1.).abs() < 1e-5, "{s:?}");
    }

    #[test]
    fn move_pane_to_an_edge() {
        let mut root = Node::Leaf(PaneId(0));
        root.split(PaneId(0), Axis::Row, PaneId(1));
        root.split(PaneId(1), Axis::Row, PaneId(2));
        assert!(root.move_pane(PaneId(2), PaneId(0), Zone::Top));
        assert_eq!(ids(&root), vec![2, 0, 1]);
        let Node::Split { axis: Axis::Row, children, .. } = &root else { panic!("{root:?}") };
        assert!(matches!(&children[0], Node::Split { axis: Axis::Column, .. }));
        assert!(!root.move_pane(PaneId(0), PaneId(0), Zone::Left));
        assert!(!root.move_pane(PaneId(0), PaneId(1), Zone::Center));
    }

    #[test]
    fn resize_moves_one_divider_within_limits() {
        let mut root = Node::Leaf(PaneId(0));
        root.split(PaneId(0), Axis::Row, PaneId(1));
        root.split(PaneId(1), Axis::Row, PaneId(2));
        root.resize(&[], 0, 0.6);
        let s = sizes(&root);
        assert!((s[0] - 0.6).abs() < 1e-5 && (s[1] - 0.15).abs() < 1e-5, "{s:?}");
        root.resize(&[], 0, 0.99);
        let s = sizes(&root);
        assert!((s[1] - MIN_SHARE).abs() < 1e-5, "{s:?}");
        assert!((s.iter().sum::<f32>() - 1.).abs() < 1e-5);
    }
}
