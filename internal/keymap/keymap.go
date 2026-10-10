// Package keymap is the single source of truth for command-mode keys; the
// status line, the help overlay and `bauddy keys` are generated from it.
package keymap

import (
	"fmt"
	"strings"
)

// Action identifies something the user can ask bauddy to do.
type Action int

// The actions a command-mode key can trigger. ActNone means the key isn't bound.
const (
	ActNone Action = iota
	ActLeaveCommand
	ActSendEscapeKey
	ActHelp
	ActBaud
	ActDetect
	ActBreak
	ActSyncSize
	ActClear
	ActLog
	ActReconnect
	ActQuit
)

// Binding maps a key to an action, with text for people.
type Binding struct {
	Key    string // as reported by tea.KeyPressMsg.String()
	Action Action
	Short  string // status line label, 1-2 words; set only for keys listed there
	Help   string // one line for the help overlay
	Sends  bool   // sends something to the device (capital letters, by convention)
	Keep   int    // on a narrow status line, higher stays longer; KeepAlways never goes
}

// KeepAlways marks a key the status line always shows.
const KeepAlways = 100

// DefaultEscapeKey switches from console mode to command mode.
// ctrl+] is the telnet tradition and is almost never needed by a device.
const DefaultEscapeKey = "ctrl+]"

// Keymap holds the bindings for command mode plus the escape key.
type Keymap struct {
	Escape   string
	Commands []Binding
}

// New returns the keymap with esc as the key that opens command mode.
// Lowercase keys act on bauddy; capitals send something to the device.
func New(esc string) Keymap {
	return Keymap{
		Escape: esc,
		Commands: []Binding{
			// In status line order: the link (b, d, r), then output (l, c),
			// then keys that send to the device. q and esc sit apart on the right.
			{Key: "?", Action: ActHelp, Short: "help", Help: "Show every key", Keep: KeepAlways},
			{Key: "b", Action: ActBaud, Short: "baud", Help: "Pick a baud rate", Keep: 6},
			{Key: "d", Action: ActDetect, Short: "detect", Help: "Detect the baud rate (listens; asks before sending Enter)", Keep: 7},
			{Key: "r", Action: ActReconnect, Short: "reconnect", Help: "Close and reopen the port", Keep: 3},
			{Key: "l", Action: ActLog, Short: "log", Help: "Start or stop logging to a file (includes earlier output)", Keep: 5},
			{Key: "c", Action: ActClear, Short: "clear", Help: "Clear the screen", Keep: 2},
			{Key: "q", Action: ActQuit, Short: "quit", Help: "Quit", Keep: KeepAlways},
			{Key: "esc", Action: ActLeaveCommand, Short: "back", Help: "Back to the console", Keep: KeepAlways},
			{Key: "B", Action: ActBreak, Short: "break", Help: "Send BREAK (ROMMON, SysRq; can change a Linux login prompt's baud)", Sends: true, Keep: 4},
			{Key: "S", Action: ActSyncSize, Short: "resize", Help: "Type `stty rows R cols C` so the remote shell fits (at a shell prompt)", Sends: true, Keep: 1},
			{Key: esc, Action: ActSendEscapeKey, Help: "Send " + esc + " itself", Sends: true},
		},
	}
}

// reserved are keys a device needs, so they can't open command mode.
var reserved = map[string]string{
	"ctrl+c": "interrupt", "ctrl+d": "end of input", "ctrl+z": "suspend",
	"ctrl+h": "backspace", "ctrl+i": "tab", "ctrl+j": "newline", "ctrl+m": "Enter",
	"ctrl+[": "Esc", "ctrl+s": "XOFF", "ctrl+q": "XON", "ctrl+u": "clear line",
}

// CheckEscape reports whether key can open command mode: ctrl with a
// lowercase letter or one of \ ] ^ _ (the keys terminals send as single
// control bytes), and not one that devices depend on.
func CheckEscape(key string) error {
	c, ok := strings.CutPrefix(key, "ctrl+")
	if !ok || len(c) != 1 || !(c[0] >= 'a' && c[0] <= 'z' || strings.Contains(`\]^_`, c)) {
		return fmt.Errorf("escape key %q: use ctrl and a lowercase letter or ], like ctrl+t or ctrl+]", key)
	}
	if use, ok := reserved[key]; ok {
		return fmt.Errorf("escape key %s: devices need it (%s); try ctrl+t or ctrl+]", key, use)
	}
	return nil
}

// Lookup returns the action bound to key, or ActNone.
func (k Keymap) Lookup(key string) Action {
	for _, b := range k.Commands {
		if b.Key == key {
			return b.Action
		}
	}
	return ActNone
}

// Group is a titled set of bindings for the help overlay and cheat sheet.
type Group struct {
	Title    string
	Bindings []Binding
}

// Groups splits the bindings into those that act on bauddy and those that
// send something to the device.
func (k Keymap) Groups() []Group {
	groups := []Group{{Title: "bauddy"}, {Title: "sends to the device"}}
	for _, b := range k.Commands {
		i := 0
		if b.Sends {
			i = 1
		}
		groups[i].Bindings = append(groups[i].Bindings, b)
	}
	return groups
}

// Intro is the two lines that explain command mode.
func (k Keymap) Intro() string {
	return fmt.Sprintf("Everything you type goes to the device, except %s.\nPress %s, then one key:", k.Escape, k.Escape)
}

// Cheatsheet renders the full keymap as plain text.
func (k Keymap) Cheatsheet() string {
	var sb strings.Builder
	sb.WriteString(k.Intro() + "\n")
	for _, g := range k.Groups() {
		fmt.Fprintf(&sb, "\n%s\n", strings.ToUpper(g.Title))
		for _, b := range g.Bindings {
			fmt.Fprintf(&sb, "  %-8s %s\n", b.Key, b.Help)
		}
	}
	return sb.String()
}
