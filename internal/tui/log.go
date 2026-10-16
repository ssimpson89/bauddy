package tui

import (
	"fmt"
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
	if len(b) == 0 {
		return nil
	}
	m.heard, m.quiet = true, false
	m.noteAnswer(b)
	m.show(b)
	m.rememberLines(m.screen.Feed(b))
	learned := m.learnDevice()
	if m.log == nil {
		m.unlogged = append(m.unlogged, b...)
		if over := len(m.unlogged) - unloggedMax; over > 0 {
			m.unlogged = m.unlogged[over:]
		}
		return learned
	}
	if err := m.log.Write(b); err != nil {
		return tea.Batch(learned, m.logFailed(err))
	}
	return learned
}

// show puts device bytes on the screen, with a CR before each bare LF when
// the line feed fix (n) is on. Only the screen: logs and history keep what
// came.
func (m *Model) show(b []byte) {
	if m.addCR {
		_, _ = m.emu.Write(withCR(b, &m.lastCR))
		return
	}
	_, _ = m.emu.Write(b)
	m.lastCR = b[len(b)-1] == '\r'
}

// startLog opens a log, writes the history into it, and returns a message
// for the status line.
func (m *Model) startLog() (string, error) {
	now := time.Now()
	path, err := logfile.Resolve(m.opt.LogPath, m.label(), now)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("bauddy log: %s (%s) at %s, started %s",
		m.label(), m.sess.Path(), m.lineSettings(), now.Format("2006-01-02 15:04:05"))
	l, err := logfile.Open(path, header)
	if err != nil {
		return "", err
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
		m.hist.lines = append(m.hist.lines, line)
		m.hist.linesTotal++
	}
	if over := len(m.hist.lines) - historyLinesMax; over > 0 {
		m.hist.lines = m.hist.lines[over:]
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

// done reports something that worked: a marker in the log and a note on the
// status line. If the log can't be written, its error stays on the status
// line instead, so logging never stops unnoticed.
func (m *Model) done(note, mark string) tea.Cmd {
	logging := m.log != nil
	cmd := m.logMark(mark)
	if logging && m.log == nil {
		return cmd
	}
	return tea.Batch(cmd, m.setNote(note))
}

// failed reports something that went wrong, on the status line and in the
// log, and says if logging stopped along the way.
func (m *Model) failed(text string) tea.Cmd {
	logging := m.log != nil
	cmd := m.logMark(text)
	if logging && m.log == nil {
		text += "; logging stopped too"
	}
	return tea.Batch(cmd, m.setError(text))
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
