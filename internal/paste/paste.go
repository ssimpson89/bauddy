// Package paste sends multi-line pastes to a serial device without
// overrunning it. Each line is typed a few characters ahead of the echo, and
// Enter is pressed only when the device shows the whole line correctly. The
// pacer then waits for the prompt (or for quiet, if no prompt is known) and
// stops at the first error or question.
package paste

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ssimpson89/bauddy/internal/termtext"
)

// Lines splits pasted text into lines to send. Each ends in \r (Enter),
// except a final line with no newline, which is left at the prompt. Tabs
// become spaces, because a CLI treats Tab as "complete this word".
func Lines(text string) []string {
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", " ").Replace(text)
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		body := strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1 // other control characters would act, not type
			}
			return r
		}, l)
		if strings.HasSuffix(l, "\n") {
			body += "\r"
		}
		lines[i] = body
	}
	return lines
}

var dangerous = regexp.MustCompile(`(?i)^\s*(reload|write erase|erase|format|delete|factory-?reset|` +
	`request system (reboot|halt|power-off|zeroize)|load (override|factory-default)|configure replace|` +
	`clear config|crypto key zeroize|copy\s+\S+\s+start|boot system|execute (factory-?reset|reboot)|` +
	`/system (reset-configuration|reboot)|reboot|poweroff|halt|shutdown -[hr]|init [06]|rm -rf)\b`)

// Risky reports why a paste deserves a confirmation, or "" if it doesn't:
// a dangerous command first, then a ?, then sheer length.
func Risky(lines []string) string {
	for i, l := range lines {
		if dangerous.MatchString(l) {
			return fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(l))
		}
	}
	for i, l := range lines {
		if strings.Contains(l, "?") {
			return fmt.Sprintf("line %d has a ?, which Cisco treats as a request for help", i+1)
		}
	}
	if len(lines) >= 10 {
		return fmt.Sprintf("%d lines", len(lines))
	}
	return ""
}

// StopError explains why a paste stopped early.
type StopError struct {
	Line      int    // 1-based line that triggered the stop
	Text      string // the line that was sent
	Reason    string // what the device said, or why bauddy stopped
	Unentered bool   // the line was typed but Enter wasn't pressed: it's still at the prompt
}

func (e *StopError) Error() string {
	return fmt.Sprintf("line %d (%s): %s", e.Line, e.Text, e.Reason)
}

func stop(format string, a ...any) error { return &StopError{Reason: fmt.Sprintf(format, a...)} }

var (
	// devErr matches error messages from common CLIs.
	devErr = regexp.MustCompile(`(?i)^\s*%\s*(invalid|incomplete|ambiguous|unknown|error|bad)|^\s*invalid input|` +
		`syntax error|unknown command|command not found|command fail|unknown action|command parse error|` +
		`bad command name|no such item|^\s*failure:|^\s*error:`)
	// question matches prompts that would take the next pasted line as an answer.
	question = regexp.MustCompile(`(?i)\[confirm\]|\[y(es)?/n(o)?\]|\(y(es)?/n(o)?\)|\[yes,\s*no\]|` +
		`-+\s*more\s*-+|---\(more.*\)---|-- \[q quit|\[[^\]]*\]\?\s*$|\?\s*\[[^\]]*\]\s*$|press any key`)
	// promptChanger matches commands after which the prompt starts differently.
	promptChanger = regexp.MustCompile(`^\s*(hostname|su|sudo|exit|logout|ssh|telnet)\b`)
	// login matches username and password prompts, which count as ready.
	login    = regexp.MustCompile(`(?i)(username|login|password|passphrase)\s*:\s*$`)
	password = regexp.MustCompile(`(?i)(password|passphrase)\s*:\s*$`)
	// textInput matches devices announcing free-text input (IOS and EOS
	// banners, Junos load terminal), where no prompt appears between lines.
	textInput = regexp.MustCompile(`End with the character '(.+)'|Type '(\w+)' on its own line|\[Type \^D at a new line to end input\]`)
)

const (
	window     = 8                      // characters typed ahead of the echo: half a 16-byte UART FIFO
	echoWait   = 2 * time.Second        // no echo progress for this long: stop without pressing Enter
	quietWait  = 300 * time.Millisecond // no prompt known: this much silence means ready
	silentWait = 1 * time.Second        // no prompt known and no reply at all: move on
	textWait   = 150 * time.Millisecond // free-text input: silence between lines
	promptWait = 60 * time.Second       // a prompt is expected but hasn't come back: stop
	tick       = 5 * time.Millisecond
)

// Pacer sends lines to a device.
type Pacer struct {
	Write  func([]byte) error // sends bytes to the device
	Rx     <-chan []byte      // device output; open the tap before Run so no echo is missed
	Baud   int                // line rate, for timing
	Prompt string             // the device's current line when the paste starts
}

// Run sends lines, calling progress(i) before line i (counting from 0). It
// returns nil when every line was sent, a *StopError if the device objected
// or asked a question, ctx's error if stopped, or Write's error.
func (p *Pacer) Run(ctx context.Context, lines []string, progress func(int)) error {
	r := &run{Pacer: p}
	r.screen.Feed([]byte(p.Prompt))
	r.learn(r.screen.Partial())
	for i, line := range lines {
		progress(i)
		if err := r.line(ctx, line); err != nil {
			var se *StopError
			if errors.As(err, &se) {
				se.Line, se.Text = i+1, strings.TrimSuffix(line, "\r")
			}
			return err
		}
	}
	return nil
}

type run struct {
	*Pacer
	screen      termtext.Cleaner
	promptKnown bool   // this device shows prompts
	host        string // leading text of the prompt, e.g. "Switch" or "user@sw1"
	textEnd     string // while the device takes free text, the text that ends it
}

// lineSend is the progress of one line.
type lineSend struct {
	body, want string // the text to type, and its lower-case form for matching the screen
	enter      bool
	start      string // the cursor line before typing, usually the prompt
	blind      bool   // at a password prompt: expect no echo
	sent       int    // bytes of body written
	matched    int    // bytes of want the device shows
	lastEcho   time.Time
	entered    bool
	newLine    bool // a line ended since Enter
	atPrompt   bool
}

func (r *run) line(ctx context.Context, line string) error {
	body := strings.TrimSuffix(line, "\r")
	start := r.screen.Partial()
	ls := &lineSend{
		body: body, want: strings.ToLower(body), enter: len(body) < len(line),
		start: start, blind: password.MatchString(start), lastEcho: time.Now(),
	}
	if r.textEnd != "" && strings.Contains(body, r.textEnd) {
		r.textEnd = "" // this line ends the free text; expect a prompt again
	}
	if promptChanger.MatchString(ls.want) {
		r.host = "" // the prompt is about to change: new host name or user
	}
	if err := r.typeLine(ctx, ls); err != nil || !ls.enter {
		return err
	}
	return r.awaitPrompt(ctx, ls)
}

// typeLine types ls ahead of its echo, then presses Enter once the device
// shows the whole line.
func (r *run) typeLine(ctx context.Context, ls *lineSend) error {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		end := ls.sent
		for end < len(ls.body) && (ls.blind || end-ls.matched < window) { // blind: no echo to wait for
			end++
		}
		if end > ls.sent {
			if err := r.send(ctx, ls.body[ls.sent:end]); err != nil {
				return err
			}
			ls.sent = end
		}
		if ls.sent == len(ls.body) && (ls.blind || ls.matched == len(ls.want)) {
			if !ls.enter {
				return nil
			}
			ls.entered = true
			return r.send(ctx, "\r")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b := <-r.Rx:
			if err := r.recv(b, ls); err != nil {
				return err
			}
		case now := <-t.C:
			if now.Sub(ls.lastEcho) < echoWait {
				continue
			}
			reason := "the device didn't echo the whole line, so Enter wasn't pressed (characters may have been dropped)"
			if ls.matched == 0 {
				reason = "the device isn't echoing, so Enter wasn't pressed"
			}
			return &StopError{Reason: reason, Unentered: true}
		}
	}
}

// send writes s unless the paste has been stopped.
func (r *run) send(ctx context.Context, s string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.Write([]byte(s))
}

// track updates ls.matched from what the device's cursor line shows now.
// The typed text visible there is a stretch of the line ending at the echo
// point: all of it after the prompt, the tail after IOS's "$" scroll marker,
// or what was typed since a syslog message interrupted the echo. Reading the
// line itself, rather than counting echoed characters, means redraws and
// unrelated output can't make the echo look further along than it is, and a
// dropped character stalls it.
func (r *run) track(ls *lineSend) {
	t := strings.ToLower(r.screen.Partial())
	if start := strings.ToLower(ls.start); start != "" && strings.HasPrefix(t, start) {
		t = strings.TrimPrefix(t[len(start):], "$")
	}
	if t == "" {
		return
	}
	// Find the first end point b, no earlier than the echo so far, where the
	// line typed up to b ends with what the screen shows. Never moving
	// backwards means a redraw can't undo progress.
	for b := max(ls.matched, len(t)); b <= len(ls.want); b++ {
		if ls.want[b-len(t):b] == t {
			if b > ls.matched {
				ls.matched, ls.lastEcho = b, time.Now()
			}
			return
		}
	}
}

// awaitPrompt waits after Enter until the device is ready for the next line.
func (r *run) awaitPrompt(ctx context.Context, ls *lineSend) error {
	settle := 30*time.Millisecond + 4*time.Second*10/time.Duration(max(r.Baud, 1200)) // 4 byte-times of 10 bits
	enteredAt := time.Now()
	lastRx, replied := enteredAt, false
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b := <-r.Rx:
			lastRx, replied = time.Now(), true
			if err := r.recv(b, ls); err != nil {
				return err
			}
		case now := <-t.C:
			idle := now.Sub(lastRx)
			if idle < settle {
				continue
			}
			partial := r.screen.Partial()
			switch { // a question beats a prompt; timeouts come last
			case question.MatchString(partial):
				return stop("device is asking: %s", strings.TrimSpace(partial))
			case ls.atPrompt:
				return nil
			case r.textEnd != "" && idle >= textWait:
				return nil
			case r.promptKnown:
				if now.Sub(enteredAt) >= promptWait {
					return stop("no prompt after %s", promptWait)
				}
			case replied && idle >= quietWait, !replied && now.Sub(enteredAt) >= silentWait:
				return nil
			}
		}
	}
}

// recv processes device output: echo progress, errors, free-text mode and
// whether the prompt is back.
func (r *run) recv(b []byte, ls *lineSend) error {
	done := r.screen.Feed(b)
	if !ls.entered {
		r.track(ls)
	}
	for _, l := range strings.Split(strings.TrimSuffix(string(done), "\n"), "\n") {
		if l == "" {
			continue
		}
		if ls.entered {
			ls.newLine = true
		}
		if ls.start != "" && strings.HasPrefix(l, ls.start) {
			continue // our own echoed line, which may contain anything
		}
		if devErr.MatchString(l) {
			if !ls.entered {
				return stop("after the previous line: %s", strings.TrimSpace(l))
			}
			return stop("%s", strings.TrimSpace(l))
		}
		if m := textInput.FindStringSubmatch(l); m != nil {
			r.textEnd = m[1] + m[2]
			if r.textEnd == "" {
				r.textEnd = "\x04" // Junos: Ctrl-D on its own line ends the input
			}
		}
	}
	if !ls.entered {
		return nil
	}
	switch partial := r.screen.Partial(); {
	case ls.newLine && r.isPrompt(partial):
		ls.atPrompt = true
		r.learn(partial)
	case partial != "":
		ls.atPrompt = false
	}
	return nil
}

// isPrompt reports whether s, the cursor line, is the device waiting for
// input: a login or password prompt, or a short line ending in # > $ or %
// that starts like the prompts seen so far (so "45%" progress doesn't count).
func (r *run) isPrompt(s string) bool {
	s = strings.TrimRight(s, " ")
	if login.MatchString(s) {
		return true
	}
	if s == "" || len(s) > 120 || !strings.ContainsAny(s[len(s)-1:], "#>$%") {
		return false
	}
	return r.host == "" || strings.HasPrefix(s, r.host)
}

// learn records a prompt's shape, e.g. "Switch(config-if)#" -> "Switch".
func (r *run) learn(s string) {
	if !r.isPrompt(s) || login.MatchString(s) {
		return
	}
	r.promptKnown = true
	if i := strings.IndexAny(s, "(:#>$% "); i >= 0 {
		r.host = s[:i]
	}
}
