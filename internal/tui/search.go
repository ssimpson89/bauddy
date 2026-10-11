package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// startSearch opens an empty search prompt.
func (m *Model) startSearch() {
	m.query, m.pattern, m.searched, m.matches, m.match = "", nil, nil, nil, 0
	m.mode = modeSearch
}

// searchKey edits the search prompt. It only receives keys after bauddy's
// command escape, so search text can never be sent to the attached device.
func (m *Model) searchKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeConsole
		return m, nil
	case "enter":
		if len(m.matches) > 0 {
			m.match = len(m.matches) - 1 // start at the newest hit
			m.mode = modeSearchResults
		}
		return m, nil
	case "backspace", "ctrl+h":
		runes := []rune(m.query)
		if len(runes) > 0 {
			m.query = string(runes[:len(runes)-1])
		}
	default:
		if k.Text != "" && !unicode.IsControl([]rune(k.Text)[0]) {
			m.query += k.Text
		}
	}
	m.updateMatches()
	return m, nil
}

func (m *Model) searchResultKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.mode = modeConsole
	case "/":
		m.mode = modeSearch
	case "n", "down", "enter":
		if m.match > 0 {
			m.match-- // older
		}
	case "N", "up":
		if m.match < len(m.matches)-1 {
			m.match++ // newer
		}
	}
	return m, nil
}

// updateMatches searches the lines as they are now. It keeps that snapshot,
// so the results keep pointing at the same lines while new output arrives.
func (m *Model) updateMatches() {
	m.searched, m.matches, m.pattern = m.lines, m.matches[:0], nil
	if m.query == "" {
		return
	}
	m.pattern = searchPattern(m.query)
	for i, line := range m.searched {
		if m.pattern.MatchString(line) {
			m.matches = append(m.matches, i)
		}
	}
}

// searchPattern matches q literally, with smart case: all lowercase matches
// any case ("error" finds "ERROR"); any capital matches exactly. Matching on
// the original text keeps the match position right for any characters.
func searchPattern(q string) *regexp.Regexp {
	expr := regexp.QuoteMeta(q)
	if q == strings.ToLower(q) {
		expr = "(?i)" + expr
	}
	return regexp.MustCompile(expr)
}

func (m *Model) searchBox() string {
	box, key, dim := m.overlayStyles()
	if m.mode == modeSearch {
		label := "type to search"
		if m.query != "" {
			label = plural(len(m.matches), "match", "matches")
		}
		return box.Render(key.Render("/ ") + m.query + "\n" + dim.Render(label))
	}

	// Fit the window: the box's border and padding take 6 columns and 4 rows,
	// the heading and the blank line under it 2 more.
	width := max(20, m.width-8)
	around := max(2, (max(1, m.height-1)-7)/2)
	at := m.matches[m.match]
	head := key.Render("/ ") + m.query + "  " + dim.Render(fmt.Sprintf("match %d of %d", m.match+1, len(m.matches)))
	var lines []string
	for i := max(0, at-around); i < min(len(m.searched), at+around+1); i++ {
		if i == at {
			lines = append(lines, m.showMatch(m.searched[i], width))
		} else {
			lines = append(lines, dim.Render(ansi.Truncate(m.searched[i], width, "…")))
		}
	}
	return box.Render(head + "\n\n" + strings.Join(lines, "\n"))
}

// showMatch renders line with its match highlighted, cut to width cells.
// When the line is too wide, it scrolls so the match starts a third of the
// way in, rather than running off the edge.
func (m *Model) showMatch(line string, width int) string {
	loc := m.pattern.FindStringIndex(line)
	if loc == nil {
		return ansi.Truncate(line, width, "…")
	}
	before, hit, after := line[:loc[0]], line[loc[0]:loc[1]], line[loc[1]:]
	if keep := width / 3; ansi.StringWidth(line) > width && ansi.StringWidth(before) > keep {
		w := ansi.StringWidth(before)
		before = "…" + ansi.Cut(before, w-keep+1, w)
	}
	style := lipgloss.NewStyle().Background(cAmber).Foreground(cOnAmber).Bold(true)
	return ansi.Truncate(before+style.Render(hit)+after, width, "…")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
