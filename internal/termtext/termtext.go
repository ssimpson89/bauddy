// Package termtext reduces terminal output to the plain text a person saw.
package termtext

import "unicode/utf8"

// Cleaner turns a raw terminal byte stream into plain lines: escape sequences
// removed, backspaces and carriage-return overwrites applied. It keeps state
// between calls because escape sequences and lines span reads.
type Cleaner struct {
	line  []byte
	state int
	cr    bool // saw \r; a following \n ends the line, anything else overwrites it
}

const (
	sText = iota
	sEsc  // after ESC
	sEscI // ESC, intermediate bytes such as ( in ESC ( B, until a final byte
	sCSI  // ESC [ ... final byte
	sStr  // OSC/DCS/APC/PM string, until BEL or ESC \
	sStrEsc
)

// Feed consumes b and returns the lines it completed, each ending in \n.
func (c *Cleaner) Feed(b []byte) []byte {
	var out []byte
	for _, ch := range b {
		switch c.state {
		case sEsc:
			switch ch {
			case '[':
				c.state = sCSI
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
			if ch >= 0x40 && ch <= 0x7e {
				c.state = sText
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

		if c.cr && ch != '\n' {
			c.line = c.line[:0] // bare \r: the line is being redrawn
		}
		c.cr = false
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
