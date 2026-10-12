package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ssimpson89/bauddy/internal/serialx"
	"github.com/ssimpson89/bauddy/internal/share"
	"github.com/ssimpson89/bauddy/internal/simdev"
)

// simPort is a simulated switch on a serial port.
type simPort struct {
	d      *simdev.Device
	events chan serialx.Event
	stop   context.CancelFunc

	mu     sync.Mutex
	baud   int
	breaks int
	detect error // what Detect returns; nil finds the device's rate
}

func newSimPort(t *testing.T, d *simdev.Device) *simPort {
	ctx, stop := context.WithCancel(context.Background())
	d.Start(ctx)
	p := &simPort{d: d, events: make(chan serialx.Event, 256), stop: stop, baud: d.Baud}
	go func() {
		defer close(p.events)
		for {
			select {
			case b := <-d.Out():
				p.events <- serialx.Event{Data: b}
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(stop)
	return p
}

func (p *simPort) Path() string                 { return "/dev/ttySIM0" }
func (p *simPort) Events() <-chan serialx.Event { return p.events }
func (p *simPort) Write(b []byte) (int, error)  { return len(b), p.d.Write(b) }
func (p *simPort) Framing() serialx.Framing     { return serialx.Framing8N1 }
func (p *simPort) Close() error                 { p.stop(); return nil }
func (p *simPort) Baud() int                    { p.mu.Lock(); defer p.mu.Unlock(); return p.baud }
func (p *simPort) Break(time.Duration) error    { p.mu.Lock(); p.breaks++; p.mu.Unlock(); return nil }
func (p *simPort) Detect(serialx.DetectConfig) (serialx.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.detect != nil {
		return serialx.Result{}, p.detect
	}
	p.baud = p.d.Baud
	return serialx.Result{Baud: p.d.Baud, Framing: serialx.Framing8N1}, nil
}

func (p *simPort) SetBaud(int) error { return nil }
func (p *simPort) Reconnect()        {}

// say makes the device print s.
func (p *simPort) say(s string) { p.events <- serialx.Event{Data: []byte(s)} }

// harness is a server with one simulated switch plugged in, and a client.
type harness struct {
	t       *testing.T
	server  *server
	port    *simPort
	opened  []int // the baud of each open
	openErr error // what the next open returns
	cs      *mcp.ClientSession
	logDir  string
	asked   []string // the approval questions the user saw
}

// start connects a client to a server. With answer set, the client can ask
// the user to approve things, and the user answers: "accept" ticks "Send it"
// and accepts, "unticked" accepts without ticking, anything else is the
// action, such as "decline".
func start(t *testing.T, d *simdev.Device, opt Options, answer string) *harness {
	return startVersion(t, d, opt, answer, "")
}

// startVersion is start with a client speaking an older protocol version.
func startVersion(t *testing.T, d *simdev.Device, opt Options, answer, version string) *harness {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // nothing saved
	h := &harness{t: t, port: newSimPort(t, d), logDir: t.TempDir()}
	if opt.LogDir == "" {
		opt.LogDir = h.logDir
	}
	s := newServer(opt)
	h.server = s
	s.list = func() ([]serialx.PortInfo, error) {
		return []serialx.PortInfo{{Path: "/dev/ttySIM0", Key: "usb:1:2:3", USB: true, VID: "0403", PID: "6001"}}, nil
	}
	s.open = func(key, path string, baud int, f serialx.Framing, who string) (Port, error) {
		if err := h.openErr; err != nil {
			return nil, err
		}
		h.opened = append(h.opened, baud)
		h.port.mu.Lock()
		h.port.baud = baud
		h.port.mu.Unlock()
		return h.port, nil
	}
	t.Cleanup(s.disconnect)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.mcp().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	copt := &mcp.ClientOptions{}
	if answer != "" {
		copt.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			h.asked = append(h.asked, r.Params.Message)
			switch answer {
			case "accept":
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"allow": true}}, nil
			case "unticked":
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"allow": false}}, nil
			}
			return &mcp.ElicitResult{Action: answer}, nil
		}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, copt).Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	h.cs = cs
	return h
}

// call runs a tool and returns its text and whether it was an error.
func (h *harness) call(name string, args map[string]any) (string, bool) {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.(*mcp.TextContent).Text)
	}
	return b.String(), res.IsError
}

func (h *harness) connect() string {
	h.t.Helper()
	out, isErr := h.call("connect", nil)
	if isErr {
		h.t.Fatalf("connect: %s", out)
	}
	return out
}

func (h *harness) ran() string { return strings.Join(h.port.d.Applied(), "|") }

func TestConnectDetectsAndRunsCommands(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	out := h.connect()
	if !strings.Contains(out, "Connected to ttySIM0 (/dev/ttySIM0) at 9600 baud, detected.") || !strings.Contains(out, "Logging to "+h.logDir) {
		t.Errorf("connect said:\n%s", out)
	}
	if !strings.Contains(out, "send_keys with key enter") {
		t.Errorf("a silent device should get a hint:\n%s", out)
	}
	if len(h.opened) != 1 || h.opened[0] != defaultBaud {
		t.Errorf("opened at %v, want once at %d before detecting", h.opened, defaultBaud)
	}

	out, isErr := h.call("send_command", map[string]any{"command": "terminal length 0\nshow clock"})
	if isErr {
		t.Fatalf("send_command failed:\n%s", out)
	}
	// The simulated switch prints no prompt until its first command.
	if want := "terminal length 0\nSwitch#show clock\noutput of show clock\nSwitch#"; !strings.HasSuffix(out, want) {
		t.Errorf("output:\n%s\nwant it to end:\n%s", out, want)
	}
	if h.ran() != "terminal length 0|show clock" {
		t.Errorf("device ran %q", h.ran())
	}
	if out, _ := h.call("read_output", nil); !strings.Contains(out, "no new lines") || !strings.HasSuffix(out, "Switch#") {
		t.Errorf("read_output repeated what was returned:\n%s", out)
	}

	if out, _ := h.call("read_history", map[string]any{"search": "CLOCK"}); out != "(nothing found)" {
		t.Errorf("a capital should match exactly: %q", out)
	}
	if out, _ := h.call("read_history", map[string]any{"search": "clock"}); out != "Switch#show clock\noutput of show clock" {
		t.Errorf("history search: %q", out)
	}

	out, _ = h.call("disconnect", nil)
	logs, _ := filepath.Glob(filepath.Join(h.logDir, "*.log"))
	if len(logs) != 1 || !strings.Contains(out, "Log saved: "+logs[0]) {
		t.Fatalf("disconnect said %q; logs %v", out, logs)
	}
	b, _ := os.ReadFile(logs[0])
	for _, want := range []string{"bauddy mcp log: ttySIM0 (/dev/ttySIM0) at 115200 baud", "an AI agent typed", "detected 9600 baud", "Switch#show clock\noutput of show clock\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lacks %q:\n%s", want, b)
		}
	}
	if fi, _ := os.Stat(logs[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v; it can hold secrets, so only the user should read it", fi.Mode().Perm())
	}
	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); !isErr || !strings.Contains(out, "connect first") {
		t.Errorf("after disconnect: %q", out)
	}
}

func TestCommandStopsAtErrorsAndQuestions(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	out, isErr := h.call("send_command", map[string]any{"command": "conf t\nbogus\nhostname x"})
	if !isErr || !strings.Contains(out, "% Invalid input") || !strings.Contains(out, "Stopped at line 2 (bogus)") ||
		!strings.Contains(out, "The line after it wasn't sent") {
		t.Errorf("error not reported:\n%s", out)
	}
	if h.ran() != "conf t" {
		t.Errorf("device ran %q; nothing after the error should be sent", h.ran())
	}

	h.call("send_command", map[string]any{"command": "end"})
	out, isErr = h.call("send_command", map[string]any{"command": "copy run tftp:"})
	if isErr || !strings.Contains(out, "Stopped: device is asking: Destination filename [startup-config]?") ||
		!strings.Contains(out, "Answer with send_keys") {
		t.Fatalf("a question isn't an error, and should say how to answer (isError %v):\n%s", isErr, out)
	}
	out, _ = h.call("send_keys", map[string]any{"key": "enter"})
	if !strings.Contains(out, "[OK]") || !strings.HasSuffix(out, "Switch#") {
		t.Errorf("answer with enter:\n%s", out)
	}

	if out, isErr := h.call("send_command", map[string]any{"command": "show ip route ?"}); !isErr || !strings.Contains(out, "send_keys") {
		t.Errorf("? should be refused: %s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"text": "show\r"}); !isErr || !strings.Contains(out, "control characters") {
		t.Errorf("control characters in text should be refused: %s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"key": "f12"}); !isErr {
		t.Errorf("unknown key accepted: %s", out)
	}
}

func TestRiskyNeedsApproval(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer string // "" means the client can't ask
		opt    Options
		sent   bool
		says   string
	}{
		{"client can't ask", "", Options{}, false, "can't ask for it"},
		{"user declines", "decline", Options{}, false, "the user refused"},
		{"user accepts without ticking", "unticked", Options{}, false, "the user refused"},
		{"user approves", "accept", Options{}, true, "Proceed with reload? [confirm]"},
		{"allowed when the client can't ask", "", Options{AllowRisky: true}, true, "Proceed with reload? [confirm]"},
		{"still asked when the client can", "decline", Options{AllowRisky: true}, false, "the user refused"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, c.opt, c.answer)
			h.connect()
			out, _ := h.call("send_command", map[string]any{"command": "reload"})
			if !strings.Contains(out, c.says) {
				t.Errorf("reload: %s", out)
			}
			if sent := strings.Contains(out, "Proceed"); sent != c.sent {
				t.Errorf("sent = %v, want %v; device ran %q", sent, c.sent, h.ran())
			}
			out, _ = h.call("send_break", nil)
			if c.sent != (h.port.breaks == 1) {
				t.Errorf("break sent %d times; said %s", h.port.breaks, out)
			}
		})
	}
}

// Before 2026-07-28 the server asks the user mid-call; the SDK does that and
// calls the tool again with the answer.
func TestApprovalOnOlderProtocol(t *testing.T) {
	for answer, sent := range map[string]bool{"accept": true, "decline": false} {
		h := startVersion(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, answer, "2025-11-25")
		h.connect()
		out, _ := h.call("send_command", map[string]any{"command": "reload"})
		if got := strings.Contains(out, "Proceed with reload"); got != sent {
			t.Errorf("%s: sent = %v:\n%s", answer, got, out)
		}
	}
}

// An approval is for one call: it can't be replayed, or used for another.
func TestApprovalIsForThatCallOnly(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "accept")
	h.connect()
	s := h.server
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}
	req := func(cmd, state string) *mcp.CallToolRequest {
		args, _ := json.Marshal(map[string]any{"command": cmd})
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Meta: mcp.Meta{mcp.MetaKeyClientCapabilities: caps}, Name: "send_command", Arguments: args, RequestState: state,
			InputResponses: mcp.InputResponseMap{approvalID: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"allow": true}}},
		}}
	}
	why := []string{whyRestart}
	s.mu.Lock()
	defer s.mu.Unlock()
	asking, err := s.approve(req("reload", ""), s.con.target(), "reload", why)
	if err != nil || asking == nil || asking.RequestState == "" {
		t.Fatalf("first call should ask: %v, %v", asking, err)
	}
	if _, err := s.approve(req("write erase", asking.RequestState), s.con.target(), "write erase", why); err == nil {
		t.Error("an approval for reload was accepted for write erase")
	}
	asking, _ = s.approve(req("reload", ""), s.con.target(), "reload", why)
	if _, err := s.approve(req("reload", asking.RequestState), s.con.target(), "Switch#reload", why); err == nil {
		t.Error("an approval was accepted for different text on the same call")
	}
	asking, _ = s.approve(req("reload", ""), s.con.target(), "reload", why)
	state := asking.RequestState
	if res, err := s.approve(req("reload", state), s.con.target(), "reload", why); res != nil || err != nil {
		t.Errorf("the approved call should go ahead: %v, %v", res, err)
	}
	if _, err := s.approve(req("reload", state), s.con.target(), "reload", why); err == nil {
		t.Error("an approval was used twice")
	}
	if _, err := s.approve(req("reload", "made-up"), s.con.target(), "reload", why); err == nil {
		t.Error("a made-up approval was accepted")
	}
}

func TestApprovalShowsTheWholeCommand(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "decline")
	h.connect()
	h.call("send_command", map[string]any{"command": "interface Gi1/0/1\nshutdown\nend\nwr"})
	if len(h.asked) != 1 || !strings.Contains(h.asked[0], "interface Gi1/0/1\nshutdown\nend\nwr   <- this one") ||
		!strings.Contains(h.asked[0], "saves or commits") {
		t.Errorf("approval asked:\n%s", h.asked)
	}
	if h.ran() != "" {
		t.Errorf("nothing should run before approval; ran %q", h.ran())
	}
}

func TestTypedTextAtThePromptIsNotSkipped(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	h.call("send_command", map[string]any{"command": "show clock"}) // the switch prints its prompt
	h.call("send_keys", map[string]any{"text": "rel"})              // abbreviated, typed in pieces
	out, isErr := h.call("send_keys", map[string]any{"key": "enter"})
	if !isErr || !strings.Contains(out, "can't ask for it") {
		t.Errorf("enter after typing rel: %s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"text": "oad", "key": "enter"}); !isErr {
		t.Errorf("text plus enter making reload: %s", out)
	}
	if out, isErr := h.call("send_command", map[string]any{"command": "oad"}); !isErr || !strings.Contains(out, `"rel" typed after it`) {
		t.Errorf("send_command after typed text should be refused: %s", out)
	}
	if h.ran() != "show clock" {
		t.Errorf("device ran %q", h.ran())
	}
}

// The echo of typed text can arrive after a short wait returns, so the
// checks can't rely on it alone.
func TestTypedTextIsRememberedBeforeTheEcho(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	h.call("send_command", map[string]any{"command": "show clock"})
	h.call("send_keys", map[string]any{"text": "rel", "wait_seconds": 0.001})
	if out, isErr := h.call("send_command", map[string]any{"command": "oad"}); !isErr || !strings.Contains(out, `"rel" typed`) {
		t.Errorf("send_command right after typing: %s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"text": "oad", "key": "enter", "wait_seconds": 0.001}); !isErr || !strings.Contains(out, "can't ask for it") {
		t.Errorf("enter right after typing: %s", out)
	}
	if h.ran() != "show clock" {
		t.Errorf("device ran %q", h.ran())
	}
	pending := func() string { p, _ := h.server.typing.pending(h.server.typing.at); return p }
	h.call("send_keys", map[string]any{"key": "ctrl+u"})
	if p := pending(); p != "" {
		t.Errorf("ctrl+u should forget the typed text, got %q", p)
	}
	h.call("send_keys", map[string]any{"text": "sh ?", "wait_seconds": 0.001}) // help: the device retypes "sh "
	h.call("send_keys", map[string]any{"text": "clock", "key": "backspace", "wait_seconds": 0.001})
	if p := pending(); p != "sh cloc" {
		t.Errorf("typed = %q, want the ? dropped and one character erased", p)
	}
}

func TestTypingTracksKeys(t *testing.T) {
	var ty typing
	ty.add("rm", "space", 1)
	ty.add("-rf /", "", 1)
	if p, _ := ty.pending(1); p != "rm -rf /" {
		t.Errorf("space key: pending %q", p)
	}
	ty.add("", "up", 1)
	if _, unknown := ty.pending(1); !unknown {
		t.Error("after up, the line can't be known")
	}
	if p, unknown := ty.pending(2); p != "" || unknown {
		t.Errorf("after a new prompt, nothing is pending: %q %v", p, unknown)
	}
}

// up recalls a line bauddy can't read for certain, so Enter after it asks.
func TestRecalledLineNeedsApproval(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	h.call("send_command", map[string]any{"command": "show clock"})
	h.call("send_keys", map[string]any{"key": "up", "wait_seconds": 0.001})
	if out, isErr := h.call("send_keys", map[string]any{"key": "enter"}); !isErr || !strings.Contains(out, "can't ask for it") {
		t.Errorf("enter after up: %s", out)
	}
	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); !isErr || !strings.Contains(out, "recalled") {
		t.Errorf("send_command after up: %s", out)
	}
}

func TestProbeNeedsApproval(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	if out, isErr := h.call("connect", map[string]any{"detect": true, "probe": true}); !isErr || !strings.Contains(out, "presses Enter") {
		t.Errorf("connect with probe: %s", out)
	}
	if len(h.opened) != 0 {
		t.Errorf("the port was opened %d times before approval", len(h.opened))
	}
	h = start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "accept")
	if out, isErr := h.call("connect", map[string]any{"detect": true, "probe": true}); isErr || len(h.asked) != 1 {
		t.Errorf("approved probe: asked %d times: %s", len(h.asked), out)
	}
}

// Ways around the checks found in review, each typed in pieces.
func TestNoWayAroundTheChecks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		send  map[string]any
		tool  string
	}{
		{"typed after a prompt with spaces", func(h *harness) {
			h.port.say("\r\n[root@host ~]# ")
			h.call("send_keys", map[string]any{"text": "reb"})
		}, map[string]any{"command": "oot"}, "send_command"},
		{"send_command answering [confirm]", func(h *harness) {
			h.call("send_command", map[string]any{"command": "clear counters"})
		}, map[string]any{"command": "y"}, "send_command"},
		{"send_keys answering [confirm] with other text", func(h *harness) {
			h.call("send_command", map[string]any{"command": "clear counters"})
		}, map[string]any{"text": "ye", "key": "enter"}, "send_keys"},
		{"typed text ending like a prompt", func(h *harness) {
			h.port.say("\r\n[root@host ~]# ")
			h.call("send_keys", map[string]any{"text": "reboot #"})
		}, map[string]any{"command": "show clock"}, "send_command"},
		{"bootloader command ending like a prompt", func(h *harness) {
			h.port.say("\r\nrommon 1 > ")
			h.call("send_keys", map[string]any{"text": "reset #"})
		}, map[string]any{"key": "enter"}, "send_keys"},
		{"bootloader command typed then entered", func(h *harness) {
			h.port.say("\r\nrommon 1 > ")
			h.call("send_keys", map[string]any{"text": "confreg 0x2142"})
		}, map[string]any{"key": "enter"}, "send_keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
			h.connect()
			c.setup(h)
			before := h.ran()
			if out, isErr := h.call(c.tool, c.send); !isErr || !strings.Contains(out, "nothing was sent") {
				t.Errorf("not refused: %s", out)
			}
			if h.ran() != before {
				t.Errorf("device ran %q", h.ran())
			}
		})
	}
}

func TestAnsweringQuestions(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	out, _ := h.call("send_command", map[string]any{"command": "clear counters"})
	if !strings.Contains(out, "[confirm]") {
		t.Fatalf("clear counters:\n%s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"key": "enter"}); !isErr || !strings.Contains(out, "can't ask for it") {
		t.Errorf("confirming needs approval: %s", out)
	}
	if out, isErr := h.call("send_keys", map[string]any{"text": "n"}); isErr {
		t.Errorf("declining shouldn't: %s", out)
	}

	// An approved command's own questions don't ask again, until the next prompt.
	h = start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "accept")
	h.connect()
	h.call("send_command", map[string]any{"command": "show clock"})
	h.call("send_command", map[string]any{"command": "reload"})
	if out, isErr := h.call("send_keys", map[string]any{"key": "enter"}); isErr || len(h.asked) != 1 {
		t.Errorf("confirming an approved reload asked %d times: %s", len(h.asked), out)
	}
	h.port.say("\r\nSwitch>\r\nWould you like to enter the initial configuration dialog? [yes/no]: ")
	h.call("read_output", map[string]any{"until": "dialog", "wait_seconds": 2})
	h.call("send_keys", map[string]any{"text": "yes", "key": "enter"})
	if len(h.asked) != 2 {
		t.Errorf("after a new prompt, answering should ask again; asked %d times", len(h.asked))
	}
}

func TestBootloaderNeedsApproval(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	h.port.say("\r\nrommon 1 > ")
	h.call("read_output", map[string]any{"until": "rommon", "wait_seconds": 2})
	if out, isErr := h.call("send_command", map[string]any{"command": "confreg 0x2142"}); !isErr || !strings.Contains(out, "bootloader") {
		t.Errorf("at rommon: %s", out)
	}
}

func TestUnpluggedRefusesToSend(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.connect()
	h.port.events <- serialx.Event{State: serialx.Disconnected}
	h.server.mu.Lock()
	c := h.server.con
	h.server.mu.Unlock()
	for c.up() {
		time.Sleep(time.Millisecond)
	}
	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); !isErr || !strings.Contains(out, "unplugged") {
		t.Errorf("send while unplugged: %s", out)
	}
	if out := h.connect(); !strings.Contains(out, "isn't responding") || !strings.Contains(out, "bauddy reconnects") {
		t.Errorf("status while unplugged:\n%s", out)
	}
}

func TestLoginPromptIsPointedOut(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	go func() {
		time.Sleep(100 * time.Millisecond) // after connect has opened the port
		h.port.say("\r\nUser Access Verification\r\n\r\nUsername: ")
	}()
	if out := h.connect(); !strings.Contains(out, "Username: ") || !strings.Contains(out, "asking for a login") {
		t.Errorf("connect should wait for the login prompt and point it out:\n%s", out)
	}
}

func TestConnectReportsQuietDevice(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	h.port.detect = serialx.ErrNoSignal
	out := h.connect()
	if !strings.Contains(out, "at 115200 baud, the default, since nothing was heard") || !strings.Contains(out, "send_keys with key enter") {
		t.Errorf("connect said:\n%s", out)
	}
	if again := h.connect(); !strings.HasPrefix(again, "Connected to ttySIM0 (/dev/ttySIM0) at 115200 baud.") || len(h.opened) != 1 {
		t.Errorf("connect with no arguments should report, not reopen (opened %d times):\n%s", len(h.opened), again)
	}
	if out, _ := h.call("connect", map[string]any{"baud": 9600}); !strings.Contains(out, "Disconnected from ttySIM0") || h.opened[1] != 9600 {
		t.Errorf("connect with a baud should reopen at it (opened at %v):\n%s", h.opened, out)
	}
	h.openErr = errors.New("boom")
	if out, isErr := h.call("connect", map[string]any{"baud": 9600}); !isErr || !strings.Contains(out, "Disconnected from ttySIM0") || !strings.Contains(out, "boom") {
		t.Errorf("a failed reopen should say the old connection closed: %s", out)
	}
}

func TestReadOutputWaits(t *testing.T) {
	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16, Noise: 150 * time.Millisecond}, Options{}, "")
	h.connect()
	began := time.Now()
	out, _ := h.call("read_output", map[string]any{"until": "changed state", "wait_seconds": 5})
	if !strings.Contains(out, "changed state to up") || time.Since(began) > 2*time.Second {
		t.Errorf("until should return as soon as the text appears (took %s):\n%s", time.Since(began), out)
	}
	out, _ = h.call("read_output", map[string]any{"until": "never printed", "wait_seconds": 0.3})
	if !strings.Contains(out, `"never printed" didn't appear within 300ms`) {
		t.Errorf("timeout not reported:\n%s", out)
	}
	if _, isErr := h.call("read_output", map[string]any{"wait_seconds": 61}); !isErr {
		t.Error("wait_seconds over the limit accepted")
	}

	quiet := start(t, &simdev.Device{Baud: 9600, FIFO: 16}, Options{}, "")
	quiet.connect()
	quiet.call("send_command", map[string]any{"command": "show clock"})
	if out, _ := quiet.call("read_output", map[string]any{"until": "Switch#", "wait_seconds": 0.3}); !strings.Contains(out, "didn't appear") {
		t.Errorf("until shouldn't match the prompt already returned:\n%s", out)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		quiet.port.say("late line\r\n")
	}()
	began = time.Now()
	out, _ = quiet.call("read_output", map[string]any{"wait_seconds": 10})
	if !strings.Contains(out, "late line") || time.Since(began) > 2*time.Second {
		t.Errorf("wait_seconds should return once the device goes quiet (took %s):\n%s", time.Since(began), out)
	}
}

func TestOutputLimits(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = "line"
	}
	lines[999] = "last"
	got := shown(lines, "Switch#", 5)
	if !strings.HasPrefix(got, "[705 earlier lines not shown") || !strings.HasSuffix(got, "last\nSwitch#") {
		t.Errorf("got %q…%q", got[:60], got[len(got)-20:])
	}
	if n := strings.Count(got, "\n"); n != shownLinesMax+1 {
		t.Errorf("%d lines shown, want %d", n, shownLinesMax)
	}
	garbage := strings.Repeat("\xfe", 100000) // the wrong baud rate: no newlines
	if got := shown(nil, garbage, 0); len(got) > lineMax+100 {
		t.Errorf("a huge cursor line should be cut, got %d bytes", len(got))
	}

	c := &console{changed: make(chan struct{})}
	for range historyMax + 5 {
		c.feed([]byte("x\n"))
	}
	c.feed([]byte("end\nSwitch#"))
	if got := c.unread(); !strings.HasPrefix(got, "[9706 earlier lines not shown") || !strings.HasSuffix(got, "end\nSwitch#") {
		t.Errorf("after overflowing history: %q…", got[:60])
	}
	if got := c.history(nil, 2); got != "end\nSwitch#" {
		t.Errorf("history: %q", got)
	}
	if got := (&console{changed: make(chan struct{})}).history(nil, 5); got != "(nothing found)" {
		t.Errorf("empty history: %q", got)
	}
}

func TestRisk(t *testing.T) {
	for line, want := range map[string]string{
		"reload":               whyRestart,
		"wr":                   whySave,
		"write memory":         whySave,
		"copy run start":       whySave,
		"copy r s":             whySave,
		"copy run scp:":        "",
		"save force":           whySave,
		"commit":               whySave,
		"commit check":         "",
		"copy tftp: start":     whyRestart,
		"debug ip packet":      whyDebug,
		"undebug all":          "",
		"clear ip bgp *":       whyClear,
		"clear counters":       "",
		"show running-config":  "",
		"interface Gi1/0/1":    "",
		"do wr":                whySave,
		"write terminal":       "",
		"show debug":           "",
		"terminal length 0":    "",
		"configure terminal":   "",
		"ping 10.0.0.1":        "",
		"show clock | i debug": "",
	} {
		if got := risk(line); got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
	for line, want := range map[string]bool{
		"Switch#reload":            true,
		"Switch(config)#do rel":    true,
		"user@host:~$ sudo reboot": true,
		"Switch#show reload":       false,
		"Switch#wr er":             true,
		"Proceed? [confirm]":       false,
	} {
		if got := lineRisk(line) != ""; got != want {
			t.Errorf("%q: got %v, want %v", line, got, want)
		}
	}
	for _, c := range []struct{ cursor, prompt, want string }{
		{"Switch#", "Switch#", ""},
		{"Switch#rel", "Switch#", "rel"},
		{"[root@host ~]# reb", "[root@host ~]# ", "reb"},
		{"pi@rpi:~ $ reb", "pi@rpi:~ $ ", "reb"},
		{"Switch#rel", "", "rel"}, // no prompt seen yet
		{"Password: ", "Switch#", ""},
		{"rommon 1 > ", "rommon 1 > ", ""},
	} {
		if got := (line{cursor: c.cursor, prompt: c.prompt}).typed(); got != c.want {
			t.Errorf("typed after %q on %q = %q, want %q", c.prompt, c.cursor, got, c.want)
		}
	}
}

// The user has the console open in bauddy; Claude joins it.
func TestJoiningAConsoleOpenInBauddy(t *testing.T) {
	d, err := os.MkdirTemp("", "bd") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("XDG_RUNTIME_DIR", d)

	h := start(t, &simdev.Device{Baud: 9600, FIFO: 16, Slow: map[string]time.Duration{"show tech": 3 * time.Second}}, Options{}, "")
	opener := func(string, int, serialx.Framing) (share.Port, error) { return h.port, nil }
	window, err := share.Open(share.Options{Key: "k", Path: "/dev/ttySIM0", Baud: 9600, Replay: true, Open: opener})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { window.Close() })
	h.port.say("Username: admin\r\nPassword:\r\nSwitch#")
	time.Sleep(50 * time.Millisecond)
	h.server.open = func(key, path string, baud int, f serialx.Framing, who string) (Port, error) {
		return share.Open(share.Options{Key: "k", Path: path, Agent: true, Name: who, Open: opener})
	}

	out := h.connect()
	if !strings.Contains(out, "Joined ttySIM0") || strings.Contains(out, "admin") || !strings.HasSuffix(out, "Switch#") {
		t.Errorf("joining should show only the current line:\n%s", out)
	}
	if got := window.Status().Agent; got != "test" {
		t.Errorf("the window should show the AI tool by its client's name, got %q", got)
	}

	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); isErr || !strings.Contains(out, "output of show clock") {
		t.Fatalf("send_command through the shared console:\n%s", out)
	}

	done := make(chan string)
	go func() { out, _ := h.call("send_command", map[string]any{"command": "show tech"}); done <- out }()
	for !window.Status().Working {
		time.Sleep(5 * time.Millisecond)
	}
	if err := window.StopAgent(false); err != nil {
		t.Fatal(err)
	}
	if out := <-done; !strings.Contains(out, "The user stopped you in bauddy") {
		t.Errorf("stopped mid-command:\n%s", out)
	}
	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); !isErr || !strings.Contains(out, "Paused by the user") {
		t.Errorf("while paused:\n%s", out)
	}
	if err := window.TogglePause(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // let "show tech" finish, as the user would
	if out, isErr := h.call("send_command", map[string]any{"command": "show clock"}); isErr {
		t.Errorf("after the user lets it type again:\n%s", out)
	}
}
