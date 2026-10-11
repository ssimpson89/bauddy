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
		m.lines = append(m.lines, fmt.Sprintf("line %d", i))
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

func TestHistoryKeepsPlainTextLinesBounded(t *testing.T) {
	m := &Model{}
	m.rememberLines(m.text.Feed([]byte("\x1b[1mone\x1b[0m\r\ntwo\n")))
	if want := []string{"one", "two"}; !reflect.DeepEqual(m.lines, want) {
		t.Fatalf("lines = %#v, want %#v", m.lines, want)
	}
	m.lines = make([]string, historyLinesMax)
	m.rememberLines(m.text.Feed([]byte("latest\n")))
	if len(m.lines) != historyLinesMax || m.lines[len(m.lines)-1] != "latest" {
		t.Errorf("not bounded: len %d, last %q", len(m.lines), m.lines[len(m.lines)-1])
	}
}

func TestHistoryOpensAtTheEndWithThePrompt(t *testing.T) {
	m := historyModel(100, 80, 24)
	m.openHistory(false)
	r := rows(m)
	if len(r) != 23 || r[22] != "core-sw-01#" || r[21] != "line 99" {
		t.Fatalf("last rows = %q, %q (of %d)", r[21], r[22], len(r))
	}
	if bar := ansi.Strip(m.historyBar(80)); !strings.Contains(bar, "line 101 of 101") {
		t.Errorf("bar should show the position: %q", bar)
	}
}

func TestHistoryScrollsWithinRange(t *testing.T) {
	m := historyModel(100, 80, 24) // 101 lines, 23 on screen: top runs 0..78
	m.openHistory(false)
	cases := []struct {
		keys []string
		top  int
	}{
		{[]string{"up"}, 77},
		{[]string{"pgup"}, 55}, // a page, less one line of overlap
		{[]string{"home"}, 0},
		{[]string{"up"}, 0},
		{[]string{"down", "down"}, 2},
		{[]string{"pgdown"}, 24},
		{[]string{"G"}, 78},
		{[]string{"down"}, 78},
	}
	for _, c := range cases {
		press(m, c.keys...)
		if m.top != c.top {
			t.Errorf("after %v: top = %d, want %d", c.keys, m.top, c.top)
		}
	}
	press(m, "q")
	if m.mode != modeConsole {
		t.Error("q should close history")
	}
}

func TestHistoryHoldsStillUntilEnd(t *testing.T) {
	m := historyModel(100, 80, 24)
	m.openHistory(false)
	press(m, "home")
	m.rememberLines(m.text.Feed([]byte("new 1\nnew 2\nnew 3\n")))
	if r := rows(m); r[0] != "line 0" {
		t.Errorf("view moved when output arrived: first row %q", r[0])
	}
	if bar := ansi.Strip(m.historyBar(120)); !strings.Contains(bar, "+3 new") {
		t.Errorf("bar should count the new lines: %q", bar)
	}
	press(m, "end")
	r := rows(m)
	if r[len(r)-2] != "new 3" {
		t.Errorf("end should show the latest output, got %q", r[len(r)-2])
	}
	if bar := ansi.Strip(m.historyBar(120)); strings.Contains(bar, "new") {
		t.Errorf("after end, nothing is new: %q", bar)
	}
}

func TestHistorySearch(t *testing.T) {
	m := historyModel(200, 80, 24)
	m.lines[20], m.lines[120], m.lines[150] = "ERROR fan", "error: link down", "ERROR power"
	m.openHistory(true)
	if !m.editing {
		t.Fatal("search should start with the prompt open")
	}
	press(m, "e", "r", "r", "o", "r")
	if want := []int{20, 120, 150}; !reflect.DeepEqual(m.matches, want) {
		t.Fatalf("matches = %v, want %v (smart case)", m.matches, want)
	}
	press(m, "enter")
	if m.editing || m.matches[m.match] != 150 {
		t.Fatalf("enter should select the newest match above the screen's bottom, got line %d", m.matches[m.match])
	}
	if want := 150 - m.pageHeight()/2; m.top != want {
		t.Errorf("selected match should be centred: top %d, want %d", m.top, want)
	}
	press(m, "n")
	if m.matches[m.match] != 120 {
		t.Errorf("n should go older, got line %d", m.matches[m.match])
	}
	press(m, "N")
	if m.matches[m.match] != 150 {
		t.Errorf("N should go newer, got line %d", m.matches[m.match])
	}
	if bar := ansi.Strip(m.historyBar(120)); !strings.Contains(bar, "match 3 of 3") {
		t.Errorf("bar should show the match: %q", bar)
	}

	press(m, "home", "/", "E", "R", "R", "O", "R")
	if want := []int{20, 150}; !reflect.DeepEqual(m.matches, want) {
		t.Errorf("a capital should match exactly: %v, want %v", m.matches, want)
	}
	press(m, "enter")
	if m.matches[m.match] != 20 {
		t.Errorf("from the top, enter should find the match on screen, got line %d", m.matches[m.match])
	}
	press(m, "/", "x", "esc")
	if m.editing || m.pattern != nil {
		t.Error("esc should cancel the search")
	}
}

func TestHistorySearchMatchesLiterally(t *testing.T) {
	m := historyModel(0, 80, 24)
	m.lines = []string{"cpu 95 load", "cpu 9.5% load", "Gi1/0/1 up"}
	m.openHistory(true)
	press(m, "9", ".", "5")
	if want := []int{1}; !reflect.DeepEqual(m.matches, want) {
		t.Errorf(`"9.5" matched %v; the dot must be literal`, m.matches)
	}
	m.query = ""
	m.typeSearch("gi1/0/1\nignored")
	if m.query != "gi1/0/1" || !reflect.DeepEqual(m.matches, []int{2}) {
		t.Errorf("paste should search its first line: query %q, matches %v", m.query, m.matches)
	}
}

func TestHistoryHighlightsAnyText(t *testing.T) {
	cases := []struct{ line, query, hit string }{
		{"ȺȺȺȺ link down", "down", "down"},   // lowercases to more bytes
		{"İİİİ link down", "down", "down"},   // lowercases to fewer bytes
		{"KKKK fan ERROR", "error", "ERROR"}, // Kelvin sign
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			m := historyModel(0, 200, 24)
			m.lines = []string{c.line}
			m.openHistory(false)
			m.query = c.query
			m.findMatches()
			m.selectMatch(0)
			got := m.historyLine(c.line, true)
			if !strings.Contains(got, selectedStyle.Render(c.hit)) {
				t.Errorf("highlight wrong: %q", ansi.Strip(got))
			}
		})
	}
}

func TestHistoryShowsMatchesPastTheEdge(t *testing.T) {
	line := "%LINK-3-UPDOWN: Interface Gi1/0/7 " + strings.Repeat("x", 150) + " error at the end"
	m := historyModel(0, 80, 24)
	m.lines = []string{line}
	m.openHistory(true)
	press(m, "e", "r", "r", "o", "r", "enter")
	for _, r := range rows(m) {
		if ansi.StringWidth(r) > 80 {
			t.Errorf("row is %d cells, wider than the window", ansi.StringWidth(r))
		}
	}
	if r := rows(m); !strings.Contains(strings.Join(r, "\n"), "error at the end") {
		t.Errorf("the match should scroll into view:\n%s", strings.Join(r, "\n"))
	}
}

func TestHistoryOpensFromTheCommandKeysOnly(t *testing.T) {
	for key, editing := range map[string]bool{"h": false, "/": true} {
		m := historyModel(10, 80, 24)
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != modeHistory || m.editing != editing {
			t.Errorf("%s: mode %v, editing %v", key, m.mode, m.editing)
		}
	}
	m := historyModel(10, 80, 24) // in the console
	if m.mouseMode() != tea.MouseModeNone {
		t.Error("the console shouldn't capture the mouse")
	}
	m.historyWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.mode == modeHistory {
		t.Error("the wheel shouldn't open history")
	}
}

func TestHistoryWheelScrollsWhileOpen(t *testing.T) {
	m := historyModel(100, 80, 24)
	m.openHistory(false)
	if m.mouseMode() == tea.MouseModeNone {
		t.Fatal("history should capture the mouse for the wheel")
	}
	m.historyWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.top != 75 {
		t.Errorf("wheel up: top %d, want 75", m.top)
	}
	press(m, "esc")
	if m.mouseMode() != tea.MouseModeNone {
		t.Error("closing history should release the mouse")
	}
}

func TestHistoryBarFitsAtEveryWidth(t *testing.T) {
	m := historyModel(200, 80, 24)
	m.lines[150] = "ERROR power"
	m.openHistory(true)
	press(m, "e", "r", "r")
	for _, editing := range []bool{true, false} {
		m.editing = editing
		for w := 1; w <= 200; w++ {
			if got := ansi.StringWidth(m.historyBar(w)); got != w {
				t.Errorf("editing %v, width %d: bar is %d cells", editing, w, got)
			}
		}
	}
	empty := &Model{width: 80, height: 24}
	empty.openHistory(false)
	if bar := ansi.Strip(empty.historyBar(80)); !strings.Contains(bar, "nothing yet") {
		t.Errorf("empty history should say so: %q", bar)
	}
}
