package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ssimpson89/bauddy/internal/share"
)

// Shared is a console that other bauddy windows, or an AI tool through
// bauddy mcp, may also be on.
type Shared interface {
	Status() share.Status
	Notes() <-chan share.Note // a Note with no text: Status changed
	KeyPressed()
	StopAgent(interrupt bool) error
	TogglePause() error
}

const (
	ignoredFor   = 2 * time.Second // how long the AI tool's bar says your keys were ignored
	shareNoteFor = 6 * time.Second // notes about the AI tool and other windows: long enough to read
)

// shareMsg is a note from the console, or just a change to redraw for.
type shareMsg struct{ note share.Note }

// shareErrMsg is a sharing control that failed.
type shareErrMsg struct{ err error }

func (m *Model) waitShare() tea.Cmd {
	if m.shared == nil {
		return nil
	}
	ch := m.shared.Notes()
	return func() tea.Msg { return shareMsg{<-ch} }
}

func (m *Model) shareNote(n share.Note) tea.Cmd {
	m.rateChanged() // another window may have set the rate: a round trip still needs a new answer
	switch {
	case n.Mark: // the AI tool ran a command: mark it in history and the log
		m.rememberLines([]byte("── " + n.Text + " ──\n"))
		return m.logMark(n.Text)
	case n.Text != "":
		m.note = note{text: strings.ReplaceAll(n.Text, "{keys}", m.keys.Escape)}
		return m.clearNoteAfter(shareNoteFor)
	}
	return nil
}

// agent returns the connected AI tool's name, or "".
func (m *Model) agent() string {
	if m.shared == nil {
		return ""
	}
	return m.shared.Status().Agent
}

// detectingAnywhere reports whether baud detection is running, started in
// this window or another; keys can't reach the device meanwhile.
func (m *Model) detectingAnywhere() bool {
	return m.detecting || m.shared != nil && m.shared.Status().Detecting
}

// aiPaused reports whether the user has paused AI tools on this console. It
// can outlast the tool, so a tool that reconnects can't type until resumed.
func (m *Model) aiPaused() bool { return m.shared != nil && m.shared.Status().Paused }

// agentWorking reports whether an AI tool has the keyboard.
func (m *Model) agentWorking() bool { return m.shared != nil && m.shared.Status().Working }

// agentKey handles a key while an AI tool has the keyboard: esc stops it,
// ctrl+c stops it and interrupts the device, ctrl+] still opens the keys,
// and anything else is ignored so it can't land in the tool's command.
func (m *Model) agentKey(key string) (bool, tea.Cmd) {
	switch key {
	case m.keys.Escape:
		return false, nil
	case "esc", "ctrl+c":
		s, interrupt := m.shared, key == "ctrl+c"
		return true, func() tea.Msg {
			if err := s.StopAgent(interrupt); err != nil {
				return shareErrMsg{err}
			}
			return nil
		}
	}
	m.ignoredUntil = time.Now().Add(ignoredFor)
	return true, nil
}

func (m *Model) togglePause() tea.Cmd {
	if m.agent() == "" && !m.aiPaused() { // paused with none on: resuming must still work
		return m.setNote("no AI tool is using this console")
	}
	s := m.shared
	return func() tea.Msg {
		if err := s.TogglePause(); err != nil {
			return shareErrMsg{err}
		}
		return nil
	}
}
