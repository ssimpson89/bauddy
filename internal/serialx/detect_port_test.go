package serialx

import (
	"bytes"
	"errors"
	"fmt"
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
	writeAt     []int         // the rate of each write, in order
	wrote       []string      // each write
	waiting     string        // already received before detection, at the device's rate
	breaks      int           // writes slow enough to look like a BREAK to the device
}

func (f *fakePort) SetMode(m *serial.Mode) error { f.mode = *m; f.probed = false; return nil }
func (f *fakePort) ResetInputBuffer() error      { return nil }
func (f *fakePort) Write(b []byte) (int, error) {
	f.writes++
	f.probed = true
	f.writeAt = append(f.writeAt, f.mode.BaudRate)
	f.wrote = append(f.wrote, string(b))
	// Sent at 1/2.4 of the device's rate or slower, Enter's run of four zero
	// bits outlasts a whole character at the device: a BREAK.
	if float64(f.mode.BaudRate)*2.4 <= float64(f.rate) {
		f.breaks++
	}
	return len(b), nil
}
func (f *fakePort) Read(b []byte) (int, error) {
	time.Sleep(10 * time.Millisecond) // like a real port's read timeout
	if w := f.waiting; w != "" && f.mode.BaudRate == f.rate {
		f.waiting = ""
		return copy(b, w), nil
	}
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

// On a Linux console a BREAK arms SysRq, so the next key is a command (b
// reboots); during a Cisco boot it drops to ROMMON. A probe must never send
// one, whatever rate the device is at and whatever rate bauddy starts at.
func TestProbeNeverSendsABreak(t *testing.T) {
	for _, dev := range []int{9600, 19200, 38400, 57600, 115200, 230400, 460800} {
		for _, start := range []int{9600, 115200} {
			t.Run(fmt.Sprintf("device %d, starting at %d", dev, start), func(t *testing.T) {
				t.Parallel()
				p := &fakePort{rate: dev, idle: true}
				r, err := detect(p, mode8N1(start), DetectConfig{Probe: true})
				if err != nil || r.Baud != dev {
					t.Errorf("got %d, %v", r.Baud, err)
				}
				if p.breaks != 0 {
					t.Errorf("%d BREAKs (writes at %v)", p.breaks, p.writeAt)
				}
			})
		}
	}
}

// A terse prompt scores low, but it's a reply: probing must stop there, not
// carry on to slower rates.
func TestProbeStopsAtAShortPrompt(t *testing.T) {
	p := &fakePort{rate: 115200, idle: true, reply: "\r\n$ "}
	r, err := detect(p, mode8N1(9600), DetectConfig{Probe: true})
	if err != nil || r.Baud != 115200 {
		t.Fatalf("got %d, %v; want 115200 from a bare $ prompt", r.Baud, err)
	}
	if last := p.writeAt[len(p.writeAt)-1]; last != 115200 || p.breaks != 0 {
		t.Errorf("last Enter at %d, %d BREAKs; want it to stop at 115200 (writes at %v)", last, p.breaks, p.writeAt)
	}
}

func TestProbeFinds7BitGearWithoutABreak(t *testing.T) {
	p := &fakePort{rate: 9600, idle: true, parity: serial.EvenParity}
	r, err := detect(p, mode8N1(115200), DetectConfig{Probe: true})
	if err != nil || r.Baud != 9600 || r.Framing.DataBits != 7 || p.breaks != 0 {
		t.Errorf("got %d %v, %v, %d BREAKs; want 9600 7E1 and none", r.Baud, r.Framing, err, p.breaks)
	}
}

// A faster rate's probe lands as a stray character on the device's line,
// so each probe erases the line (Ctrl-U) before Enter: at a login prompt
// the stray character isn't entered as a username.
func TestProbeClearsTheLineBeforeEnter(t *testing.T) {
	p := &fakePort{rate: 115200, idle: true}
	if _, err := detect(p, mode8N1(9600), DetectConfig{Probe: true}); err != nil {
		t.Fatal(err)
	}
	for i, w := range p.wrote {
		if w != "\x15\r" {
			t.Errorf("probe %d at %d sent %q, want Ctrl-U then Enter", i, p.writeAt[i], w)
		}
	}
}

// A clean prompt already waiting at the current rate is an answer: probing
// on would send at slower rates.
func TestProbeStopsAtAPromptAlreadyThere(t *testing.T) {
	p := &fakePort{rate: 115200, idle: true, waiting: "$ ", mode: mode8N1(115200)}
	r, err := detect(p, mode8N1(115200), DetectConfig{Probe: true})
	if err != nil || r.Baud != 115200 || p.writes != 0 {
		t.Errorf("got %d, %v, %d writes; want 115200 from what was there, nothing sent", r.Baud, err, p.writes)
	}
}

func TestProbeStopsAtABareOrNonEnglishPrompt(t *testing.T) {
	for _, reply := range []string{"$", "#", "用户:"} {
		p := &fakePort{rate: 115200, idle: true, reply: reply}
		r, err := detect(p, mode8N1(9600), DetectConfig{Probe: true})
		if err != nil || r.Baud != 115200 || p.breaks != 0 {
			t.Errorf("reply %q: got %d, %v, %d BREAKs; want 115200 and none", reply, r.Baud, err, p.breaks)
		}
	}
}

func TestProbeFlagsAPasswordPrompt(t *testing.T) {
	p := &fakePort{rate: 115200, idle: true, reply: "\r\nPassword: "}
	r, err := detect(p, mode8N1(9600), DetectConfig{Probe: true})
	if err != nil || !r.StrayLogin {
		t.Errorf("got %+v, %v; want StrayLogin set", r, err)
	}
}
