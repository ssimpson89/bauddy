package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/ssimpson89/bauddy/internal/keymap"
)

func TestOutputFromDetectionCountsAsHeard(t *testing.T) {
	m := &Model{emu: vt.NewSafeEmulator(80, 24)}
	m.output([]byte("\r\nSwitch#")) // what detectDone shows
	m.Update(quietCheckMsg{})
	if !m.heard || m.quiet {
		t.Errorf("after showing detected output: heard %v, quiet %v; want heard and not quiet", m.heard, m.quiet)
	}
}

func TestCommandsThatUseThePortWaitForDetection(t *testing.T) {
	m := &Model{keys: keymap.New(keymap.DefaultEscapeKey), detecting: true, note: note{text: "detecting baud…", kind: noteSticky}}
	for _, key := range []string{"b", "d", "r", "B", "S", keymap.DefaultEscapeKey} {
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != modeConsole || m.note.kind != noteSticky || !strings.Contains(m.note.text, "detecting") {
			t.Errorf("%s during detection: mode %v, note %q", key, m.mode, m.note.text)
		}
	}
}
