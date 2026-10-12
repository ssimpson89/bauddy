package serialx

import "sync"

// Taps hands a copy of output to whoever asks for one. A slow reader misses
// data rather than stalling the sender.
type Taps struct {
	mu    sync.Mutex
	chans map[chan []byte]struct{}
}

// Tap returns a channel that receives a copy of everything sent from now
// on, until cancel is called.
func (t *Taps) Tap() (<-chan []byte, func()) {
	ch := make(chan []byte, 1024)
	t.mu.Lock()
	if t.chans == nil {
		t.chans = map[chan []byte]struct{}{}
	}
	t.chans[ch] = struct{}{}
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		delete(t.chans, ch)
		t.mu.Unlock()
	}
}

// Send copies b to every tap.
func (t *Taps) Send(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ch := range t.chans {
		select {
		case ch <- b:
		default:
		}
	}
}
