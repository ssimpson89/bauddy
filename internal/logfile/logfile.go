// Package logfile writes console output to a plain-text log: escape
// sequences removed, backspaces and carriage-return overwrites applied, so
// the file reads like what was on screen.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ssimpson89/bauddy/internal/termtext"
)

// Log is an open log file.
type Log struct {
	f    *os.File
	path string
	c    termtext.Cleaner
}

// Open appends to path, creating it if needed, and writes a header.
func Open(path, header string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // logs can hold configs and secrets
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, path: path}
	if err := l.Mark(header); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

// Path returns the file's path.
func (l *Log) Path() string { return l.path }

// Write logs raw device output. Complete lines are written immediately;
// a partial line (usually a prompt) waits for its newline or Close.
func (l *Log) Write(raw []byte) error {
	_, err := l.f.Write(l.c.Feed(raw))
	return err
}

// Mark writes a marker line, e.g. "--- 09:13:01 disconnected ---".
func (l *Log) Mark(msg string) error {
	out := l.c.Flush()
	out = append(out, fmt.Sprintf("--- %s %s ---\n", time.Now().Format("15:04:05"), msg)...)
	_, err := l.f.Write(out)
	return err
}

// Close writes any partial line and closes the file.
func (l *Log) Close() error {
	_, werr := l.f.Write(l.c.Flush())
	if err := l.f.Close(); err != nil {
		return err
	}
	return werr
}

// Resolve turns a --log value into an absolute file path. "" means the
// current directory. A directory, or a path with no extension (created if
// needed), gets a generated file name; anything else is the file itself.
func Resolve(arg, device string, now time.Time) (string, error) {
	if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		arg = filepath.Join(home, arg[2:])
	}
	if arg == "" {
		arg = "."
	}
	fi, err := os.Stat(arg)
	switch {
	case err == nil && fi.IsDir():
		arg = filepath.Join(arg, Name(device, now))
	case err != nil && filepath.Ext(arg) == "":
		if err := os.MkdirAll(arg, 0o700); err != nil {
			return "", err
		}
		arg = filepath.Join(arg, Name(device, now))
	}
	return filepath.Abs(arg)
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Name is the generated file name, e.g. bauddy-core-sw-01-20261010-091203.log.
func Name(device string, now time.Time) string {
	return fmt.Sprintf("bauddy-%s-%s.log", unsafeChars.ReplaceAllString(device, "-"), now.Format("20060102-150405"))
}
