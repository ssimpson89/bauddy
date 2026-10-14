package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

func usbPort(n int) serialx.PortInfo {
	path := fmt.Sprintf("/dev/ttyUSB%d", n)
	return serialx.PortInfo{Path: path, Key: path, USB: true, Product: "FT232R USB UART"}
}

// cables is a cable screen whose ports are whatever found holds.
type cables struct {
	*cableModel
	found     []serialx.PortInfo
	err       error
	openPaths map[string]bool // consoles open in another bauddy
}

func newCables(t *testing.T, start ...serialx.PortInfo) *cables {
	t.Helper()
	c := &cables{openPaths: map[string]bool{}}
	c.cableModel = newCableModel(start, "Plug in your console cable and bauddy will connect to it.",
		func(p serialx.PortInfo) string {
			if p.Path == "/dev/ttyUSB0" {
				return "core-sw · 9600 last time"
			}
			return ""
		},
		func(p serialx.PortInfo) bool { return c.openPaths[p.Path] },
		func() ([]serialx.PortInfo, error) { return c.found, c.err })
	c.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	return c
}

func (c *cables) look(ports ...serialx.PortInfo) {
	c.found = ports
	c.Update(cableTick{})
}

func (c *cables) press(k tea.KeyPressMsg) tea.Cmd {
	_, cmd := c.Update(k)
	return cmd
}

func (c *cables) screen() string { return ansi.Strip(c.View().Content) }

func (c *cables) chose() string {
	if c.chosen == nil {
		return ""
	}
	return c.chosen.Path
}

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	up    = tea.KeyPressMsg{Code: tea.KeyUp}
)

func digit(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestWaitingForACable(t *testing.T) {
	c := newCables(t)
	s := c.screen()
	if !strings.HasPrefix(s, " Plug in your console cable") || !strings.Contains(s, "○ waiting for a USB console cable…") || !strings.Contains(s, "q quit") {
		t.Errorf("waiting screen:\n%s", s)
	}
	if n := strings.Count(s, "\n"); n != 9 {
		t.Errorf("%d line breaks, want 9 for a 10-line window", n)
	}
	for _, k := range []tea.KeyPressMsg{digit('x'), enter, digit('1'), {Code: ']', Mod: tea.ModCtrl}} {
		if c.press(k) != nil {
			t.Errorf("%s while waiting: want it ignored", k.String())
		}
	}

	c.look(usbPort(0))
	if c.chose() != "" {
		t.Fatal("a cable at one look: want one more look, for the rest of a multi-port adapter")
	}
	c.look(usbPort(0))
	if c.chose() != "/dev/ttyUSB0" {
		t.Errorf("a lone cable at two looks: chose %q, want it connected", c.chose())
	}
}

func TestWaitingWantsTheSamePortsTwiceInARow(t *testing.T) {
	c := newCables(t)
	for _, step := range []struct {
		what  string
		found []serialx.PortInfo
		err   error
	}{
		{"appears", []serialx.PortInfo{usbPort(0)}, nil},
		{"gone again", nil, nil},
		{"back", []serialx.PortInfo{usbPort(0)}, nil},
		{"listing fails", []serialx.PortInfo{usbPort(0)}, errors.New("busy")},
		{"a second port of the adapter", []serialx.PortInfo{usbPort(0), usbPort(1)}, nil},
	} {
		c.err = step.err
		c.look(step.found...)
		if c.chose() != "" || c.listing {
			t.Fatalf("%s: chose %q, listing %v; want it still waiting", step.what, c.chose(), c.listing)
		}
	}
	c.look(usbPort(0), usbPort(1))
	if c.chose() != "" || !c.listing {
		t.Fatalf("two ports settled: chose %q, listing %v; want the list", c.chose(), c.listing)
	}
	if s := c.screen(); strings.Contains(s, "just plugged in") {
		t.Errorf("ports that turned up while waiting aren't just plugged in:\n%s", s)
	}
}

func TestChoosingACable(t *testing.T) {
	c := newCables(t, usbPort(0), usbPort(1))
	s := c.screen()
	for _, want := range []string{"Which cable?", "▸ 1  FT232R USB UART  ttyUSB0  core-sw · 9600 last time", "  2  FT232R USB UART  ttyUSB1",
		"Not sure which? Unplug yours and plug it back in, and it's marked.", "↑↓ choose  enter connect", "q quit"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	c.press(up)
	c.press(down)
	c.press(down)
	if c.cursor != 1 {
		t.Errorf("cursor %d after up, down, down; want 1 (it stops at the ends)", c.cursor)
	}
	c.press(enter)
	if c.chose() != "/dev/ttyUSB1" {
		t.Errorf("enter chose %q", c.chose())
	}

	c = newCables(t, usbPort(0), usbPort(1))
	for _, r := range "039" {
		if c.press(digit(r)) != nil || c.chose() != "" {
			t.Errorf("%c with two cables: want it ignored", r)
		}
	}
	c.press(digit('2'))
	if c.chose() != "/dev/ttyUSB1" {
		t.Errorf("2 chose %q", c.chose())
	}
}

func TestPluggingAndPullingWhileChoosing(t *testing.T) {
	c := newCables(t, usbPort(0), usbPort(1))
	c.look(usbPort(0), usbPort(1))

	c.look(usbPort(0), usbPort(1), usbPort(2))
	if c.cursor != 2 || !strings.Contains(c.screen(), "▸ 3  FT232R USB UART  ttyUSB2   just plugged in") {
		t.Errorf("a cable plugged in: cursor %d, want it on the new one, marked:\n%s", c.cursor, c.screen())
	}

	// Replugging the cable that goes to your device: it leaves and comes back.
	c.look(usbPort(1), usbPort(2))
	if c.cursor != 1 || c.rows[c.cursor].Path != "/dev/ttyUSB2" {
		t.Errorf("a cable pulled: cursor on %s, want it to stay on ttyUSB2", c.rows[c.cursor].Path)
	}
	c.look(usbPort(1), usbPort(2), usbPort(0))
	if c.rows[c.cursor].Path != "/dev/ttyUSB0" || strings.Count(c.screen(), "just plugged in") != 1 {
		t.Errorf("replugged: cursor on %s, want ttyUSB0, the only one marked:\n%s", c.rows[c.cursor].Path, c.screen())
	}

	// The cursor's cable pulled: it moves, and nothing connects.
	c.look(usbPort(1), usbPort(2))
	c.look(usbPort(1))
	c.look(usbPort(1))
	c.look(usbPort(1))
	if c.chose() != "" || !strings.Contains(c.screen(), "Plug your cable back in, or press enter to use this one.") {
		t.Errorf("one cable left: chose %q; want it to wait for enter:\n%s", c.chose(), c.screen())
	}

	// All pulled: waiting again, and a lone cable connects as usual.
	c.look()
	if c.listing || !strings.Contains(c.screen(), "○ waiting for a USB console cable…") {
		t.Errorf("all pulled: want the waiting screen:\n%s", c.screen())
	}
	c.look(usbPort(3))
	c.look(usbPort(3))
	if c.chose() != "/dev/ttyUSB3" {
		t.Errorf("then one plugged in: chose %q", c.chose())
	}
}

func TestBuiltInPorts(t *testing.T) {
	c := newCables(t, usbPort(0), serialx.PortInfo{Path: "/dev/ttyS0", Key: "/dev/ttyS0"})
	if s := c.screen(); !strings.Contains(s, "▸ 1  FT232R USB UART  ttyUSB0") || !strings.Contains(s, "2  built-in         ttyS0") {
		t.Errorf("USB first with the cursor on it, then the built-in port:\n%s", s)
	}
}

func TestCableScreenQuits(t *testing.T) {
	for _, start := range [][]serialx.PortInfo{nil, {usbPort(0), usbPort(1)}} {
		for _, k := range []tea.KeyPressMsg{digit('q'), {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}} {
			c := newCables(t, start...)
			if cmd := c.press(k); cmd == nil {
				t.Errorf("%s with %d cables: want quit", k.String(), len(start))
			} else if _, ok := cmd().(tea.QuitMsg); !ok || c.chosen != nil {
				t.Errorf("%s with %d cables: want quit, nothing chosen", k.String(), len(start))
			}
		}
	}
}

func TestCableScreenFits(t *testing.T) {
	var many []serialx.PortInfo
	for i := range 12 {
		many = append(many, usbPort(i))
	}
	for _, start := range [][]serialx.PortInfo{nil, many} {
		c := newCables(t, start...)
		for _, w := range []int{120, 50, 30, 10} {
			c.Update(tea.WindowSizeMsg{Width: w, Height: 10})
			lines := strings.Split(c.screen(), "\n")
			for _, l := range lines {
				if ansi.StringWidth(l) > w {
					t.Errorf("width %d: a line wider than the window would push the bar off: %q", w, l)
				}
			}
			bar := lines[len(lines)-1]
			if ansi.StringWidth(bar) != w || (w >= 30 && !strings.Contains(bar, "q quit")) {
				t.Errorf("width %d: bar %q", w, bar)
			}
		}
	}

	// 12 cables in a 10-line window: 9 lines above the bar, 4 of them the
	// question and the tip, leaves 5 rows.
	c := newCables(t, many...)
	for range 11 {
		c.press(down)
	}
	s := c.screen()
	if !strings.Contains(s, "▸    FT232R USB UART  ttyUSB11") || strings.Count(s, "ttyUSB") != 5 {
		t.Errorf("at the bottom: want the cursor's row, unnumbered past 9, and 5 rows:\n%s", s)
	}
	for range 11 {
		c.press(up)
	}
	if s := c.screen(); !strings.Contains(s, "▸ 1  FT232R USB UART  ttyUSB0 ") {
		t.Errorf("back at the top: want the first row under the cursor:\n%s", s)
	}

	// A window too short for the question and the tip still shows the cursor.
	c.Update(tea.WindowSizeMsg{Width: 80, Height: 3})
	if s := c.screen(); !strings.Contains(s, "▸ 1") {
		t.Errorf("3-line window: want the cursor's row:\n%s", s)
	}
}

// Cheap cables can share a serial number, so their Keys change as their twins
// come and go; rows follow the path, so nothing looks just plugged in.
func TestTwinCablesKeepTheirRows(t *testing.T) {
	twin := func(n int, key string) serialx.PortInfo { p := usbPort(n); p.Key = key; return p }
	c := newCables(t, twin(0, "usb:1a86:7523:1:ttyUSB0"), twin(1, "usb:1a86:7523:1:ttyUSB1"))
	c.press(down)
	c.look(twin(1, "usb:1a86:7523:1"))
	c.look(twin(0, "usb:1a86:7523:1:ttyUSB0"), twin(1, "usb:1a86:7523:1:ttyUSB1"))
	if c.rows[0].Path != "/dev/ttyUSB1" || c.rows[c.cursor].Path != "/dev/ttyUSB0" {
		t.Errorf("rows %v, cursor on %s: want ttyUSB1 to stay put and the replugged ttyUSB0 at the end, under the cursor",
			[]string{c.rows[0].Path, c.rows[1].Path}, c.rows[c.cursor].Path)
	}
	if s := c.screen(); strings.Count(s, "just plugged in") != 1 || !strings.Contains(s, "ttyUSB0   just plugged in") {
		t.Errorf("only the replugged twin is marked:\n%s", s)
	}
}

func TestCablesOpenInAnotherBauddy(t *testing.T) {
	c := &cables{openPaths: map[string]bool{"/dev/ttyUSB0": true}}
	c.cableModel = newCableModel([]serialx.PortInfo{usbPort(0), usbPort(1)}, "",
		func(serialx.PortInfo) string { return "" },
		func(p serialx.PortInfo) bool { return c.openPaths[p.Path] },
		func() ([]serialx.PortInfo, error) { return c.found, nil })
	c.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	s := c.screen()
	if !strings.Contains(s, "▸ 1  FT232R USB UART  ttyUSB1") || !strings.Contains(s, "2  FT232R USB UART  ttyUSB0  open in bauddy") {
		t.Errorf("want the free cable first under the cursor, the open one last and tagged:\n%s", s)
	}

	// The other window closes: the tag goes at the next look; nothing moves.
	delete(c.openPaths, "/dev/ttyUSB0")
	c.look(usbPort(0), usbPort(1))
	if s := c.screen(); strings.Contains(s, "open in bauddy") || c.rows[1].Path != "/dev/ttyUSB0" {
		t.Errorf("after the other window closed:\n%s", s)
	}

	// Choosing an open one is allowed: it joins.
	c.openPaths["/dev/ttyUSB1"] = true
	c.look(usbPort(0), usbPort(1))
	c.press(enter)
	if c.chose() != "/dev/ttyUSB1" {
		t.Errorf("enter on a cable open elsewhere chose %q; want it, to join", c.chose())
	}
}

// Starting with no cable, the list goes up once ports settle; cables open
// elsewhere go last then too.
func TestWaitingThenListingPutsOpenCablesLast(t *testing.T) {
	c := newCables(t)
	c.openPaths["/dev/ttyUSB0"] = true
	c.look(usbPort(0), usbPort(1))
	c.look(usbPort(0), usbPort(1))
	if !c.listing || c.rows[0].Path != "/dev/ttyUSB1" || c.cursor != 0 {
		t.Errorf("listing %v, rows %s %s, cursor %d: want the free ttyUSB1 first, under the cursor", c.listing, c.rows[0].Path, c.rows[1].Path, c.cursor)
	}
}
