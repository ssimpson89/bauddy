package cli

import (
	"os"
	"testing"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

func TestEscapeKey(t *testing.T) {
	t.Setenv("BAUDDY_ESCAPE", "ctrl+t")
	if got := escapeKey(""); got != "ctrl+t" {
		t.Errorf("env: got %q, want ctrl+t", got)
	}
	if got := escapeKey("ctrl+a"); got != "ctrl+a" {
		t.Errorf("flag beats env: got %q, want ctrl+a", got)
	}
	t.Setenv("BAUDDY_ESCAPE", "")
	if got := escapeKey(""); got != "ctrl+]" {
		t.Errorf("default: got %q, want ctrl+]", got)
	}
}

func TestDevNullIsntATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("/dev/null counted as a terminal, so bauddy < /dev/null would wait for a cable unseen")
	}
}

func TestWhenTheCableScreenAsks(t *testing.T) {
	usb := serialx.PortInfo{Path: "/dev/ttyUSB0", USB: true}
	usb2 := serialx.PortInfo{Path: "/dev/ttyUSB1", USB: true}
	builtin := serialx.PortInfo{Path: "/dev/ttyS0"}
	for _, c := range []struct {
		what     string
		arg      string
		ports    []serialx.PortInfo
		terminal bool
		want     bool
	}{
		{"no cable: wait for one", "", nil, true, true},
		{"one cable: connect", "", []serialx.PortInfo{usb}, true, false},
		{"one built-in port: connect", "", []serialx.PortInfo{builtin}, true, false},
		{"two cables: ask", "", []serialx.PortInfo{usb, usb2}, true, true},
		// On purpose: unlike Choose, a lone USB cable doesn't beat a real
		// built-in port when someone is there to ask.
		{"a cable and a built-in port: ask", "", []serialx.PortInfo{usb, builtin}, true, true},
		{"a port named: never ask", "ttyS0", []serialx.PortInfo{usb, builtin}, true, false},
		{"no terminal: never ask", "", []serialx.PortInfo{usb, usb2}, false, false},
	} {
		if got := askForCable(c.arg, c.ports, c.terminal); got != c.want {
			t.Errorf("%s: askForCable = %v, want %v", c.what, got, c.want)
		}
	}
}
