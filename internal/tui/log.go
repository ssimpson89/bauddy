package tui

import (
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ssimpson89/bauddy/internal/logfile"
)

// unloggedMax is how much earlier output a new log starts with, so turning
// logging on late still captures what already happened.
const unloggedMax = 1 << 20

// historyLinesMax is how many lines of earlier output history keeps.
const historyLinesMax = 10000

// output shows device bytes and records them: in the log if one is open,
// otherwise in the history that the next log starts with.
func (m *Model) output(b []byte) tea.Cmd {
	_, _ = m.emu.Write(b)
	m.screen.Feed(b)
	m.rememberLines(m.text.Feed(b))
	if m.log == nil {
		m.unlogged = append(m.unlogged, b...)
		if over := len(m.unlogged) - unloggedMax; over > 0 {
			m.unlogged = m.unlogged[over:]
		}
		return nil
	}
	if err := m.log.Write(b); err != nil {
		return m.logFailed(err)
	}
	return nil
}

// startLog opens a log, writes the history into it, and returns a message
// for the status line.
func (m *Model) startLog() (string, error) {
	now := time.Now()
	path, err := logfile.Resolve(m.opt.LogPath, m.opt.Name, now)
	if err != nil {
		return "", err
	}
	l, err := logfile.Open(path, "bauddy log started")
	if err != nil {
		return "", err
	}
	// Record enough context for a log to stand on its own. Keystrokes aren't
	// logged, only what the device sends back.
	for _, mark := range []string{
		"device: " + m.opt.Name,
		"port: " + m.sess.Path(),
		"line: " + m.lineSettings(),
		"started: " + now.Format(time.RFC3339),
	} {
		if err := l.Mark(mark); err != nil {
			_ = l.Close()
			m.log = nil
			return "", err
		}
	}
	if len(m.unlogged) > 0 {
		_ = l.Write(m.unlogged)
		_ = l.Mark("logging turned on; output above was captured before that")
		m.unlogged = nil
	}
	m.log = l
	if len(m.logs) == 0 || m.logs[len(m.logs)-1] != path {
		m.logs = append(m.logs, path)
	}
	return "logging to " + filepath.Base(path), nil
}

// rememberLines keeps completed lines of output, as plain text, for history.
func (m *Model) rememberLines(b []byte) {
	if len(b) == 0 {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		m.lines = append(m.lines, line)
		m.linesTotal++
	}
	if over := len(m.lines) - historyLinesMax; over > 0 {
		m.lines = m.lines[over:]
	}
}

func (m *Model) toggleLog() tea.Cmd {
	if m.log == nil {
		msg, err := m.startLog()
		if err != nil {
			return m.setError("can't start the log: " + err.Error())
		}
		return m.setNote(msg)
	}
	path := m.log.Path()
	err := m.log.Close()
	m.log = nil
	if err != nil {
		return m.setError("can't save the log: " + err.Error())
	}
	return m.setNote("log saved: " + filepath.Base(path))
}

// logMark writes a marker line to the log, if one is open.
func (m *Model) logMark(msg string) tea.Cmd {
	if m.log == nil {
		return nil
	}
	if err := m.log.Mark(msg); err != nil {
		return m.logFailed(err)
	}
	return nil
}

func (m *Model) logFailed(err error) tea.Cmd {
	_ = m.log.Close()
	m.log = nil
	return m.setError("can't write the log, so logging stopped: " + err.Error())
}

// Finish closes any open log and returns every log file written this session.
func (m *Model) Finish() []string {
	if m.log != nil {
		_ = m.log.Close()
		m.log = nil
	}
	return m.logs
}
