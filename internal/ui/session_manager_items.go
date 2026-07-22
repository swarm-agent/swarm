package ui

import (
	"sort"
	"strconv"
	"strings"
)

var sessionManagerGroupOrder = map[string]int{
	"needs_review": 0,
	"in_progress":  1,
	"pinned":       2,
	"active_chats": 3,
	"archived":     4,
}

var sessionManagerFilters = []struct {
	Label string
	Group string
}{
	{Label: "REVIEW", Group: "needs_review"},
	{Label: "IN PROGRESS", Group: "in_progress"},
	{Label: "CHATS", Group: "active_chats"},
}

func sessionManagerFilterCount() int {
	return len(sessionManagerFilters)
}

func sessionManagerFilterLabel(index int) string {
	if index < 0 || index >= len(sessionManagerFilters) {
		return ""
	}
	return sessionManagerFilters[index].Label
}

func sessionManagerItemMatchesFilter(item ChatSessionPaletteItem, index int) bool {
	if index < 0 || index >= len(sessionManagerFilters) {
		index = 0
	}
	group := strings.ToLower(strings.TrimSpace(item.Group))
	if sessionManagerFilters[index].Group == "active_chats" {
		return group != "needs_review" && group != "in_progress" && group != "archived"
	}
	return group == sessionManagerFilters[index].Group
}

func filterSessionManagerItems(items []ChatSessionPaletteItem, index int) []ChatSessionPaletteItem {
	filtered := make([]ChatSessionPaletteItem, 0, len(items))
	for _, item := range items {
		if sessionManagerItemMatchesFilter(item, index) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func sessionManagerFilterCounts(items []ChatSessionPaletteItem) []int {
	counts := make([]int, len(sessionManagerFilters))
	for _, item := range items {
		if item.Depth > 0 {
			continue
		}
		for index := range sessionManagerFilters {
			if sessionManagerItemMatchesFilter(item, index) {
				counts[index]++
				break
			}
		}
	}
	return counts
}

func prepareSessionManagerItems(items []ChatSessionPaletteItem) []ChatSessionPaletteItem {
	ordered := append([]ChatSessionPaletteItem(nil), items...)
	itemByID := make(map[string]ChatSessionPaletteItem, len(ordered))
	for _, item := range ordered {
		itemByID[strings.TrimSpace(item.ID)] = item
	}
	var inheritedGroup func(ChatSessionPaletteItem, map[string]bool) string
	inheritedGroup = func(item ChatSessionPaletteItem, seen map[string]bool) string {
		parentID := sessionManagerParentID(item)
		if parentID == "" || seen[parentID] {
			return strings.TrimSpace(item.Group)
		}
		parent, ok := itemByID[parentID]
		if !ok {
			return strings.TrimSpace(item.Group)
		}
		seen[parentID] = true
		if group := inheritedGroup(parent, seen); group != "" {
			return group
		}
		return strings.TrimSpace(item.Group)
	}
	for index := range ordered {
		if group := inheritedGroup(ordered[index], make(map[string]bool)); group != "" {
			ordered[index].Group = group
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		leftGroup, rightGroup := sessionManagerGroupRank(left.Group), sessionManagerGroupRank(right.Group)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		if left.NeedsAttention != right.NeedsAttention {
			return left.NeedsAttention
		}
		if left.Active != right.Active {
			return left.Active
		}
		if left.Active && right.Active {
			if left.ActiveStartedAt != right.ActiveStartedAt {
				if left.ActiveStartedAt <= 0 {
					return false
				}
				if right.ActiveStartedAt <= 0 {
					return true
				}
				return left.ActiveStartedAt < right.ActiveStartedAt
			}
		} else if left.UpdatedAt != right.UpdatedAt {
			return left.UpdatedAt > right.UpdatedAt
		}
		return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
	})
	return orderSessionManagerLineage(ordered)
}

func sessionManagerGroupRank(group string) int {
	if rank, ok := sessionManagerGroupOrder[strings.ToLower(strings.TrimSpace(group))]; ok {
		return rank
	}
	return sessionManagerGroupOrder["active_chats"]
}

func orderSessionManagerLineage(items []ChatSessionPaletteItem) []ChatSessionPaletteItem {
	byParent := make(map[string][]ChatSessionPaletteItem)
	roots := make([]ChatSessionPaletteItem, 0, len(items))
	known := make(map[string]struct{}, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			known[id] = struct{}{}
		}
	}
	for _, item := range items {
		parentID := sessionManagerParentID(item)
		if _, ok := known[parentID]; parentID == "" || !ok {
			roots = append(roots, item)
			continue
		}
		byParent[parentID] = append(byParent[parentID], item)
	}
	ordered := make([]ChatSessionPaletteItem, 0, len(items))
	var appendNode func(ChatSessionPaletteItem, int, map[string]bool)
	appendNode = func(item ChatSessionPaletteItem, depth int, seen map[string]bool) {
		id := strings.TrimSpace(item.ID)
		if id != "" && seen[id] {
			return
		}
		if id != "" {
			seen[id] = true
		}
		item.Depth = depth
		ordered = append(ordered, item)
		for _, child := range byParent[id] {
			child.Group = item.Group
			appendNode(child, depth+1, seen)
		}
	}
	seen := make(map[string]bool, len(items))
	for _, root := range roots {
		appendNode(root, 0, seen)
	}
	for _, item := range items {
		if !seen[strings.TrimSpace(item.ID)] {
			appendNode(item, 0, seen)
		}
	}
	return ordered
}

func visibleSessionManagerItems(items []ChatSessionPaletteItem, expanded map[string]bool) []ChatSessionPaletteItem {
	visible := make([]ChatSessionPaletteItem, 0, len(items))
	collapsedDepth := -1
	for _, item := range items {
		if collapsedDepth >= 0 {
			if item.Depth > collapsedDepth {
				continue
			}
			collapsedDepth = -1
		}
		visible = append(visible, item)
		if sessionManagerChildCount(items, item.ID) > 0 && !expanded[strings.TrimSpace(item.ID)] {
			collapsedDepth = item.Depth
		}
	}
	return visible
}

func sessionManagerChildCount(items []ChatSessionPaletteItem, parentID string) int {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return 0
	}
	seen := map[string]bool{parentID: true}
	var countDescendants func(string) int
	countDescendants = func(id string) int {
		count := 0
		for _, item := range items {
			if sessionManagerParentID(item) != id {
				continue
			}
			childID := strings.TrimSpace(item.ID)
			if childID == "" || seen[childID] {
				continue
			}
			seen[childID] = true
			count += 1 + countDescendants(childID)
		}
		return count
	}
	return countDescendants(parentID)
}

func sessionManagerParentID(item ChatSessionPaletteItem) string {
	if strings.EqualFold(strings.TrimSpace(item.LineageKind), "session_deploy") {
		return ""
	}
	return strings.TrimSpace(item.ParentSessionID)
}

func sessionManagerWorkspaceLabel(item ChatSessionPaletteItem) string {
	workspace := strings.TrimSpace(item.WorkspaceName)
	if workspace == "" {
		workspace = strings.TrimSpace(item.WorkspacePath)
	}
	branch := strings.TrimSpace(item.WorktreeBranch)
	if item.WorktreeEnabled && branch != "" && !strings.EqualFold(workspace, branch) {
		if workspace == "" {
			return branch
		}
		return workspace + " · " + branch
	}
	return workspace
}

func sessionManagerItemSuffix(item ChatSessionPaletteItem, childCount int, expanded bool) string {
	parts := make([]string, 0, 2)
	if progress := strings.TrimSpace(item.ProgressLabel); progress != "" {
		parts = append(parts, progress)
	}
	if childCount > 0 {
		marker := "▸"
		if expanded {
			marker = "▾"
		}
		label := "subagents"
		if childCount == 1 {
			label = "subagent"
		}
		parts = append(parts, marker+" "+strconv.Itoa(childCount)+" "+label)
	}
	return strings.Join(parts, " · ")
}
