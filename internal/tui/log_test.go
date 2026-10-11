package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssimpson89/bauddy/internal/logfile"
)

// brokenLog is an open log whose next write fails, like a full disk.
func brokenLog(t *testing.T) *logfile.Log {
	t.Helper()
	l, err := logfile.Open(filepath.Join(t.TempDir(), "x.log"), "header")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	return l
}

func TestDoneKeepsALogFailureVisible(t *testing.T) {
	m := &Model{}
	m.done("break sent", "sent BREAK")
	if m.note.text != "break sent" {
		t.Errorf("without a log, done should show its note: %q", m.note.text)
	}

	m = &Model{log: brokenLog(t)}
	m.done("break sent", "sent BREAK")
	if m.note.kind != noteError || !strings.Contains(m.note.text, "logging stopped") || m.log != nil {
		t.Errorf("a failed log write should stop logging and say so, got %q", m.note.text)
	}
}

func TestFailedSaysWhenLoggingStopped(t *testing.T) {
	m := &Model{}
	m.failed("paste stopped at line 3 of 9: % Invalid input")
	if m.note.kind != noteError || strings.Contains(m.note.text, "logging") {
		t.Errorf("without a log: %q", m.note.text)
	}

	m = &Model{log: brokenLog(t)}
	m.failed("paste stopped at line 3 of 9: % Invalid input")
	if !strings.HasSuffix(m.note.text, "; logging stopped too") {
		t.Errorf("should say logging stopped too: %q", m.note.text)
	}
}
