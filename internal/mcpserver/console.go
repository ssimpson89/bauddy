package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ssimpson89/bauddy/internal/logfile"
	"github.com/ssimpson89/bauddy/internal/paste"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/termtext"
)

// Port is the serial connection a console drives: a *serialx.Session, or a
// simulated device in tests.
type Port interface {
	Path() string
	Events() <-chan serialx.Event
	Write([]byte) (int, error)
	Break(time.Duration) error
	Baud() int
	Framing() serialx.Framing
	Detect(serialx.DetectConfig) (serialx.Result, error)
	Close() error
}

const (
	historyMax = 10000 // lines of output kept for read_history
	// What one result shows at most, keeping the newest. The agent reads
	// every byte, and a boot log can run to thousands of lines.
	shownLinesMax = 300
	shownBytesMax = 22 << 10
	lineMax       = 2 << 10 // a longer line, such as garbage at the wrong baud rate, keeps its end
	// quietFor is how long the device must be silent before its answer
	// counts as finished.
	quietFor = 300 * time.Millisecond
)

// console is one open port and everything it has printed, as plain text.
type console struct {
	port Port
	name string // the adapter's saved name, or the port's

	mu        sync.Mutex
	screen    termtext.Cleaner
	lines     []string // the newest historyMax completed lines
	total     int      // lines completed so far
	seen      int      // total when output was last returned
	seenAt    string   // the cursor line then
	cursorWas string   // the cursor line after the last output
	prompt    string   // the last cursor line that looked like a prompt, with nothing typed
	prompts   int      // how many times a prompt has appeared
	connected bool
	heardAt   int    // the rate the device last answered at in plain text; 0: not since connecting
	answer    []byte // plain text towards an answer, across reads
	meter     serialx.GarbleMeter
	changed   chan struct{} // closed and replaced whenever something arrives
	rx        chan []byte   // a copy of the output for the paste pacer, while a command runs
	log       *logfile.Log
	logPath   string
	logErr    error // why logging stopped

	drained chan struct{} // closed once the port's events end
}

// newConsole starts recording p's output, and logging it if log isn't nil.
func newConsole(p Port, name string, log *logfile.Log) *console {
	c := &console{
		port: p, name: name, log: log, connected: true,
		changed: make(chan struct{}), drained: make(chan struct{}),
	}
	if log != nil {
		c.logPath = log.Path()
	}
	go c.drain()
	return c
}

// drain records output and state changes until the port closes.
func (c *console) drain() {
	defer close(c.drained)
	for e := range c.port.Events() {
		c.mu.Lock()
		c.feed(e.Data)
		if !e.Replay { // output from before the agent joined may be from another rate
			c.noteAnswer(e.Data)
		}
		if up := e.State == serialx.Connected; up != c.connected {
			c.connected = up
			c.meter.Reset() // the device may come back at a different rate
			c.heardAt, c.answer = 0, nil
			msg := "disconnected"
			if up {
				msg = "reconnected"
			}
			c.mark(msg)
		}
		c.notify()
		c.mu.Unlock()
	}
}

// feed records device output. Call with mu held.
func (c *console) feed(b []byte) {
	if len(b) == 0 {
		return
	}
	if done := c.screen.Feed(b); len(done) > 0 {
		for _, l := range strings.Split(strings.TrimSuffix(string(done), "\n"), "\n") {
			c.lines = append(c.lines, clip(l)) // only what a result can show
			c.total++
		}
		if over := len(c.lines) - historyMax; over > 0 {
			c.lines = c.lines[over:]
		}
	}
	if cursor := c.screen.Partial(); cursor != c.cursorWas {
		c.cursorWas = cursor
		// Typed text ending in # (reboot #) extends the prompt; it isn't a new one.
		extends := c.prompt != "" && len(cursor) > len(c.prompt) && strings.HasPrefix(cursor, c.prompt)
		if looksLikePrompt(cursor) && !extends {
			c.prompt = cursor
			c.prompts++
		}
	}
	c.meter.Add(b)
	if c.rx != nil {
		select {
		case c.rx <- b:
		default: // the pacer is behind; it will stop rather than guess
		}
	}
	if c.log != nil {
		if err := c.log.Write(b); err != nil {
			c.logFailed(err)
		}
	}
}

// notify wakes anything waiting for output. Call with mu held.
func (c *console) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// changes returns a channel that closes when something next arrives. Take
// it before sending, so a fast answer isn't missed.
func (c *console) changes() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

// mark writes a marker line in the log. Call with mu held.
func (c *console) mark(msg string) {
	if c.log == nil {
		return
	}
	if err := c.log.Mark(msg); err != nil {
		c.logFailed(err)
	}
}

func (c *console) logFailed(err error) {
	_ = c.log.Close()
	c.log, c.logErr = nil, err
}

// Mark writes a marker line in the log.
func (c *console) Mark(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mark(msg)
}

// detected records what detection heard, now that the port is at the
// right rate.
func (c *console) detected(r serialx.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meter.Reset()
	c.mark("detected " + serialx.LineSettings(r.Baud, r.Framing))
	c.feed(r.Heard)
	c.heardAt, c.answer = c.port.Baud(), nil // it heard the device at this rate
	c.notify()
}

// noteAnswer counts b towards an answer. Two bytes of plain text in a row,
// across reads, are one; garbage starts the count again: at a rate slower
// than the device's, some still gets through. Call with mu held.
func (c *console) noteAnswer(b []byte) {
	if len(b) == 0 {
		return
	}
	if !serialx.Answered(b) {
		c.answer = nil
		return
	}
	if c.answer = append(c.answer, b...); len(c.answer) >= 2 {
		c.heardAt, c.answer = c.port.Baud(), nil
	}
}

// answered says whether the device has answered in plain text at the
// current rate. Until it has, the rate is unconfirmed: at a rate slower
// than the device's, every byte reaches it as a BREAK (on a Linux console
// that makes the next key a SysRq command) while its replies are dropped.
func (c *console) answered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.heardAt != 0 && c.heardAt == c.port.Baud()
}

// unconfirmed is what the agent is told when it would type at a rate the
// device hasn't answered at.
func (c *console) unconfirmed() error {
	return fmt.Errorf("nothing was sent: the device hasn't answered at %s yet, so the baud rate may be wrong. "+
		"Press Enter with send_keys key enter to check; if there's still no reply, connect again with detect, "+
		"or ask the user", c.settings())
}

// settings describes the line now, e.g. "9600 baud 7E1".
func (c *console) settings() string {
	return serialx.LineSettings(c.port.Baud(), c.port.Framing())
}

// up reports whether the adapter is connected.
func (c *console) up() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// write sends b to the device.
func (c *console) write(b []byte) error {
	_, err := c.port.Write(b)
	return err
}

// unread returns what the device printed since output was last returned,
// ending with the line the cursor is on, and marks it returned.
func (c *console) unread() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.total - c.seen
	c.seen, c.seenAt = c.total, c.screen.Partial()
	lost := max(0, n-len(c.lines)) // dropped from history before anyone looked
	n -= lost
	return shown(c.lines[len(c.lines)-n:], c.seenAt, lost)
}

// clip cuts a line longer than lineMax, keeping its end.
func clip(l string) string {
	if len(l) <= lineMax {
		return l
	}
	l = l[len(l)-lineMax:]
	for len(l) > 0 && !utf8.RuneStart(l[0]) {
		l = l[1:]
	}
	return "…" + l
}

// shown renders lines and the cursor line for a result, keeping the newest
// within shownLinesMax and shownBytesMax.
func shown(lines []string, cursor string, lost int) string {
	if len(lines) == 0 && cursor == "" {
		return "(no new output)"
	}
	cursor = clip(cursor)
	size, keep := len(cursor), 0
	for keep < len(lines) && keep < shownLinesMax {
		l := clip(lines[len(lines)-1-keep])
		if size+len(l)+1 > shownBytesMax {
			break
		}
		size += len(l) + 1
		keep++
	}
	var b strings.Builder
	if cut := lost + len(lines) - keep; cut > 0 {
		fmt.Fprintf(&b, "[%d earlier lines not shown; read_history has the last %d]\n", cut, historyMax)
	} else if len(lines) == 0 {
		b.WriteString("(no new lines; the cursor is on this one)\n")
	}
	for _, l := range lines[len(lines)-keep:] {
		b.WriteString(clip(l) + "\n")
	}
	b.WriteString(cursor)
	return b.String()
}

// notes are warnings for the agent about the connection, if any.
func (c *console) notes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n []string
	if !c.connected {
		n = append(n, "The adapter is unplugged or not responding; bauddy reconnects when it's back.")
	}
	if c.meter.Garbled() {
		n = append(n, "Output looks garbled, so the baud rate is probably wrong: connect again with detect, or with a baud.")
	}
	if paste.AtLogin(c.screen.Partial()) {
		n = append(n, "The device is asking for a login. Ask the user to log in in their bauddy window, or to give you "+
			"credentials if they want you to. Don't guess: failed logins can lock the account.")
	}
	if c.logErr != nil {
		n = append(n, "Logging stopped: "+c.logErr.Error())
	}
	return n
}

// cursor is the line the cursor is on, usually the prompt.
func (c *console) cursor() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.screen.Partial()
}

// line is where the cursor is: the line it's on, the last prompt seen, and
// how many prompts have appeared so far.
type line struct {
	cursor, prompt string
	prompts        int
}

func (c *console) line() line {
	c.mu.Lock()
	defer c.mu.Unlock()
	return line{c.screen.Partial(), c.prompt, c.prompts}
}

// unreadWaiting reports whether there's output not yet returned.
func (c *console) unreadWaiting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total != c.seen || c.screen.Partial() != c.seenAt
}

// run sends lines through the paste pacer, which types each one only as
// fast as the device echoes it and waits for the prompt in between.
func (c *console) run(ctx context.Context, lines []string) error {
	rx := make(chan []byte, 1024)
	c.mu.Lock()
	c.rx = rx
	prompt := c.screen.Partial()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.rx = nil
		c.mu.Unlock()
	}()
	p := &paste.Pacer{Write: c.write, Rx: rx, Baud: c.port.Baud(), Prompt: prompt}
	return p.Run(ctx, lines, func(int) {})
}

// settle waits for the device to answer: until it has been quiet for
// quietFor after it last printed, or limit passes. changed is from changes,
// taken before sending. With started, output has already begun, so quiet
// counts from now.
func (c *console) settle(ctx context.Context, changed <-chan struct{}, limit time.Duration, started bool) {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	var quiet <-chan time.Time // runs once output starts
	if started {
		quiet = time.After(quietFor)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-quiet:
			return
		case <-changed:
			changed = c.changes()
			quiet = time.After(quietFor)
		}
	}
}

// waitFor waits until unread output matches p, or limit passes, and
// reports whether it matched.
func (c *console) waitFor(ctx context.Context, p *regexp.Regexp, limit time.Duration) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	for {
		changed := c.changes()
		if c.unreadMatches(p) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-changed:
		}
	}
}

func (c *console) unreadMatches(p *regexp.Regexp) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(c.total-c.seen, len(c.lines))
	for _, l := range c.lines[len(c.lines)-n:] {
		if p.MatchString(l) {
			return true
		}
	}
	cursor := c.screen.Partial()
	return (n > 0 || cursor != c.seenAt) && p.MatchString(cursor) // not the prompt already returned
}

// history returns the last n lines, including the cursor line, that match
// p (all of them if p is nil), within shownBytesMax.
func (c *console) history(p *regexp.Regexp, n int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	all := c.lines
	if cursor := c.screen.Partial(); cursor != "" {
		all = append(all[:len(all):len(all)], cursor)
	}
	var out []string
	size := 0
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		if l := clip(all[i]); p == nil || p.MatchString(all[i]) {
			if size += len(l) + 1; size > shownBytesMax {
				break
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "(nothing found)"
	}
	slices.Reverse(out)
	return strings.Join(out, "\n")
}

// close releases the port, waits for its last output, and closes the log.
// It returns the log's path, if there was one.
func (c *console) close() (string, error) {
	err := c.port.Close()
	<-c.drained
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.log != nil {
		if lerr := c.log.Close(); lerr != nil && err == nil {
			err = fmt.Errorf("can't save the log: %w", lerr)
		}
		c.log = nil
	}
	return c.logPath, err // the path even if logging stopped: what was written is there
}
