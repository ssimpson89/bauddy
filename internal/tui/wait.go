package tui

import (
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

// ErrQuit means you quit while bauddy was waiting for a cable.
var ErrQuit = errors.New("quit while waiting for a cable")

// waitPoll is how often the waiting screen looks for a port.
const waitPoll = 500 * time.Millisecond

// WaitForCable shows hint above the status bar until a serial port
// appears, then returns the ports. q or ctrl+c gives up with ErrQuit. Keys
// typed while waiting are dropped, so nothing reaches the device later. If
// listing ports fails, it keeps trying.
func WaitForCable(hint string) ([]serialx.PortInfo, error) {
	m := &waitModel{hint: hint, list: serialx.List}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		return nil, err
	}
	if m.ports == nil {
		return nil, ErrQuit
	}
	return m.ports, nil
}

type waitModel struct {
	hint          string
	list          func() ([]serialx.PortInfo, error)
	width, height int
	seen          bool // ports were there at the last look; go on if they still are
	ports         []serialx.PortInfo
}

type waitTick struct{}

func (m *waitModel) Init() tea.Cmd { return m.tick() }

func (m *waitModel) tick() tea.Cmd {
	return tea.Tick(waitPoll, func(time.Time) tea.Msg { return waitTick{} })
}

func (m *waitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if k := msg.String(); k == "q" || k == "ctrl+c" {
			return m, tea.Quit
		}
	case waitTick:
		ports, err := m.list()
		if err == nil && len(ports) > 0 && m.seen {
			m.ports = ports
			return m, tea.Quit
		}
		// Look once more before going on: a multi-port adapter's ports
		// appear one after another, and all of them should be offered.
		m.seen = err == nil && len(ports) > 0
		return m, m.tick()
	}
	return m, nil
}

func (m *waitModel) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
	}
	body := strings.Split(m.hint, "\n")
	for i := range body {
		body[i] = ansi.Truncate(body[i], m.width, "") // so the bar stays on screen
	}
	for len(body) < max(1, m.height-1) {
		body = append(body, "")
	}
	v := tea.NewView(strings.Join(body[:max(1, m.height-1)], "\n") + "\n" + m.bar(m.width))
	v.AltScreen = true
	return v
}

// bar is the status line: what bauddy is doing, and the one key that
// matters. It's the normal bar, not red: plugging in after starting bauddy
// is a normal first step.
func (m *waitModel) bar(w int) string {
	quit := []seg{{"q", cAccent, true}, {" quit ", cText, false}}
	return fitLine(cBar, w, []layout{
		{[]seg{{" ○ waiting for a USB console cable…", cText, false}}, quit},
		{[]seg{{" ○ waiting…", cText, false}}, quit},
	})
}
