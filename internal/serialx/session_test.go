package serialx

import (
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// ratePort answers every read, and counts input-buffer resets.
type ratePort struct {
	serial.Port // unimplemented methods panic
	mu          sync.Mutex
	resets      int
}

func (p *ratePort) SetMode(*serial.Mode) error { return nil }
func (p *ratePort) ResetInputBuffer() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resets++
	return nil
}
func (p *ratePort) Read(b []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return copy(b, "Switch#"), nil
}

// Each read carries the rate it ran at: once reads at the new rate begin,
// none from the old one follows, and what was waiting at the old rate is
// dropped rather than read as the new one's.
func TestReadsCarryTheRateTheyRanAt(t *testing.T) {
	p := &ratePort{}
	s := &Session{port: p, mode: serial.Mode{BaudRate: 9600}, events: make(chan Event, 4096), done: make(chan struct{})}
	go s.readLoop()
	defer close(s.done)
	time.Sleep(20 * time.Millisecond)
	if err := s.SetBaud(19200); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	var sawNew bool
	for len(s.events) > 0 {
		e := <-s.events
		switch {
		case e.Baud == 19200:
			sawNew = true
		case e.Baud == 9600 && sawNew:
			t.Fatal("a read stamped 9600 after reads at 19200 had begun")
		case e.Baud != 9600:
			t.Fatalf("read stamped %d", e.Baud)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !sawNew || p.resets != 1 {
		t.Errorf("saw reads at 19200: %v; input buffer reset %d times, want once", sawNew, p.resets)
	}
}
