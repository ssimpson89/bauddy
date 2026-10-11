package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/ssimpson89/bauddy/internal/keymap"
	"github.com/ssimpson89/bauddy/internal/serialx"
)

func TestOutputFromDetectionCountsAsHeard(t *testing.T) {
	m := &Model{emu: vt.NewSafeEmulator(80, 24)}
	m.output([]byte("\r\nSwitch#")) // what detectDone shows
	m.Update(quietCheckMsg{})
	if !m.heard || m.quiet {
		t.Errorf("after showing detected output: heard %v, quiet %v; want heard and not quiet", m.heard, m.quiet)
	}
}

func detectingModel() *Model {
	return &Model{keys: keymap.New(keymap.DefaultEscapeKey), detecting: true, note: detectingNote}
}

func TestCommandsThatUseThePortWaitForDetection(t *testing.T) {
	m := detectingModel()
	for _, key := range []string{"b", "d", "r", "B", "S", keymap.DefaultEscapeKey} {
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != modeConsole || m.note.kind != noteSticky || !strings.Contains(m.note.text, key+" didn't run; try again") {
			t.Errorf("%s during detection: mode %v, note %q", key, m.mode, m.note.text)
		}
	}

	m.note = note{text: "can't write the log", kind: noteError}
	m.mode = modeCommand
	_, _ = m.commandKey(tea.KeyPressMsg{Text: "b"}, "b")
	if m.note.kind != noteError {
		t.Errorf("an unseen error was replaced by %q", m.note.text)
	}
}

func TestOtherCommandsWorkDuringDetection(t *testing.T) {
	for key, mode := range map[string]mode{"?": modeHelp, "h": modeHistory, "/": modeHistory} {
		m := detectingModel()
		m.mode = modeCommand
		_, _ = m.commandKey(tea.KeyPressMsg{Text: key}, key)
		if m.mode != mode {
			t.Errorf("%s during detection: mode %v, want %v", key, m.mode, mode)
		}
	}
	m := detectingModel()
	m.mode = modeCommand
	if _, cmd := m.commandKey(tea.KeyPressMsg{Text: "q"}, "q"); cmd == nil {
		t.Error("q during detection should quit")
	}
}

func TestDetectingNoteComesBack(t *testing.T) {
	m := detectingModel()
	m.receivePaste("show version\n") // refused while detecting, with a note that clears itself
	if m.note.kind != noteInfo {
		t.Fatalf("paste during detection: note %+v", m.note)
	}
	m.Update(noteClearMsg{m.noteID})
	if m.note != detectingNote {
		t.Errorf("after the paste note cleared: %q, want the detecting note back", m.note.text)
	}
}

func TestNothingHeardSaysWhatToDo(t *testing.T) {
	for mode, want := range map[mode]string{modeCommand: "; d tries again", modeHelp: "; ctrl+] d tries again"} {
		m := detectingModel()
		m.mode = mode // looking at the keys, or at help, when detection gave up
		m.detectDone(detectDoneMsg{err: serialx.ErrNoSignal})
		if !strings.Contains(m.note.text, want) {
			t.Errorf("mode %v: note %q should say %q", mode, m.note.text, want)
		}
	}

	m := detectingModel()
	m.mode = modeCommand
	m.note = note{text: "can't write the log", kind: noteError}
	m.detectDone(detectDoneMsg{err: serialx.ErrNoSignal})
	if m.note.kind != noteError {
		t.Errorf("an error from during detection was replaced by %q", m.note.text)
	}
	m.detecting = true
	m.detectDone(detectDoneMsg{err: errors.New("port gone")})
	if !strings.Contains(m.note.text, "can't write the log; can't detect the baud rate: port gone") {
		t.Errorf("both errors should show: %q", m.note.text)
	}
}
