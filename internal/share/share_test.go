package share

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ssimpson89/bauddy/internal/serialx"
)

// fakePort is a serial port that records what's written to it.
type fakePort struct {
	events chan serialx.Event
	mu     sync.Mutex
	wrote  []byte
	baud   int
	quiet  bool // detection hears nothing
	once   sync.Once
}

func newFakePort() *fakePort { return &fakePort{events: make(chan serialx.Event, 64), baud: 9600} }

func (p *fakePort) Events() <-chan serialx.Event { return p.events }
func (p *fakePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.wrote = append(p.wrote, b...)
	p.mu.Unlock()
	return len(b), nil
}
func (p *fakePort) written() string           { p.mu.Lock(); defer p.mu.Unlock(); return string(p.wrote) }
func (p *fakePort) Baud() int                 { p.mu.Lock(); defer p.mu.Unlock(); return p.baud }
func (p *fakePort) Framing() serialx.Framing  { return serialx.Framing8N1 }
func (p *fakePort) SetBaud(b int) error       { p.mu.Lock(); p.baud = b; p.mu.Unlock(); return nil }
func (p *fakePort) Break(time.Duration) error { return nil }
func (p *fakePort) Reconnect()                {}
func (p *fakePort) say(s string)              { p.events <- serialx.Event{Data: []byte(s)} }
func (p *fakePort) Close() error              { p.once.Do(func() { close(p.events) }); return nil }
func (p *fakePort) Detect(serialx.DetectConfig) (serialx.Result, error) {
	p.mu.Lock()
	quiet := p.quiet
	p.mu.Unlock()
	if quiet {
		return serialx.Result{}, serialx.ErrNoSignal
	}
	p.SetBaud(115200)
	return serialx.Result{Baud: 115200, Framing: serialx.Framing8N1, Heard: []byte("heard\r\n")}, nil
}

// console is one shared console for a test: every Open gets the same
// options, and each port opened is recorded.
type console struct {
	t     *testing.T
	key   string
	mu    sync.Mutex
	ports []*fakePort
}

func newConsole(t *testing.T) *console {
	d, err := os.MkdirTemp("", "bd") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("XDG_RUNTIME_DIR", d)
	return &console{t: t, key: t.Name()}
}

func (k *console) open(agent bool) *Client {
	k.t.Helper()
	c, err := Open(Options{
		Key: k.key, Path: "/dev/ttyTEST", Baud: 9600, Agent: agent, Name: map[bool]string{true: "Claude Code"}[agent],
		Replay: !agent,
		Open: func(_ string, baud int, _ serialx.Framing) (Port, error) {
			p := newFakePort()
			p.baud = baud
			k.mu.Lock()
			k.ports = append(k.ports, p)
			k.mu.Unlock()
			return p, nil
		},
	})
	if err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { c.Close() })
	return c
}

func (k *console) port(i int) *fakePort {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.ports[i]
}

// eventually waits for cond, failing after a while.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// output collects a client's output until it contains want.
func output(t *testing.T, c *Client, want string) (got string, replay bool) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for !strings.Contains(got, want) {
		select {
		case e := <-c.Events():
			got += string(e.Data)
			replay = replay || e.Replay
		case <-timeout:
			t.Fatalf("output %q never contained %q", got, want)
		}
	}
	return got, replay
}

func TestWindowsShareOnePort(t *testing.T) {
	k := newConsole(t)
	a := k.open(false)
	if a.Joined() {
		t.Error("the first window should hold the port, not join")
	}
	k.port(0).say("earlier line\r\nSwitch#")
	output(t, a, "Switch#")

	b := k.open(false)
	if !b.Joined() || len(k.ports) != 1 {
		t.Fatalf("the second window should join: joined %v, ports opened %d", b.Joined(), len(k.ports))
	}
	if got, replay := output(t, b, "Switch#"); !replay || !strings.Contains(got, "earlier line") {
		t.Errorf("a joining window should get the recent output, marked as replay: %q", got)
	}
	eventually(t, "both windows counted", func() bool { return a.Status().Windows == 2 && b.Status().Windows == 2 })

	k.port(0).say("live\r\n")
	output(t, a, "live")
	output(t, b, "live")
	a.Write([]byte("a"))
	b.Write([]byte("b"))
	eventually(t, "both windows' keys sent", func() bool { w := k.port(0).written(); return strings.Contains(w, "a") && strings.Contains(w, "b") })

	if err := b.SetBaud(19200); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new baud everywhere", func() bool { return a.Baud() == 19200 && b.Baud() == 19200 })
}

// Windows waiting for a cable all see it at once and open it together.
func TestWindowsOpeningAtOnceShareOnePort(t *testing.T) {
	k := newConsole(t)
	const n = 4
	clients, errs := make([]*Client, n), make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			clients[i], errs[i] = Open(Options{Key: k.key, Path: "/dev/ttyTEST", Baud: 9600, Replay: true,
				Open: func(string, int, serialx.Framing) (Port, error) {
					p := newFakePort()
					k.mu.Lock()
					k.ports = append(k.ports, p)
					k.mu.Unlock()
					return p, nil
				}})
		})
	}
	wg.Wait()
	for _, c := range clients {
		if c != nil {
			t.Cleanup(func() { c.Close() })
		}
	}
	holders := 0
	for i, c := range clients {
		if errs[i] != nil {
			t.Fatalf("window %d: %v", i, errs[i])
		}
		if !c.Joined() {
			holders++
		}
	}
	k.mu.Lock()
	opened := len(k.ports)
	k.mu.Unlock()
	if holders != 1 || opened != 1 {
		t.Fatalf("want one window holding one port: %d holding, %d ports opened", holders, opened)
	}
	eventually(t, "every window counted", func() bool { return clients[0].Status().Windows == n })
}

func TestAnAIToolSeesNothingFromBeforeItJoined(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	k.port(0).say("Username: admin\r\nPassword:\r\nSwitch#")
	output(t, w, "Switch#")
	ai := k.open(true)
	got, _ := output(t, ai, "Switch#")
	if strings.Contains(got, "admin") || got != "Switch#" {
		t.Errorf("an AI tool should get only the current line: %q", got)
	}
	eventually(t, "the AI tool shown", func() bool { return w.Status().Agent == "Claude Code" })
	if second, err := Open(Options{Key: k.key, Path: "/dev/ttyTEST", Agent: true, Name: "Codex"}); err == nil {
		second.Close()
		t.Error("a second AI tool should be refused")
	}
}

func TestTheAIToolHasTheKeyboard(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	run, why, release, err := ai.Lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "working shown", func() bool { return w.Status().Working })
	w.Write([]byte("x")) // ignored while the tool types
	ai.Write([]byte("show int"))
	eventually(t, "the tool's typing", func() bool { return k.port(0).written() == "show int" })
	time.Sleep(20 * time.Millisecond)
	if got := k.port(0).written(); got != "show int" {
		t.Errorf("the window's key reached the device: %q", got)
	}

	if err := w.StopAgent(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.Done():
		if why() != StopCleared {
			t.Errorf("stopped because %q, want cleared", why())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the tool wasn't told it was stopped")
	}
	if got := k.port(0).written(); got != "show int"+strings.Repeat("\b", 8) {
		t.Errorf("its unsent command should be erased: %q", got)
	}
	ai.Write([]byte("reload\r")) // after the stop: dropped
	release()
	time.Sleep(20 * time.Millisecond)
	if strings.Contains(k.port(0).written(), "reload") {
		t.Error("the tool typed after it was stopped")
	}
	if _, _, _, err := ai.Lease(context.Background()); !errors.Is(err, ErrPaused) {
		t.Errorf("a paused tool should be refused, got %v", err)
	}
	if err := w.TogglePause(); err != nil {
		t.Fatal(err)
	}
	if _, _, release, err := ai.Lease(context.Background()); err != nil {
		t.Errorf("after resuming: %v", err)
	} else {
		release()
	}
}

func TestStoppingAfterEnterAndInterrupting(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	run, why, _, _ := ai.Lease(context.Background())
	ai.Write([]byte("debug all\r"))
	eventually(t, "the command", func() bool { return k.port(0).written() == "debug all\r" })
	w.StopAgent(true)
	<-run.Done()
	if why() != StopInterrupted {
		t.Errorf("stopped because %q", why())
	}
	if got := k.port(0).written(); got != "debug all\r\x03" {
		t.Errorf("ctrl+c should follow, with nothing erased: %q", got)
	}
	timeout := time.After(time.Second)
	for {
		select {
		case n := <-w.Notes():
			if n.Text == "Claude Code typed: debug all" {
				if !n.Mark {
					t.Error("the command should be marked for history")
				}
				return
			}
		case <-timeout:
			t.Fatal("no history mark for the tool's command")
		}
	}
}

func TestTheToolWaitsForTheUser(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	w.KeyPressed()
	time.Sleep(20 * time.Millisecond)
	began := time.Now()
	_, _, release, err := ai.Lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if waited := time.Since(began); waited < 2*time.Second {
		t.Errorf("took the keyboard %s after the user's key", waited)
	}
}

func TestAnotherWindowTakesOverWhenTheHolderQuits(t *testing.T) {
	k := newConsole(t)
	a := k.open(false)
	b := k.open(false)
	a.Close()
	eventually(t, "b holding the port", func() bool { k.mu.Lock(); defer k.mu.Unlock(); return len(k.ports) == 2 })
	// After its first connect, b sees a disconnect, then a reconnect.
	var states []serialx.State
	timeout := time.After(3 * time.Second)
	for n := len(states); n < 2 || states[n-2] != serialx.Disconnected || states[n-1] != serialx.Connected; n = len(states) {
		select {
		case e := <-b.Events():
			if len(e.Data) == 0 {
				states = append(states, e.State)
			}
		case <-timeout:
			t.Fatalf("states %v", states)
		}
	}
	b.Write([]byte("hello"))
	eventually(t, "b's keys on the new port", func() bool { return k.port(1).written() == "hello" })
}

func TestDetectionIsShared(t *testing.T) {
	k := newConsole(t)
	a := k.open(false)
	b := k.open(false)
	r, err := b.Detect(serialx.DetectConfig{})
	if err != nil || r.Baud != 115200 || len(r.Heard) != 0 {
		t.Fatalf("detect: %+v, %v", r, err)
	}
	output(t, a, "heard")
	output(t, b, "heard")
	eventually(t, "the new baud everywhere", func() bool { return a.Baud() == 115200 })
}

func TestReplayStartsAtALine(t *testing.T) {
	h := &hub{ring: append(bytes.Repeat([]byte("x"), replayMax-10), []byte("cut\r\nwhole\r\n#")...)}
	h.ring = h.ring[len(h.ring)-replayMax:]
	if got := string(h.replay(false, true)); got != "whole\r\n#" {
		t.Errorf("replay = %q", got)
	}
	if got := string(h.replay(true, true)); got != "#" {
		t.Errorf("an AI tool's replay = %q", got)
	}
}

// The user can stop the tool the instant it's given the keyboard.
func TestAStopRightAfterTheLeaseIsntLost(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	for i := range 200 {
		if i > 0 { // resume after the last stop
			if err := w.TogglePause(); err != nil {
				t.Fatal(err)
			}
		}
		eventually(t, "resumed", func() bool { return !ai.Status().Paused })
		run, why, release, err := ai.Lease(context.Background())
		if err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		w.StopAgent(false)
		select {
		case <-run.Done():
		case <-time.After(3 * time.Second):
			t.Fatalf("stop %d was lost", i)
		}
		if why() == "" {
			t.Fatalf("stop %d gave no reason", i)
		}
		release()
	}
}

func TestTextTheUserLeftOnTheLineBlocksTheTool(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	w.Write([]byte("sh"))
	eventually(t, "the user's typing", func() bool { return k.port(0).written() == "sh" })
	time.Sleep(typingWindow) // the user stopped typing a while ago, but left the text
	if _, _, _, err := ai.Lease(context.Background()); !errors.Is(err, ErrTyping) {
		t.Errorf("text on the line should make the tool wait, got %v", err)
	}
}

func TestWindowsCantChangeThePortWhileTheToolTypes(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	_, _, release, err := ai.Lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := w.Break(0); err == nil || !strings.Contains(err.Error(), "is working") {
		t.Errorf("BREAK while the tool types: %v", err)
	}
	if err := w.SetBaud(115200); err == nil {
		t.Error("a baud change while the tool types was allowed")
	}
}

func TestTheToolCantSendBreakWhileTheUserTypes(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	w.KeyPressed()
	time.Sleep(20 * time.Millisecond)
	if err := ai.Break(0); !errors.Is(err, ErrTyping) {
		t.Errorf("BREAK while the user types: %v", err)
	}
}

func TestTheToolLeavingMidCommandErasesIt(t *testing.T) {
	k := newConsole(t)
	k.open(false)
	ai := k.open(true)
	ai.Lease(context.Background())
	ai.Write([]byte("rel"))
	eventually(t, "typed", func() bool { return k.port(0).written() == "rel" })
	ai.Close()
	eventually(t, "erased", func() bool { return k.port(0).written() == "rel\b\b\b" })
}

func TestTakeoverKeepsTheConsolesRate(t *testing.T) {
	k := newConsole(t)
	a := k.open(false)
	b := k.open(false)
	if err := a.SetBaud(115200); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b knows the rate", func() bool { return b.Baud() == 115200 })
	a.Close()
	eventually(t, "b holds the port", func() bool { k.mu.Lock(); defer k.mu.Unlock(); return len(k.ports) == 2 })
	eventually(t, "b reconnected", func() bool { return b.Status().Connected })
	if got := k.port(1).Baud(); got != 115200 {
		t.Errorf("b reopened the port at %d, want the console's 115200", got)
	}
}

func TestClosingDuringATakeoverLeavesNothingBehind(t *testing.T) {
	k := newConsole(t)
	a := k.open(false)
	b := k.open(false)
	a.Close()
	b.Close() // possibly while it's taking over
	eventually(t, "the lock free", func() bool {
		_, lockPath, _ := paths(k.key)
		f, err := lock(lockPath)
		if err == nil {
			f.Close()
		}
		return err == nil
	})
}

func TestTheToolSeesOnlyTheCursorLineOfAScreen(t *testing.T) {
	h := &hub{ring: []byte("enable secret 5 $1$abc\x1b[24;1H\rSwitch#")}
	if got := string(h.replay(true, false)); got != "Switch#" {
		t.Errorf("replay %q", got)
	}
}

// A password the tool types never shows in history, however the prompt
// looks.
func TestPasswordsArentMarked(t *testing.T) {
	for _, prompt := range []string{"Password: ", "[sudo] password for alice: ", "\x1b[1mPassword:\x1b[0m ", "Enter passphrase for key 'id': "} {
		t.Run(prompt, func(t *testing.T) {
			k := newConsole(t)
			w := k.open(false)
			ai := k.open(true)
			k.port(0).say("\r\n" + prompt)
			output(t, w, ":")
			_, _, release, err := ai.Lease(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ai.Write([]byte("hunter2\r"))
			release()
			for n := range w.Notes() {
				if n.Mark {
					if strings.Contains(n.Text, "hunter2") {
						t.Errorf("the password was marked: %q", n.Text)
					}
					return
				}
			}
		})
	}
}

func TestDetectionsNothingHeardSurvives(t *testing.T) {
	k := newConsole(t)
	k.open(false)
	b := k.open(false)
	k.port(0).mu.Lock()
	k.port(0).quiet = true
	k.port(0).mu.Unlock()
	if _, err := b.Detect(serialx.DetectConfig{}); !errors.Is(err, serialx.ErrNoSignal) {
		t.Errorf("got %v, want serialx.ErrNoSignal", err)
	}
}

// Text the tool typed without Enter, in an earlier command, is still erased
// when it leaves.
func TestUnenteredTextFromAnEarlierCommandIsErased(t *testing.T) {
	k := newConsole(t)
	k.open(false)
	ai := k.open(true)
	_, _, release, _ := ai.Lease(context.Background())
	ai.Write([]byte("rel"))
	eventually(t, "typed", func() bool { return k.port(0).written() == "rel" })
	release()
	ai.Close()
	eventually(t, "erased", func() bool { return k.port(0).written() == "rel\b\b\b" })
}

// A window that rejoins after a takeover gets what the new hub heard
// before it got there, as live output.
func TestRejoiningANewHubCatchesUp(t *testing.T) {
	p := newFakePort()
	h := newHub(p)
	p.say("during the takeover\r\n")
	eventually(t, "the hub heard it", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.ring) > 0 })
	mine, theirs := net.Pipe()
	go h.serve(theirs)
	w := newWire(mine)
	w.send(msg{T: tHello, Version: version, Hub: "the old one"})
	for {
		m, err := w.recv()
		if err != nil {
			t.Fatal(err)
		}
		if m.T == tData {
			if m.Replay || string(m.Data) != "during the takeover\r\n" {
				t.Errorf("caught up with %q (replay %v)", m.Data, m.Replay)
			}
			break
		}
	}
	mine.Close()
	p.Close()
}

// Arrow keys reach the device as escape sequences, which aren't text on
// the line.
func TestArrowKeysArentTextOnTheLine(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	w.Write([]byte("\x1b[A\x1b[B\x1bOP\x1bx")) // up, down, F1, alt+x
	eventually(t, "the keys", func() bool { return len(k.port(0).written()) == 11 })
	time.Sleep(typingWindow)
	_, _, release, err := ai.Lease(context.Background())
	if err != nil {
		t.Fatalf("the tool should get the keyboard after arrow keys, got %v", err)
	}
	release()
}

// The tool's own ctrl+c clears its line, so a stop afterwards has nothing
// to erase.
func TestTheToolsCtrlCClearsItsLine(t *testing.T) {
	k := newConsole(t)
	w := k.open(false)
	ai := k.open(true)
	_, why, _, _ := ai.Lease(context.Background())
	ai.Write([]byte("show tech\x03"))
	eventually(t, "typed", func() bool { return k.port(0).written() == "show tech\x03" })
	w.StopAgent(false)
	eventually(t, "stopped", func() bool { return why() != "" })
	if why() != StopPaused || k.port(0).written() != "show tech\x03" {
		t.Errorf("stopped because %q, written %q", why(), k.port(0).written())
	}
}
