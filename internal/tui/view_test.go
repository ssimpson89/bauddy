package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/logfile"
	"github.com/ssimpson89/bauddy/internal/serialx"
)

func TestFitLineNeverExceedsWidth(t *testing.T) {
	layouts := []layout{
		{[]seg{{"core-sw-01  115200 ●  garbled? ctrl+] d", nil, false}}, []seg{{"ctrl+] menu ", nil, false}}},
		{[]seg{{"115200 ●", nil, false}}, []seg{{"ctrl+] ", nil, false}}},
	}
	for w := 1; w <= 80; w++ {
		if got := lipgloss.Width(fitLine(cBar, w, layouts)); got != w {
			t.Errorf("width %d: rendered %d cells", w, got)
		}
	}
}

// bar renders the command bar for m at width w as plain text.
func bar(m *Model, w int) string { return ansi.Strip(m.commandLine(w)) }

// has reports whether the bar shows key k with its label, e.g. "b baud".
func has(s, hint string) bool { return strings.Contains(s, " "+hint+" ") }

func newBarModel() *Model { return &Model{keys: keymap.New(keymap.DefaultEscapeKey)} }

// The left-hand keys from least to most needed: the drop order the Keep
// values in keymap.New give.
var dropOrder = []string{"S resize", "c clear", "r reconnect", "B break", "l log", "b baud", "d detect"}

// The left-hand keys in the order the bar shows them.
var shownOrder = []string{"? help", "b baud", "d detect", "r reconnect", "l log", "c clear", "B break", "S resize"}

// pinned reports whether s shows ?, q and esc, spelled out or not.
func pinned(s string) bool {
	s = strings.TrimRight(s, " ") // a bar cut at the window edge loses its last space
	return strings.HasPrefix(s, " ? ") &&
		(strings.Contains(s, " q quit ") || strings.Contains(s, " q  ")) &&
		(strings.HasSuffix(s, " esc back") || strings.HasSuffix(s, " esc"))
}

func TestCommandBarIsAlwaysTheWindowWidth(t *testing.T) {
	for _, esc := range []string{keymap.DefaultEscapeKey, "ctrl+t"} {
		for _, n := range []string{"", "z isn't a command; ? shows the keys", "can't set 9600 baud: 端口忙 🔌"} {
			m := &Model{keys: keymap.New(esc), note: note{text: n}}
			for w := 1; w <= 300; w++ {
				if got := lipgloss.Width(m.commandLine(w)); got != w {
					t.Errorf("escape %s, note %q, width %d: rendered %d cells", esc, n, w, got)
				}
			}
		}
	}
}

func TestCommandBarAtEveryWidth(t *testing.T) {
	m := newBarModel()
	for w := 16; w <= 200; w++ {
		s := bar(m, w)
		if !pinned(s) {
			t.Errorf("width %d: ?, q or esc missing: %q", w, s)
		}
		// Keys drop strictly in priority order: if one is shown, every
		// more-needed key is too.
		for i, k := range dropOrder {
			if has(s, k) {
				for _, more := range dropOrder[i+1:] {
					if !has(s, more) {
						t.Errorf("width %d: %q shown but more-needed %q dropped: %q", w, k, more, s)
					}
				}
			}
		}
		all := true
		for _, k := range dropOrder {
			all = all && has(s, k)
		}
		if all != has(s, "? help") || all == has(s, "? more") {
			t.Errorf("width %d: ? should read help only when every key shows: %q", w, s)
		}
		if strings.Contains(s, "│") != (has(s, "B break") || has(s, "S resize")) {
			t.Errorf("width %d: divider should show only with a capital key: %q", w, s)
		}
	}
	if s := bar(m, 200); !has(s, "q quit") || !strings.Contains(s, "esc back") {
		t.Errorf("wide bar should spell out quit and back: %q", s)
	}
}

func TestCommandBarKeysBeforeADroppedOneStayPut(t *testing.T) {
	m := newBarModel()
	wide := bar(m, 200)
	for w := 30; w < 200; w++ {
		s := bar(m, w)
		for _, k := range shownOrder[1:] {
			if !has(s, k) {
				break // keys after the first dropped one close up
			}
			if strings.Index(s, k) != strings.Index(wide, k) {
				t.Errorf("width %d: %q moved:\n%q\n%q", w, k, wide, s)
			}
		}
	}
}

// onlyOne is the widest bar that shows just one of the droppable keys.
func onlyOne(t *testing.T, m *Model) (int, string) {
	t.Helper()
	for w := 200; w > 16; w-- {
		s := bar(m, w)
		n := 0
		for _, k := range dropOrder {
			if has(s, k) {
				n++
			}
		}
		if n == 1 {
			return w, s
		}
	}
	t.Fatal("no width shows exactly one droppable key")
	return 0, ""
}

func TestCommandBarPromotesReconnectWhenUnplugged(t *testing.T) {
	m := newBarModel()
	if w, s := onlyOne(t, m); !has(s, "d detect") {
		t.Errorf("width %d: connected, the last key standing should be d: %q", w, s)
	}
	m.state = serialx.Disconnected
	if w, s := onlyOne(t, m); !has(s, "r reconnect") {
		t.Errorf("width %d: unplugged, the last key standing should be r: %q", w, s)
	}
}

func TestCommandBarFitsANote(t *testing.T) {
	m := newBarModel()
	m.note = note{text: "z isn't a command; ? shows the keys"}
	for _, w := range []int{200, 100, 80, 50} {
		s := bar(m, w)
		if !strings.Contains(s, "z isn't") || !has(s, "q quit") {
			t.Errorf("width %d: note or quit missing: %q", w, s)
		}
		if got := lipgloss.Width(m.commandLine(w)); got != w {
			t.Errorf("width %d: rendered %d cells", w, got)
		}
	}
	shortened, dropped := false, false
	for w := 60; w >= 16; w-- {
		s := bar(m, w)
		if i := strings.Index(s, "…"); i >= 0 {
			shortened = true
			start := strings.Index(s, "z isn")
			if got := lipgloss.Width(s[start : i+len("…")]); got < noteMin {
				t.Errorf("width %d: note cut to %d cells, below %d: %q", w, got, noteMin, s)
			}
		}
		dropped = dropped || !strings.Contains(s, "z isn")
		if !has(s, "q") && !has(s, "q quit") {
			t.Errorf("width %d: quit missing with a note: %q", w, s)
		}
	}
	if !shortened || !dropped {
		t.Errorf("as the bar narrows, a note should be shortened, then dropped (shortened %v, dropped %v)", shortened, dropped)
	}
}

func TestCommandBarStopLog(t *testing.T) {
	m := newBarModel()
	m.log = &logfile.Log{} // any open log; the bar only checks for one
	if s := bar(m, 200); !has(s, "l stop log") {
		t.Errorf("while logging, l should read stop log: %q", s)
	}
}
