// Package tui is bauddy's interface: the device's screen plus one status
// line, which is quiet when things are normal and loud when they aren't.
package tui

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync/atomic"
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

// Port is the serial connection the console drives: a *serialx.Session, or
// a console shared with other bauddy windows.
type Port interface {
	io.Writer
	Path() string
	Events() <-chan serialx.Event
	Tap() (<-chan []byte, func())
	Baud() int
	Framing() serialx.Framing
	SetBaud(baud int) error
	Break(d time.Duration) error
	Reconnect()
	Detect(cfg serialx.DetectConfig) (serialx.Result, error)
}

// Options configures the TUI.
type Options struct {
	Session     Port
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

	// BackspaceH and AddCR start the two keyboard and screen fixes (x and n)
	// on; SaveKeys remembers them for this adapter when they change.
	BackspaceH, AddCR bool
	SaveKeys          func(backspaceH, addCR bool) error
}

// note is the message shown after the baud rate.
type note struct {
	text string
	kind noteKind
}

type noteKind int

const (
	noteInfo   noteKind = iota // accent colour, clears itself
	noteSticky                 // accent colour, stays until a key
	noteError                  // red, stays until a key
)

// Model is the Bubble Tea model.
type Model struct {
	opt    Options
	sess   Port
	shared Shared // nil unless the console can be shared
	// watching means this window is on the console as the AI tool (bauddy
	// started from its shell), so what's typed here isn't sent.
	watching bool

	ignoredUntil time.Time // the AI tool's bar says your keys were ignored until then
	emu          *vt.SafeEmulator
	keys         keymap.Keymap
	mode         mode
	state        serialx.State
	reconnecting bool // you asked to reconnect, so a closed port isn't an unplugged adapter

	width, height int

	note   note
	noteID int

	meter        serialx.GarbleMeter
	baudCursor   int
	detecting    bool
	recheckUntil time.Time // until then, garbage means the saved rate is wrong
	quiet        bool      // connected, but nothing has arrived yet
	// answered: the device has answered in plain text since the rate was
	// last set (rateAt), here or in another window, or the port reconnected.
	// Until it has, the rate is unconfirmed, and if it's slower than the
	// device's every key reaches it as a BREAK (on a Linux console the next
	// key is then a SysRq command) while its replies are dropped, so it
	// seems silent. So until then only Enter goes out.
	answered   bool
	backspaceH atomic.Bool // Backspace sends ^H (x); the key pump reads it
	addCR      bool        // show each bare line feed as a new line (n)
	lastCR     bool        // the last byte shown was a CR, for addCR
	rateAt     int         // the rate when answered was last reset
	answer     []byte      // plain text since then, across reads, until it's enough
	awaitReply bool        // Enter was pressed at an unconfirmed rate; a noReplyMsg is due
	offer      string      // the probe offer's opening words, e.g. "no reply at 9600."

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
	noReplyMsg    struct{}
	detectDoneMsg struct {
		serialx.Result
		probe bool
		err   error
	}
)

const (
	noteFor       = 2 * time.Second         // how long an info note stays
	firstNoteFor  = 4 * time.Second         // a note at startup stays longer, while you get your bearings
	breakDuration = 300 * time.Millisecond  // within the usual 250-500ms
	quietAfter    = 3 * time.Second         // silence before saying nothing has arrived
	noReplyAfter  = 1500 * time.Millisecond // silence after Enter at an unconfirmed rate before offering to detect
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
	m.shared, _ = opt.Session.(Shared)
	if j, ok := opt.Session.(interface{ Joined() bool }); ok && j.Joined() {
		m.note = note{text: "joined the open console"}
	}
	if w, ok := opt.Session.(interface{ Watching() bool }); ok && w.Watching() {
		m.watching, m.shared = true, nil // the AI tool on the status line would be this window
		m.note = note{text: watchingNote, kind: noteSticky}
	}
	if opt.Recheck {
		m.recheckUntil = time.Now().Add(recheckWithin)
	}
	m.backspaceH.Store(opt.BackspaceH)
	m.addCR = opt.AddCR
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
				if m.backspaceH.Load() {
					backspaceToH(buf[:n])
				}
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
		m.waitShare(),
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

	case shareMsg:
		return m, tea.Batch(m.waitShare(), m.shareNote(msg.note))

	case shareErrMsg:
		return m, m.setError(msg.err.Error())

	case noteClearMsg:
		if msg.id == m.noteID && m.note.kind == noteInfo {
			m.note = note{}
			if m.detecting {
				m.note = detectingNote // a note shown meanwhile has gone; detection hasn't
			}
		}
		return m, nil

	case noReplyMsg:
		m.awaitReply = false
		if !m.confirmed() && m.mode == modeConsole && !m.detecting && m.paste == nil && m.state == serialx.Connected {
			m.mode, m.offer = modeProbeOffer, fmt.Sprintf("no reply at %d · wrong baud?", m.sess.Baud())
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
	if e.Replay && len(e.Data) > 0 { // output from before this window joined: for the screen and history only
		m.show(e.Data)
		m.rememberLines(m.screen.Feed(e.Data))
		m.heard = true // but not answered: it may be from before a rate change
		return nil
	}
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
			m.resetAnswer()
			cmds = append(cmds, m.logMark("reconnected"))
			if m.reconnecting {
				m.reconnecting = false
				cmds = append(cmds, m.setNote("reconnected"))
			}
		} else {
			cmds = append(cmds, m.logMark("disconnected"))
			if m.paste != nil && m.reconnecting {
				cmds = append(cmds, m.abandonPaste("you reconnected"))
			} else if m.paste != nil {
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
	switch {
	case m.mode != modeConsole || m.shared == nil:
	case m.agentWorking():
		if handled, cmd := m.agentKey(key); handled {
			return m, cmd
		}
	default:
		m.shared.KeyPressed() // so an AI tool waits for you
	}
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
	case m.detectingAnywhere() && key != m.keys.Escape:
		return m, nil // the port is busy detecting; the status line says so
	case m.watching && key != m.keys.Escape:
		return m, m.setNote(watchingNote)
	case m.paste != nil && (key == "esc" || key == m.keys.Escape):
		return m, m.stopPaste()
	case m.paste != nil:
		return m, m.setNote("pasting; esc stops it") // typing now would corrupt the paste
	case key == m.keys.Escape:
		m.mode = modeCommand
		return m, nil
	case key != "enter" && !m.confirmed():
		return m, m.setNote(unconfirmedNote)
	}
	m.sendKey(k)
	if key == "enter" && !m.confirmed() && !m.awaitReply {
		// Nothing has come back at this rate yet: if Enter gets no reply
		// either, the rate is likely wrong, so offer to detect it.
		m.awaitReply = true
		return m, tea.Tick(noReplyAfter, func(time.Time) tea.Msg { return noReplyMsg{} })
	}
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
	if !m.confirmed() {
		return m, m.setNote(unconfirmedNote)
	}
	m.sendKey(k) // anything else: carry on typing
	return m, nil
}

// unconfirmedNote is why a key wasn't sent: nothing has come back at this
// rate yet, and at a wrong one the key would go out as a BREAK.
const unconfirmedNote = "nothing back from the device yet · press Enter first"

// confirmed says whether the device has answered since the rate was set.
func (m *Model) confirmed() bool {
	m.rateChanged()
	return m.answered
}

// resetAnswer makes the rate unconfirmed until the device answers again.
func (m *Model) resetAnswer() {
	m.answered, m.answer = false, nil
	if m.sess != nil {
		m.rateAt = m.sess.Baud()
	}
}

// rateChanged resets the answer if the rate has changed since, such as
// by another window.
func (m *Model) rateChanged() {
	if m.sess != nil && m.sess.Baud() != m.rateAt {
		m.resetAnswer()
	}
}

// noteAnswer counts b towards an answer. Two bytes of plain text in a row,
// across reads, are one; garbage starts the count again: at a rate slower
// than the device's, some still gets through.
func (m *Model) noteAnswer(b []byte) {
	m.rateChanged()
	if !serialx.Answered(b) {
		m.answer = nil
		return
	}
	m.answer = append(m.answer, b...)
	if len(m.answer) >= 2 {
		m.answered, m.answer = true, nil
	}
}

// commandKey runs one command. Command mode is one-shot: afterwards you're
// back at the console.
func (m *Model) commandKey(k tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	act := m.keys.Lookup(key)
	if act == keymap.ActNone {
		return m, m.setNote(fmt.Sprintf("%s isn't a command; ? shows the keys", key))
	}
	m.mode = modeConsole
	if m.detectingAnywhere() && act.UsesPort() {
		if m.note.kind != noteError { // don't hide an error that hasn't been seen
			m.note = note{text: "detecting baud… " + key + " didn't run; try again when it's done", kind: noteSticky}
		}
		return m, nil
	}
	switch act {
	case keymap.ActLeaveCommand:
	case keymap.ActSendEscapeKey:
		if !m.confirmed() {
			return m, m.setNote(unconfirmedNote)
		}
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
		if !m.confirmed() {
			return m, m.setNote(unconfirmedNote)
		}
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
		m.reconnecting = m.reconnecting || m.state == serialx.Connected // unplugged stays unplugged
		m.sess.Reconnect()
	case keymap.ActPause:
		return m, m.togglePause()
	case keymap.ActBackspace:
		on := !m.backspaceH.Load()
		m.backspaceH.Store(on)
		say := map[bool]string{true: "Backspace sends ^H", false: "Backspace sends DEL"}[on]
		return m, tea.Batch(m.done(say, say), m.saveKeys())
	case keymap.ActAddCR:
		m.addCR = !m.addCR
		say := map[bool]string{true: "starting each line at the left", false: "showing line feeds as they come"}[m.addCR]
		return m, tea.Batch(m.done(say, say), m.saveKeys())
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
	m.resetAnswer()
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
	m.note = detectingNote
	sess, keep := m.sess, m.opt.KeepFraming
	return func() tea.Msg {
		r, err := sess.Detect(serialx.DetectConfig{Probe: probe, KeepFraming: keep})
		return detectDoneMsg{Result: r, probe: probe, err: err}
	}
}

// detectingNote stays on the status line while detection has the port.
var detectingNote = note{text: "detecting baud… typing is ignored until it's done", kind: noteSticky}

const watchingNote = "watching only: started from an AI tool's shell, so keys aren't sent"

// detectDone shows what detection found. An error that arrived meanwhile,
// such as the log failing, stays up until a key, as errors do.
func (m *Model) detectDone(msg detectDoneMsg) tea.Cmd {
	m.detecting = false
	unseen := m.note
	m.note = note{}
	cmd := m.detected(msg)
	switch {
	case unseen.kind != noteError:
	case m.note.kind == noteError:
		m.note.text = unseen.text + "; " + m.note.text
	default:
		m.note = unseen
	}
	return cmd
}

func (m *Model) detected(msg detectDoneMsg) tea.Cmd {
	switch {
	case errors.Is(msg.err, serialx.ErrNoSignal) && !msg.probe && m.mode == modeConsole:
		m.mode, m.offer = modeProbeOffer, "nothing heard."
		return nil
	case errors.Is(msg.err, serialx.ErrNoSignal):
		again := m.keys.Escape + " d"
		if m.mode == modeCommand {
			again = "d" // the command bar is open already
		}
		return tea.Batch(m.setNote("nothing heard at any rate; "+again+" tries again"), m.checkQuietLater())
	case msg.err != nil:
		return m.setError("can't detect the baud rate: " + msg.err.Error())
	}
	m.meter.Reset()
	found := m.lineSettings()
	m.resetAnswer()
	m.answered = true // it heard the device at this rate
	say := "detected " + found
	if msg.StrayLogin {
		say += " · a stray character may have gone in as a username; Enter starts the login again"
	}
	return tea.Batch(m.output(msg.Heard), m.done(say, found+" (detected)"), m.save())
}

// lineSettings describes the rate and framing, e.g. "9600 baud 7E1".
func (m *Model) lineSettings() string {
	return serialx.LineSettings(m.sess.Baud(), m.sess.Framing())
}

// save remembers the current rate and framing for this adapter.
// saveKeys remembers the Backspace and line feed fixes for this adapter.
func (m *Model) saveKeys() tea.Cmd {
	if m.opt.SaveKeys == nil {
		return nil
	}
	if err := m.opt.SaveKeys(m.backspaceH.Load(), m.addCR); err != nil {
		return m.setError("can't save the setting: " + err.Error())
	}
	return nil
}

// backspaceToH turns DEL, what Backspace sends, into ^H, in place.
func backspaceToH(b []byte) {
	for i, c := range b {
		if c == 0x7f {
			b[i] = 0x08
		}
	}
}

// withCR puts a carriage return before each line feed that hasn't one, so
// output from a device that ends lines with LF alone doesn't staircase. cr
// says whether the last byte shown was a CR, across reads.
func withCR(b []byte, cr *bool) []byte {
	out := make([]byte, 0, len(b)+8)
	for _, c := range b {
		if c == '\n' && !*cr {
			out = append(out, '\r')
		}
		out = append(out, c)
		*cr = c == '\r'
	}
	return out
}

func (m *Model) save() tea.Cmd {
	if m.opt.Save == nil {
		return nil
	}
	if err := m.opt.Save(m.sess.Baud(), m.sess.Framing()); err != nil {
		return m.setError("can't save the baud rate: " + err.Error())
	}
	return nil
}
