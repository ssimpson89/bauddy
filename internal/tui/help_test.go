package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/share"
)

// agentConsole is a shared console with an AI tool connected.
type agentConsole struct{}

func (agentConsole) Status() share.Status           { return share.Status{Connected: true, Agent: "claude"} }
func (agentConsole) Notes() <-chan share.Note       { return nil }
func (agentConsole) KeyPressed()                    {}
func (agentConsole) StopAgent(interrupt bool) error { return nil }
func (agentConsole) TogglePause() error             { return nil }

// Copilot's find: in 80×24 the help box was cropped, losing the fixes at
// the bottom. Help must fit there whole, with or without an AI tool, and
// in a smaller window say where the rest is.
func TestHelpFitsTheWindow(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		agent bool
		want  []string
	}{
		{80, 24, false, []string{"╭", "bauddy keys", "Detect the baud rate", "Send BREAK", "won't erase  [off]", "further right      [off]", "╰"}},
		{80, 24, true, []string{"╭", "claude is connected", "Pause or resume the AI tool", "won't erase  [off]", "further right      [off]", "╰"}},
		{60, 12, false, []string{"bauddy keys", "More: quit and run bauddy keys"}}, // too short
		{60, 24, false, []string{"bauddy keys", "More: quit and run bauddy keys"}}, // too narrow, though every line fits
	} {
		t.Run(fmt.Sprintf("%dx%d agent=%v", c.w, c.h, c.agent), func(t *testing.T) {
			m := unconfirmedModel(9600)
			m.width, m.height, m.mode = c.w, c.h, modeHelp
			if c.agent {
				m.shared = agentConsole{}
			}
			s := ansi.Strip(m.View().Content)
			lines := strings.Split(s, "\n")
			if len(lines) != c.h {
				t.Errorf("%d rows, want %d", len(lines), c.h)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > c.w {
					t.Errorf("row %d is %d wide, want at most %d: %q", i, w, c.w, l)
				}
			}
			for _, want := range c.want {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q:\n%s", want, s)
				}
			}
			if strings.Contains(s, "Show every key") || strings.Contains(s, "Back to the console") {
				t.Errorf("?, q and esc are on the status line, not in help:\n%s", s)
			}
		})
	}
}
