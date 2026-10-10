package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	l, err := Open(path, "bauddy log")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Write([]byte("hello\r\nRouter#")); err != nil {
		t.Fatal(err)
	}
	if err := l.Mark("disconnected"); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || lines[1] != "hello" || lines[2] != "Router#" || !strings.HasSuffix(lines[3], "disconnected ---") {
		t.Errorf("unexpected log:\n%s", b)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 10, 9, 12, 3, 0, time.UTC)
	got, _ := Resolve(dir, "core sw/01", now)
	if want := filepath.Join(dir, "bauddy-core-sw-01-20261010-091203.log"); got != want {
		t.Errorf("dir: got %q, want %q", got, want)
	}
	newDir := filepath.Join(dir, "console-logs")
	if got, _ := Resolve(newDir, "x", now); filepath.Dir(got) != newDir {
		t.Errorf("new dir: got %q, want a file inside %q", got, newDir)
	}
	file := filepath.Join(dir, "mine.log")
	if got, _ := Resolve(file, "x", now); got != file {
		t.Errorf("file: got %q, want %q", got, file)
	}
}
