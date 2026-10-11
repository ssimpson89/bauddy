// Package tui is bauddy's interface: the device's screen plus one status
// line, which is quiet when things are normal and loud when they aren't.
package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/logfile"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/termtext"
)

type mode int

const (
	modeConsole      mode = iota // every key goes to the device
	modeCommand                  // next key is a bauddy command
	modeHelp                     // help overlay
	modeBaud                     // baud picker overlay
	modePasteConfirm             // asking before a risky paste
	modeProbeOffer               // detection heard nothing; offering to send Enter
	modeHistory                  // scrolling back through earlier output
)

// Options configures the TUI.
type Options struct {
	Session     *serialx.Session
	Name        string // short label for the status line, e.g. "core-sw-01" or "ttyUSB0"
	Keymap      keymap.Keymap
	LogPath     string // log from the start to this file or directory ("." for the current one)
	Detect      bool   // detect the baud rate at startup
	Recheck     bool   // the rate is a saved one: re-detect if output starts garbled
	KeepFraming bool   // the user chose the framing: detection only looks for the rate
	Version     string // shown in the help overlay

	// Save is called with a rate and framing that worked, to remember them
	// for this adapter.
	Save func(baud int, framing serialx.Framing) error
}

// note is the message shown after the baud rate.
type note struct {
	text string
	kind noteKind
}

type noteKind int

const (
	noteInfo   noteKind = iota // amber, clears itself
	noteSticky                 // amber, stays until a key
	noteError                  // red, stays until a key
)

// Model is the Bubble Tea model.
type Model struct {
	opt   Options
	sess  *serialx.Session
	emu   *vt.SafeEmulator
	keys  keymap.Keymap
	mode  mode
	state serialx.State

	width, height int

	note   note
	noteID int

	meter        serialx.GarbleMeter
	baudCursor   int
	detecting    bool
	recheckUntil time.Time // until then, garbage means the saved rate is wrong
	quiet        bool      // connected, but nothing has arrived yet

	heard    bool             // any output has arrived (including what detection heard)
	unlogged []byte           // output since the last log, written at the start of the next one
	screen   termtext.Cleaner // tracks the cursor line, e.g. the prompt
	log      *logfile.Log     // nil when not logging
	logs     []string         // every log file written this session

	hist history // earlier output, for scrolling back and searching

	paste   *pasteJob     // nil when no paste is in progress
	confirm *confirmPaste // a risky paste awaiting an answer

	light bool // the terminal has a light background
}

type (
	eventMsg      serialx.Event
	sessionDone   struct{}
	noteClearMsg  struct{ id int }
	quietCheckMsg struct{}
	detectDoneMsg struct {
		serialx.Result
		probe bool
		err   error
	}
)

const (
	noteFor       = 2 * time.Second        // how long an info note stays
	firstNoteFor  = 4 * time.Second        // a note at startup stays longer, while you get your bearings
	breakDuration = 300 * time.Millisecond // within the usual 250-500ms
	quietAfter    = 3 * time.Second        // silence before saying nothing has arrived
	// recheckWithin: garbage this soon after connecting at a saved rate
	// probably means a different device is on the adapter, so detect again.
	// Later garbage is more likely a bootloader changing rate: just hint.
	recheckWithin = 10 * time.Second
)

// New builds the model and wires the emulator's input side to the port.
// It fails only if opt.LogPath is set and the log can't be opened.
func New(opt Options) (*Model, error) {
	m := &Model{
		opt:   opt,
		sess:  opt.Session,
		emu:   vt.NewSafeEmulator(80, 24),
		keys:  opt.Keymap,
		state: serialx.Connected,
	}
	if opt.Recheck {
		m.recheckUntil = time.Now().Add(recheckWithin)
	}
	m.emu.SetScrollbackSize(1) // history keeps earlier output; 0 would mean the default, 10,000 lines
	if opt.LogPath != "" {
		msg, err := m.startLog()
		if err != nil {
			return nil, fmt.Errorf("can't start the log: %w", err)
		}
		m.note = note{text: msg}
	}
	// Keys we send to the emulator, and its replies to terminal queries
	// (cursor position, device attributes), come out of emu.Read and go to
	// the port. Not io.Copy: a write error while the cable is unplugged must
	// not stop the pump, or the next keypress would block forever.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := m.emu.Read(buf)
			if n > 0 {
				_, _ = m.sess.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return m, nil
}

// Init starts event delivery, the quiet-console check and, if asked, baud
// detection.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.waitEvent(),
		m.checkQuietLater(),
		tea.RequestBackgroundColor, // to pick overlay colours for light or dark themes
	}
	if m.note.text != "" {
		cmds = append(cmds, m.clearNoteAfter(firstNoteFor))
	}
	if m.opt.Detect {
		cmds = append(cmds, m.startDetect(false))
	}
	return tea.Batch(cmds...)
}

func (m *Model) waitEvent() tea.Cmd {
	ch := m.sess.Events()
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return sessionDone{}
		}
		return eventMsg(e)
	}
}

func (m *Model) checkQuietLater() tea.Cmd {
	return tea.Tick(quietAfter, func(time.Time) tea.Msg { return quietCheckMsg{} })
}

// setNote shows an info note that clears itself.
func (m *Model) setNote(s string) tea.Cmd {
	m.note = note{text: s}
	return m.clearNoteAfter(noteFor)
}

// setError shows a red note that stays until the next key.
func (m *Model) setError(s string) tea.Cmd {
	m.noteID++ // cancel any pending clear
	m.note = note{text: s, kind: noteError}
	return nil
}

func (m *Model) clearNoteAfter(d time.Duration) tea.Cmd {
	m.noteID++
	id := m.noteID
	return tea.Tick(d, func(time.Time) tea.Msg { return noteClearMsg{id} })
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.emu.Resize(m.width, max(1, m.height-1))
		m.scrollTo(m.hist.top) // keep history's view in range
		return m, nil

	case eventMsg:
		return m, tea.Batch(m.waitEvent(), m.event(serialx.Event(msg)))

	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
		return m, nil

	case sessionDone:
		return m, tea.Quit

	case noteClearMsg:
		if msg.id == m.noteID && m.note.kind == noteInfo {
			m.note = note{}
		}
		return m, nil

	case quietCheckMsg:
		switch {
		case m.heard: // output has arrived; nothing to say
		case m.detecting || m.mode != modeConsole || m.state != serialx.Connected:
			return m, m.checkQuietLater() // not now; look again later
		default:
			m.quiet = true
		}
		return m, nil

	case detectDoneMsg:
		return m, m.detectDone(msg)

	case tea.PasteMsg:
		if m.mode == modeHistory && m.hist.editing {
			m.typeSearch(msg.Content)
			return m, nil
		}
		if m.mode != modeConsole || m.paste != nil {
			return m, m.setNote("can't paste right now")
		}
		return m, m.receivePaste(msg.Content)

	case pasteProgressMsg:
		return m, m.pasteProgress(msg)

	case pasteDoneMsg:
		return m, m.pasteDone(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	}
	return m, nil
}

func (m *Model) event(e serialx.Event) tea.Cmd {
	var cmds []tea.Cmd
	if len(e.Data) > 0 {
		cmds = append(cmds, m.output(e.Data))
		if m.mode == modeProbeOffer {
			m.mode = modeConsole // the device spoke after all
		}
		if m.meter.Add(e.Data) && m.mode == modeConsole && !m.detecting && m.paste == nil && time.Now().Before(m.recheckUntil) {
			m.recheckUntil = time.Time{} // once: the saved rate is wrong for what's plugged in now
			cmds = append(cmds, m.startDetect(false))
			m.note.text = "garbled at the saved rate; detecting…" // startDetect set a sticky note; reword it
		}
	}
	if e.State != m.state {
		if e.State == serialx.Connected {
			m.meter.Reset() // the device may have come back at a different rate
			cmds = append(cmds, m.logMark("reconnected"))
		} else {
			cmds = append(cmds, m.logMark("disconnected"))
			if m.paste != nil {
				cmds = append(cmds, m.abandonPaste("the adapter was unplugged"))
			}
		}
	}
	m.state = e.State
	return tea.Batch(cmds...)
}

func (m *Model) sendKey(k tea.KeyPressMsg) {
	m.emu.SendKey(uv.KeyPressEvent{Code: k.Code, Text: k.Text, Mod: k.Mod})
}

func (m *Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.note.kind != noteInfo && m.note.text != "" && !m.detecting { // keep "detecting baud…" up
		m.note = note{}
		if key == "esc" {
			return m, nil // esc just dismisses the message
		}
	}
	switch m.mode {
	case modeConsole:
		return m.consoleKey(k, key)
	case modeHelp:
		m.mode = modeConsole
		return m, nil
	case modeBaud:
		return m.baudKey(key)
	case modePasteConfirm:
		return m.confirmKey(key)
	case modeProbeOffer:
		return m.probeKey(k, key)
	case modeHistory:
		return m.historyKey(k, key)
	}
	return m.commandKey(k, key)
}

func (m *Model) consoleKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch {
	case m.detecting && key != m.keys.Escape:
		return m, nil // the port is busy detecting; the status line says so
	case m.paste != nil && (key == "esc" || key == m.keys.Escape):
		return m, m.stopPaste()
	case m.paste != nil:
		return m, m.setNote("pasting; esc stops it") // typing now would corrupt the paste
	case key == m.keys.Escape:
		m.mode = modeCommand
		return m, nil
	}
	m.sendKey(k)
	return m, nil
}

func (m *Model) probeKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	m.mode = modeConsole
	switch key {
	case "enter":
		return m, m.startDetect(true)
	case "esc":
		return m, nil
	case m.keys.Escape:
		m.mode = modeCommand
		return m, nil
	}
	m.sendKey(k) // anything else: carry on typing
	return m, nil
}

// commandKey runs one command. Command mode is one-shot: afterwards you're
// back at the console.
func (m *Model) commandKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	act := m.keys.Lookup(key)
	if act == keymap.ActNone {
		return m, m.setNote(fmt.Sprintf("%s isn't a command; ? shows the keys", key))
	}
	m.mode = modeConsole
	if m.detecting && act.UsesPort() {
		m.note = note{text: "detecting baud… (" + key + " must wait)", kind: noteSticky}
		return m, nil
	}
	switch act {
	case keymap.ActLeaveCommand:
	case keymap.ActSendEscapeKey:
		m.sendKey(k)
	case keymap.ActHelp:
		m.mode = modeHelp
	case keymap.ActHistory:
		m.openHistory(false)
	case keymap.ActSearch:
		m.openHistory(true)
	case keymap.ActBaud:
		m.mode = modeBaud
		m.baudCursor = max(0, slices.Index(baudChoices, m.sess.Baud()))
	case keymap.ActDetect:
		return m, m.startDetect(false)
	case keymap.ActBreak:
		if err := m.sess.Break(breakDuration); err != nil {
			return m, m.setError("can't send BREAK: " + err.Error())
		}
		return m, m.done("break sent", "sent BREAK")
	case keymap.ActSyncSize:
		w, h := m.emu.Width(), m.emu.Height()
		if _, err := fmt.Fprintf(m.sess, "stty rows %d cols %d\r", h, w); err != nil {
			return m, m.setError("can't send the window size: " + err.Error())
		}
		return m, m.done(fmt.Sprintf("size %dx%d sent", w, h), fmt.Sprintf("sent window size %dx%d", w, h))
	case keymap.ActClear:
		_, _ = m.emu.Write([]byte("\x1b[2J\x1b[H")) // clear screen, cursor home
	case keymap.ActLog:
		return m, m.toggleLog()
	case keymap.ActReconnect:
		m.sess.Reconnect()
		return m, m.setNote("reconnecting…")
	case keymap.ActQuit:
		return m, tea.Quit
	}
	return m, nil
}

// baudChoices are the picker's options, selectable with 1-8.
var baudChoices = []int{9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600}

func (m *Model) baudKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.baudCursor = max(0, m.baudCursor-1)
	case "down", "j":
		m.baudCursor = min(len(baudChoices)-1, m.baudCursor+1)
	case "esc", "q":
		m.mode = modeConsole
	case "enter":
		return m.applyBaud(baudChoices[m.baudCursor])
	case "d":
		m.mode = modeConsole
		return m, m.startDetect(false)
	default:
		if i, err := strconv.Atoi(key); err == nil && i >= 1 && i <= len(baudChoices) {
			return m.applyBaud(baudChoices[i-1])
		}
	}
	return m, nil
}

func (m *Model) applyBaud(b int) (tea.Model, tea.Cmd) {
	m.mode = modeConsole
	if err := m.sess.SetBaud(b); err != nil {
		return m, m.setError(fmt.Sprintf("can't set %d baud: %v", b, err))
	}
	m.meter.Reset()
	set := m.lineSettings()
	return m, tea.Batch(m.done(set, set), m.save())
}

// startDetect runs baud detection in the background. Without probe it only
// listens; if it hears nothing, the status line offers to send Enter.
func (m *Model) startDetect(probe bool) tea.Cmd {
	switch {
	case m.detecting:
		return m.setNote("already detecting")
	case m.paste != nil:
		return m.setNote("wait for the paste to finish")
	}
	m.detecting, m.quiet = true, false
	m.note = note{text: "detecting baud…", kind: noteSticky}
	sess, keep := m.sess, m.opt.KeepFraming
	return func() tea.Msg {
		r, err := sess.Detect(serialx.DetectConfig{Probe: probe, KeepFraming: keep})
		return detectDoneMsg{Result: r, probe: probe, err: err}
	}
}

func (m *Model) detectDone(msg detectDoneMsg) tea.Cmd {
	m.detecting = false
	m.note = note{}
	switch {
	case errors.Is(msg.err, serialx.ErrNoSignal) && !msg.probe && m.mode == modeConsole:
		m.mode = modeProbeOffer
		return nil
	case errors.Is(msg.err, serialx.ErrNoSignal):
		return tea.Batch(m.setNote("nothing heard at any rate"), m.checkQuietLater())
	case msg.err != nil:
		return m.setError("can't detect the baud rate: " + msg.err.Error())
	}
	m.meter.Reset()
	found := m.lineSettings()
	return tea.Batch(m.output(msg.Heard), m.done("detected "+found, found+" (detected)"), m.save())
}

// lineSettings describes the rate and framing, e.g. "9600 baud 7E1".
func (m *Model) lineSettings() string {
	return serialx.LineSettings(m.sess.Baud(), m.sess.Framing())
}

// save remembers the current rate and framing for this adapter.
func (m *Model) save() tea.Cmd {
	if m.opt.Save == nil {
		return nil
	}
	if err := m.opt.Save(m.sess.Baud(), m.sess.Framing()); err != nil {
		return m.setError("can't save the baud rate: " + err.Error())
	}
	return nil
}
