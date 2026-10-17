package termtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleaner(t *testing.T) {
	cases := []struct {
		name string
		in   []string // separate reads
		want string
	}{
		{"crlf", []string{"line one\r\nline two\r\n"}, "line one\nline two\n"},
		{"ansi colour", []string{"\x1b[1;32mOK\x1b[0m done\r\n"}, "OK done\n"},
		{"escape split across reads", []string{"a\x1b[3", "1mb\r\n"}, "ab\n"},
		{"osc title", []string{"\x1b]0;host\x07prompt$ \r\n"}, "prompt$ \n"},
		{"backspace edit", []string{"shw\b\bhow ver\r\n"}, "show ver\n"},
		{"cisco erase", []string{"Router#ab\b \b\b \bshow\r\n"}, "Router#show\n"},
		{"progress overwrite", []string{"10%\r20%\r100%\r\n"}, "100%\n"},
		{"echoed enter on FreeBSD", []string{"root@fw:~ # true\r\r\n"}, "root@fw:~ # true\n"}, // \r, then \r\n from onlcr
		{"crs split across reads", []string{"ok\r", "\r\n"}, "ok\n"},
		{"cr then colour reset", []string{"a\r\x1b[0m\n"}, "a\n"},
		{"cr then bell", []string{"x\r\a\n"}, "x\n"},
		{"progress cleared with esc[k", []string{"10%\r\x1b[K20%\r\n"}, "20%\n"},
		{"progress erased, then a new line", []string{"10%\r\x1b[K\n"}, "\n"},
		{"whole line erased", []string{"old\x1b[2Knew\r\n"}, "new\n"},
		{"erase to end, at the end", []string{"keep\x1b[K\r\n"}, "keep\n"},
		{"utf8", []string{"[ ✓ ] ok\r\n"}, "[ ✓ ] ok\n"},
		{"charset select", []string{"a\x1b(Bb\r\n"}, "ab\n"},
		{"partial line held", []string{"login: "}, ""},
		// What tcsh on FreeBSD sends for line editing, captured on OPNsense.
		{"tcsh ctrl+u", []string{"# echo abc", "\x1b[8D\x1b[K", "\r\n"}, "# \n"},
		{"tcsh history recall", []string{"# echo abc\x1b[8D\x1b[K", "sleep 30\r\n"}, "# sleep 30\n"},
		{"tcsh insert mid-line", []string{"# echo abcdef", "\b\b\b", "Xdef\b\b\b", "\r\n"}, "# echo abcXdef\n"},
		{"tcsh backspace mid-line", []string{"# echo abcXdef\b\b\b", "\bdef\x1b[K\b\b\b", "\r\n"}, "# echo abcdef\n"},
		{"cr overwrites, as on screen", []string{"abcdef\rxy\r\n"}, "xycdef\n"},
		{"move right pads", []string{"ab\x1b[3Cx\r\n"}, "ab   x\n"},
		{"delete a character", []string{"abcd\b\b\x1b[P\r\n"}, "abd\n"},
		{"insert a blank", []string{"abd\b\x1b[@c\r\n"}, "abcd\n"},
		{"full-screen program, then a prompt", []string{"  32709 root ospfd\x1b[43;1Hroot@fw:~ # \r\n"}, "  32709 root ospfd\nroot@fw:~ # \n"},
		{"utf8 split across reads", []string{"\xe2\x9c", "\x93 ok\r\n"}, "✓ ok\n"},
		{"broken utf8, then text", []string{"\xe2X\r\n"}, "\uFFFDX\n"},
		{"colour after cr keeps a prompt's space", []string{"# \r\x1b[0m\n"}, "# \n"},
		{"cursor home with no column", []string{"\x1b[20G\x1b[Hroot# \r\n"}, "root# \n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cl Cleaner
			var got []byte
			for _, in := range c.in {
				got = append(got, cl.Feed([]byte(in))...)
			}
			if string(got) != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLongLineIsBounded(t *testing.T) {
	var c Cleaner
	for range 100 {
		c.Feed([]byte(strings.Repeat("\xfe", 4096))) // garbage, no newline
	}
	c.Feed([]byte("end"))
	if p := c.Partial(); utf8.RuneCountInString(p) > lineMax || !strings.HasSuffix(p, "end") {
		t.Errorf("partial line is %d characters, ending %q", utf8.RuneCountInString(p), p[len(p)-3:])
	}
}

// Line noise can hold any number: moves and inserts stay within a line's
// bounds rather than allocating what the number says.
func TestHugeCursorNumbersAreBounded(t *testing.T) {
	for _, in := range []string{"\x1b[1000000000Cx", "a\r\x1b[1000000000@", "\x1b[1000000000Gx", "\x1b[1;1000000000Hx"} {
		var c Cleaner
		c.Feed([]byte(in))
		if n := utf8.RuneCountInString(c.Partial()); n > lineMax+1 {
			t.Errorf("%q: line of %d characters", in, n)
		}
	}
}

func TestFlushShowsACharacterCutShort(t *testing.T) {
	var c Cleaner
	c.Feed([]byte("ok \xe2"))
	if got := string(c.Flush()); got != "ok \uFFFD\n" {
		t.Errorf("got %q", got)
	}
	if got := c.Feed([]byte("\x9c\x93\n")); strings.Contains(string(got), "✓") {
		t.Errorf("the next read joined the old character: %q", got)
	}
}

func TestFlushStartsTheNextLineAtTheLeft(t *testing.T) {
	var c Cleaner
	c.Feed([]byte("\x1b[20G"))
	c.Flush()
	c.Feed([]byte("root# "))
	if got := c.Partial(); got != "root# " {
		t.Errorf("got %q", got)
	}
}
