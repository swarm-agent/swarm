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
    /// The last pane is never removed.
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

    #[test]
    fn close_collapses_and_keeps_last() {
        let mut root = Node::Leaf(PaneId(0));
        root.split(PaneId(0), Axis::Row, PaneId(1));
        root.split(PaneId(1), Axis::Column, PaneId(2));
        assert!(root.close(PaneId(2)));
        assert_eq!(ids(&root), vec![0, 1]);
        assert!(root.close(PaneId(1)));
        assert!(matches!(root, Node::Leaf(PaneId(0))));
        assert!(!root.close(PaneId(0)));
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
