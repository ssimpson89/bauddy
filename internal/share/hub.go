package share

import (
	"bytes"
	"crypto/rand"
	"errors"
	"net"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ssimpson89/bauddy/internal/paste"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/termtext"
)

// Port is the serial connection a hub shares: a *serialx.Session. Its
// Events channel closes after Close, which is what shuts the hub down.
type Port interface {
	Events() <-chan serialx.Event
	Write([]byte) (int, error)
	Baud() int
	Framing() serialx.Framing
	SetBaud(baud int) error
	Break(d time.Duration) error
	Reconnect()
	Detect(cfg serialx.DetectConfig) (serialx.Result, error)
	Close() error
}

const (
	replayMax      = 64 << 10        // recent output a joining window gets
	agentReplayMax = 2 << 10         // of the current line, all an AI tool gets on joining
	typingWindow   = 3 * time.Second // an AI tool can't take the keyboard this soon after the user's last key
	queueMax       = 1024            // messages waiting for one client before it's dropped as too slow
	sendTimeout    = 5 * time.Second
	helloTimeout   = 5 * time.Second
	breakLength    = 300 * time.Millisecond // as the console sends it
)

var (
	// pager matches a pager waiting at the end of the output (--More--,
	// Junos ---(more)---), where a backspace could be taken as a key.
	pager = regexp.MustCompile(`(?i)-\s*\(?more\b[^\n]*$`)
	// lineStart finds where the cursor's line begins: after a newline, a
	// carriage return, or a cursor move (screens drawn without newlines).
	lineStart = regexp.MustCompile("[\r\n]|\x1b\\[[0-9;]*[Hf]")
)

// cursorText is the cursor's line as the user sees it, without escape
// sequences. Call with mu held.
func (h *hub) cursorText() string {
	var c termtext.Cleaner
	c.Feed(h.cursorLine())
	return c.Partial()
}

// cursorLine is the output since the cursor's line began. Call with mu held.
func (h *hub) cursorLine() []byte {
	start := 0
	for _, loc := range lineStart.FindAllIndex(h.ring, -1) {
		start = loc[1]
	}
	return h.ring[start:]
}

// hub shares one port between the clients connected to it: the windows
// and at most one AI tool. It runs in whichever process holds the port.
//
// mu guards every field below it. Bytes reach the port only in write and
// in the stop path, both under mu, so checking the AI tool's lease and
// writing its bytes can't be split by a stop. (A port that blocks on write
// would stall the hub; serial ports don't, short of hardware flow control,
// which bauddy doesn't use.)
type hub struct {
	port Port
	now  func() time.Time
	id   string // tells clients this hub from the one before a takeover

	mu        sync.Mutex
	peers     map[*peer]struct{}
	ring      []byte // the latest output, for joining windows
	connected bool
	detecting bool
	busy      bool  // a port operation is running
	agent     *peer // the connected AI tool, if any
	lease     bool  // the AI tool has the keyboard
	paused    bool  // the user paused the AI tool
	lastKey   time.Time
	userTyped int    // characters the user has on the line, not yet entered
	agentLine typing // what the AI tool has on the line
	closed    bool
}

// typing is what the AI tool has typed since it last pressed Enter.
type typing struct {
	chars   int    // characters, for erasing them
	ctrl    bool   // it sent a control key too, so they can't be counted
	text    []byte // for the history mark
	entered bool   // it pressed Enter during this lease
}

// peer is one connected client. Its own two goroutines read and write the
// connection; queue hands it messages without waiting.
type peer struct {
	w     *wire
	conn  net.Conn
	out   chan msg
	done  chan struct{} // closed when dropped
	agent bool
	name  string
	once  sync.Once
}

func newHub(p Port) *hub {
	h := &hub{port: p, now: time.Now, id: rand.Text(), peers: map[*peer]struct{}{}, connected: true}
	go h.pump()
	return h
}

// pump shares the port's output and state changes until the port closes.
func (h *hub) pump() {
	for e := range h.port.Events() {
		h.mu.Lock()
		if len(e.Data) > 0 {
			h.output(e.Data)
		}
		if up := e.State == serialx.Connected; up != h.connected {
			h.connected = up
			h.broadcastState()
		}
		h.mu.Unlock()
	}
	h.mu.Lock()
	h.closed = true
	peers := h.peers
	h.peers = map[*peer]struct{}{}
	h.mu.Unlock()
	for p := range peers {
		p.drop()
	}
}

// output records device output and sends it to everyone. Call with mu held.
func (h *hub) output(b []byte) {
	h.ring = append(h.ring, b...)
	if over := len(h.ring) - replayMax; over > 0 {
		h.ring = h.ring[over:]
	}
	for p := range h.peers {
		h.queue(p, msg{T: tData, Data: b})
	}
}

// serve talks to one client until it goes away.
func (h *hub) serve(conn net.Conn) {
	w := newWire(conn)
	_ = conn.SetReadDeadline(h.now().Add(helloTimeout))
	hello, err := w.recv()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || hello.T != tHello {
		conn.Close()
		return
	}
	if hello.Version != version {
		_ = w.send(msg{T: tBye, Text: "another bauddy version has this console open · close that one, then r"})
		conn.Close()
		return
	}
	p := &peer{w: w, conn: conn, out: make(chan msg, queueMax), done: make(chan struct{}), agent: hello.Agent, name: hello.Name}

	h.mu.Lock()
	switch {
	case h.closed: // shutting down: the client will find whoever holds the port next
		h.mu.Unlock()
		conn.Close()
		return
	case p.agent && h.agent != nil:
		h.mu.Unlock()
		_ = w.send(msg{T: tBye, Text: "another AI tool is using this console"})
		conn.Close()
		return
	}
	h.peers[p] = struct{}{}
	h.paused = h.paused || hello.Paused // the user's pause outlives the process that held the port
	if p.agent {
		h.agent = p
		h.note(p.name + " joined")
	}
	// The state, then the output so far, then live output: all queued under
	// mu, so nothing is lost or repeated in between. The client counts on
	// the state coming first.
	h.broadcastState()
	if replay := h.replay(p.agent, hello.Replay); len(replay) > 0 {
		h.queue(p, msg{T: tData, Data: replay, Replay: true})
	} else if !p.agent && hello.Hub != "" && hello.Hub != h.id && len(h.ring) > 0 {
		// It rejoined after a takeover: what this hub has heard so far is
		// new to it.
		h.queue(p, msg{T: tData, Data: bytes.Clone(h.ring)})
	}
	h.mu.Unlock()

	go p.writeLoop()
	h.readLoop(p)
	h.leave(p)
}

// replay is the output a new client starts with: for a window, the recent
// output from the start of a line; for an AI tool, only the line the
// cursor is on, so it never sees what came before it joined.
func (h *hub) replay(agent, wanted bool) []byte {
	if agent {
		line := h.cursorLine()
		return bytes.Clone(line[max(0, len(line)-agentReplayMax):])
	}
	if !wanted {
		return nil
	}
	if i := bytes.IndexByte(h.ring, '\n'); i >= 0 && len(h.ring) == replayMax {
		return bytes.Clone(h.ring[i+1:]) // the oldest line is cut off: start at the next
	}
	return bytes.Clone(h.ring)
}

// leave forgets a client that went away. An AI tool leaving mid-command
// has its unsent text erased, so it can't leave half a command on the line.
func (h *hub) leave(p *peer) {
	p.drop()
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.peers[p]; !ok {
		return
	}
	delete(h.peers, p)
	if p == h.agent {
		if h.lease || h.agentLine.chars > 0 { // mid-command, or it left text it never entered
			h.revoke(false)
		}
		h.agent = nil
		h.note(p.name + " left · {keys} h reviews")
	}
	h.broadcastState()
}

func (h *hub) readLoop(p *peer) {
	for {
		m, err := p.w.recv()
		if err != nil {
			return
		}
		switch m.T {
		case tWrite:
			h.write(p, m.Data)
		case tKey:
			h.mu.Lock()
			h.lastKey = h.now()
			h.mu.Unlock()
		case tReq:
			go h.request(p, m)
		}
	}
}

// write sends a client's bytes to the device. While the AI tool has the
// keyboard only its bytes get through, so the device's line never mixes
// the user's typing with the tool's; and once the user stops it, nothing
// more of it gets through.
func (h *hub) write(p *peer, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case p.agent && (!h.lease || h.paused):
		return
	case p.agent:
		h.trackAgent(b)
	case h.lease:
		return
	default:
		h.lastKey = h.now() // a paste counts as typing too
		h.trackUser(b)
	}
	_, _ = h.port.Write(b)
}

// trackUser counts what the user has typed and not entered, so an AI tool
// doesn't type onto it. Arrow keys and the like arrive as escape sequences,
// which aren't text. The user taking the line also ends the AI tool's claim
// on it: a later stop must not backspace a count from text the user has
// since edited. Call with mu held.
func (h *hub) trackUser(b []byte) {
	if len(b) > 0 {
		h.agentLine = typing{}
	}
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '\r' || c == '\n' || c == 0x03 || c == 0x15: // Enter, ctrl+c, ctrl+u
			h.userTyped, h.agentLine = 0, typing{} // the line, whoever typed it, is gone
		case c == 0x7f || c == '\b':
			h.userTyped = max(0, h.userTyped-1)
		case c == 0x1b:
			i += escapeLen(b[i+1:])
		case c >= 0x20 && utf8.RuneStart(c):
			h.userTyped++
		}
	}
}

// escapeLen is how many bytes after an ESC belong to its sequence: a CSI
// or SS3 sequence (ESC [ 1 ; 5 A, ESC O P) runs to its final byte; anything
// else (alt+key) is ESC and one more.
func escapeLen(rest []byte) int {
	if len(rest) == 0 {
		return 0
	}
	if rest[0] != '[' && rest[0] != 'O' {
		return 1
	}
	for i := 1; i < len(rest); i++ {
		if rest[i] >= 0x40 {
			return i + 1
		}
	}
	return len(rest)
}

// trackAgent follows what the AI tool types, so a stop can erase it and
// the windows' history can mark its commands. Call with mu held.
func (h *hub) trackAgent(b []byte) {
	t := &h.agentLine
	for _, c := range b {
		switch {
		case c == '\r' || c == '\n':
			if len(t.text) > 0 {
				h.markCommand(string(t.text))
			}
			*t = typing{entered: true}
		case c == 0x03 || c == 0x15: // ctrl+c, ctrl+u: the line is gone
			*t = typing{}
		case c >= 0x20 && c != 0x7f:
			if utf8.RuneStart(c) {
				t.chars++
			}
			t.text = append(t.text, c)
		default:
			t.ctrl = true // a backspace, tab or escape: the line can't be counted
		}
	}
}

// markCommand puts the AI tool's command in the windows' history and log,
// except an answer to a password prompt: the device doesn't show that, so
// history mustn't either. Call with mu held.
func (h *hub) markCommand(text string) {
	if paste.AtLogin(h.cursorText()) {
		text = "answered a login prompt"
	} else {
		text = "typed: " + text
	}
	for p := range h.peers {
		if !p.agent {
			h.queue(p, msg{T: tMark, Text: h.agent.name + " " + text})
		}
	}
}

// Why the user stopped the AI tool, as it's told.
const (
	StopCleared     = "cleared"          // its unsent command was erased
	StopLeft        = "left on the line" // its unsent text couldn't be erased
	StopRunning     = "running"          // its command was entered and may still be running
	StopInterrupted = "interrupted"      // the user pressed ctrl+c
	StopPaused      = "paused"           // nothing of it was on the line
)

// revoke takes the keyboard back from the AI tool, erasing what it typed
// and didn't enter, and says what happened. Call with mu held.
func (h *hub) revoke(interrupt bool) (note, reason string) {
	t := h.agentLine
	h.lease, h.agentLine = false, typing{}
	switch {
	case interrupt:
		return "paused · interrupted", StopInterrupted
	case t.chars > 0 && !t.ctrl && !pager.Match(h.ring) && !paste.AtLogin(h.cursorText()):
		// Backspace (0x08) is understood almost everywhere; ctrl+u isn't.
		_, _ = h.port.Write(bytes.Repeat([]byte{'\b'}, t.chars))
		return "paused · unsent command cleared", StopCleared
	case t.chars > 0:
		return "paused · its text is still on the line", StopLeft
	case t.entered:
		return "paused · still running · ctrl+c interrupts", StopRunning
	}
	return "paused", StopPaused
}

func (h *hub) request(p *peer, m msg) {
	var r *Result
	var err error
	switch {
	case m.Op == opStop || m.Op == opPause:
		err = h.windowOp(p, m)
	case m.Op == opLease || m.Op == opRelease:
		err = h.agentOp(p, m)
	default:
		r, err = h.portOp(p, m)
	}
	reply := msg{T: tReply, ID: m.ID, Result: r}
	if err != nil {
		reply.Err = err.Error()
	}
	h.mu.Lock()
	h.queue(p, reply)
	h.mu.Unlock()
}

// windowOp is the user stopping, pausing or resuming the AI tool.
func (h *hub) windowOp(p *peer, m msg) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case p.agent:
		return errors.New("only a bauddy window can do that")
	case m.Op == opStop && h.agent == nil:
		if m.Interrupt {
			_, _ = h.port.Write([]byte{0x03}) // it left first; the user's ctrl+c still goes through
		}
		return nil
	case h.agent == nil:
		return errors.New("no AI tool is using this console")
	case m.Op == opPause && h.paused:
		h.paused = false
		h.note(h.agent.name + " resumed")
		h.broadcastState()
		return nil
	}
	note, reason := h.agent.name+" paused", StopPaused
	// Text typed and not entered stays tracked after the tool gives the
	// keyboard back, so a stop or pause still erases it. Once the user has
	// edited the line, that tracking is gone (see trackUser).
	if h.lease || h.agentLine.chars > 0 || h.agentLine.ctrl {
		leased := h.lease
		note, reason = h.revoke(m.Interrupt && leased)
		if leased { // a stop for a command already finished must not cancel the next one
			h.queue(h.agent, msg{T: tStopped, Text: reason})
		}
	}
	if m.Interrupt {
		_, _ = h.port.Write([]byte{0x03}) // the user's ctrl+c, after the tool's last byte
	}
	h.paused = true
	h.note(note)
	h.broadcastState()
	return nil
}

// agentOp is the AI tool taking the keyboard for a command, or giving it
// back.
func (h *hub) agentOp(p *peer, m msg) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case !p.agent:
		return errors.New("only an AI tool takes the keyboard")
	case m.Op == opRelease:
		if h.lease {
			h.lease = false // what it typed without Enter stays tracked, for its next command or a stop
			h.broadcastState()
		}
		return nil
	case h.lease:
		return ErrBusy
	}
	if err := h.refuse(p); err != nil {
		return err
	}
	h.lease, h.agentLine.entered = true, false
	h.broadcastState()
	return nil
}

// refuse says why p can't act on the console right now, or nil. Neither
// side can while the other is typing. Call with mu held.
func (h *hub) refuse(p *peer) error {
	switch {
	case p.agent && h.paused:
		return ErrPaused
	case h.busy:
		return ErrBusy
	case !p.agent && h.lease:
		return errors.New(h.agent.name + " is working · esc stops it")
	case p.agent && (h.userTyped > 0 || h.now().Sub(h.lastKey) < typingWindow):
		return ErrTyping
	}
	return nil
}

// portOp runs an operation on the port, one at a time, without holding mu
// while it runs.
func (h *hub) portOp(p *peer, m msg) (*Result, error) {
	h.mu.Lock()
	if err := h.refuse(p); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if p.agent && m.Op == opDetect && m.Probe && len(h.peers) > 1 {
		// Probing presses Enter, which a stop couldn't take back.
		h.mu.Unlock()
		return nil, errors.New("the console is open in bauddy: ask the user to detect the baud rate there")
	}
	h.busy, h.detecting = true, m.Op == opDetect
	if h.detecting {
		h.broadcastState()
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.busy, h.detecting = false, false
		h.broadcastState()
		h.mu.Unlock()
	}()

	switch m.Op {
	case opSetBaud:
		return nil, h.port.SetBaud(m.Baud)
	case opBreak:
		return nil, h.port.Break(breakLength)
	case opReconnect:
		h.port.Reconnect()
		return nil, nil
	case opDetect:
		r, err := h.port.Detect(serialx.DetectConfig{Probe: m.Probe, KeepFraming: m.KeepFraming})
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.output(r.Heard) // everyone sees what it heard
		h.mu.Unlock()
		return &Result{Baud: r.Baud, Framing: r.Framing.String()}, nil
	}
	return nil, errors.New("unknown operation " + m.Op)
}

// Messages to clients. Call these with mu held.

func (h *hub) broadcastState() {
	s := Status{
		Connected: h.connected, Baud: h.port.Baud(), Framing: h.port.Framing().String(),
		Working: h.lease, Paused: h.paused, Detecting: h.detecting, Windows: len(h.peers), Hub: h.id,
	}
	if h.agent != nil {
		s.Agent, s.Windows = h.agent.name, s.Windows-1
	}
	for p := range h.peers {
		h.queue(p, msg{T: tState, State: &s})
	}
}

// note shows text on every window's status line. "{keys}" stands for the
// window's own command key (ctrl+] unless the user changed it).
func (h *hub) note(text string) {
	for p := range h.peers {
		if !p.agent {
			h.queue(p, msg{T: tNote, Text: text})
		}
	}
}

// queue sends m to p without waiting. A client that has fallen this far
// behind is dropped rather than allowed to stall everyone; it reconnects.
func (h *hub) queue(p *peer, m msg) {
	select {
	case p.out <- m:
	default:
		p.drop()
	}
}

func (p *peer) writeLoop() {
	defer p.drop()
	for {
		select {
		case m := <-p.out:
			_ = p.conn.SetWriteDeadline(time.Now().Add(sendTimeout))
			if p.w.send(m) != nil {
				return
			}
		case <-p.done:
			return
		}
	}
}

// drop closes the connection, which ends both of the peer's loops.
func (p *peer) drop() {
	p.once.Do(func() {
		close(p.done)
		p.conn.Close()
	})
}
