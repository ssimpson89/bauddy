package serialx

import (
	"fmt"
	"math/bits"
	"strings"

	"go.bug.st/serial"
)

// Framing is data bits, parity and stop bits, written like "8N1" or "7E1".
type Framing struct {
	DataBits int
	Parity   serial.Parity
	StopBits serial.StopBits
}

// Framing8N1 is what nearly everything uses.
var Framing8N1 = Framing{8, serial.NoParity, serial.OneStopBit}

var parityLetters = map[serial.Parity]string{
	serial.NoParity: "N", serial.EvenParity: "E", serial.OddParity: "O",
	serial.MarkParity: "M", serial.SpaceParity: "S",
}

// String writes f the usual way, e.g. "8N1".
func (f Framing) String() string {
	stop := "1"
	if f.StopBits == serial.TwoStopBits {
		stop = "2"
	}
	return fmt.Sprintf("%d%s%s", f.DataBits, parityLetters[f.Parity], stop)
}

// ParseFraming reads "8N1", "7E1", "8E1", "7O1", "8N2" and the like.
func ParseFraming(s string) (Framing, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	bad := fmt.Errorf("framing %q: want data bits, parity and stop bits, like 8N1 or 7E1", s)
	if len(s) != 3 || s[0] < '5' || s[0] > '8' || (s[2] != '1' && s[2] != '2') {
		return Framing{}, bad
	}
	f := Framing{DataBits: int(s[0] - '0'), StopBits: serial.OneStopBit}
	if s[2] == '2' {
		f.StopBits = serial.TwoStopBits
	}
	for p, l := range parityLetters {
		if l == s[1:2] {
			f.Parity = p
			return f, nil
		}
	}
	return Framing{}, bad
}

func (f Framing) apply(m *serial.Mode) {
	m.DataBits, m.Parity, m.StopBits = f.DataBits, f.Parity, f.StopBits
}

func framingOf(m serial.Mode) Framing { return Framing{m.DataBits, m.Parity, m.StopBits} }

// sevenBit reports whether b, read as 8N1, looks like 7-bit data with a
// parity bit: some byte has the top bit set and nearly every byte agrees on
// parity. It returns 7E1 or 7O1. The caller still checks that the bytes read
// as text once the top bit is dropped.
func sevenBit(b []byte) (Framing, bool) {
	if len(b) < 8 {
		return Framing{}, false // too few bytes for parity agreement to mean anything
	}
	even, high := 0, 0
	for _, c := range b {
		if bits.OnesCount8(c)%2 == 0 {
			even++
		}
		if c&0x80 != 0 {
			high++
		}
	}
	if high == 0 {
		return Framing{}, false // plain ASCII: nothing says it isn't 8N1
	}
	// 95%: allow for a little line noise; real 8-bit data agrees by chance
	// about half the time per byte, so it never gets close.
	switch n := len(b); {
	case even*20 >= n*19:
		return Framing{7, serial.EvenParity, serial.OneStopBit}, true
	case (n-even)*20 >= n*19:
		return Framing{7, serial.OddParity, serial.OneStopBit}, true
	}
	return Framing{}, false
}

func strip7(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c & 0x7f
	}
	return out
}
