package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/serialx"
)

// Palette. Amber is bauddy's one accent; red only means "something's wrong".
var (
	cAmber   = lipgloss.Color("#E8A33D")
	cOnAmber = lipgloss.Color("#2A1A00")
	cBar     = lipgloss.Color("#262626")
	cText    = lipgloss.Color("#D0D0D0")
	cBright  = lipgloss.Color("#ECECEC")
	cDim     = lipgloss.Color("#9A9A9A")
	cGreen   = lipgloss.Color("#5FB760")
	cRedBar  = lipgloss.Color("#5A1F1B")
	cOnRed   = lipgloss.Color("#FFD9D4")

	// Overlays draw on the terminal's own background, so they need darker
	// text on light themes: cAmber and cDim have poor contrast on white. The
	// status bar sets its own background and works on any theme.
	cAmberOnLight = lipgloss.Color("#8A5200")

	cDivider    = lipgloss.Color("#8A5200") // the command bar's divider, on amber
	cDimOnLight = lipgloss.Color("#5C5C5C")
)

// overlayStyles returns the box, key and dim styles for the help and baud
// overlays, suited to the terminal's background.
func (m *Model) overlayStyles() (box, key, dim lipgloss.Style) {
	accent, faint := color.Color(cAmber), color.Color(cDim)
	if m.light {
		accent, faint = cAmberOnLight, cDimOnLight
	}
	box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 2)
	return box, lipgloss.NewStyle().Foreground(accent).Bold(true), lipgloss.NewStyle().Foreground(faint)
}

// seg is one run of text on the status line. Every seg is rendered with the
// bar's background, so the bar never has holes in it.
type seg struct {
	text string
	fg   color.Color
	bold bool
}

func width(ss []seg) int {
	n := 0
	for _, s := range ss {
		n += lipgloss.Width(s.text)
	}
	return n
}

func paint(bg color.Color, ss []seg) string {
	var sb strings.Builder
	for _, s := range ss {
		sb.WriteString(lipgloss.NewStyle().Background(bg).Foreground(s.fg).Bold(s.bold).Render(s.text))
	}
	return sb.String()
}

// line lays out left and right on one row of w cells with bg behind all of
// it. If they don't fit, the row is cut at w.
func line(bg color.Color, w int, left, right []seg) string {
	gap := max(1, w-width(left)-width(right))
	return ansi.Truncate(paint(bg, slices.Concat(left, []seg{{text: strings.Repeat(" ", gap)}}, right)), w, "")
}

// layout is one way to arrange the status line.
type layout struct{ left, right []seg }

// fitLine renders the first layout that fits in w, or the last one, cut to w.
func fitLine(bg color.Color, w int, layouts []layout) string {
	for _, l := range layouts {
		if width(l.left)+width(l.right)+1 <= w { // +1: at least one cell between them
			return line(bg, w, l.left, l.right)
		}
	}
	last := layouts[len(layouts)-1]
	return line(bg, w, last.left, last.right)
}

// View draws the device's screen, or an overlay, above the status line.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
	}
	paneH := max(1, m.height-1)

	var body string
	switch m.mode {
	case modeHelp:
		body = overlay(m.width, paneH, m.helpBox())
	case modeBaud:
		body = overlay(m.width, paneH, m.baudBox())
	case modeSearch, modeSearchResults:
		body = overlay(m.width, paneH, m.searchBox())
	default:
		body = m.emu.Render()
	}

	v := tea.NewView(body + "\n" + m.statusLine())
	v.AltScreen = true
	if m.mode == modeConsole { // no cursor over overlays
		pos := m.emu.CursorPosition()
		v.Cursor = tea.NewCursor(pos.X, pos.Y)
	}
	return v
}

// overlay centres box in a w×h pane, cropping it if the window is too small.
func overlay(w, h int, box string) string {
	lines := strings.Split(box, "\n")
	lines = lines[:min(len(lines), h)]
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "")
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}

// hint is a key and what it does, for the amber bars: "enter send".
func hint(key, label string) []seg {
	return []seg{{key, cOnAmber, true}, {" " + label + "  ", cOnAmber, false}}
}

// amber renders a bar asking for a key: the lead text and hints on the left,
// esc on the right. Hints are dropped from the end if the window is narrow.
func amber(w int, lead string, hints [][]seg, esc string) string {
	left := []seg{{" " + lead, cOnAmber, true}}
	if lead != "" {
		left[0].text += "  "
	}
	right := []seg{{"esc", cOnAmber, true}, {" " + esc + " ", cOnAmber, false}}
	var layouts []layout
	for n := len(hints); n >= 0; n-- {
		layouts = append(layouts, layout{slices.Concat(left, slices.Concat(hints[:n]...)), right})
	}
	return fitLine(cAmber, w, layouts)
}

// statusLine renders the one-row bar. The key to the commands and the baud
// always show; on a narrow window the other parts give way in a fixed order.
func (m *Model) statusLine() string {
	w := m.width
	switch m.mode {
	case modeCommand:
		return m.commandLine(w)
	case modeBaud:
		return amber(w, "", [][]seg{hint("↑↓", "choose"), hint("enter", "set"), hint("d", "detect")}, "cancel")
	case modeHelp:
		return line(cAmber, w, []seg{{" any key closes", cOnAmber, false}}, nil)
	case modePasteConfirm:
		return amber(w, m.confirm.text(), [][]seg{hint("enter", "send"), hint("l", "log and send")}, "cancel")
	case modeProbeOffer:
		return amber(w, "nothing heard.", [][]seg{hint("enter", "send Enter at each baud rate")}, "skip")
	case modeSearch:
		return amber(w, "search", [][]seg{hint("enter", "show")}, "cancel")
	case modeSearchResults:
		return amber(w, "search", [][]seg{hint("n", "older"), hint("N", "newer"), hint("/", "edit")}, "close")
	}
	return m.mainLine(w)
}

// mainLine is the bar in console mode: name, rate, state and any message,
// with the key to the commands on the right. It turns red when something
// is wrong.
func (m *Model) mainLine(w int) string {
	bad := m.state != serialx.Connected || m.note.kind == noteError
	bg, fg, accent := cBar, cText, cAmber
	if bad {
		bg, fg, accent = cRedBar, cOnRed, cOnRed
	}
	state, stateShort := seg{"●", cGreen, false}, seg{"●", cGreen, false}
	if m.state != serialx.Connected {
		state = seg{"○ adapter unplugged, waiting…", cOnRed, true}
		stateShort = seg{"○ unplugged", cOnRed, true}
	}

	sp := []seg{{" ", nil, false}}
	name := []seg{{m.opt.Name + "  ", cBright, false}}
	if bad {
		name[0].fg = cOnRed
	}
	rate := strconv.Itoa(m.sess.Baud())
	if f := m.sess.Framing(); f != serialx.Framing8N1 {
		rate += " " + f.String() // only mention framing when it's unusual
	}
	baud := seg{rate + " ", fg, false}
	var logging []seg
	if m.log != nil {
		logging = []seg{{" log", cBright, true}}
		if bad {
			logging[0].fg = cOnRed
		}
	}
	var note, noteShort []seg
	switch {
	case m.paste != nil:
		progress := fmt.Sprintf("  pasting %d/%d", m.paste.next+1, len(m.paste.lines))
		note = []seg{{progress + " · ", accent, false}, {"esc", accent, true}, {" stops", accent, false}}
		noteShort = []seg{{progress, accent, false}}
	case m.note.text != "":
		note = []seg{{"  " + m.note.text, accent, m.note.kind == noteError}}
		noteShort = note
	case m.meter.Garbled() && m.state == serialx.Connected:
		note = []seg{{"  garbled? ", accent, false}, {m.keys.Escape + " d", accent, true}, {" detect", accent, false}}
		noteShort = []seg{{"  ", nil, false}, {m.keys.Escape + " d", accent, true}}
	case m.quiet:
		note = []seg{{"  quiet · nothing received yet", accent, false}}
		noteShort = []seg{{"  quiet", accent, false}}
	}
	keys := []seg{{m.keys.Escape, accent, true}, {" keys ", fg, false}}
	short := []seg{{shortKey(m.keys.Escape), accent, true}, {" keys ", fg, false}}

	return fitLine(bg, w, []layout{ // widest first; each step drops or shortens something
		{slices.Concat(sp, name, []seg{baud, state}, logging, note), keys},
		{slices.Concat(sp, name, []seg{baud, state}, logging, note), short},
		{slices.Concat(sp, []seg{baud, state}, logging, note), short},
		{slices.Concat(sp, []seg{baud, stateShort}, logging, noteShort), short},
		{slices.Concat(sp, []seg{baud, stateShort}), short},
	})
}

// shortKey abbreviates a key for narrow windows: "ctrl+]" -> "^]".
func shortKey(k string) string {
	if rest, ok := strings.CutPrefix(k, "ctrl+"); ok {
		return "^" + rest
	}
	return k
}

// barKey is one key on the command bar.
type barKey struct {
	key, label string
	keep       int  // higher stays longer on a narrow bar
	sends      bool // sends something to the device: goes after the divider
	help       bool // the ? key, which reads "more" once anything is hidden
}

// commandLine is the amber bar in command mode.
func (m *Model) commandLine(w int) string {
	var keys []barKey
	var quit, back barKey
	for _, b := range m.keys.Commands {
		if b.Short == "" {
			continue
		}
		k := barKey{key: b.Key, label: b.Short, keep: b.Keep, sends: b.Sends, help: b.Action == keymap.ActHelp}
		switch b.Action {
		case keymap.ActQuit:
			quit = k
			continue
		case keymap.ActLeaveCommand:
			back = k
			continue
		case keymap.ActLog:
			if m.log != nil {
				k.label = "stop log"
			}
		case keymap.ActReconnect: // unplugged: reconnect is what you need now
			if m.state != serialx.Connected {
				k.keep = keymap.KeepAlways - 1
			}
		}
		keys = append(keys, k)
	}
	return commandBar(w, keys, quit, back, m.note.text)
}

// noteMin is the narrowest a message on the command bar is cut to before
// it's dropped.
const noteMin = 12

// commandBar lays out the command bar: keys on the left in a fixed order,
// then any message, then quit and back pinned on the right. When it doesn't
// fit, it gives way in this order: the lowest-keep keys (the order never
// changes; keys after a dropped one close up), then the message is shortened
// and dropped, then "esc back" becomes "esc", "q quit" becomes "q", and "?"
// loses its label, so ? q and esc stay whole down to 12 cells.
func commandBar(w int, keys []barKey, quit, back barKey, note string) string {
	shown := make([]bool, len(keys))
	var drop []int // indexes of keys that may go, least needed first
	for i, k := range keys {
		shown[i] = true
		if k.keep < keymap.KeepAlways {
			drop = append(drop, i)
		}
	}
	slices.SortStableFunc(drop, func(a, b int) int { return keys[a].keep - keys[b].keep })

	quitFull, backFull, helpLabel := true, true, true
	for {
		left := barLeft(keys, shown, helpLabel)
		right := barRight(note, quit, back, quitFull, backFull)
		over := width(left) + 1 + width(right) - w // +1: at least one cell between them
		switch {
		case over <= 0:
			return line(cAmber, w, left, right)
		case len(drop) > 0:
			shown[drop[0]], drop = false, drop[1:]
		case note != "" && lipgloss.Width(note) > noteMin:
			note = ansi.Truncate(note, max(noteMin, lipgloss.Width(note)-over), "…")
		case note != "":
			note = ""
		case backFull:
			backFull = false
		case quitFull:
			quitFull = false
		case helpLabel:
			helpLabel = false
		default:
			return line(cAmber, w, left, right) // cut at the edge
		}
	}
}

// barLeft renders the shown keys, with a divider before the first one that
// sends to the device. Without helpLabel, the ? key is shown bare.
func barLeft(keys []barKey, shown []bool, helpLabel bool) []seg {
	hidden := slices.Contains(shown, false)
	out := []seg{{" ", cOnAmber, false}}
	divided := false
	for i, k := range keys {
		if !shown[i] {
			continue
		}
		if k.sends && !divided {
			out = append(out, seg{"│  ", cDivider, false})
			divided = true
		}
		label := k.label
		switch {
		case k.help && !helpLabel:
			out = append(out, seg{k.key + "  ", cOnAmber, true})
			continue
		case k.help && hidden:
			label = "more"
		}
		out = append(out, hint(k.key, label)...)
	}
	return out
}

// barRight renders the message and the pinned quit and back keys.
func barRight(note string, quit, back barKey, quitFull, backFull bool) []seg {
	var out []seg
	if note != "" {
		out = append(out, seg{note + "   ", cOnAmber, true})
	}
	if quitFull {
		out = append(out, hint(quit.key, quit.label)...)
	} else {
		out = append(out, seg{quit.key + "  ", cOnAmber, true})
	}
	if backFull {
		out = append(out, seg{back.key, cOnAmber, true}, seg{" " + back.label + " ", cOnAmber, false})
	} else {
		out = append(out, seg{back.key + " ", cOnAmber, true})
	}
	return out
}

func (m *Model) helpBox() string {
	boxStyle, keyStyle, dimStyle := m.overlayStyles()
	var sb strings.Builder
	sb.WriteString(keyStyle.Render("bauddy keys") + dimStyle.Render("  "+m.opt.Version) + "\n\n")
	sb.WriteString(dimStyle.Render(m.keys.Intro()) + "\n")
	for _, g := range m.keys.Groups() {
		sb.WriteString("\n" + g.Title + "\n")
		for _, b := range g.Bindings {
			sb.WriteString("  " + keyStyle.Render(fmt.Sprintf("%-8s", b.Key)) + " " + b.Help + "\n")
		}
	}
	return boxStyle.Render(strings.TrimRight(sb.String(), "\n"))
}

func (m *Model) baudBox() string {
	boxStyle, keyStyle, dimStyle := m.overlayStyles()
	var sb strings.Builder
	sb.WriteString(keyStyle.Render("baud rate") + "\n\n")
	cur := m.sess.Baud()
	for i, b := range baudChoices {
		cursor, mark := "  ", ""
		if i == m.baudCursor {
			cursor = keyStyle.Render("▸ ")
		}
		if b == cur {
			mark = dimStyle.Render("  current")
		}
		fmt.Fprintf(&sb, "%s%s %d%s\n", cursor, dimStyle.Render(strconv.Itoa(i+1)), b, mark)
	}
	sb.WriteString("\n" + dimStyle.Render("Not sure? Press d to detect it.") + "\n")
	sb.WriteString(dimStyle.Render("Other rates: bauddy -b N"))
	return boxStyle.Render(sb.String())
}
