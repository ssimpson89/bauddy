package cli

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestFriendlyOpenError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing", &fs.PathError{Op: "open", Path: "/dev/ttyUSB9", Err: fs.ErrNotExist}, "doesn't exist; 'bauddy list'"},
		{"other", errors.New("boom"), "can't open /dev/ttyUSB9: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := friendlyOpenError("/dev/ttyUSB9", c.err).Error(); !strings.Contains(got, c.want) {
				t.Errorf("got %q, want it to contain %q", got, c.want)
			}
		})
	}
}

func TestEscapeKey(t *testing.T) {
	t.Setenv("BAUDDY_ESCAPE", "ctrl+t")
	if got := escapeKey(""); got != "ctrl+t" {
		t.Errorf("env: got %q, want ctrl+t", got)
	}
	if got := escapeKey("ctrl+a"); got != "ctrl+a" {
		t.Errorf("flag beats env: got %q, want ctrl+a", got)
	}
	t.Setenv("BAUDDY_ESCAPE", "")
	if got := escapeKey(""); got != "ctrl+]" {
		t.Errorf("default: got %q, want ctrl+]", got)
	}
}
