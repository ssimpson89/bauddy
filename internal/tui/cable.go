package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

// ErrQuit means you quit before choosing a cable.
var ErrQuit = errors.New("quit before choosing a cable")

// cablePoll is how often the cable screen looks at the ports.
const cablePoll = 500 * time.Millisecond

// PickCable finds the cable to connect to, starting from ports, the ports
// already there. With none, it waits for one (showing hint) and connects to
// it, so a lone cable never needs a key. With several, it lists them and
// waits for you to choose: while the list is up it never connects on its
// own, even if a cable is pulled and one is left (pull them all, and it
// waits again). The list follows cables as they're plugged in and pulled
// out, and the cursor jumps to the one just plugged in, which is how you
// tell identical cables apart. known says what bauddy remembers about a
// port ("core-sw · 9600 last time"), and inUse whether another bauddy has
// its console open: those are tagged, listed last, and joined if chosen.
// q, esc or ctrl+c gives up with
// ErrQuit. Other keys are dropped, so nothing reaches the device later. If
// listing ports fails, it keeps trying.
func PickCable(ports []serialx.PortInfo, hint string, known func(serialx.PortInfo) string, inUse func(serialx.PortInfo) bool) (serialx.PortInfo, error) {
	m := newCableModel(ports, hint, known, inUse, serialx.List)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		return serialx.PortInfo{}, err
	}
	if m.chosen == nil {
		return serialx.PortInfo{}, ErrQuit
	}
	return *m.chosen, nil
}

// Rows are tracked by path, not Key: identical cheap cables share a serial
// number, and their Keys change as their twins come and go.
type cableModel struct {
	hint          string
	known         func(serialx.PortInfo) string
	inUse         func(serialx.PortInfo) bool
	list          func() ([]serialx.PortInfo, error)
	width, height int
	light         bool // the terminal has a light background

	rows        []serialx.PortInfo // in the order they appeared, so rows don't jump
	cursor      int
	listing     bool            // the list is up, so only you connect; never with no rows
	justPlugged string          // path of the cable plugged in while listing, if still there
	lastLook    []string        // paths at the last look: waiting goes on when they repeat
	open        map[string]bool // paths whose console another bauddy has open
	chosen      *serialx.PortInfo
}

func newCableModel(ports []serialx.PortInfo, hint string, known func(serialx.PortInfo) string, inUse func(serialx.PortInfo) bool, list func() ([]serialx.PortInfo, error)) *cableModel {
	m := &cableModel{hint: hint, known: known, inUse: inUse, list: list, listing: len(ports) > 0, rows: slices.Clone(ports)}
	m.checkOpen(ports)
	m.openLast()
	return m
}

// openLast puts cables open in another window last, with the cursor on the
// first, as the list goes up: a second window is usually for the other
// cable. After that rows stay put.
func (m *cableModel) openLast() {
	slices.SortStableFunc(m.rows, func(a, b serialx.PortInfo) int {
		switch {
		case m.open[a.Path] == m.open[b.Path]:
			return 0
		case m.open[b.Path]:
			return -1
		}
		return 1
	})
	m.cursor = 0
}

// checkOpen notes which ports' consoles another bauddy has open.
func (m *cableModel) checkOpen(ports []serialx.PortInfo) {
	m.open = map[string]bool{}
	for _, p := range ports {
		m.open[p.Path] = m.inUse != nil && m.inUse(p)
	}
}

type cableTick struct{}

func (m *cableModel) Init() tea.Cmd {
	return tea.Batch(m.tick(), tea.RequestBackgroundColor) // the box's colours suit the terminal
}

func (m *cableModel) tick() tea.Cmd {
	return tea.Tick(cablePoll, func(time.Time) tea.Msg { return cableTick{} })
}

func (m *cableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	case cableTick:
		ports, err := m.list()
		if err != nil {
			return m, m.tick() // not a look
		}
		m.follow(ports)
		m.checkOpen(ports)
		// Waiting: go on once the same ports are there at two looks in a
		// row, so all of a multi-port adapter's ports are in, then connect
		// to a lone one or list several.
		paths := portPaths(ports)
		if !m.listing && len(ports) > 0 && slices.Equal(paths, m.lastLook) {
			if len(ports) == 1 {
				m.chosen = &ports[0]
				return m, tea.Quit
			}
			m.listing = true
			m.openLast()
		}
		m.lastLook = paths
		return m, m.tick()
	}
	return m, nil
}

func (m *cableModel) key(k string) tea.Cmd {
	switch k {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	}
	if !m.listing {
		return nil
	}
	switch k {
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.rows)-1, m.cursor+1)
	case "enter":
		m.chosen = &m.rows[m.cursor]
		return tea.Quit
	default:
		if len(k) == 1 && k[0] >= '1' && int(k[0]-'0') <= min(9, len(m.rows)) {
			m.chosen = &m.rows[k[0]-'1']
			return tea.Quit
		}
	}
	return nil
}

// follow brings the rows in line with ports. A pulled cable's row goes; the
// cursor stays on its cable, or if that was the one pulled, on the row that
// moves up into its place. A new cable joins the end, and while listing the
// cursor moves to it. (While waiting, new ports aren't news: they're simply
// what turned up.)
func (m *cableModel) follow(ports []serialx.PortInfo) {
	now := map[string]serialx.PortInfo{}
	for _, p := range ports {
		now[p.Path] = p
	}
	at := ""
	if m.cursor < len(m.rows) {
		at = m.rows[m.cursor].Path
	}
	var rows []serialx.PortInfo
	had := map[string]bool{}
	for _, r := range m.rows {
		if p, ok := now[r.Path]; ok {
			rows = append(rows, p)
			had[r.Path] = true
		}
	}
	for _, p := range ports {
		if !had[p.Path] {
			rows = append(rows, p)
			if m.listing {
				at, m.justPlugged = p.Path, p.Path
			}
		}
	}
	m.rows = rows
	if len(rows) == 0 {
		m.listing = false // all pulled: wait for a cable again
	}
	m.cursor = min(m.cursor, max(0, len(rows)-1))
	for i, r := range rows {
		if r.Path == at {
			m.cursor = i
		}
	}
	if _, ok := now[m.justPlugged]; !ok {
		m.justPlugged = ""
	}
}

func portPaths(ports []serialx.PortInfo) []string {
	paths := make([]string, len(ports))
	for i, p := range ports {
		paths[i] = p.Path
	}
	slices.Sort(paths)
	return paths
}

func (m *cableModel) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
	}
	h := max(1, m.height-1)
	boxStyle, keyStyle, dimStyle := overlayStyle(m.light)
	// In the box if it fits, wrapped to the window; else the lines alone.
	inner, innerW := h-boxStyle.GetVerticalFrameSize(), m.width-boxStyle.GetHorizontalFrameSize()
	var body string
	if innerW >= 20 && inner >= 3 {
		box := boxStyle.Render(strings.Join(m.screenLines(inner, innerW, keyStyle, dimStyle), "\n"))
		if lipgloss.Height(box) <= h {
			body = overlay(m.width, h, box)
		}
	}
	if body == "" {
		lines := m.screenLines(h, 0, keyStyle, dimStyle)
		for i := range lines {
			lines[i] = ansi.Truncate(" "+lines[i], m.width, "")
		}
		for len(lines) < h {
			lines = append(lines, "")
		}
		body = strings.Join(lines[:h], "\n")
	}
	v := tea.NewView(body + "\n" + m.bar(m.width))
	v.AltScreen = true
	return v
}

// screenLines is what the screen says, in h lines of width w (0: any): the
// list of cables, or the hint while waiting, wrapped to w.
func (m *cableModel) screenLines(h, w int, keyStyle, dimStyle lipgloss.Style) []string {
	if m.listing {
		return m.listLines(h, w, keyStyle, dimStyle)
	}
	var lines []string
	for i, l := range strings.Split(m.hint, "\n") {
		st := dimStyle
		if i == 0 {
			st = lipgloss.NewStyle().Bold(true) // the instruction; the rest are fallbacks
		}
		if w > 0 {
			st = st.Width(min(w, lipgloss.Width(l)))
		}
		lines = append(lines, strings.Split(st.Render(l), "\n")...)
	}
	return lines
}

// listLines is the list of cables in h lines of width w (0: any): as many
// rows as fit, the cursor's among them, with the question and the tip when
// there's room. Rows are cut to w; the tip wraps.
func (m *cableModel) listLines(h, w int, keyStyle, dimStyle lipgloss.Style) []string {
	tip := "Not sure which? Unplug yours and plug it back in, and it's marked."
	if len(m.rows) == 1 {
		tip = "Plug your cable back in, or press enter to use this one."
	}
	if w > 0 {
		dimStyle = dimStyle.Width(min(w, lipgloss.Width(tip)))
	}
	head := []string{keyStyle.Render("Which cable?"), ""}
	foot := append([]string{""}, strings.Split(dimStyle.Render(tip), "\n")...) // a wrapped tip is several lines
	if h < len(head)+len(foot)+1 {
		head, foot = nil, nil
	}
	fit := max(1, h-len(head)-len(foot))
	from := min(max(0, m.cursor-fit+1), max(0, len(m.rows)-fit)) // scroll so the cursor shows
	to := min(len(m.rows), from+fit)

	names, ports := 0, 0
	for _, r := range m.rows {
		names = max(names, lipgloss.Width(cableName(r)))
		ports = max(ports, lipgloss.Width(filepath.Base(r.Path)))
	}
	pad := func(s string, w int) string { return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))) }
	lines := head
	for i := from; i < to; i++ {
		r := m.rows[i]
		mark, num := "  ", "  "
		if i == m.cursor {
			mark = "▸ "
		}
		if i < 9 { // only 1-9 can be pressed
			num = fmt.Sprintf("%d ", i+1)
		}
		line := mark + num + " " + pad(cableName(r), names) + "  " + pad(filepath.Base(r.Path), ports)
		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		// The mark goes before what bauddy remembers, so a narrow window
		// cuts that first.
		if r.Path == m.justPlugged {
			line += "  " + lipgloss.NewStyle().Background(cAccent).Foreground(cOnAccent).Render(" just plugged in ")
		}
		if m.open[r.Path] {
			line += dimStyle.UnsetWidth().Render("  open in bauddy")
		}
		if m.known != nil {
			if s := m.known(r); s != "" {
				line += dimStyle.UnsetWidth().Render("  " + s)
			}
		}
		if w > 0 {
			line = ansi.Truncate(line, w, "")
		}
		lines = append(lines, line)
	}
	return append(lines, foot...)
}

// cableName is what a stressed user recognises: the adapter's own name, or
// "built-in" for the computer's own port.
func cableName(p serialx.PortInfo) string {
	switch {
	case p.Product != "":
		return p.Product
	case p.USB:
		return "USB serial cable"
	}
	return "built-in"
}

// bar is the status line: calm grey while waiting, since plugging in after
// starting bauddy is a normal first step; blue while asking which cable.
func (m *cableModel) bar(w int) string {
	if m.listing {
		quit := []seg{{"q", cOnAccent, true}, {" quit ", cOnAccent, false}}
		return fitLine(cAccent, w, []layout{
			{slices.Concat([]seg{{" ", nil, false}}, hint("↑↓", "choose"), hint("enter", "connect")), quit},
			{slices.Concat([]seg{{" ", nil, false}}, hint("enter", "connect")), quit},
		})
	}
	quit := []seg{{"q", cAccent, true}, {" quit ", cText, false}}
	return fitLine(cBar, w, []layout{
		{[]seg{{" ○ waiting for a USB console cable…", cText, false}}, quit},
		{[]seg{{" ○ waiting…", cText, false}}, quit},
	})
}
