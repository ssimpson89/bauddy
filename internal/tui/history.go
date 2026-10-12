package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/termtext"
)

// history is earlier output as plain text, shown in place of the console
// when you scroll back. It's a snapshot, so what you're reading stays put
// while the device keeps printing; End brings it up to date. bauddy never
// captures the mouse, so selecting text to copy works here too.
type history struct {
	lines      []string // the newest historyLinesMax completed lines
	linesTotal int      // lines completed so far, to count what's new

	shown      []string // the snapshot being viewed, ending with the prompt line
	shownTotal int      // linesTotal when the snapshot was taken
	top        int      // index in shown of the first line on screen

	editing bool // typing a search
	query   string
	pattern *regexp.Regexp // the query as a pattern; nil when there's none
	matches []int          // indexes in shown, oldest first
	match   int            // index in matches of the selected one
}

// openHistory shows history from the end. With search, it starts with the
// search prompt open.
func (m *Model) openHistory(search bool) {
	m.mode = modeHistory
	m.hist.query, m.hist.editing = "", search
	m.snapshot()
	m.scrollTo(len(m.hist.shown))
}

// snapshot takes the lines as they are now, plus the line in progress
// (usually the prompt), and finds the query in them again.
func (m *Model) snapshot() {
	h := &m.hist
	h.shown = slices.Clip(h.lines) // appending below must copy, not touch lines
	if p := m.screen.Partial(); p != "" {
		h.shown = append(h.shown, p)
	}
	h.shownTotal = h.linesTotal
	m.findMatches()
}

func (m *Model) pageHeight() int { return max(1, m.height-1) }

// scrollTo puts line top at the top of the screen, within range.
func (m *Model) scrollTo(top int) {
	m.hist.top = min(max(0, top), max(0, len(m.hist.shown)-m.pageHeight()))
}

func (m *Model) historyKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	if m.hist.editing {
		m.searchKey(k, key)
		return m, nil
	}
	h, page := &m.hist, m.pageHeight()
	switch key {
	case "esc":
		m.mode, h.shown, h.matches = modeConsole, nil, nil
	case "up":
		m.scrollTo(h.top - 1)
	case "down":
		m.scrollTo(h.top + 1)
	case "pgup":
		m.scrollTo(h.top - page + 1)
	case "pgdown":
		m.scrollTo(h.top + page - 1)
	case "home", "g": // g and G for keyboards without Home and End
		m.scrollTo(0)
	case "end", "G":
		m.snapshot() // the latest output, too
		m.scrollTo(len(h.shown))
	case "/":
		h.editing, h.query = true, ""
		m.findMatches()
	case "n":
		m.selectMatch(h.match - 1) // older
	case "N":
		m.selectMatch(h.match + 1) // newer
	}
	return m, nil
}

// searchKey edits the search. Its keys never reach the device.
func (m *Model) searchKey(k tea.KeyPressMsg, key string) {
	h := &m.hist
	switch key {
	case "esc":
		h.editing, h.query = false, ""
		m.findMatches()
	case "enter":
		h.editing = false
		m.selectMatch(m.nearestMatch())
	case "backspace", "ctrl+h":
		runes := []rune(h.query)
		if len(runes) > 0 {
			h.query = string(runes[:len(runes)-1])
		}
		m.findMatches()
	default:
		m.typeSearch(k.Text)
	}
}

// typeSearch adds typed or pasted text to the search, up to its first line.
func (m *Model) typeSearch(s string) {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if s != "" {
		m.hist.query += s
		m.findMatches()
	}
}

// findMatches finds the query in the snapshot.
func (m *Model) findMatches() {
	h := &m.hist
	h.matches, h.pattern = h.matches[:0], nil
	if h.query == "" {
		return
	}
	h.pattern = termtext.Pattern(h.query)
	for i, line := range h.shown {
		if h.pattern.MatchString(line) {
			h.matches = append(h.matches, i)
		}
	}
	h.match = min(h.match, max(0, len(h.matches)-1))
}

// nearestMatch is the last match at or above the bottom of the screen, so a
// search looks back from where you are; with none there, the newest.
func (m *Model) nearestMatch() int {
	bottom := m.hist.top + m.pageHeight() - 1
	for i := len(m.hist.matches) - 1; i >= 0; i-- {
		if m.hist.matches[i] <= bottom {
			return i
		}
	}
	return len(m.hist.matches) - 1
}

// selectMatch selects match i, within range, and centres its line.
func (m *Model) selectMatch(i int) {
	h := &m.hist
	if len(h.matches) == 0 {
		return
	}
	h.match = min(max(0, i), len(h.matches)-1)
	m.scrollTo(h.matches[h.match] - m.pageHeight()/2)
}

// selectedStyle is the selected match: dark on blue, readable on any theme.
var selectedStyle = lipgloss.NewStyle().Background(cAccent).Foreground(cOnAccent).Bold(true)

// matchStyle is the other matches, drawn on the terminal's own background:
// the light blue is too pale on a light theme, so that gets the dark one.
func (m *Model) matchStyle() lipgloss.Style {
	fg := cAccent
	if m.light {
		fg = cAccentOnLight
	}
	return lipgloss.NewStyle().Foreground(fg).Bold(true)
}

// historyView draws the screenful of history starting at top.
func (m *Model) historyView(height int) string {
	h, selected := &m.hist, -1
	if len(h.matches) > 0 && !h.editing {
		selected = h.matches[h.match]
	}
	rows := make([]string, height)
	for r := range rows {
		if i := h.top + r; i < len(h.shown) {
			rows[r] = m.historyLine(h.shown[i], i == selected)
		}
	}
	return strings.Join(rows, "\n")
}

// historyLine draws one line cut to the window, with matches picked out. On
// the selected line, a match past the edge scrolls into view, starting a
// third of the way in.
func (m *Model) historyLine(line string, selected bool) string {
	p := m.hist.pattern
	if p == nil {
		return ansi.Truncate(line, m.width, "…")
	}
	prefix := ""
	if loc := p.FindStringIndex(line); selected && loc != nil && ansi.StringWidth(line) > m.width {
		if before, keep := line[:loc[0]], m.width/3; ansi.StringWidth(before) > keep {
			w := ansi.StringWidth(before)
			prefix, line = "…"+ansi.Cut(before, w-keep+1, w), line[loc[0]:]
		}
	}
	style := m.matchStyle()
	if selected {
		style = selectedStyle
	}
	var b strings.Builder
	b.WriteString(prefix)
	last := 0
	for _, loc := range p.FindAllStringIndex(line, -1) {
		b.WriteString(line[last:loc[0]] + style.Render(line[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(line[last:])
	return ansi.Truncate(b.String(), m.width, "…")
}

// historyBar is the status line while history is open. On a narrow window
// the hints go first, then the extra details, so esc always shows.
func (m *Model) historyBar(w int) string {
	h := &m.hist
	if h.editing {
		leads := []string{"/ " + h.query}
		if h.query != "" {
			leads = slices.Insert(leads, 0, leads[0]+"  "+plural(len(h.matches), "match", "matches"))
		}
		var layouts []layout
		for _, lead := range leads {
			layouts = append(layouts, askLayouts(lead, [][]seg{hint("enter", "find")}, "cancel")...)
		}
		return fitLine(cAccent, w, layouts)
	}
	where := "no output yet"
	if len(h.shown) > 0 {
		where = fmt.Sprintf("%d/%d", min(len(h.shown), h.top+m.pageHeight()), len(h.shown))
	}
	found := ""
	switch {
	case len(h.matches) > 0:
		found = fmt.Sprintf("  match %d/%d", h.match+1, len(h.matches))
	case h.pattern != nil:
		found = "  no matches"
	}
	fresh := ""
	if n := h.linesTotal - h.shownTotal; n > 0 {
		fresh = fmt.Sprintf("  +%d new", n)
	}
	hints := [][]seg{hint("/", "search"), hint("end/G", "latest")}
	if len(h.matches) > 0 {
		hints = append(hints, hint("n", "older"), hint("N", "newer"))
	}
	hints = append(hints, hint("↑↓", "scroll"))
	var layouts []layout
	for _, lead := range []string{"history  " + where + found + fresh, "history  " + where + found, "history  " + where, "history"} {
		layouts = append(layouts, askLayouts(lead, hints, "back")...)
	}
	return fitLine(cAccent, w, layouts)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
