package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

func TestWaitForCable(t *testing.T) {
	var found []serialx.PortInfo
	m := &waitModel{hint: "Plug in your console cable and bauddy will connect to it.\n\nKnow the port's name?", list: func() ([]serialx.PortInfo, error) { return found, nil }}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 5})

	view := ansi.Strip(m.View().Content)
	if !strings.HasPrefix(view, "Plug in your console cable") || !strings.Contains(view, "○ waiting for a USB console cable…") || !strings.Contains(view, "q quit") {
		t.Errorf("view:\n%s", view)
	}
	if n := strings.Count(view, "\n"); n != 4 {
		t.Errorf("view has %d line breaks, want 4 for a 5-line window", n)
	}
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	for _, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if ansi.StringWidth(l) > 20 {
			t.Errorf("a line wider than a 20-column window would push the bar off: %q", l)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 5})

	for _, k := range []tea.KeyPressMsg{{Text: "x", Code: 'x'}, {Code: tea.KeyEnter}, {Code: ']', Mod: tea.ModCtrl}} {
		if _, cmd := m.Update(k); cmd != nil {
			t.Errorf("%s: want it ignored, got a command", k.String())
		}
	}
	if _, cmd := m.Update(waitTick{}); cmd == nil || m.ports != nil {
		t.Error("no ports yet: want it to keep waiting")
	}
	found = []serialx.PortInfo{{Path: "/dev/ttyUSB0", USB: true}}
	if _, cmd := m.Update(waitTick{}); cmd == nil || m.ports != nil {
		t.Error("a port appeared: want one more look, for the rest of a multi-port adapter")
	}
	found = append(found, serialx.PortInfo{Path: "/dev/ttyUSB1", USB: true})
	if _, cmd := m.Update(waitTick{}); cmd == nil || len(m.ports) != 2 {
		t.Errorf("both ports of the adapter: got %v", m.ports)
	}
}

func TestWaitForCableWantsTheCableTwiceInARow(t *testing.T) {
	var found []serialx.PortInfo
	var err error
	m := &waitModel{list: func() ([]serialx.PortInfo, error) { return found, err }}
	port := []serialx.PortInfo{{Path: "/dev/ttyUSB0", USB: true}}
	for _, step := range []struct {
		what  string
		found []serialx.PortInfo
		err   error
		done  bool
	}{
		{"appears", port, nil, false},
		{"gone again", nil, nil, false},
		{"back", port, nil, false},
		{"listing fails", port, errors.New("busy"), false},
		{"back", port, nil, false},
		{"still there", port, nil, true},
	} {
		found, err = step.found, step.err
		m.Update(waitTick{})
		if done := m.ports != nil; done != step.done {
			t.Fatalf("%s: done %v, want %v", step.what, done, step.done)
		}
	}
}

func TestWaitForCableQuits(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{{Text: "q", Code: 'q'}, {Code: 'c', Mod: tea.ModCtrl}} {
		m := &waitModel{}
		if _, cmd := m.Update(k); cmd == nil {
			t.Errorf("%s: want quit", k.String())
		} else if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: want quit", k.String())
		}
	}
}

func TestWaitBarFits(t *testing.T) {
	m := &waitModel{}
	for _, w := range []int{120, 50, 30, 10} {
		s := ansi.Strip(m.bar(w))
		if got := ansi.StringWidth(s); got != w {
			t.Errorf("width %d: rendered %d cells: %q", w, got, s)
		}
		if w >= 30 && !strings.Contains(s, "q quit") {
			t.Errorf("width %d: no quit key: %q", w, s)
		}
	}
}
