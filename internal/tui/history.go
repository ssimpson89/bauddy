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
// while the device keeps printing; End brings it up to date.
type history struct {
	text       termtext.Cleaner // turns output into plain lines
	lines      []string         // the newest historyLinesMax completed lines
	linesTotal int              // lines completed so far, to count what's new

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
	m.query, m.editing = "", search
	m.snapshot()
	m.scrollTo(len(m.shown))
}

// snapshot takes the lines as they are now, plus the line in progress
// (usually the prompt), and finds the query in them again.
func (m *Model) snapshot() {
	m.shown = slices.Clip(m.lines) // appending below must copy, not touch m.lines
	if p := m.screen.Partial(); p != "" {
		m.shown = append(m.shown, p)
	}
	m.shownTotal = m.linesTotal
	m.findMatches()
}

func (m *Model) pageHeight() int { return max(1, m.height-1) }

// scrollTo puts line top at the top of the screen, within range.
func (m *Model) scrollTo(top int) {
	m.top = min(max(0, top), max(0, len(m.shown)-m.pageHeight()))
}

func (m *Model) historyKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	if m.editing {
		m.searchKey(k, key)
		return m, nil
	}
	page := m.pageHeight()
	switch key {
	case "esc", "q":
		m.mode, m.shown, m.matches = modeConsole, nil, nil
	case "up", "k":
		m.scrollTo(m.top - 1)
	case "down", "j":
		m.scrollTo(m.top + 1)
	case "pgup", "b":
		m.scrollTo(m.top - page + 1)
	case "pgdown", "space", "f":
		m.scrollTo(m.top + page - 1)
	case "home", "g":
		m.scrollTo(0)
	case "end", "G":
		m.snapshot() // the latest output, too
		m.scrollTo(len(m.shown))
	case "/":
		m.editing, m.query = true, ""
		m.findMatches()
	case "n":
		m.selectMatch(m.match - 1) // older
	case "N":
		m.selectMatch(m.match + 1) // newer
	}
	return m, nil
}

// mouseMode captures the mouse only while history is open, for the wheel.
// Everywhere else the terminal keeps it, so selecting text works as usual.
func (m *Model) mouseMode() tea.MouseMode {
	if m.mode == modeHistory {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeNone
}

// historyWheel scrolls history with the mouse.
func (m *Model) historyWheel(msg tea.MouseWheelMsg) {
	if m.mode != modeHistory {
		return
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.scrollTo(m.top - 3)
	case tea.MouseWheelDown:
		m.scrollTo(m.top + 3)
	}
}

// searchKey edits the search. Its keys never reach the device.
func (m *Model) searchKey(k tea.KeyPressMsg, key string) {
	switch key {
	case "esc":
		m.editing, m.query = false, ""
		m.findMatches()
	case "enter":
		m.editing = false
		m.selectMatch(m.nearestMatch())
	case "backspace", "ctrl+h":
		runes := []rune(m.query)
		if len(runes) > 0 {
			m.query = string(runes[:len(runes)-1])
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
		m.query += s
		m.findMatches()
	}
}

// findMatches finds the query in the snapshot.
func (m *Model) findMatches() {
	m.matches, m.pattern = m.matches[:0], nil
	if m.query == "" {
		return
	}
	m.pattern = searchPattern(m.query)
	for i, line := range m.shown {
		if m.pattern.MatchString(line) {
			m.matches = append(m.matches, i)
		}
	}
	m.match = min(m.match, max(0, len(m.matches)-1))
}

// nearestMatch is the last match at or above the bottom of the screen, so a
// search looks back from where you are; with none there, the newest.
func (m *Model) nearestMatch() int {
	bottom := m.top + m.pageHeight() - 1
	for i := len(m.matches) - 1; i >= 0; i-- {
		if m.matches[i] <= bottom {
			return i
		}
	}
	return len(m.matches) - 1
}

// selectMatch selects match i, within range, and centres its line.
func (m *Model) selectMatch(i int) {
	if len(m.matches) == 0 {
		return
	}
	m.match = min(max(0, i), len(m.matches)-1)
	m.scrollTo(m.matches[m.match] - m.pageHeight()/2)
}

// searchPattern matches q literally, with smart case: all lowercase matches
// any case ("error" finds "ERROR"); any capital matches exactly. Matching on
// the original text keeps match positions right for any characters.
func searchPattern(q string) *regexp.Regexp {
	expr := regexp.QuoteMeta(q)
	if q == strings.ToLower(q) {
		expr = "(?i)" + expr
	}
	return regexp.MustCompile(expr)
}

var (
	matchStyle    = lipgloss.NewStyle().Foreground(cAmber).Bold(true)
	selectedStyle = lipgloss.NewStyle().Background(cAmber).Foreground(cOnAmber).Bold(true)
)

// historyView draws the screenful of history starting at top.
func (m *Model) historyView(height int) string {
	selected := -1
	if len(m.matches) > 0 && !m.editing {
		selected = m.matches[m.match]
	}
	rows := make([]string, height)
	for r := range rows {
		if i := m.top + r; i < len(m.shown) {
			rows[r] = m.historyLine(m.shown[i], i == selected)
		}
	}
	return strings.Join(rows, "\n")
}

// historyLine draws one line cut to the window, with matches picked out. On
// the selected line, a match past the edge scrolls into view, starting a
// third of the way in.
func (m *Model) historyLine(line string, selected bool) string {
	if m.pattern == nil {
		return ansi.Truncate(line, m.width, "…")
	}
	prefix := ""
	if loc := m.pattern.FindStringIndex(line); selected && loc != nil && ansi.StringWidth(line) > m.width {
		if before, keep := line[:loc[0]], m.width/3; ansi.StringWidth(before) > keep {
			w := ansi.StringWidth(before)
			prefix, line = "…"+ansi.Cut(before, w-keep+1, w), line[loc[0]:]
		}
	}
	style := matchStyle
	if selected {
		style = selectedStyle
	}
	var b strings.Builder
	b.WriteString(prefix)
	last := 0
	for _, loc := range m.pattern.FindAllStringIndex(line, -1) {
		b.WriteString(line[last:loc[0]] + style.Render(line[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(line[last:])
	return ansi.Truncate(b.String(), m.width, "…")
}

// historyBar is the status line while history is open.
func (m *Model) historyBar(w int) string {
	if m.editing {
		lead := "/ " + m.query
		if m.query != "" {
			lead += "  " + plural(len(m.matches), "match", "matches")
		}
		return amber(w, lead, [][]seg{hint("enter", "find")}, "cancel")
	}
	where := "nothing yet"
	if len(m.shown) > 0 {
		where = fmt.Sprintf("line %d of %d", min(len(m.shown), m.top+m.pageHeight()), len(m.shown))
	}
	switch {
	case len(m.matches) > 0:
		where += fmt.Sprintf("  match %d of %d", m.match+1, len(m.matches))
	case m.pattern != nil:
		where += "  no matches"
	}
	if n := m.linesTotal - m.shownTotal; n > 0 {
		where += fmt.Sprintf("  +%d new", n)
	}
	hints := [][]seg{hint("↑↓", "scroll"), hint("/", "search")}
	if len(m.matches) > 0 {
		hints = append(hints, hint("n/N", "older/newer"))
	}
	hints = append(hints, hint("end", "latest"))
	return amber(w, "history  "+where, hints, "back")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
