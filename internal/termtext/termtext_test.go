package termtext

import (
	"strings"
	"testing"
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
	if p := c.Partial(); len(p) > lineMax || !strings.HasSuffix(p, "end") {
		t.Errorf("partial line is %d bytes, ending %q", len(p), p[len(p)-3:])
	}
}
