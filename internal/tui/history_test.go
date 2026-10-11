package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/keymap"
)

// historyModel has n lines of output and a prompt, in a w×h window.
func historyModel(n, w, h int) *Model {
	m := &Model{width: w, height: h, keys: keymap.New(keymap.DefaultEscapeKey)}
	for i := range n {
		m.hist.lines = append(m.hist.lines, fmt.Sprintf("line %d", i))
	}
	m.screen.Feed([]byte("core-sw-01#"))
	return m
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		_, _ = m.historyKey(tea.KeyPressMsg{Text: k}, k)
	}
}

func rows(m *Model) []string {
	return strings.Split(ansi.Strip(m.historyView(m.pageHeight())), "\n")
}

func hbar(m *Model, w int) string { return ansi.Strip(m.historyBar(w)) }

func TestHistoryKeepsPlainTextLinesBounded(t *testing.T) {
	m := &Model{}
	m.rememberLines(m.screen.Feed([]byte("\x1b[1mone\x1b[0m\r\ntwo\n")))
	if want := []string{"one", "two"}; !reflect.DeepEqual(m.hist.lines, want) {
		t.Fatalf("lines = %#v, want %#v", m.hist.lines, want)
	}
	m.hist.lines = make([]string, historyLinesMax)
	m.rememberLines(m.screen.Feed([]byte("latest\n")))
	if l := m.hist.lines; len(l) != historyLinesMax || l[len(l)-1] != "latest" {
		t.Errorf("not bounded: len %d, last %q", len(l), l[len(l)-1])
	}
}

func TestHistoryOpensAtTheEndWithThePrompt(t *testing.T) {
	m := historyModel(100, 80, 24)
	m.openHistory(false)
	r := rows(m)
	if len(r) != 23 || r[22] != "core-sw-01#" || r[21] != "line 99" {
		t.Fatalf("last rows = %q, %q (of %d)", r[21], r[22], len(r))
	}
	if b := hbar(m, 80); !strings.Contains(b, "101/101") {
		t.Errorf("bar should show the position: %q", b)
	}
}

func TestHistoryScrollsWithinRange(t *testing.T) {
	m := historyModel(100, 80, 24) // 101 lines, 23 on screen: top runs 0..78
	m.openHistory(false)
	for _, c := range []struct {
		key string
		top int
	}{
		{"up", 77},
		{"pgup", 55}, // a page, less one line of overlap
		{"home", 0},
		{"up", 0},
		{"down", 1},
		{"pgdown", 23},
		{"G", 78},
		{"down", 78},
		{"g", 0},
	} {
		press(m, c.key)
		if m.hist.top != c.top {
			t.Errorf("after %s: top = %d, want %d", c.key, m.hist.top, c.top)
		}
	}
	press(m, "q")
	if m.mode != modeHistory {
		t.Error("q shouldn't close history; esc does")
	}
	press(m, "esc")
	if m.mode != modeConsole {
		t.Error("esc should close history")
	}
}

func TestHistoryHoldsStillUntilEnd(t *testing.T) {
	m := historyModel(100, 80, 24)
	m.openHistory(false)
	press(m, "home")
	m.rememberLines(m.screen.Feed([]byte("new 1\nnew 2\nnew 3\n")))
	if r := rows(m); r[0] != "line 0" {
		t.Errorf("view moved when output arrived: first row %q", r[0])
	}
	if b := hbar(m, 120); !strings.Contains(b, "+3 new") {
		t.Errorf("bar should count the new lines: %q", b)
	}
	press(m, "end")
	if r := rows(m); r[len(r)-1] != "new 3" {
		t.Errorf("end should show the latest output, got %q", r[len(r)-1])
	}
	if b := hbar(m, 120); strings.Contains(b, "new") {
		t.Errorf("after end, nothing is new: %q", b)
	}
}

func TestHistorySearch(t *testing.T) {
	m := historyModel(200, 80, 24)
	m.hist.lines[20], m.hist.lines[120], m.hist.lines[150] = "ERROR fan", "error: link down", "ERROR power"
	m.openHistory(true)
	if !m.hist.editing {
		t.Fatal("search should start with the prompt open")
	}
	press(m, "e", "r", "r", "o", "r")
	if want := []int{20, 120, 150}; !reflect.DeepEqual(m.hist.matches, want) {
		t.Fatalf("matches = %v, want %v (smart case)", m.hist.matches, want)
	}
	press(m, "enter")
	if got := m.hist.matches[m.hist.match]; m.hist.editing || got != 150 {
		t.Fatalf("enter should select the newest match above the screen's bottom, got line %d", got)
	}
	if want := 150 - m.pageHeight()/2; m.hist.top != want {
		t.Errorf("selected match should be centred: top %d, want %d", m.hist.top, want)
	}
	press(m, "n")
	if got := m.hist.matches[m.hist.match]; got != 120 {
		t.Errorf("n should go older, got line %d", got)
	}
	press(m, "N")
	if got := m.hist.matches[m.hist.match]; got != 150 {
		t.Errorf("N should go newer, got line %d", got)
	}
	if b := hbar(m, 120); !strings.Contains(b, "match 3/3") || !strings.Contains(b, "n older") {
		t.Errorf("bar should show the match and how to step: %q", b)
	}

	press(m, "home", "/", "E", "R", "R", "O", "R")
	if want := []int{20, 150}; !reflect.DeepEqual(m.hist.matches, want) {
		t.Errorf("a capital should match exactly: %v, want %v", m.hist.matches, want)
	}
	press(m, "enter")
	if got := m.hist.matches[m.hist.match]; got != 20 {
		t.Errorf("from the top, enter should find the match on screen, got line %d", got)
	}
	press(m, "/", "x", "esc")
	if m.hist.editing || m.hist.pattern != nil {
		t.Error("esc should cancel the search")
	}
}

func TestHistorySearchMatchesLiterally(t *testing.T) {
	m := historyModel(0, 80, 24)
	m.hist.lines = []string{"cpu 95 load", "cpu 9.5% load", "Gi1/0/1 up"}
	m.openHistory(true)
	press(m, "9", ".", "5")
	if want := []int{1}; !reflect.DeepEqual(m.hist.matches, want) {
		t.Errorf(`"9.5" matched %v; the dot must be literal`, m.hist.matches)
	}
	m.hist.query = ""
	m.typeSearch("gi1/0/1\nignored")
	if m.hist.query != "gi1/0/1" || !reflect.DeepEqual(m.hist.matches, []int{2}) {
		t.Errorf("paste should search its first line: query %q, matches %v", m.hist.query, m.hist.matches)
	}
}

// Characters that change length when lowercased once crashed highlighting.
func TestHistoryHighlightsAnyText(t *testing.T) {
	for _, c := range []struct{ line, query, hit string }{
		{"ȺȺȺȺ link down", "down", "down"},
		{"İİİİ link down", "down", "down"},
		{"KKKK fan ERROR", "error", "ERROR"}, // Kelvin sign
	} {
		m := historyModel(0, 200, 24)
		m.hist.lines = []string{c.line}
		m.openHistory(false)
		m.hist.query = c.query
		m.findMatches()
		if got := m.historyLine(c.line, true); !strings.Contains(got, selectedStyle.Render(c.hit)) {
			t.Errorf("%q: highlight wrong: %q", c.line, ansi.Strip(got))
		}
	}
}

func TestHistoryShowsMatchesPastTheEdge(t *testing.T) {
	m := historyModel(0, 80, 24)
	m.hist.lines = []string{"%LINK-3-UPDOWN: Interface Gi1/0/7 " + strings.Repeat("x", 150) + " error at the end"}
	m.openHistory(true)
	press(m, "e", "r", "r", "o", "r", "enter")
	r := rows(m)
	for _, row := range r {
		if w := ansi.StringWidth(row); w > 80 {
			t.Errorf("row is %d cells, wider than the window", w)
		}
	}
	if !strings.Contains(strings.Join(r, "\n"), "error at the end") {
		t.Errorf("the match should scroll into view:\n%s", strings.Join(r, "\n"))
	}
}

func TestHistoryOpensFromTheCommandKeys(t *testing.T) {
	for key, editing := range map[string]bool{"h": false, "/": true} {
		m := historyModel(10, 80, 24)
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != modeHistory || m.hist.editing != editing {
			t.Errorf("%s: mode %v, editing %v", key, m.mode, m.hist.editing)
		}
	}
}

func TestHistoryBarFitsAndKeepsEsc(t *testing.T) {
	m := historyModel(9999, 80, 24)
	m.hist.lines[150] = "ERROR power"
	m.openHistory(true)
	press(m, "e", "r", "r")
	m.rememberLines(m.screen.Feed([]byte(strings.Repeat("new\n", 123))))
	for _, editing := range []bool{true, false} {
		m.hist.editing = editing
		for w := 20; w <= 200; w++ {
			s := m.historyBar(w)
			if got := ansi.StringWidth(s); got != w {
				t.Errorf("editing %v, width %d: bar is %d cells", editing, w, got)
			}
			if !strings.HasSuffix(strings.TrimRight(ansi.Strip(s), " "), " esc") && !strings.Contains(ansi.Strip(s), " esc ") {
				t.Errorf("editing %v, width %d: esc missing: %q", editing, w, ansi.Strip(s))
			}
		}
	}
	empty := &Model{width: 80, height: 24}
	empty.openHistory(false)
	if b := hbar(empty, 80); !strings.Contains(b, "no output yet") {
		t.Errorf("empty history should say so: %q", b)
	}
}
