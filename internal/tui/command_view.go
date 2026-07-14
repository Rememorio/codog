package tui

import (
	"fmt"
	"strings"
)

func renderCommandView(view CommandView, tabIndex int, itemIndex int, offset int, width int, height int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	title := strings.TrimSpace(view.Title)
	if title == "" {
		title = "Settings"
	}
	limit := composerLimit(width)
	lines := []string{styles.completionTitle().Render(" " + strings.ToLower(title) + " ")}
	if len(view.Tabs) == 0 {
		lines = append(lines, styles.completion().Render("  no settings"), styles.completion().Render("  Esc close"))
		return strings.Join(lines, "\n")
	}
	tabIndex = clampIndex(tabIndex, len(view.Tabs))
	lines = append(lines, "  "+strings.Join(commandViewTabLabels(view.Tabs, tabIndex, styles), " "))
	tab := view.Tabs[tabIndex]
	if len(tab.Items) > 0 {
		return renderCommandItemTab(lines, tab, itemIndex, offset, limit, height, styles)
	}
	return renderCommandTextTab(lines, tab, offset, limit, height, styles)
}

func commandViewTabLabels(tabs []CommandViewTab, selected int, styles themeStyles) []string {
	labels := make([]string, 0, len(tabs))
	for index, tab := range tabs {
		label := strings.TrimSpace(tab.Title)
		if label == "" {
			label = fmt.Sprintf("Tab %d", index+1)
		}
		style := styles.completion()
		if index == selected {
			style = styles.selectedCompletion()
		}
		labels = append(labels, style.Render(" "+label+" "))
	}
	return labels
}

func renderCommandItemTab(lines []string, tab CommandViewTab, itemIndex int, offset int, limit int, height int, styles themeStyles) string {
	itemIndex = clampIndex(itemIndex, len(tab.Items))
	lines = appendCommandViewHeaders(lines, tab.Lines, limit, styles)
	offset, end, capacity := commandViewItemWindow(tab, itemIndex, offset, height)
	lines = appendCommandViewItems(lines, tab.Items, itemIndex, offset, end, limit, styles)
	footer := commandViewItemFooter(tab, itemIndex, offset, end, capacity)
	lines = append(lines, styles.completion().Render("  "+footer+" · Esc close"))
	return strings.Join(lines, "\n")
}

func appendCommandViewHeaders(lines []string, headers []string, limit int, styles themeStyles) []string {
	count := 0
	for _, line := range headers {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, styles.completion().Render(truncateForComposer("  "+trimmed, limit)))
		count++
		if count == 2 {
			break
		}
	}
	return lines
}

func commandViewItemWindow(tab CommandViewTab, itemIndex int, offset int, height int) (int, int, int) {
	capacity := commandViewItemCapacity(tab, height)
	maximum := max(0, len(tab.Items)-capacity)
	offset = min(max(0, offset), maximum)
	if itemIndex < offset {
		offset = itemIndex
	}
	if itemIndex >= offset+capacity {
		offset = itemIndex - capacity + 1
	}
	return offset, min(len(tab.Items), offset+capacity), capacity
}

func appendCommandViewItems(lines []string, items []CommandViewItem, selected int, offset int, end int, limit int, styles themeStyles) []string {
	for index := offset; index < end; index++ {
		item := items[index]
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = strings.TrimSpace(item.Action)
		}
		row := prefix + label
		if value := strings.TrimSpace(item.Value); value != "" {
			row += "  " + value
		}
		lines = append(lines, style.Render(truncateForComposer(row, limit)))
		if index == selected && strings.TrimSpace(item.Description) != "" {
			lines = append(lines, styles.completion().Render(truncateForComposer("    "+strings.TrimSpace(item.Description), limit)))
		}
	}
	return lines
}

func commandViewItemFooter(tab CommandViewTab, itemIndex int, offset int, end int, capacity int) string {
	footer := "←/→ tab · ↑/↓ select · Enter open"
	item := tab.Items[itemIndex]
	if strings.TrimSpace(item.SecondaryCommand) != "" {
		label := strings.TrimSpace(item.SecondaryLabel)
		if label == "" {
			label = "action"
		}
		footer += " · " + commandViewSecondaryKeyLabel(item) + " " + label
	}
	if strings.TrimSpace(tab.RefreshCommand) != "" {
		footer += " · R refresh"
	}
	if len(tab.Items) > capacity {
		footer = fmt.Sprintf("%d-%d/%d · %s", offset+1, end, len(tab.Items), footer)
	}
	return footer
}

func renderCommandTextTab(lines []string, tab CommandViewTab, offset int, limit int, height int, styles themeStyles) string {
	visible := commandViewVisibleLines(height)
	maximum := max(0, len(tab.Lines)-visible)
	offset = min(max(0, offset), maximum)
	end := min(len(tab.Lines), offset+visible)
	for _, line := range tab.Lines[offset:end] {
		lines = append(lines, styles.completion().Render(truncateForComposer("  "+line, limit)))
	}
	if len(tab.Lines) == 0 {
		lines = append(lines, styles.completion().Render("  no information"))
	}
	footer := commandViewTextFooter(tab, offset, end, visible)
	lines = append(lines, styles.completion().Render("  "+footer))
	return strings.Join(lines, "\n")
}

func commandViewTextFooter(tab CommandViewTab, offset int, end int, visible int) string {
	footer := "←/→ tab"
	if strings.TrimSpace(tab.RefreshCommand) != "" {
		footer += " · R refresh"
	}
	footer += " · Esc close"
	if len(tab.Lines) > visible {
		return fmt.Sprintf("%d-%d/%d · ↑/↓ scroll · %s", offset+1, end, len(tab.Lines), footer)
	}
	return footer
}
