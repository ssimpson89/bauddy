package serialx

import (
	"bytes"
	"errors"
	"time"
	"unicode/utf8"

	"go.bug.st/serial"
)

// detectRates is the order baud detection tries: 115200 (Linux, BMCs, BIOS
// redirection) and 9600 (most network gear) first, then by how often they
// turn up on consoles.
var detectRates = []int{115200, 9600, 57600, 38400, 19200, 230400}

// probeRates is the order a probe sends probeKeys in: fastest first, and
// it stops at the first rate that answers. A byte sent slower than the
// device's rate holds the line low long enough to look like a BREAK, which
// on a Linux console arms Magic SysRq (the next key it gets is a command: b
// reboots) and on Cisco gear during boot drops to ROMMON. Sent faster, it's
// at worst a stray byte with its top bit set, which lands on the device's
// line like a typed character.
var probeRates = []int{921600, 460800, 230400, 115200, 57600, 38400, 19200, 9600}

// probeKeys is what a probe sends at each rate: Ctrl-U, which erases the
// line (at a login prompt, a shell or a network CLI), so a stray byte left
// by a faster rate isn't entered as a username, then Enter. Neither is a
// SysRq command, so even a console armed by keys typed at a wrong rate
// can't be made to act on one.
const probeKeys = "\x15\r"

// strayLogin says whether a probe's reply suggests a stray character, left
// by a faster rate's probe, was taken as a username: the device asks for a
// password. Ctrl-U erases it on most consoles, but not every login prompt
// honours it.
func strayLogin(heard []byte) bool {
	return bytes.Contains(bytes.ToLower(heard), []byte("password"))
}

// ErrNoSignal means nothing readable arrived at any tested rate.
var ErrNoSignal = errors.New("no readable data at any baud rate")

const (
	// detectWindow is how long to listen at each rate. Long enough for a
	// line of boot output at 9600 (about 1 byte per ms).
	detectWindow = 400 * time.Millisecond
	// detectSure stops early: no other rate will beat a near-perfect score.
	detectSure = 0.95
	// detectMin is the lowest score accepted as the right rate.
	detectMin = 0.6
)

// DetectConfig controls baud detection.
type DetectConfig struct {
	// Probe listens at the current rate, then sends a carriage return at
	// each of probeRates to provoke a prompt. Off by default: Enter at a GRUB
	// menu boots the default entry, at a [confirm] prompt it confirms, and
	// any byte stops a boot countdown.
	Probe bool
	// KeepFraming searches rates only, at the current framing (the user set it).
	KeepFraming bool
}

// Result is what detection found.
type Result struct {
	Baud    int
	Framing Framing
	Heard   []byte // what was heard at that rate, top bit dropped for 7-bit framing
	// StrayLogin: probing left a password prompt, probably because a stray
	// character from a faster rate went in as a username (see strayLogin).
	StrayLogin bool
}

// detect tries each rate on p and leaves p at the best one. It listens in
// 8N1, which also reveals 7-bit gear: its parity bit arrives as the top bit.
// If nothing scores at least detectMin, or anything fails, p is restored to
// mode.
func detect(p serial.Port, mode serial.Mode, cfg DetectConfig) (best Result, err error) {
	orig := mode
	defer func() {
		if err != nil {
			_ = p.SetMode(&orig)
		}
	}()
	if framingOf(mode) != Framing8N1 && !cfg.KeepFraming {
		Framing8N1.apply(&mode)
		if err := p.SetMode(&mode); err != nil {
			return Result{}, err
		}
		_ = p.ResetInputBuffer()
	}

	// Try the current rate first, only listening: what's already buffered
	// arrived at it. A probe then sends Enter fastest first (see probeRates).
	type try struct {
		rate int
		send bool
	}
	tries := []try{{orig.BaudRate, false}}
	if cfg.Probe {
		for _, r := range probeRates {
			tries = append(tries, try{r, true})
		}
	} else {
		for _, r := range detectRates {
			if r != orig.BaudRate {
				tries = append(tries, try{r, false})
			}
		}
	}
	var bestScore float64
	buf := make([]byte, 512)
	for _, t := range tries {
		rate := t.rate
		if rate != mode.BaudRate {
			mode.BaudRate = rate
			if err := p.SetMode(&mode); err != nil {
				continue // some adapters reject odd rates; skip them
			}
			_ = p.ResetInputBuffer() // drop bytes received at the previous rate
		} // else keep what's buffered: it arrived at this rate
		if t.send {
			_, _ = p.Write([]byte(probeKeys))
		}

		// Read returns at least every readTimeout, so this honours the deadline.
		var got []byte
		for deadline := time.Now().Add(detectWindow); time.Now().Before(deadline); {
			n, err := p.Read(buf)
			if err != nil {
				return Result{}, err
			}
			got = append(got, buf[:n]...)
		}

		r, s := Result{Baud: rate, Framing: framingOf(mode), Heard: got}, score(got)
		if f, ok := sevenBit(got); ok && !cfg.KeepFraming {
			text := strip7(got)
			if ts := score(text); ts > s {
				r, s = Result{Baud: rate, Framing: f, Heard: text}, ts
			}
		}
		if cfg.Probe && Answered(r.Heard) {
			// A clean reply, however short ("$"), means this is the rate:
			// probing on would send at slower rates, which is what BREAKs.
			best, bestScore = r, max(s, detectMin)
			best.StrayLogin = t.send && strayLogin(r.Heard)
			break
		}
		if s > bestScore {
			best, bestScore = r, s
			if s >= detectSure {
				break
			}
		}
	}

	if bestScore < detectMin {
		return Result{}, ErrNoSignal
	}
	mode.BaudRate = best.Baud
	best.Framing.apply(&mode)
	if err := p.SetMode(&mode); err != nil {
		return Result{}, err
	}
	return best, nil
}

// Answered says whether b is console text and nothing else: printable
// characters, line endings, the terminal controls score allows and valid
// UTF-8 ("用户:"), however short ("$"). A probe stops at the first such
// reply, and only such replies confirm a rate: at a rate slower than the
// device's some garbage still gets through.
func Answered(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case textByte(c):
			i++
		case c >= 0x80:
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError || size < 2 {
				return false
			}
			i += size
		default:
			return false
		}
	}
	return true
}

// textByte is an ASCII byte normal in console output.
func textByte(c byte) bool {
	return c >= 0x20 && c <= 0x7e || c == '\r' || c == '\n' || c == '\t' ||
		c == 0x1b || c == 0x08 || c == 0x07 // ESC, BS, BEL
}

// score rates how much b looks like real console text, from 0 to 1.
//
// At the wrong baud rate, a UART produces mostly garbage: high-bit bytes,
// NULs and stray control characters. At the right rate you get printable
// ASCII, line endings, ANSI escapes and valid UTF-8 (systemd and modern
// installers draw with it). Short samples are discounted because a couple
// of bytes can look clean by chance.
func score(b []byte) float64 {
	if len(b) < 3 {
		return 0
	}
	good := 0
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case textByte(c):
			good++
			i++
		case c >= 0x80:
			// A valid multi-byte rune counts; a stray high byte doesn't.
			r, size := utf8.DecodeRune(b[i:])
			if r != utf8.RuneError && size > 1 {
				good += size
			}
			i += size
		default:
			i++
		}
	}
	ratio := float64(good) / float64(len(b))
	confidence := min(1, float64(len(b))/8) // full confidence from 8 bytes, e.g. "\r\nSwitch#"
	return ratio * confidence
}
