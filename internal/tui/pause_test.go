package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/logfile"
	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/share"
)

// The owner found pausing unintuitive: once paused, the bottom line has to
// say so and say how to resume, even after the AI tool has gone.
func TestPausedSaysHowToResume(t *testing.T) {
	for _, c := range []struct {
		what   string
		status share.Status
		w      int
		want   string
		not    string
	}{
		{"connected", share.Status{Agent: "claude"}, 80, "+claude", "paused"},
		{"paused", share.Status{Agent: "claude", Paused: true}, 80, "claude paused · ctrl+] p resumes", "+claude"},
		{"paused, narrow", share.Status{Agent: "claude", Paused: true}, 40, "paused · ^] p", ""},
		{"paused, tool gone", share.Status{Paused: true}, 80, "AI tool paused · ctrl+] p resumes", ""},
	} {
		m := unconfirmedModel(9600)
		m.shared = fakeShared(c.status)
		line := ansi.Strip(m.mainLine(c.w))
		if ansi.StringWidth(line) != c.w || !strings.Contains(line, c.want) || c.not != "" && strings.Contains(line, c.not) {
			t.Errorf("%s at %d: %q, want %q", c.what, c.w, line, c.want)
		}
	}
}

// Copilot's finds: a note (the tool leaving, an error) took the hint's
// place, and at 40 columns an unplugged or logging console lost it.
func TestPausedHintOutlastsTheRest(t *testing.T) {
	for _, c := range []struct {
		what  string
		setup func(m *Model)
		w     int
	}{
		{"with a note", func(m *Model) { m.note.text = "claude left · ctrl+] h reviews" }, 80},
		{"with a note, narrow", func(m *Model) { m.note.text = "claude left · ctrl+] h reviews" }, 40},
		{"unplugged, narrow", func(m *Model) { m.state = serialx.Disconnected }, 40},
		{"115200, logging with +CR, narrow", func(m *Model) { m.sess.(*ratePort).baud = 115200; m.addCR = true; m.log = &logfile.Log{} }, 40},
	} {
		m := unconfirmedModel(9600)
		m.shared = fakeShared{Paused: true}
		c.setup(m)
		line := ansi.Strip(m.mainLine(c.w))
		if ansi.StringWidth(line) != c.w || !strings.Contains(line, "] p") {
			t.Errorf("%s at %d: %q, want how to resume", c.what, c.w, line)
		}
	}
}

func TestPauseKeyFollowsTheState(t *testing.T) {
	for _, c := range []struct {
		status    share.Status
		want, not string
	}{
		{share.Status{}, "", "pause"},
		{share.Status{Agent: "claude"}, "pause claude", "resume"},
		{share.Status{Agent: "claude", Paused: true}, "resume claude", ""},
		{share.Status{Paused: true}, "resume AI", ""}, // the tool left; resuming must still be offered
	} {
		m := unconfirmedModel(9600)
		m.shared = fakeShared(c.status)
		bar := ansi.Strip(m.commandLine(120))
		if c.want != "" && !strings.Contains(bar, c.want) || c.not != "" && strings.Contains(bar, c.not) {
			t.Errorf("%+v: %q, want %q", c.status, bar, c.want)
		}
		m.togglePause()
		refused := strings.Contains(m.note.text, "no AI tool")
		if refused != (c.status.Agent == "" && !c.status.Paused) {
			t.Errorf("%+v: p should work exactly when a tool is on or it's paused (refused: %v)", c.status, refused)
		}
	}
}

func TestWorkingBarSaysWhatEachKeyStops(t *testing.T) {
	m := unconfirmedModel(9600)
	m.shared = fakeShared{Agent: "claude", Working: true}
	for w, want := range map[int][]string{
		80: {"claude is working", "esc stop it   ctrl+c stop + interrupt device"},
		60: {"claude is working", "esc stop  ctrl+c stop + interrupt"},
		45: {" working", "esc stop  ctrl+c stop + interrupt"}, // both keys outlast the name
	} {
		line := ansi.Strip(m.agentLine(w))
		for _, s := range want {
			if !strings.Contains(line, s) {
				t.Errorf("at %d: %q, want %q", w, line, s)
			}
		}
	}
}
