package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSearchUsesSmartCaseAndStartsAtNewestMatch(t *testing.T) {
	m := &Model{lines: []string{"boot complete", "ERROR: fan", "error: link", "ERROR: power"}, query: "error", mode: modeSearch}
	m.updateMatches()
	if want := []int{1, 2, 3}; !reflect.DeepEqual(m.matches, want) {
		t.Fatalf("matches = %v, want %v", m.matches, want)
	}
	_, _ = m.searchKey(tea.KeyPressMsg{}, "enter")
	if m.mode != modeSearchResults || m.match != 2 {
		t.Fatalf("mode, match = %v, %d; want results, 2", m.mode, m.match)
	}
	_, _ = m.searchResultKey("n")
	if m.match != 1 {
		t.Errorf("n selected %d, want older match 1", m.match)
	}
	m.query = "ERROR"
	m.updateMatches()
	if want := []int{1, 3}; !reflect.DeepEqual(m.matches, want) {
		t.Errorf("capital query matches = %v, want %v", m.matches, want)
	}
}

func TestSearchHistoryIsPlainTextAndBounded(t *testing.T) {
	m := &Model{}
	m.rememberSearchLines(m.search.Feed([]byte("one\r\ntwo\n")))
	if want := []string{"one", "two"}; !reflect.DeepEqual(m.lines, want) {
		t.Fatalf("lines = %#v, want %#v", m.lines, want)
	}
	m.lines = make([]string, searchLinesMax)
	m.rememberSearchLines(m.search.Feed([]byte("latest\n")))
	if len(m.lines) != searchLinesMax || m.lines[len(m.lines)-1] != "latest" {
		t.Errorf("history wasn't bounded correctly: len %d, last %q", len(m.lines), m.lines[len(m.lines)-1])
	}
}

func TestSearchFindsAndHighlightsAnyText(t *testing.T) {
	cases := []struct{ line, query, hit string }{
		{"ȺȺȺȺ link down", "down", "down"},   // Ⱥ lowercases to more bytes
		{"İİİİ link down", "down", "down"},   // İ lowercases to fewer bytes
		{"KKKK fan ERROR", "error", "ERROR"}, // Kelvin sign
		{"Gi1/0/1 up", "gi1/0/1", "Gi1/0/1"}, // slashes
		{"cpu 9.5% load", "9.5%", "9.5%"},    // pattern characters match literally
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			m := &Model{lines: []string{c.line}, query: c.query}
			m.updateMatches()
			if len(m.matches) != 1 {
				t.Fatalf("query %q found %d matches in %q", c.query, len(m.matches), c.line)
			}
			got := m.showMatch(c.line, 200)
			hl := lipgloss.NewStyle().Background(cAmber).Foreground(cOnAmber).Bold(true).Render(c.hit)
			if !strings.Contains(got, hl) {
				t.Errorf("highlight wrong: %q, want %q highlighted", ansi.Strip(got), c.hit)
			}
		})
	}
	m := &Model{lines: []string{"cpu 95 load"}, query: "9.5"}
	m.updateMatches()
	if len(m.matches) != 0 {
		t.Error(`"9.5" matched "95": the dot was treated as a pattern`)
	}
}

func TestSearchResultsDontMoveWhenOutputArrives(t *testing.T) {
	m := &Model{}
	for i := range searchLinesMax {
		m.lines = append(m.lines, fmt.Sprintf("line %d", i))
	}
	m.lines[searchLinesMax-5] = "ERROR power supply"
	m.query, m.mode = "error", modeSearch
	m.updateMatches()
	_, _ = m.searchKey(tea.KeyPressMsg{}, "enter")
	m.rememberSearchLines(m.search.Feed([]byte(strings.Repeat("new output\n", 3))))
	if got := m.searched[m.matches[m.match]]; got != "ERROR power supply" {
		t.Errorf("selected line changed to %q when output arrived", got)
	}
	if box := ansi.Strip(m.searchBox()); !strings.Contains(box, "ERROR power supply") {
		t.Errorf("result box lost the match:\n%s", box)
	}
}

func TestSearchShowsMatchesAtTheEndOfLongLines(t *testing.T) {
	line := "%LINK-3-UPDOWN: Interface Gi1/0/7 " + strings.Repeat("x", 150) + " error at the end"
	m := &Model{lines: []string{line}, query: "error", width: 80, height: 24, mode: modeSearch}
	m.updateMatches()
	_, _ = m.searchKey(tea.KeyPressMsg{}, "enter")
	box := ansi.Strip(m.searchBox())
	if !strings.Contains(box, "error at the end") {
		t.Errorf("match not visible in an 80-column window:\n%s", box)
	}
	for _, l := range strings.Split(box, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("box line is %d cells, wider than the window: %q", w, l)
		}
	}
}

func TestSearchContextFillsTheWindow(t *testing.T) {
	m := &Model{width: 100, height: 30, mode: modeSearch}
	for i := range 100 {
		m.lines = append(m.lines, fmt.Sprintf("line %d", i))
	}
	m.lines[50] = "ERROR here"
	m.query = "error"
	m.updateMatches()
	_, _ = m.searchKey(tea.KeyPressMsg{}, "enter")
	box := strings.Split(ansi.Strip(m.searchBox()), "\n")
	if len(box) > m.height-1 {
		t.Errorf("box is %d rows, taller than the %d-row pane", len(box), m.height-1)
	}
	if len(box) < m.height-4 {
		t.Errorf("box is %d rows; it should use most of the %d-row pane for context", len(box), m.height-1)
	}
	if head := box[2]; !strings.Contains(head, "match 1 of 1") {
		t.Errorf("heading should say match 1 of 1: %q", head)
	}
}
