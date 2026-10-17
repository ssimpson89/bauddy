// Package simdev simulates a Cisco-style switch on the far end of a serial
// line, for testing paste pacing without hardware. Only tests use it.
//
// It models what makes real consoles drop characters: bytes arrive at the
// baud rate into a small input FIFO, and while the CPU is busy running a
// command, whatever overflows the FIFO is lost.
package simdev

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Device is a simulated switch. Set the fields, then call Start.
type Device struct {
	Baud     int
	FIFO     int                      // input buffer in bytes; overflow is dropped
	Hostname string                   // default "Switch"
	Slow     map[string]time.Duration // command prefix -> processing time
	Noise    time.Duration            // if set, print a syslog line this often
	SyncLog  bool                     // after a syslog line, reprint the prompt and typed text (IOS "logging synchronous")
	Width    int                      // if set, scroll long input lines IOS-style: "prompt$tail"
	NoEcho   bool                     // never echo input

	out     chan []byte
	outMu   sync.Mutex // held while sending, so output leaves in order, as on a wire
	outNext time.Time  // when the transmitter is next free
	talk    sync.Mutex // the CLI's echo and a syslog reprint of the line don't cross

	mu      sync.Mutex
	wire    []byte // sent by the host, still in transit
	fifo    []byte
	applied []string
	dropped int
	mode    string // "", "config", "config-if", "config-vlan"
	textEnd string // banner delimiter while reading banner text
	typed   string // the input line so far, for redraws
	secret  bool   // reading a password: no echo
	asking  string // a question is waiting for this line as its answer
}

// Start runs the device until ctx is done. Output arrives on Out.
func (d *Device) Start(ctx context.Context) {
	if d.Hostname == "" {
		d.Hostname = "Switch"
	}
	d.out = make(chan []byte, 4096)
	go d.wireLoop(ctx)
	go d.cpuLoop(ctx)
	if d.Noise > 0 {
		go d.noiseLoop(ctx)
	}
}

// Out delivers device output, metered at the baud rate.
func (d *Device) Out() <-chan []byte { return d.out }

// Write sends bytes from the host. They arrive one byte-time apart.
func (d *Device) Write(b []byte) error {
	d.mu.Lock()
	d.wire = append(d.wire, b...)
	d.mu.Unlock()
	return nil
}

// Applied returns the commands the device ran, in order.
func (d *Device) Applied() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.applied...)
}

// Dropped returns how many input bytes were lost to FIFO overflow.
func (d *Device) Dropped() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Prompt returns the prompt for the current mode, e.g. "Switch(config-if)#".
func (d *Device) Prompt() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prompt()
}

func (d *Device) prompt() string {
	if d.mode == "" {
		return d.Hostname + "#"
	}
	return d.Hostname + "(" + d.mode + ")#"
}

// byteTime is one byte on the wire: 10 bits (start, 8 data, stop).
func (d *Device) byteTime() time.Duration { return time.Second * 10 / time.Duration(d.Baud) }

// wireLoop moves bytes from the wire into the FIFO at the baud rate.
func (d *Device) wireLoop(ctx context.Context) {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	last, credit := time.Now(), time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			credit += now.Sub(last)
			last = now
			d.mu.Lock()
			for credit >= d.byteTime() && len(d.wire) > 0 {
				credit -= d.byteTime()
				c := d.wire[0]
				d.wire = d.wire[1:]
				if len(d.fifo) < d.FIFO {
					d.fifo = append(d.fifo, c)
				} else {
					d.dropped++
				}
			}
			if len(d.wire) == 0 {
				credit = 0 // an idle line doesn't bank time
			}
			d.mu.Unlock()
		}
	}
}

// cpuLoop is the CLI: it reads the FIFO only when not busy.
func (d *Device) cpuLoop(ctx context.Context) {
	var line []byte
	shift := 0 // characters scrolled off the left of a long line
	for ctx.Err() == nil {
		d.mu.Lock()
		if len(d.fifo) == 0 {
			d.mu.Unlock()
			time.Sleep(time.Millisecond)
			continue
		}
		c := d.fifo[0]
		d.fifo = d.fifo[1:]
		d.mu.Unlock()

		d.talk.Lock()
		if c == '\b' || c == 0x7f { // erase the last character, and on screen: back, blank, back
			if len(line) > 0 {
				line = line[:len(line)-1]
				d.mu.Lock()
				d.typed = string(line)
				quiet := d.NoEcho || d.secret
				d.mu.Unlock()
				if !quiet {
					d.send(ctx, "\b \b")
				}
			}
			d.talk.Unlock()
			continue
		}
		if c != '\r' {
			line = append(line, c)
			d.mu.Lock()
			d.typed = string(line)
			quiet := d.NoEcho || d.secret
			p := d.prompt()
			d.mu.Unlock()
			switch {
			case quiet:
			case d.Width > 0 && len(p)+1+len(line)-shift >= d.Width:
				was := len(p) + len(line) - 1 - shift // what showed before this character
				if shift > 0 {
					was++ // the "$"
				}
				shift += 10 // IOS scrolls a long line left 10 at a time: "prompt$tail"
				redraw := "\r" + p + "$" + string(line[shift:])
				if left := was - (len(p) + 1 + len(line) - shift); left > 0 { // blank what the shorter line leaves
					redraw += strings.Repeat(" ", left) + strings.Repeat("\b", left)
				}
				d.send(ctx, redraw)
			default:
				d.send(ctx, string(c))
			}
			d.talk.Unlock()
			continue
		}
		d.send(ctx, "\r\n")
		shift = 0
		d.mu.Lock()
		d.typed = ""
		d.mu.Unlock()
		d.talk.Unlock()
		d.run(ctx, strings.TrimSpace(string(line)))
		line = line[:0]
	}
}

func (d *Device) run(ctx context.Context, cmd string) {
	d.mu.Lock()
	if d.secret { // the line after "enable" is the password
		d.secret = false
		d.applied = append(d.applied, "<password>")
		p := d.prompt()
		d.mu.Unlock()
		d.send(ctx, p)
		return
	}
	if d.asking != "" {
		d.asking = ""
		d.applied = append(d.applied, "<answer:"+cmd+">")
		p := d.prompt()
		d.mu.Unlock()
		d.send(ctx, "[OK]\r\n"+p)
		return
	}
	if d.textEnd != "" { // inside a banner: no prompt until the delimiter
		if strings.Contains(cmd, d.textEnd) {
			d.textEnd = ""
			d.applied = append(d.applied, cmd)
			p := d.prompt()
			d.mu.Unlock()
			d.send(ctx, p)
			return
		}
		d.applied = append(d.applied, cmd)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	reply := ""
	ok := true
	switch f := strings.Fields(cmd); {
	case cmd == "":
	case strings.HasPrefix(cmd, "conf"):
		d.setMode("config")
	case len(f) > 0 && (f[0] == "interface" || f[0] == "int"):
		d.setMode("config-if")
	case len(f) > 0 && f[0] == "vlan":
		d.setMode("config-vlan")
	case cmd == "exit":
		if d.Prompt() == d.Hostname+"(config)#" {
			d.setMode("")
		} else {
			d.setMode("config")
		}
	case cmd == "end":
		d.setMode("")
	case strings.HasPrefix(cmd, "banner ") && len(f) == 3:
		d.mu.Lock()
		d.textEnd = f[2]
		d.applied = append(d.applied, cmd)
		d.mu.Unlock()
		d.send(ctx, "Enter TEXT message.  End with the character '"+f[2]+"'.\r\n")
		return
	case cmd == "enable":
		d.mu.Lock()
		d.secret = true
		d.applied = append(d.applied, cmd)
		d.mu.Unlock()
		d.send(ctx, "Password: ")
		return
	case strings.HasPrefix(cmd, "copy run"):
		d.mu.Lock()
		d.asking = cmd
		d.mu.Unlock()
		d.send(ctx, "Destination filename [startup-config]? ")
		return
	case strings.HasPrefix(cmd, "clear counters"):
		d.mu.Lock()
		d.asking = cmd
		d.mu.Unlock()
		d.send(ctx, "Clear \"show interface\" counters on all interfaces [confirm]")
		return
	case cmd == "reload":
		d.send(ctx, "Proceed with reload? [confirm]")
		return // the next byte answers; a paste must not send one
	case strings.HasPrefix(cmd, "bogus"):
		ok = false
		reply = "                 ^\r\n% Invalid input detected at '^' marker.\r\n\r\n"
	case strings.HasPrefix(cmd, "show"):
		reply = "output of " + cmd + "\r\n"
	case strings.HasPrefix(cmd, "write"):
		d.send(ctx, "Building configuration...\r\n")
		reply = "[OK]\r\n"
	}
	for prefix, dur := range d.Slow {
		if strings.HasPrefix(cmd, prefix) {
			select { // busy: the FIFO fills and overflows meanwhile
			case <-time.After(dur):
			case <-ctx.Done():
			}
		}
	}
	if ok && cmd != "" {
		d.mu.Lock()
		d.applied = append(d.applied, cmd)
		d.mu.Unlock()
	}
	d.send(ctx, reply+d.Prompt())
}

func (d *Device) setMode(m string) {
	d.mu.Lock()
	d.mode = m
	d.mu.Unlock()
}

// send writes s to the host, taking one byte-time per byte. Time is kept
// as a running budget because OS timers are too coarse to sleep per byte.
func (d *Device) send(ctx context.Context, s string) {
	d.outMu.Lock()
	defer d.outMu.Unlock()
	now := time.Now()
	if d.outNext.Before(now) {
		d.outNext = now
	}
	d.outNext = d.outNext.Add(time.Duration(len(s)) * d.byteTime())
	wait := d.outNext.Sub(now)
	if wait > time.Millisecond {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
	d.out <- []byte(s)
}

// noiseLoop prints unsolicited syslog lines, which can land mid-echo.
func (d *Device) noiseLoop(ctx context.Context) {
	for {
		select {
		case <-time.After(d.Noise):
			msg := "\r\n%LINK-3-UPDOWN: Interface Gi1/0/9, changed state to up\r\n"
			d.talk.Lock()
			if d.SyncLog {
				d.mu.Lock()
				msg += d.prompt() + d.typed
				d.mu.Unlock()
			}
			d.send(ctx, msg)
			d.talk.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
