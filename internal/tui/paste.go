package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ssimpson89/bauddy/internal/paste"
)

// pasteJob is a paste in progress. The pacer runs in its own goroutine and
// reports on events, which is buffered for every message it can send, so
// the goroutine never blocks even after the UI stops listening.
type pasteJob struct {
	lines  []string
	next   int // line being sent, counting from 0
	cancel context.CancelFunc
	events chan tea.Msg
	done   chan struct{} // closed when the pacer has stopped writing
}

// confirmPaste is a risky paste waiting for enter or esc.
type confirmPaste struct {
	lines []string
	why   string
}

type (
	pasteProgressMsg struct {
		job *pasteJob
		i   int
	}
	pasteDoneMsg struct {
		job *pasteJob
		err error
	}
)

// receivePaste handles text pasted in console mode. A safe single line
// without Enter is typed straight away; anything else is paced, after asking
// if it's risky.
func (m *Model) receivePaste(text string) tea.Cmd {
	if m.detecting {
		return m.setNote("detecting the baud rate; paste again when it's done")
	}
	lines := paste.Lines(text)
	switch why := paste.Risky(lines); {
	case len(lines) == 0:
		return nil
	case why != "":
		m.mode, m.confirm = modePasteConfirm, &confirmPaste{lines, why}
		return nil
	case len(lines) == 1 && !strings.HasSuffix(lines[0], "\r"):
		m.emu.Paste(lines[0]) // no Enter: nothing to pace
		return nil
	}
	return m.startPaste(lines)
}

// confirmKey answers the risky-paste question: enter sends, l starts a log
// and sends, esc cancels.
func (m *Model) confirmKey(key string) (tea.Model, tea.Cmd) {
	c := m.confirm
	switch key {
	case "enter": // send as is
	case "l":
		if m.log == nil {
			if _, err := m.startLog(); err != nil {
				m.mode, m.confirm = modeConsole, nil
				return m, m.setError("can't start the log: " + err.Error())
			}
		}
	case "esc", m.keys.Escape:
		m.mode, m.confirm = modeConsole, nil
		return m, m.setNote("paste cancelled")
	default:
		return m, nil
	}
	m.mode, m.confirm = modeConsole, nil
	if len(c.lines) == 1 && !strings.HasSuffix(c.lines[0], "\r") {
		m.emu.Paste(c.lines[0]) // no Enter: nothing to pace
		return m, nil
	}
	return m, m.startPaste(c.lines)
}

func (m *Model) startPaste(lines []string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	rx, untap := m.sess.Tap()
	job := &pasteJob{lines: lines, cancel: cancel, events: make(chan tea.Msg, len(lines)+1), done: make(chan struct{})}
	p := &paste.Pacer{
		Write:  func(b []byte) error { _, err := m.sess.Write(b); return err },
		Rx:     rx,
		Baud:   m.sess.Baud(),
		Prompt: m.screen.Partial(),
	}
	go func() {
		defer untap()
		err := p.Run(ctx, lines, func(i int) { job.events <- pasteProgressMsg{job, i} })
		close(job.done)
		job.events <- pasteDoneMsg{job, err}
	}()
	m.paste = job
	return waitPaste(job)
}

// waitPaste delivers the pacer's next message; each handler re-arms it.
func waitPaste(job *pasteJob) tea.Cmd {
	return func() tea.Msg { return <-job.events }
}

func (m *Model) pasteProgress(msg pasteProgressMsg) tea.Cmd {
	if msg.job != m.paste {
		return nil
	}
	m.paste.next = msg.i
	return waitPaste(msg.job)
}

func (m *Model) pasteDone(msg pasteDoneMsg) tea.Cmd {
	if msg.job != m.paste {
		return nil // already stopped
	}
	m.paste = nil
	n := len(msg.job.lines)
	var se *paste.StopError
	switch {
	case msg.err == nil:
		return m.setNote(fmt.Sprintf("pasted %d lines", n))
	case errors.As(msg.err, &se):
		text := fmt.Sprintf("paste stopped at line %d of %d: %s", se.Line, n, se.Reason)
		if se.Unentered {
			text += "; ctrl+u clears the line"
		}
		return m.failed(text)
	default:
		return m.abandonPaste(msg.err.Error())
	}
}

// stopPaste stops the paste at the user's request.
func (m *Model) stopPaste() tea.Cmd {
	job := m.halt()
	text := fmt.Sprintf("paste stopped after %d of %d lines; a half-typed line may remain (ctrl+u clears it)", job.next, len(job.lines))
	logging := m.log != nil
	cmd := m.logMark(text)
	if !logging || m.log != nil { // if the log write failed, its error shows instead
		m.note = note{text: text, kind: noteSticky}
	}
	return cmd
}

// abandonPaste stops the paste because something went wrong.
func (m *Model) abandonPaste(why string) tea.Cmd {
	job := m.halt()
	text := fmt.Sprintf("paste stopped at line %d of %d: %s", job.next+1, len(job.lines), why)
	return m.failed(text)
}

// halt cancels the paste and waits up to a second for the pacer to stop
// writing. It doesn't wait forever because a write to a wedged port can
// block; in practice the pacer stops within one tick.
func (m *Model) halt() *pasteJob {
	job := m.paste
	m.paste = nil
	job.cancel()
	select {
	case <-job.done:
	case <-time.After(time.Second):
	}
	return job
}

// text is the question for a risky paste.
func (c *confirmPaste) text() string {
	q := fmt.Sprintf("paste %d lines?", len(c.lines))
	if len(c.lines) == 1 {
		q = "paste this line?"
	}
	if strings.HasPrefix(c.why, "line ") {
		q += " " + c.why
	}
	return q
}
