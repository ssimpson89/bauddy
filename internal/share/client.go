// Package share lets several bauddy windows, and an AI tool through
// bauddy mcp, use one serial console at once.
//
// Whichever opens the console first holds the port and runs a hub that
// shares it; later ones join the hub over a socket only this user can
// reach. A lock file decides who holds the port, and the OS releases it if
// that process dies. Nothing is handed over: when the holder quits, the
// others see a short disconnect and one of them takes the port.
//
// Every process, the holder included, talks to the hub through a Client:
// the holder over an in-memory pipe, the rest over the socket. A Client
// has two goroutines: run reads from the hub (and reconnects), and forward
// passes events to the caller, so the link never waits on a slow UI. The
// hub has pump, which shares the port's output, and two goroutines per
// client. Each side guards its state with one mutex, mu.
package share

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

var (
	// ErrPaused means the user paused the AI tool.
	ErrPaused = errors.New("paused by the user")
	// ErrTyping means the user is typing, so the AI tool must wait.
	ErrTyping = errors.New("the user is typing")
	// ErrBusy means another operation has the console.
	ErrBusy = errors.New("the console is busy")
	// ErrNotConnected means the link to the console is down for now.
	ErrNotConnected = errors.New("not connected to the console")

	errHeld   = errors.New("another bauddy holds this console")
	errClosed = errors.New("console closed")
)

const (
	retryEvery = 250 * time.Millisecond // between attempts to reach the console
	joinWait   = 2 * time.Second        // at start, how long another bauddy may take to come up
	agentDelay = 500 * time.Millisecond // an AI tool waits this long before taking a port, so a window gets it
	writeMax   = 16 << 10               // bytes per write message, well under maxMsg
)

// Options say which console to open and as what.
type Options struct {
	Adapter string // the cable's own identity (serialx.PortInfo.Adapter), if it has one
	Path    string // the port to open; bauddys reaching the same cable share it
	Baud    int
	Framing serialx.Framing
	Agent   bool   // an AI tool (bauddy mcp), not a window
	Name    string // the AI tool's name
	Replay  bool   // a window joining wants the recent output
	// Open opens the port when this process becomes the holder; tests
	// replace it. Default: serialx.Open.
	Open func(path string, baud int, f serialx.Framing) (Port, error)
}

// Note is something a hub wants a window to show: a status-line note, a
// line for history and the log (Mark), or with no text, just a change in
// Status to redraw for.
type Note struct {
	Text string
	Mark bool
}

// Client is this process's side of a shared console. It has the methods
// of a serial session, so the console and the MCP server use it the same
// way, plus the sharing controls.
type Client struct {
	opt     Options
	node    string // the device the port's path led to at Open
	console string // what its lock and socket are named after (see consoleName)
	events  chan serialx.Event
	taps    serialx.Taps
	notes   chan Note
	done    chan struct{} // closed by Close

	qmu   sync.Mutex
	queue []serialx.Event // events waiting for forward
	wake  chan struct{}

	mu        sync.Mutex
	w         *wire // nil while the link is down
	link      net.Conn
	holder    *holder // set while this process holds the port
	status    Status
	joined    bool // the console was already open in another bauddy when Open returned
	saidHello bool
	pending   map[int]chan msg
	nextID    int
	lease     context.CancelCauseFunc // while an AI tool has the keyboard: ends its command if the user stops it
	err       error                   // why the hub turned this client away for good
	closed    bool
}

// holder is the hub, port and socket of the process holding the console.
type holder struct {
	port Port
	ln   net.Listener // nil if sharing isn't possible here
	lock *os.File
	once sync.Once
}

// Open opens the console the options name, joining another bauddy that has
// it open, or else opening the port and sharing it.
func Open(opt Options) (*Client, error) {
	if opt.Open == nil {
		opt.Open = func(path string, baud int, f serialx.Framing) (Port, error) { return serialx.Open(path, baud, f) }
	}
	c := &Client{
		opt: opt, events: make(chan serialx.Event, 256), notes: make(chan Note, 64),
		done: make(chan struct{}), wake: make(chan struct{}, 1), pending: map[int]chan msg{},
	}
	c.node = deviceNode(opt.Path)
	c.console = opt.Adapter + "@" + c.node // once, so a takeover can't split the windows
	w, err := c.connect(time.Now().Add(joinWait), true)
	if err != nil {
		return nil, err
	}
	go c.forward()
	go c.run(w)
	return c, nil
}

// consoleName names the console for a cable by two things. The device node
// its path leads to: every way of reaching one cable (a by-id link,
// /dev/ttyUSB0) is one console, and identical twins are two. And the
// cable's identity: another cable that comes up on the same node isn't
// this one. A port bauddy doesn't list (a pty) has no identity, only a node.
func consoleName(adapter, path string) string { return adapter + "@" + deviceNode(path) }

// deviceNode is the device a path leads to (a by-id link to /dev/ttyUSB0),
// or the path itself if it leads nowhere now.
func deviceNode(path string) string {
	if node, err := filepath.EvalSymlinks(path); err == nil {
		return node
	}
	return path
}

// InUse says whether another bauddy (a window, or bauddy mcp) has the
// console for a cable open, by reaching its socket and hanging up, which
// the hub ignores. It takes no lock, so it can't get in the way of a bauddy
// starting up, and it never touches the port.
func InUse(adapter, path string) bool {
	sock, _, ok := paths(consoleName(adapter, path))
	if !ok {
		return false
	}
	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false // nobody, or a socket left by a bauddy that died
	}
	conn.Close()
	return true
}

// paths returns the socket and lock for a console, or ok false if it can't
// be shared here.
func paths(console string) (sock, lock string, ok bool) {
	if !sharing {
		return "", "", false
	}
	d, err := dir()
	if err != nil {
		return "", "", false
	}
	sum := sha256.Sum256([]byte(console))
	name := hex.EncodeToString(sum[:6])
	sock = filepath.Join(d, name+".sock")
	if len(sock) > 100 { // the socket path limit is about 104 bytes
		return "", "", false
	}
	return sock, filepath.Join(d, name+".lock"), true
}

// connect reaches the console: it takes the lock and opens the port, or
// joins whoever has it. Until deadline it keeps trying while another
// bauddy is starting up or letting go. At first, a port that won't open is
// reported at once.
func (c *Client) connect(deadline time.Time, first bool) (*wire, error) {
	sock, lockPath, ok := paths(c.console)
	if !ok {
		return c.hold(nil, "") // can't share here: just open the port, as before
	}
	for {
		f, err := lock(lockPath)
		switch {
		case err == nil:
			w, err := c.hold(f, sock)
			if err == nil || first || time.Now().After(deadline) {
				return w, err
			}
		case errors.Is(err, errHeld):
			w, err := c.join(sock)
			if err == nil || c.refused() || time.Now().After(deadline) {
				if err != nil && !c.refused() {
					err = fmt.Errorf("this console is open in another bauddy that isn't answering: %w", err)
				}
				return w, err
			}
		default:
			return c.hold(nil, "") // no lock to be had: just open the port, as before
		}
		select {
		case <-time.After(retryEvery):
		case <-c.done:
			return nil, errClosed
		}
	}
}

// portPath is what hold opens: the path, unless it now leads to another
// device than the one Open named the console after (by-id moves to a twin
// cable plugged in later), and then the console's own device. A path that
// leads nowhere (the cable is out) is opened as it is, and fails: the
// console's old device name may belong to another cable by now.
func (c *Client) portPath() string {
	node, err := filepath.EvalSymlinks(c.opt.Path)
	if err != nil || node == c.node {
		return c.opt.Path
	}
	if _, err := os.Stat(c.node); err != nil {
		return c.opt.Path
	}
	return c.node
}

// hold opens the port and shares it, with this process as the first
// client of its own hub. After a takeover it opens at the console's rate,
// not the one it started with.
func (c *Client) hold(lockFile *os.File, sock string) (*wire, error) {
	c.mu.Lock()
	baud, framing := c.opt.Baud, c.opt.Framing
	if c.status.Baud > 0 {
		baud = c.status.Baud
		framing, _ = serialx.ParseFraming(c.status.Framing)
	}
	c.mu.Unlock()
	port, err := c.opt.Open(c.portPath(), baud, framing)
	if err != nil {
		if lockFile != nil {
			lockFile.Close()
		}
		return nil, err
	}
	h := &holder{port: port, lock: lockFile}
	hub := newHub(port)
	if lockFile != nil {
		_ = os.Remove(sock) // left by a bauddy that died; ours now that we hold the lock
		ln, err := net.Listen("unix", sock)
		if err != nil { // holding the lock without listening would leave the others unable to join
			h.close()
			return nil, fmt.Errorf("can't share this console: %w", err)
		}
		h.ln = ln
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go hub.serve(conn)
			}
		}()
	}
	mine, theirs := net.Pipe()
	go hub.serve(theirs)
	c.mu.Lock()
	c.holder = h // before hello, so a Close from now on closes it
	c.mu.Unlock()
	w, err := c.hello(mine)
	if err != nil {
		c.mu.Lock()
		c.holder = nil
		c.mu.Unlock()
		h.close()
		return nil, err
	}
	return w, nil
}

func (c *Client) join(sock string) (*wire, error) {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	first := !c.saidHello
	c.mu.Unlock()
	w, err := c.hello(conn)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.joined = c.joined || first
	c.mu.Unlock()
	return w, nil
}

// hello introduces this client and waits for the first message, the
// console's state, so the caller knows the baud before it starts.
func (c *Client) hello(conn net.Conn) (*wire, error) {
	c.mu.Lock()
	first, paused, hub := !c.saidHello, c.status.Paused, c.status.Hub
	c.mu.Unlock()
	w := newWire(conn)
	m, err := c.exchangeHello(conn, w, msg{T: tHello, Version: version, Agent: c.opt.Agent, Name: c.opt.Name,
		Replay: c.opt.Replay && first, Paused: paused, Hub: hub}) // a window that rejoins already has the screen
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.mu.Lock()
	closed := c.closed
	if !closed {
		c.w, c.link, c.saidHello = w, conn, true
	}
	c.mu.Unlock()
	if closed {
		conn.Close()
		return nil, errClosed
	}
	c.handle(m)
	return w, nil
}

// exchangeHello sends the hello and returns the state the hub answers with.
func (c *Client) exchangeHello(conn net.Conn, w *wire, hello msg) (msg, error) {
	if err := w.send(hello); err != nil {
		return msg{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	m, err := w.recv()
	_ = conn.SetReadDeadline(time.Time{})
	switch {
	case err != nil:
		return msg{}, err
	case m.T == tBye:
		err := errors.New(m.Text)
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		return msg{}, err
	case m.T != tState:
		return msg{}, fmt.Errorf("unexpected first message %q", m.T)
	}
	return m, nil
}

// refused reports whether the hub turned this client away for good.
func (c *Client) refused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err != nil
}

// run reads from the hub and reconnects when the link drops, until Close.
// If the hub turns it away for good, the client closes: Err says why.
func (c *Client) run(w *wire) {
	for {
		for {
			m, err := w.recv()
			if err != nil {
				break
			}
			c.handle(m)
		}
		if !c.down() {
			return
		}
		if c.opt.Agent {
			select { // let a window take the port first
			case <-time.After(agentDelay):
			case <-c.done:
				return
			}
		}
		var err error
		if w, err = c.connect(time.Now().Add(24*time.Hour), false); err != nil {
			c.Close()
			return
		}
	}
}

// down handles losing the link. It reports false once the client is closed.
func (c *Client) down() bool {
	c.mu.Lock()
	h := c.holder
	c.holder = nil
	closed := c.closed
	c.mu.Unlock()
	if h != nil {
		h.close() // not under mu: closing a port can hang, and Status mustn't
	}
	if closed {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w, c.link = nil, nil
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.stop("disconnected")
	// Nobody is known to be working or watching until the hub says so again.
	c.status.Working, c.status.Agent, c.status.Windows, c.status.Detecting = false, "", 0, false
	if c.status.Connected {
		c.status.Connected = false
		c.deliver(serialx.Event{State: serialx.Disconnected})
	}
	c.notify(Note{})
	return true
}

func (c *Client) handle(m msg) {
	switch m.T {
	case tData:
		c.taps.Send(m.Data)
		c.deliver(serialx.Event{Data: m.Data, Replay: m.Replay})
	case tState:
		c.mu.Lock()
		was := c.status.Connected
		c.status = *m.State
		c.mu.Unlock()
		if up := m.State.Connected; up != was {
			state := serialx.Disconnected
			if up {
				state = serialx.Connected
			}
			c.deliver(serialx.Event{State: state})
		}
		c.notify(Note{})
	case tNote, tMark:
		c.notify(Note{Text: m.Text, Mark: m.T == tMark})
	case tStopped:
		c.mu.Lock()
		c.stop(m.Text)
		c.mu.Unlock()
	case tReply:
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

// notify passes n to Notes without waiting; a window that falls behind
// misses some.
func (c *Client) notify(n Note) {
	select {
	case c.notes <- n:
	default:
	}
}

// stopReason is why the user stopped the AI tool, carried as the cause of
// its command's context.
type stopReason string

func (r stopReason) Error() string { return string(r) }

// stop ends the AI tool's command, if it has one. Call with mu held.
func (c *Client) stop(why string) {
	if c.lease != nil {
		c.lease(stopReason(why))
		c.lease = nil
	}
}

// deliver queues an event for Events without ever blocking the link.
func (c *Client) deliver(e serialx.Event) {
	c.qmu.Lock()
	c.queue = append(c.queue, e)
	c.qmu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) forward() {
	defer close(c.events)
	for {
		c.qmu.Lock()
		q := c.queue
		c.queue = nil
		c.qmu.Unlock()
		for _, e := range q {
			select {
			case c.events <- e:
			case <-c.done:
				return
			}
		}
		select {
		case <-c.wake:
		case <-c.done:
			return
		}
	}
}

// send sends m to the hub, or fails if the link is down.
func (c *Client) send(m msg) error {
	c.mu.Lock()
	w := c.w
	c.mu.Unlock()
	if w == nil {
		return ErrNotConnected
	}
	return w.send(m)
}

// call makes a request and waits for its reply.
func (c *Client) call(m msg) (msg, error) {
	ch := make(chan msg, 1)
	c.mu.Lock()
	if c.w == nil {
		c.mu.Unlock()
		return msg{}, ErrNotConnected
	}
	c.nextID++
	m.T, m.ID = tReq, c.nextID
	c.pending[m.ID] = ch
	w := c.w
	c.mu.Unlock()
	if err := w.send(m); err != nil {
		return msg{}, err
	}
	reply, ok := <-ch
	switch {
	case !ok:
		return msg{}, ErrNotConnected
	case reply.Err != "":
		for _, e := range []error{ErrPaused, ErrTyping, ErrBusy, serialx.ErrNoSignal} {
			if reply.Err == e.Error() {
				return reply, e
			}
		}
		return reply, errors.New(reply.Err)
	}
	return reply, nil
}

// The serial session's methods.

// Path returns the port's path.
func (c *Client) Path() string { return c.opt.Path }

// Events delivers device output and state changes. Closed after Close.
func (c *Client) Events() <-chan serialx.Event { return c.events }

// Tap returns a copy of the output from now on, until cancel is called.
func (c *Client) Tap() (<-chan []byte, func()) { return c.taps.Tap() }

// Write sends bytes to the device. It returns at once: while an AI tool has
// the keyboard the hub drops a window's bytes, and the other way round.
func (c *Client) Write(b []byte) (int, error) {
	for n := 0; n < len(b); n += writeMax {
		if err := c.send(msg{T: tWrite, Data: b[n:min(len(b), n+writeMax)]}); err != nil {
			return n, err
		}
	}
	return len(b), nil
}

// Baud returns the console's rate.
func (c *Client) Baud() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status.Baud
}

// Framing returns the console's data bits, parity and stop bits.
func (c *Client) Framing() serialx.Framing {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := serialx.ParseFraming(c.status.Framing)
	if err != nil {
		return serialx.Framing8N1
	}
	return f
}

// SetBaud changes the console's rate, for everyone on it.
func (c *Client) SetBaud(baud int) error {
	_, err := c.call(msg{Op: opSetBaud, Baud: baud})
	return err
}

// Break sends BREAK. The duration is the console's own.
func (c *Client) Break(time.Duration) error {
	_, err := c.call(msg{Op: opBreak})
	return err
}

// Reconnect reopens the port. With the link down it does nothing; the
// client is already trying to reconnect.
func (c *Client) Reconnect() { _, _ = c.call(msg{Op: opReconnect}) }

// Detect detects the baud rate. What it heard arrives as output, to
// everyone, so Heard is empty.
func (c *Client) Detect(cfg serialx.DetectConfig) (serialx.Result, error) {
	reply, err := c.call(msg{Op: opDetect, Probe: cfg.Probe, KeepFraming: cfg.KeepFraming})
	if err != nil {
		return serialx.Result{}, err
	}
	if reply.Result == nil {
		return serialx.Result{}, errors.New("detection gave no result")
	}
	f, _ := serialx.ParseFraming(reply.Result.Framing)
	return serialx.Result{Baud: reply.Result.Baud, Framing: f}, nil
}

// Close leaves the console; if this process holds it, the port closes and
// another bauddy takes it.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	link, h := c.link, c.holder
	c.mu.Unlock()
	if link != nil {
		link.Close()
	}
	if h != nil {
		return h.close()
	}
	return nil
}

// close stops sharing and closes the port. The hub stops when the port's
// events end. It's safe to call more than once.
func (h *holder) close() error {
	var err error
	h.once.Do(func() {
		if h.ln != nil {
			h.ln.Close() // stop new joiners; this also removes the socket
		}
		err = h.port.Close()
		if h.lock != nil {
			h.lock.Close() // last, so nobody else opens the port before it's closed
		}
	})
	return err
}

// Sharing.

// Status is the console as everyone sees it.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Joined reports whether the console was already open in another bauddy
// when this client opened it. It stays true if this process later takes
// over the port.
func (c *Client) Joined() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.joined
}

// Watching reports whether this client is on the console as the AI tool,
// which can't type unless the tool leases the keyboard.
func (c *Client) Watching() bool { return c.opt.Agent }

// Err says why the hub turned this client away for good, if it did.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Notes delivers what the hub wants a window to show, and a Note with no
// text whenever Status changes.
func (c *Client) Notes() <-chan Note { return c.notes }

// KeyPressed tells the hub the user is typing, so an AI tool waits.
func (c *Client) KeyPressed() { _ = c.send(msg{T: tKey}) }

// StopAgent stops the AI tool and pauses it. With interrupt, the user's
// ctrl+c goes to the device too, after the tool's last byte.
func (c *Client) StopAgent(interrupt bool) error {
	_, err := c.call(msg{Op: opStop, Interrupt: interrupt})
	return err
}

// TogglePause pauses the AI tool, or lets it type again.
func (c *Client) TogglePause() error {
	_, err := c.call(msg{Op: opPause})
	return err
}

// Lease gives an AI tool the keyboard for one command. The returned
// context ends if the user stops it; why then says how (one of the Stop
// constants, or "disconnected"). If the user is typing it waits, up to
// typingWindow, for them to pause. Call release when done.
func (c *Client) Lease(ctx context.Context) (_ context.Context, why func() string, release func(), err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	// Ready for a stop before asking: the user may stop it the moment the
	// hub says yes, before the reply arrives.
	c.mu.Lock()
	c.lease = cancel
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		c.lease = nil
		c.mu.Unlock()
		cancel(nil)
	}
	giveUpAt := time.Now().Add(typingWindow)
	for {
		_, err = c.call(msg{Op: opLease})
		if !errors.Is(err, ErrTyping) || time.Now().After(giveUpAt) {
			break
		}
		select {
		case <-time.After(retryEvery):
		case <-ctx.Done():
			err = ctx.Err()
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		forget()
		return nil, nil, nil, err
	}
	why = func() string {
		var r stopReason
		if errors.As(context.Cause(ctx), &r) {
			return string(r)
		}
		return ""
	}
	return ctx, why, func() {
		forget()
		_, _ = c.call(msg{Op: opRelease})
	}, nil
}
