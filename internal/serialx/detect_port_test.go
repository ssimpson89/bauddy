package serialx

import (
	"bytes"
	"errors"
	"math/bits"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

// fakePort is a device that talks at rate and emits garbage at any other.
type fakePort struct {
	serial.Port // unimplemented methods panic
	rate        int
	idle        bool // says nothing unless probed
	mode        serial.Mode
	writes      int
	probed      bool
	reply       string        // what the device says; default a login prompt
	parity      serial.Parity // the device's framing: 7 data bits with this parity, if set
	readAt      []int         // the rate of each read, in order
}

func (f *fakePort) SetMode(m *serial.Mode) error { f.mode = *m; f.probed = false; return nil }
func (f *fakePort) ResetInputBuffer() error      { return nil }
func (f *fakePort) Write(b []byte) (int, error)  { f.writes++; f.probed = true; return len(b), nil }
func (f *fakePort) Read(b []byte) (int, error) {
	time.Sleep(10 * time.Millisecond) // like a real port's read timeout
	if f.idle && !f.probed {
		return 0, nil
	}
	f.readAt = append(f.readAt, f.mode.BaudRate)
	out := []byte("switch01 login: \r\n")
	if f.reply != "" {
		out = []byte(f.reply)
		f.probed = false // answers once per probe, like a prompt
	}
	if f.mode.BaudRate != f.rate {
		out = []byte{0xf0, 0x80, 0x00, 0xfe, 0x9c, 0x00, 0xe0, 0x11, 0xff, 0x80, 0x00, 0xc3}
	} else if f.parity != serial.NoParity && f.mode.DataBits == 8 {
		out = withParity(out, f.parity) // read as 8N1, the parity bit lands on top
	}
	return copy(b, out), nil
}

func TestDetectFindsRate(t *testing.T) {
	p := &fakePort{rate: 9600}
	r, err := detect(p, mode8N1(115200), DetectConfig{})
	if err != nil || r.Baud != 9600 || p.mode.BaudRate != 9600 || r.Framing != Framing8N1 {
		t.Fatalf("got %+v port=%d err=%v, want 9600 8N1", r, p.mode.BaudRate, err)
	}
	if !bytes.Contains(r.Heard, []byte("login:")) {
		t.Errorf("heard = %q, want the login prompt", r.Heard)
	}
	if p.writes != 0 {
		t.Errorf("listen-only detect wrote %d times", p.writes)
	}
}

func TestDetectIdleRestoresRate(t *testing.T) {
	p := &fakePort{rate: 9600, idle: true}
	_, err := detect(p, mode8N1(19200), DetectConfig{})
	if !errors.Is(err, ErrNoSignal) {
		t.Fatalf("err = %v, want ErrNoSignal", err)
	}
	if p.mode.BaudRate != 19200 || framingOf(p.mode) != Framing8N1 {
		t.Errorf("port left at %d, want original 19200", p.mode.BaudRate)
	}
}

func TestDetectProbeWakesIdle(t *testing.T) {
	p := &fakePort{rate: 9600, idle: true}
	r, err := detect(p, mode8N1(115200), DetectConfig{Probe: true})
	if err != nil || r.Baud != 9600 {
		t.Fatalf("got %d, %v; want 9600", r.Baud, err)
	}
}

func TestDetectShortProbeReply(t *testing.T) {
	p := &fakePort{rate: 9600, idle: true, reply: "\r\nSwitch#"}
	r, err := detect(p, mode8N1(115200), DetectConfig{Probe: true})
	if err != nil || r.Baud != 9600 {
		t.Fatalf("got %d, %v; want 9600 from a bare prompt", r.Baud, err)
	}
}

func TestDetectTriesCurrentRateFirst(t *testing.T) {
	p := &fakePort{rate: 38400}
	p.mode.BaudRate = 38400
	r, err := detect(p, mode8N1(38400), DetectConfig{})
	if err != nil || r.Baud != 38400 || p.readAt[0] != 38400 {
		t.Fatalf("got %d, %v, first read at %d; want 38400 tried first", r.Baud, err, p.readAt[0])
	}
}

func mode8N1(baud int) serial.Mode {
	return serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit}
}

// withParity sets each byte's top bit as a 7-bit parity bit would arrive.
func withParity(b []byte, p serial.Parity) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		odd := bits.OnesCount8(c)%2 == 1
		if odd == (p == serial.EvenParity) {
			c |= 0x80
		}
		out[i] = c
	}
	return out
}

func TestDetect7Bit(t *testing.T) {
	for _, par := range []serial.Parity{serial.EvenParity, serial.OddParity} {
		p := &fakePort{rate: 9600, parity: par}
		r, err := detect(p, mode8N1(115200), DetectConfig{})
		want := Framing{7, par, serial.OneStopBit}
		if err != nil || r.Baud != 9600 || r.Framing != want || framingOf(p.mode) != want {
			t.Fatalf("got %+v, %v; want 9600 %v", r, err, want)
		}
		if !bytes.Contains(r.Heard, []byte("login:")) {
			t.Errorf("%v: heard %q, want readable text", want, r.Heard)
		}
	}
}

func TestParseFraming(t *testing.T) {
	for _, s := range []string{"8N1", "7E1", "7o1", "8E1", "8N2"} {
		f, err := ParseFraming(s)
		if err != nil || f.String() != strings.ToUpper(s) {
			t.Errorf("ParseFraming(%q) = %v, %v", s, f, err)
		}
	}
	for _, s := range []string{"", "9N1", "8X1", "8N3", "7E"} {
		if _, err := ParseFraming(s); err == nil {
			t.Errorf("ParseFraming(%q) accepted", s)
		}
	}
}
