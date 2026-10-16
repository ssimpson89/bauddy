// Package termtext reduces terminal output to the plain text a person saw.
package termtext

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Cleaner turns a raw terminal byte stream into plain lines: escape sequences
// removed, backspaces and carriage-return overwrites applied. It keeps state
// between calls because escape sequences and lines span reads.
type Cleaner struct {
	line  []byte
	state int
	cr    bool   // saw \r: a \n ends the line, visible text starts it again (a redraw), nothing else counts
	csi   []byte // the parameters of the escape sequence being read, for erase-line
}

const (
	sText = iota
	sEsc  // after ESC
	sEscI // ESC, intermediate bytes such as ( in ESC ( B, until a final byte
	sCSI  // ESC [ ... final byte
	sStr  // OSC/DCS/APC/PM string, until BEL or ESC \
	sStrEsc
)

// lineMax bounds a line. Output at the wrong baud rate may never contain a
// newline; past this, a line keeps its end.
const lineMax = 16 << 10

// Feed consumes b and returns the lines it completed, each ending in \n.
func (c *Cleaner) Feed(b []byte) []byte {
	defer c.bound()
	var out []byte
	for _, ch := range b {
		switch c.state {
		case sEsc:
			switch ch {
			case '[':
				c.state, c.csi = sCSI, c.csi[:0]
			case ']', 'P', '_', '^', 'X':
				c.state = sStr
			default:
				if ch >= 0x20 && ch <= 0x2f {
					c.state = sEscI
				} else {
					c.state = sText
				}
			}
			continue
		case sEscI:
			if ch < 0x20 || ch > 0x2f {
				c.state = sText
			}
			continue
		case sCSI:
			switch {
			case ch == 'K': // erase in line: all of it (2), or from the cursor, which a \r put at the start
				if p := string(c.csi); p == "2" || c.cr && (p == "" || p == "0") {
					c.line = c.line[:0]
				}
				c.state = sText
			case ch >= 0x40 && ch <= 0x7e:
				c.state = sText
			case len(c.csi) < 16:
				c.csi = append(c.csi, ch)
			}
			continue
		case sStr:
			switch ch {
			case 0x07:
				c.state = sText
			case 0x1b:
				c.state = sStrEsc
			}
			continue
		case sStrEsc:
			c.state = sText // ESC \ ends the string
			continue
		}

		// Only what shows decides a \r: more \r, colour codes and bells don't.
		visible := ch == '\t' || ch >= 0x20 && ch != 0x7f
		if c.cr && visible {
			c.line = c.line[:0] // \r, then text: the line is being redrawn
		}
		if visible || ch == '\n' {
			c.cr = false
		}
		switch {
		case ch == 0x1b:
			c.state = sEsc
		case ch == '\n':
			out = append(append(out, c.line...), '\n')
			c.line = c.line[:0]
		case ch == '\r':
			c.cr = true
		case ch == '\b':
			if _, n := utf8.DecodeLastRune(c.line); n > 0 {
				c.line = c.line[:len(c.line)-n]
			}
		case ch == '\t' || ch >= 0x20 && ch != 0x7f:
			c.line = append(c.line, ch)
		}
	}
	return out
}

// bound keeps the current line within lineMax, trimming it to half that
// so trimming stays rare.
func (c *Cleaner) bound() {
	if len(c.line) <= lineMax {
		return
	}
	cut := len(c.line) - lineMax/2
	for cut < len(c.line) && !utf8.RuneStart(c.line[cut]) {
		cut++
	}
	c.line = append(c.line[:0], c.line[cut:]...)
}

// Partial returns the unfinished current line, e.g. a prompt.
func (c *Cleaner) Partial() string { return string(c.line) }

// Flush returns the partial line, ending it with a newline, and clears it.
func (c *Cleaner) Flush() []byte {
	if len(c.line) == 0 {
		return nil
	}
	out := append(append([]byte(nil), c.line...), '\n')
	c.line, c.cr = c.line[:0], false
	return out
}

// Pattern matches q literally, with smart case: all lowercase matches any
// case ("error" finds "ERROR"); any capital matches exactly.
func Pattern(q string) *regexp.Regexp {
	expr := regexp.QuoteMeta(q)
	if q == strings.ToLower(q) {
		expr = "(?i)" + expr
	}
	return regexp.MustCompile(expr)
}
