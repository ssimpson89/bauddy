// Package serialx owns the serial port: opening, reading, writing,
// automatic reconnect and baud detection.
package serialx

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

// State is the connection state shown on the status line.
type State int

// The two connection states.
const (
	Connected State = iota
	Disconnected
)

// Event is device output or a state change. Data events always carry
// Connected, since bytes only arrive from a live port.
type Event struct {
	Data  []byte
	State State
	// Replay marks output from before this client joined a shared console:
	// for the screen and history, not new output.
	Replay bool
	// Baud is the rate Data was read at, so output read just before a rate
	// change isn't taken for the device answering at the new one. 0: not known.
	Baud int
}

var (
	// errNotConnected means the adapter is unplugged.
	errNotConnected = errors.New("adapter not connected")
	// errDetecting is returned by Write while baud detection runs.
	errDetecting = errors.New("baud detection in progress")
	errClosed    = errors.New("session closed")
)

// Session is one serial connection that survives unplug and replug.
type Session struct {
	path string

	mu   sync.Mutex // guards port and mode
	port serial.Port
	mode serial.Mode

	// ioMu is held by the reader for each read and by Detect for its whole
	// run, so the two never steal bytes from each other.
	ioMu      sync.Mutex
	detecting atomic.Bool // Write refuses while set: keystrokes would go out at a trial rate

	taps Taps

	events    chan Event
	done      chan struct{}
	closeOnce sync.Once
}

const (
	// readTimeout bounds each Read so the loop notices Close and Detect
	// can stop listening at its deadline.
	readTimeout = 50 * time.Millisecond
	// reconnectPoll is how often an unplugged adapter is retried.
	reconnectPoll = 500 * time.Millisecond
)

// Open opens the port at path with the given baud rate and framing, no flow
// control. It starts reading at once, so the caller must drain Events.
func Open(path string, baud int, f Framing) (*Session, error) {
	s := &Session{
		path:   path,
		mode:   serial.Mode{BaudRate: baud},
		events: make(chan Event, 256),
		done:   make(chan struct{}),
	}
	f.apply(&s.mode)
	if err := s.open(); err != nil {
		return nil, err
	}
	go s.readLoop()
	return s, nil
}

func (s *Session) open() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed() {
		return errClosed
	}
	p, err := serial.Open(s.path, &s.mode)
	if err != nil {
		return err
	}
	if err := p.SetReadTimeout(readTimeout); err != nil {
		p.Close()
		return err
	}
	s.port = p
	return nil
}

func (s *Session) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Session) current() serial.Port {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// closePort closes and forgets p if it is still the current port.
func (s *Session) closePort(p serial.Port) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != nil && s.port == p {
		p.Close()
		s.port = nil
	}
}

// emit delivers e unless the session is closing.
func (s *Session) emit(e Event) {
	select {
	case s.events <- e:
	case <-s.done:
	}
}

// Path returns the port's path, e.g. /dev/ttyUSB0.
func (s *Session) Path() string { return s.path }

// Events delivers device output and state changes. Closed after Close.
func (s *Session) Events() <-chan Event { return s.events }

// readLoop delivers output and reconnects if the adapter disappears.
func (s *Session) readLoop() {
	defer close(s.events) // the reader is the only sender
	buf := make([]byte, 4096)
	for !s.isClosed() {
		p := s.current()
		if p == nil {
			select {
			case <-time.After(reconnectPoll):
			case <-s.done:
				return
			}
			if s.open() == nil {
				s.emit(Event{State: Connected})
			}
			continue
		}
		s.ioMu.Lock()
		baud := s.Baud() // under ioMu: SetBaud and Detect change the rate only between reads
		n, err := p.Read(buf)
		s.ioMu.Unlock()
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			s.taps.Send(data)
			s.emit(Event{Data: data, Baud: baud})
		}
		if err != nil && !s.isClosed() {
			s.closePort(p)
			s.emit(Event{State: Disconnected})
		}
	}
}

// Write sends bytes to the device. Implements io.Writer.
func (s *Session) Write(b []byte) (int, error) {
	if s.detecting.Load() {
		return 0, errDetecting
	}
	p := s.current()
	if p == nil {
		return 0, errNotConnected
	}
	for n := 0; n < len(b); {
		m, err := p.Write(b[n:])
		if n += m; err != nil {
			return n, err
		}
	}
	return len(b), nil
}

// Tap returns a channel that receives a copy of everything the device sends
// from now on, until cancel is called. A slow reader misses data rather than
// stalling the session.
func (s *Session) Tap() (<-chan []byte, func()) { return s.taps.Tap() }

// Baud returns the current rate.
func (s *Session) Baud() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode.BaudRate
}

// Framing returns the current data bits, parity and stop bits.
func (s *Session) Framing() Framing {
	s.mu.Lock()
	defer s.mu.Unlock()
	return framingOf(s.mode)
}

// SetBaud changes the rate. While disconnected it applies on reconnect.
func (s *Session) SetBaud(baud int) error {
	s.ioMu.Lock() // between reads, so each read is stamped with the rate it ran at
	defer s.ioMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := s.mode
	mode.BaudRate = baud
	if s.port != nil {
		if err := s.port.SetMode(&mode); err != nil {
			return err
		}
		_ = s.port.ResetInputBuffer() // bytes received at the old rate would read as the new one's
	}
	s.mode = mode
	return nil
}

// Break holds the line in BREAK for d.
func (s *Session) Break(d time.Duration) error {
	p := s.current()
	if p == nil {
		return errNotConnected
	}
	return p.Break(d)
}

// Reconnect closes the port; the read loop notices and reopens it.
// It never emits, so it is safe to call from the event consumer.
func (s *Session) Reconnect() {
	s.closePort(s.current())
}

// Detect pauses the read loop and runs baud detection. On success the port
// is left at the detected rate and framing, and the bytes heard there are
// returned so the caller can show them. On failure nothing changes.
func (s *Session) Detect(cfg DetectConfig) (Result, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.detecting.Store(true)
	defer s.detecting.Store(false)

	p := s.current()
	if p == nil {
		return Result{}, errNotConnected
	}
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()

	r, err := detect(p, mode, cfg)
	if err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	s.mode.BaudRate = r.Baud
	r.Framing.apply(&s.mode)
	s.mu.Unlock()
	return r, nil
}

// Close stops the session and releases the port.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.port != nil {
			err = s.port.Close()
			s.port = nil
		}
	})
	return err
}
