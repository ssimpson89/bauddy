package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/serialx"
)

func TestOutputFromDetectionCountsAsHeard(t *testing.T) {
	m := &Model{emu: vt.NewSafeEmulator(80, 24)}
	m.output([]byte("\r\nSwitch#")) // what detectDone shows
	m.Update(quietCheckMsg{})
	if !m.heard || m.quiet {
		t.Errorf("after showing detected output: heard %v, quiet %v; want heard and not quiet", m.heard, m.quiet)
	}
}

func detectingModel() *Model {
	return &Model{keys: keymap.New(keymap.DefaultEscapeKey), detecting: true, note: detectingNote}
}

func TestCommandsThatUseThePortWaitForDetection(t *testing.T) {
	m := detectingModel()
	for _, key := range []string{"b", "d", "r", "B", "S", keymap.DefaultEscapeKey} {
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != modeConsole || m.note.kind != noteSticky || !strings.Contains(m.note.text, key+" didn't run; try again") {
			t.Errorf("%s during detection: mode %v, note %q", key, m.mode, m.note.text)
		}
	}

	m.note = note{text: "can't write the log", kind: noteError}
	m.mode = modeCommand
	_, _ = m.commandKey(tea.KeyPressMsg{Text: "b"}, "b")
	if m.note.kind != noteError {
		t.Errorf("an unseen error was replaced by %q", m.note.text)
	}
}

func TestOtherCommandsWorkDuringDetection(t *testing.T) {
	for key, mode := range map[string]mode{"?": modeHelp, "h": modeHistory, "/": modeHistory} {
		m := detectingModel()
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != mode {
			t.Errorf("%s during detection: mode %v, want %v", key, m.mode, mode)
		}
	}
	m := detectingModel()
	m.mode = modeCommand
	if _, cmd := m.commandKey(tea.KeyPressMsg{Text: "q"}, "q"); cmd == nil {
		t.Error("q during detection should quit")
	}
}

func TestDetectingNoteComesBack(t *testing.T) {
	m := detectingModel()
	m.receivePaste("show version\n") // refused while detecting, with a note that clears itself
	if m.note.kind != noteInfo {
		t.Fatalf("paste during detection: note %+v", m.note)
	}
	m.Update(noteClearMsg{m.noteID})
	if m.note != detectingNote {
		t.Errorf("after the paste note cleared: %q, want the detecting note back", m.note.text)
	}
}

func TestNothingHeardSaysWhatToDo(t *testing.T) {
	for mode, want := range map[mode]string{modeCommand: "; d tries again", modeHelp: "; ctrl+] d tries again"} {
		m := detectingModel()
		m.mode = mode // looking at the keys, or at help, when detection gave up
		m.detectDone(detectDoneMsg{err: serialx.ErrNoSignal})
		if !strings.Contains(m.note.text, want) {
			t.Errorf("mode %v: note %q should say %q", mode, m.note.text, want)
		}
	}

	m := detectingModel()
	m.mode = modeCommand
	m.note = note{text: "can't write the log", kind: noteError}
	m.detectDone(detectDoneMsg{err: serialx.ErrNoSignal})
	if m.note.kind != noteError {
		t.Errorf("an error from during detection was replaced by %q", m.note.text)
	}
	m.detecting = true
	m.detectDone(detectDoneMsg{err: errors.New("port gone")})
	if !strings.Contains(m.note.text, "can't write the log; can't detect the baud rate: port gone") {
		t.Errorf("both errors should show: %q", m.note.text)
	}
}

type reconnectPort struct {
	Port
	reconnects int
}

func (p *reconnectPort) Baud() int                { return 9600 }
func (p *reconnectPort) Framing() serialx.Framing { return serialx.Framing8N1 }
func (p *reconnectPort) Reconnect()               { p.reconnects++ }

func TestReconnectIsNotUnplugged(t *testing.T) {
	p := &reconnectPort{}
	m := &Model{keys: keymap.New(keymap.DefaultEscapeKey), sess: p, emu: vt.NewSafeEmulator(80, 24)}
	line := func() string { return ansi.Strip(m.mainLine(120)) }

	_, _ = m.commandKey(tea.KeyPressMsg{Text: "r"}, "r")
	m.event(serialx.Event{State: serialx.Disconnected})  // the port closing
	_, _ = m.commandKey(tea.KeyPressMsg{Text: "r"}, "r") // pressed again before it's back
	if s := line(); !strings.Contains(s, "reconnecting…") || strings.Contains(s, "unplugged") {
		t.Errorf("after r: %q, want reconnecting and not unplugged", s)
	}
	m.event(serialx.Event{State: serialx.Connected})
	if m.reconnecting || m.note.text != "reconnected" {
		t.Errorf("back: reconnecting %v, note %q", m.reconnecting, m.note.text)
	}

	m.event(serialx.Event{State: serialx.Disconnected}) // the cable comes out
	_, _ = m.commandKey(tea.KeyPressMsg{Text: "r"}, "r")
	if s := line(); !strings.Contains(s, "unplugged") || p.reconnects != 3 {
		t.Errorf("r while unplugged: %q (reconnects %d), want it still to say unplugged", s, p.reconnects)
	}
}

type ratePort struct {
	reconnectPort
	baud int
}

func (p *ratePort) Baud() int                   { return p.baud }
func (p *ratePort) SetBaud(b int) error         { p.baud = b; return nil }
func (p *ratePort) Write(b []byte) (int, error) { return len(b), nil }

func unconfirmedModel(baud int) *Model {
	m := &Model{keys: keymap.New(keymap.DefaultEscapeKey), sess: &ratePort{baud: baud}, emu: vt.NewSafeEmulator(80, 24), state: serialx.Connected, width: 100, height: 24}
	go func() { // what New's pump does: typed keys come out of the emulator
		buf := make([]byte, 256)
		for {
			if _, err := m.emu.Read(buf); err != nil {
				return
			}
		}
	}()
	return m
}

// The owner's bug: a saved 9600 against a switch at 115200. Enter gets no
// reply at all (the fast replies are dropped), so say so and offer to detect.
func TestEnterWithNoReplyOffersDetection(t *testing.T) {
	m := unconfirmedModel(9600)
	_, cmd := m.consoleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, "enter")
	if cmd == nil || !m.awaitReply {
		t.Fatal("Enter at an unconfirmed rate: want a no-reply check scheduled")
	}
	if _, again := m.consoleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"); again != nil {
		t.Error("a second Enter before the check: want no second check")
	}
	m.Update(noReplyMsg{})
	if m.mode != modeProbeOffer || !strings.Contains(ansi.Strip(m.statusLine()), "no reply at 9600 · wrong baud?") {
		t.Errorf("after silence: mode %v, bar %q; want the offer to detect", m.mode, ansi.Strip(m.statusLine()))
	}
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	if m.mode != modeConsole || !m.confirmed() {
		t.Error("the device answers after all: want the console back and the rate confirmed")
	}
	if _, cmd := m.consoleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"); cmd != nil {
		t.Error("Enter at a confirmed rate: want no check")
	}
}

func TestReplyInTimeMeansNoOffer(t *testing.T) {
	m := unconfirmedModel(115200)
	m.consoleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, "enter")
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.Update(noReplyMsg{})
	if m.mode != modeConsole {
		t.Errorf("the device replied before the check: mode %v, want the console", m.mode)
	}
}

func TestUnconfirmedRateHoldsPasteAndWindowSize(t *testing.T) {
	m := unconfirmedModel(9600)
	m.receivePaste("show version\n")
	if m.paste != nil || m.note.text != unconfirmedNote {
		t.Errorf("paste at an unconfirmed rate: paste %v, note %q; want it held", m.paste != nil, m.note.text)
	}
	m.commandKey(tea.KeyPressMsg{Text: "S"}, "S")
	if m.note.text != unconfirmedNote {
		t.Errorf("window size at an unconfirmed rate: note %q; want it held", m.note.text)
	}

	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.applyBaud(19200)
	if m.confirmed() {
		t.Error("after a rate change: want the rate unconfirmed again")
	}
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.event(serialx.Event{State: serialx.Disconnected})
	m.event(serialx.Event{State: serialx.Connected})
	if m.confirmed() {
		t.Error("after a reconnect: want the rate unconfirmed again")
	}
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.sess.(*ratePort).baud = 9600 // another window changed it
	if m.confirmed() {
		t.Error("the console's rate changed elsewhere: want it unconfirmed")
	}
}

// Until the device answers, only Enter goes out: any other key at a rate
// slower than the device's arrives as a BREAK, and then as a SysRq command.
func TestOnlyEnterUntilTheDeviceAnswers(t *testing.T) {
	m := unconfirmedModel(9600)
	for _, k := range []tea.KeyPressMsg{{Text: "b", Code: 'b'}, {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyUp}} {
		m.consoleKey(k, k.String())
		if m.note.text != unconfirmedNote || m.awaitReply {
			t.Errorf("%s at an unconfirmed rate: note %q; want it held", k.String(), m.note.text)
		}
	}
	// Garbage doesn't confirm the rate: at a slower rate than the device's
	// some still gets through.
	m.event(serialx.Event{Data: []byte{0x89, 0xb0, 0x82, 'S', '\n', 0x06, 0xea}})
	if m.confirmed() {
		t.Error("garbage confirmed the rate")
	}
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.note = note{}
	m.consoleKey(tea.KeyPressMsg{Text: "b", Code: 'b'}, "b")
	if m.note.text != "" {
		t.Errorf("a key at a confirmed rate: note %q; want it sent", m.note.text)
	}
}

// Probing faster rates can leave a stray character that a login prompt
// takes as a username, so it asks for a password: say why.
func TestProbeThatEndsAtAPasswordPromptSaysWhy(t *testing.T) {
	m := unconfirmedModel(115200)
	m.detected(detectDoneMsg{Result: serialx.Result{Baud: 115200, StrayLogin: true}, probe: true}) // shared: Heard is empty
	if !strings.Contains(m.note.text, "a stray character may have gone in as a username") {
		t.Errorf("note %q; want the stray-character explanation", m.note.text)
	}
	m.detected(detectDoneMsg{Result: serialx.Result{Baud: 115200}, probe: true})
	if strings.Contains(m.note.text, "stray") {
		t.Errorf("note %q at a plain prompt; want no explanation", m.note.text)
	}
}

func TestWhatConfirmsTheRate(t *testing.T) {
	m := unconfirmedModel(9600)
	m.event(serialx.Event{Data: []byte("\r\nUser:"), Replay: true})
	if m.confirmed() {
		t.Error("a replay confirmed the rate: it may be from before a rate change")
	}
	m.event(serialx.Event{Data: []byte("$")})
	m.event(serialx.Event{Data: []byte(" ")})
	if !m.confirmed() {
		t.Error("a prompt split across reads didn't confirm the rate")
	}
	m.applyBaud(115200)
	m.event(serialx.Event{Data: []byte("\r\nUser:")})
	m.applyBaud(9600)
	if m.confirmed() {
		t.Error("back to 9600 without a new answer: want it unconfirmed")
	}
	m.event(serialx.Event{Data: []byte("$\xff")})
	if m.confirmed() {
		t.Error("garbage mixed in confirmed the rate")
	}

	// The doubled escape key sends it to the device: held back too.
	m.note = note{}
	m.commandKey(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}, keymap.DefaultEscapeKey)
	if m.note.text != unconfirmedNote {
		t.Errorf("escape key sent at an unconfirmed rate: note %q", m.note.text)
	}

	// A risky paste waiting for an answer when the rate changes.
	m.event(serialx.Event{Data: []byte("\r\nSwitch#")})
	m.receivePaste("reload\n")
	if m.mode != modePasteConfirm {
		t.Fatalf("mode %v; want the risky paste question", m.mode)
	}
	m.applyBaud(19200)
	m.confirmKey("enter")
	if m.paste != nil || m.note.text != unconfirmedNote {
		t.Errorf("risky paste confirmed after a rate change: paste %v, note %q; want it held", m.paste != nil, m.note.text)
	}
}

func TestStartEachLineAtTheLeft(t *testing.T) {
	cr := false
	got := string(withCR([]byte("one\ntwo\r\nthree\n"), &cr)) + string(withCR([]byte("\nfour"), &cr))
	if want := "one\r\ntwo\r\nthree\r\n\r\nfour"; got != want {
		t.Errorf("got %q, want %q (a CR before each bare LF, none added after a CR)", got, want)
	}
	cr = false
	got = string(withCR([]byte("a\r"), &cr)) + string(withCR([]byte("\nb"), &cr))
	if want := "a\r\nb"; got != want {
		t.Errorf("CR and LF split across reads: got %q, want %q", got, want)
	}
}

func TestBackspaceToH(t *testing.T) {
	b := []byte("ab\x7f\x7fc")
	backspaceToH(b)
	if string(b) != "ab\b\bc" {
		t.Errorf("got %q", b)
	}
}

func TestTheKeyboardAndScreenFixes(t *testing.T) {
	m := unconfirmedModel(9600)
	var saved [][2]bool
	m.opt.SaveKeys = func(bs, cr bool) error { saved = append(saved, [2]bool{bs, cr}); return nil }
	if s := ansi.Strip(m.mainLine(100)); strings.Contains(s, "^H") || strings.Contains(s, "+CR") {
		t.Errorf("off by default, so not on the bar: %q", s)
	}
	m.commandKey(tea.KeyPressMsg{Text: "x"}, "x")
	m.commandKey(tea.KeyPressMsg{Text: "n"}, "n")
	if !m.backspaceH.Load() || !m.addCR || m.note.text != "starting each line at the left" {
		t.Errorf("after x and n: ^H %v, +CR %v, note %q", m.backspaceH.Load(), m.addCR, m.note.text)
	}
	if s := ansi.Strip(m.mainLine(100)); !strings.Contains(s, "9600 ^H +CR") {
		t.Errorf("on, so shown after the rate: %q", s)
	}
	m.commandKey(tea.KeyPressMsg{Text: "x"}, "x")
	if m.backspaceH.Load() || m.note.text != "Backspace sends DEL" {
		t.Errorf("x again: ^H %v, note %q; want it back to DEL", m.backspaceH.Load(), m.note.text)
	}
	if want := [][2]bool{{true, false}, {true, true}, {false, true}}; fmt.Sprint(saved) != fmt.Sprint(want) {
		t.Errorf("saved %v, want %v", saved, want)
	}
}

// A window joining an open console gets its recent output replayed: with
// the line feed fix on, that shows without a staircase too.
func TestTheLineFeedFixAppliesToReplay(t *testing.T) {
	m := unconfirmedModel(9600)
	m.addCR = true
	m.event(serialx.Event{Data: []byte("one\ntwo\n"), Replay: true})
	if got := m.emu.CursorPosition(); got.X != 0 || got.Y != 2 {
		t.Errorf("cursor at %+v after a replayed \"one\\ntwo\\n\"; want the start of line 3", got)
	}
}

func TestNamingTheDeviceFromItsPrompt(t *testing.T) {
	m := unconfirmedModel(9600)
	m.opt.Name = "ttyUSB0"
	var saved []string
	m.opt.SaveDevice = func(n string) error { saved = append(saved, n); return nil }
	bar := func() string { return ansi.Strip(m.mainLine(100)) }

	m.event(serialx.Event{Data: []byte("\r\ncore-sw-01#")})
	if m.label() != "core-sw-01" || !strings.HasPrefix(bar(), " core-sw-01  ") || m.note.text != "" {
		t.Errorf("first name: label %q, bar %q, note %q; want it learned quietly", m.label(), bar(), m.note.text)
	}
	m.event(serialx.Event{Data: []byte("\r\ncore-sw-01(config-if)#")})
	if len(saved) != 1 {
		t.Errorf("saved %v; config mode is the same device, so once", saved)
	}
	if got := windowTitle(m.label()); got != "core-sw-01 · bauddy" {
		t.Errorf("title %q", got)
	}

	// The cable comes back on another device: last time's name is shown dim
	// with a ?, until a prompt says otherwise.
	m.event(serialx.Event{State: serialx.Disconnected})
	m.event(serialx.Event{State: serialx.Connected})
	if !strings.HasPrefix(bar(), " core-sw-01?  ") {
		t.Errorf("after a reconnect: bar %q; want last time's name, unconfirmed", bar())
	}
	// Output with no new prompt (a bell, a cursor control) mustn't confirm
	// the old name from the line left over from before.
	m.event(serialx.Event{Data: []byte("\a\x1b[?25h")})
	if !strings.HasPrefix(bar(), " core-sw-01?  ") {
		t.Errorf("after a bell: bar %q; want the old name still unconfirmed", bar())
	}
	m.event(serialx.Event{Data: []byte("edge-rtr-02>")}) // and a prompt with no newline before it
	if m.label() != "edge-rtr-02" || m.note.text != "" || saved[len(saved)-1] != "edge-rtr-02" {
		t.Errorf("another device: label %q, note %q, saved %v", m.label(), m.note.text, saved)
	}

	// Defaults, typed commands and bootloaders aren't names.
	for _, line := range []string{"\r\nSwitch#", "\r\nedge-rtr-02>show ver", "\r\nrommon 1 >"} {
		m.event(serialx.Event{Data: []byte(line)})
	}
	if m.label() != "edge-rtr-02" {
		t.Errorf("label %q; want it kept through non-names", m.label())
	}
}

func TestTheUsersOwnNameWins(t *testing.T) {
	m := unconfirmedModel(9600)
	m.opt.Name, m.opt.UserNamed = "lab-console", true
	m.event(serialx.Event{Data: []byte("\r\ncore-sw-01#")})
	if m.label() != "lab-console" || m.note.text != "" {
		t.Errorf("label %q, note %q; want the user's name kept", m.label(), m.note.text)
	}
}

func TestWindowTitleDropsControlCharacters(t *testing.T) {
	if got := windowTitle("core\x1b]0;evil\x07-sw"); got != "core]0;evil-sw · bauddy" {
		t.Errorf("got %q", got)
	}
}

func TestHelpShowsTheFixesAndTheirState(t *testing.T) {
	m := unconfirmedModel(9600)
	help := func() string { return ansi.Strip(m.helpBox()) }
	s := help()
	for _, want := range []string{"If something looks wrong", "x        Backspace shows ^? and won't erase  [off]", "n        Each line starts further right      [off]"} {
		if !strings.Contains(s, want) {
			t.Errorf("help missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Pause or resume") {
		t.Errorf("no AI tool connected, so no pause key in help:\n%s", s)
	}
	m.commandKey(tea.KeyPressMsg{Text: "x"}, "x")
	if s := help(); !strings.Contains(s, "won't erase  [on]") || !strings.Contains(s, "further right      [off]") {
		t.Errorf("after x: help should show it on:\n%s", s)
	}
}
