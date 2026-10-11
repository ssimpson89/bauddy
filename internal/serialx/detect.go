package serialx

import (
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"go.bug.st/serial"
)

// detectRates is the order baud detection tries: 115200 (Linux, BMCs, BIOS
// redirection) and 9600 (most network gear) first, then by how often they
// turn up on consoles.
var detectRates = []int{115200, 9600, 57600, 38400, 19200, 230400}

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
	// Probe sends a carriage return at each rate to provoke a prompt.
	// Off by default: Enter at a GRUB menu boots the default entry, and at a
	// [confirm] prompt it confirms.
	Probe bool
	// KeepFraming searches rates only, at the current framing (the user set it).
	KeepFraming bool
}

// Result is what detection found.
type Result struct {
	Baud    int
	Framing Framing
	Heard   []byte // what was heard at that rate, top bit dropped for 7-bit framing
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

	// Try the current rate first: what's already buffered arrived at it.
	rates := append([]int{orig.BaudRate}, slices.DeleteFunc(slices.Clone(detectRates), func(r int) bool { return r == orig.BaudRate })...)
	var bestScore float64
	buf := make([]byte, 512)
	for _, rate := range rates {
		if rate != mode.BaudRate {
			mode.BaudRate = rate
			if err := p.SetMode(&mode); err != nil {
				continue // some adapters reject odd rates; skip them
			}
			_ = p.ResetInputBuffer() // drop bytes received at the previous rate
		} // else keep what's buffered: it arrived at this rate
		if cfg.Probe {
			_, _ = p.Write([]byte("\r"))
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

		r, s := Result{rate, framingOf(mode), got}, score(got)
		if f, ok := sevenBit(got); ok && !cfg.KeepFraming {
			text := strip7(got)
			if ts := score(text); ts > s {
				r, s = Result{rate, f, text}, ts
			}
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
		case c >= 0x20 && c <= 0x7e,
			c == '\r', c == '\n', c == '\t',
			c == 0x1b, c == 0x08, c == 0x07: // ESC, BS, BEL: normal in terminal output
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
