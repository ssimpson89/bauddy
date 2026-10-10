package tui

import (
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ssimpson89/bauddy/internal/logfile"
)

// historyMax is how much earlier output a new log starts with, so turning
// logging on late still captures what already happened.
const historyMax = 1 << 20

// output shows device bytes and records them: in the log if one is open,
// otherwise in the history that the next log starts with.
func (m *Model) output(b []byte) tea.Cmd {
	_, _ = m.emu.Write(b)
	m.screen.Feed(b)
	if m.log == nil {
		m.history = append(m.history, b...)
		if over := len(m.history) - historyMax; over > 0 {
			m.history = m.history[over:]
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
	header := fmt.Sprintf("bauddy log: %s (%s) at %s, started %s",
		m.opt.Name, m.sess.Path(), m.lineSettings(), now.Format("2006-01-02"))
	l, err := logfile.Open(path, header)
	if err != nil {
		return "", err
	}
	if len(m.history) > 0 {
		_ = l.Write(m.history)
		_ = l.Mark("logging turned on; output above was captured before that")
		m.history = nil
	}
	m.log = l
	if len(m.logs) == 0 || m.logs[len(m.logs)-1] != path {
		m.logs = append(m.logs, path)
	}
	return "logging to " + filepath.Base(path), nil
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
