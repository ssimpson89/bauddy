// Package termtext reduces terminal output to the plain text a person saw.
package termtext

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Cleaner turns a raw terminal byte stream into plain lines: escape sequences
// removed, and the cursor's moves within a line applied as a terminal does,
// so shell line editing (backspace, ctrl+u, history recall, mid-line edits)
// and carriage-return overwrites leave the text that was on screen. It keeps
// state between calls because escape sequences, characters and lines span
// reads.
type Cleaner struct {
	line  []rune
	col   int // the cursor's place in line; past its end after a move right
	crCol int // where the cursor was at a \r just before, so a line ending \r\n keeps blanks it had passed
	state int
	csi   []byte // the parameters of the escape sequence being read
	utf   []byte // a UTF-8 character split across reads
	out   []byte // lines completed during this Feed
}

const (
	sText = iota
	sEsc  // after ESC
	sEscI // ESC, intermediate bytes such as ( in ESC ( B, until a final byte
	sCSI  // ESC [ ... final byte
	sStr  // OSC/DCS/APC/PM string, until BEL or ESC \
	sStrEsc
)

// lineMax bounds a line, in characters. Output at the wrong baud rate may
// never contain a newline; past this, a line keeps its end.
const lineMax = 16 << 10

// Feed consumes b and returns the lines it completed, each ending in \n.
func (c *Cleaner) Feed(b []byte) []byte {
	c.out = nil
	for _, ch := range b {
		c.feed(ch)
	}
	c.bound()
	return c.out
}

func (c *Cleaner) feed(ch byte) {
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
		return
	case sEscI:
		if ch < 0x20 || ch > 0x2f {
			c.state = sText
		}
		return
	case sCSI:
		switch {
		case ch >= 0x40 && ch <= 0x7e:
			c.state = sText
			c.control(ch)
		case len(c.csi) < 16:
			c.csi = append(c.csi, ch)
		}
		return
	case sStr:
		switch ch {
		case 0x07:
			c.state = sText
		case 0x1b:
			c.state = sStrEsc
		}
		return
	case sStrEsc:
		c.state = sText // ESC \ ends the string
		return
	}

	if len(c.utf) > 0 || ch >= 0x80 {
		c.utf = append(c.utf, ch)
		if !utf8.FullRune(c.utf) {
			return
		}
		r, n := utf8.DecodeRune(c.utf) // utf8.RuneError for a byte that isn't UTF-8
		rest := append([]byte(nil), c.utf[n:]...)
		c.utf = c.utf[:0]
		c.put(r)
		for _, b := range rest { // what followed a broken character
			c.feed(b)
		}
		return
	}
	switch {
	case ch == 0x1b:
		c.state = sEsc
	case ch == '\n':
		c.endLine()
	case ch == '\r':
		c.crCol, c.col = max(c.crCol, c.col), 0
	case ch == '\b':
		c.col, c.crCol = max(0, c.col-1), 0 // moves the cursor; what's there stays until overwritten
	case ch == '\t' || ch >= 0x20 && ch != 0x7f:
		c.put(rune(ch))
	}
}

// put writes r at the cursor, over what's there, and moves the cursor on.
func (c *Cleaner) put(r rune) {
	c.crCol = 0
	for len(c.line) < c.col {
		c.line = append(c.line, ' ')
	}
	if c.col < len(c.line) {
		c.line[c.col] = r
	} else {
		c.line = append(c.line, r)
	}
	c.col++
}

// control applies the escape sequence ending in final, ESC [ params final,
// as far as it moves the cursor or erases within the line.
func (c *Cleaner) control(final byte) {
	params := string(c.csi)
	n := func() int { // the first parameter, default 1, kept to a line's length: noise can say anything
		first, _, _ := strings.Cut(params, ";")
		v, err := strconv.Atoi(first)
		if err != nil || v < 1 {
			return 1
		}
		return min(v, lineMax)
	}
	if strings.ContainsRune("DCGHfKJP@X", rune(final)) {
		c.crCol = 0 // a move or an edit; colour and the like leave the line as it was
	}
	switch final {
	case 'D': // left
		c.col = max(0, c.col-n())
	case 'C': // right
		c.col = min(c.col+n(), lineMax)
	case 'G': // to column
		c.col = n() - 1
	case 'H', 'f': // to row and column: a full-screen program moving on, so the line is done
		if len(c.visible()) > 0 {
			c.endLine()
		}
		c.col = 0
		_, col, _ := strings.Cut(params, ";")
		if v, err := strconv.Atoi(col); err == nil && v > 0 {
			c.col = min(v, lineMax) - 1
		}
	case 'K', 'J': // erase in line, or on screen (taken as this line)
		switch params {
		case "", "0": // from the cursor on
			c.line = c.line[:min(c.col, len(c.line))]
		case "1": // up to the cursor
			for i := 0; i <= c.col && i < len(c.line); i++ {
				c.line[i] = ' '
			}
		case "2": // all of it: what's written next starts the line
			c.line, c.col, c.crCol = c.line[:0], 0, 0
		}
	case 'P': // delete characters
		if c.col < len(c.line) {
			c.line = append(c.line[:c.col], c.line[min(len(c.line), c.col+n()):]...)
		}
	case '@': // insert blanks
		if c.col < len(c.line) {
			blanks := []rune(strings.Repeat(" ", n()))
			c.line = append(c.line[:c.col], append(blanks, c.line[c.col:]...)...)
		}
	case 'X': // erase characters
		for i := c.col; i < c.col+n() && i < len(c.line); i++ {
			c.line[i] = ' '
		}
	}
}

// endLine completes the current line.
func (c *Cleaner) endLine() {
	c.out = append(append(c.out, string(c.visible())...), '\n')
	c.line, c.col, c.crCol = c.line[:0], 0, 0
}

// visible is the line as it shows: blanks left past the cursor by erasing
// with spaces (Cisco's \b \b) aren't text, but a prompt's trailing space,
// which the cursor has been past, is.
func (c *Cleaner) visible() []rune {
	end := len(c.line)
	for end > max(c.col, c.crCol) && c.line[end-1] == ' ' {
		end--
	}
	return c.line[:end]
}

// bound keeps the current line within lineMax, trimming it to half that
// so trimming stays rare.
func (c *Cleaner) bound() {
	if len(c.line) <= lineMax {
		return
	}
	cut := len(c.line) - lineMax/2
	c.line = append(c.line[:0], c.line[cut:]...)
	c.col = max(0, c.col-cut)
}

// Partial returns the unfinished current line, e.g. a prompt.
func (c *Cleaner) Partial() string { return string(c.visible()) }

// Flush returns the partial line, ending it with a newline, and clears it.
func (c *Cleaner) Flush() []byte {
	if len(c.utf) > 0 { // a character cut short: show it was there
		c.utf = c.utf[:0]
		c.put(utf8.RuneError)
	}
	if len(c.line) == 0 {
		c.col, c.crCol = 0, 0 // a move with nothing written still mustn't carry over
		return nil
	}
	c.out = nil
	c.endLine()
	return c.out
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
