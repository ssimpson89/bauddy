package paste

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ssimpson89/bauddy/internal/simdev"
)

var config = []string{
	"interface Gi1/0/1", "description uplink", "no shutdown", "exit",
	"vlan 10", "name users", "exit",
	"write mem",
	"interface Gi1/0/2", "description server", "switchport access vlan 10", "end",
}

func pasteInto(t *testing.T, d *simdev.Device, cmds []string) ([]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d.Start(ctx)
	_ = d.Write([]byte("conf t\r")) // config mode before the paste; the simulator's Write can't fail
	var seen string
	for !strings.Contains(seen, "(config)#") {
		select {
		case b := <-d.Out():
			seen += string(b)
		case <-ctx.Done():
			t.Fatal("simulator never reached config mode")
		}
	}
	prompt := d.Prompt()
	p := &Pacer{Write: d.Write, Rx: d.Out(), Baud: d.Baud, Prompt: prompt}
	err := p.Run(ctx, Lines(strings.Join(cmds, "\n")+"\n"), func(int) {})
	time.Sleep(100 * time.Millisecond)
	return d.Applied()[1:], err // drop "conf t"
}

func TestPacing(t *testing.T) {
	cases := []struct {
		name string
		dev  *simdev.Device
	}{
		{"fast device", &simdev.Device{Baud: 9600, FIFO: 16}},
		{"slow vlan and write", &simdev.Device{Baud: 9600, FIFO: 16, Slow: map[string]time.Duration{"vlan": 300 * time.Millisecond, "write": 3 * time.Second}}},
		{"slow every command", &simdev.Device{Baud: 9600, FIFO: 16, Slow: map[string]time.Duration{"": 400 * time.Millisecond}}},
		{"syslog noise", &simdev.Device{Baud: 9600, FIFO: 16, Noise: 250 * time.Millisecond, Slow: map[string]time.Duration{"vlan": 300 * time.Millisecond}}},
		{"115200", &simdev.Device{Baud: 115200, FIFO: 16, Slow: map[string]time.Duration{"write": time.Second}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := c.dev
			got, err := pasteInto(t, d, config)
			if err != nil {
				t.Fatalf("paste stopped: %v", err)
			}
			if d.Dropped() != 0 || strings.Join(got, "|") != strings.Join(config, "|") {
				t.Errorf("dropped %d bytes; device ran:\n  %s\nwant:\n  %s", d.Dropped(), strings.Join(got, "|"), strings.Join(config, "|"))
			}
		})
	}
}

func TestStopsAtDeviceError(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	got, err := pasteInto(t, d, []string{"interface Gi1/0/1", "bogus command", "no shutdown"})
	var se *StopError
	if !errors.As(err, &se) || se.Line != 2 || !strings.Contains(se.Reason, "Invalid input") {
		t.Fatalf("err = %v, want a stop at line 2 with the device's message", err)
	}
	if strings.Join(got, "|") != "interface Gi1/0/1" {
		t.Errorf("device ran %q; nothing after the error should be sent", got)
	}
}

func TestStopsAtQuestion(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	_, err := pasteInto(t, d, []string{"end", "reload", "interface Gi1/0/1"})
	var se *StopError
	if !errors.As(err, &se) || se.Line != 2 || !strings.Contains(se.Reason, "[confirm]") {
		t.Fatalf("err = %v, want a stop at line 2 asking to confirm", err)
	}
}

func TestBanner(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	cmds := []string{"banner motd #", "Authorised access only.", "Disconnect now if not.", "#", "interface Gi1/0/1"}
	got, err := pasteInto(t, d, cmds)
	if err != nil {
		t.Fatalf("paste stopped: %v", err)
	}
	if strings.Join(got, "|") != strings.Join(cmds, "|") {
		t.Errorf("device ran %q", got)
	}
}

func TestLines(t *testing.T) {
	got := Lines("a\r\nb\rc\n\tindented\x01\nd")
	want := []string{"a\r", "b\r", "c\r", " indented\r", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Lines = %q, want %q", got, want)
	}
}

func TestRisky(t *testing.T) {
	if Risky([]string{"interface Gi1/0/1\r", "no shutdown\r"}) != "" {
		t.Error("small clean paste flagged")
	}
	if r := Risky([]string{"end\r", "reload\r"}); !strings.Contains(r, "line 2") {
		t.Errorf("reload not flagged: %q", r)
	}
	if r := Risky([]string{"end\r", "copy run start\r"}); !strings.Contains(r, "line 2") {
		t.Errorf("copy to startup not flagged: %q", r)
	}
	if r := Risky([]string{"show ip route ?\r", "x\r"}); !strings.Contains(r, "?") {
		t.Errorf("? not flagged: %q", r)
	}
	if Risky(make([]string, 10)) == "" {
		t.Error("10 lines not flagged")
	}
}

func TestDangerous(t *testing.T) {
	for _, l := range []string{
		"reload", "reload in 5", "rel", "relo\r", "  RELOAD", "do reload", "do rel",
		"write erase", "wr er", "wri era", "erase startup-config", "era nvram:",
		"del flash:vlan.dat", "delete /force flash:config.text", "format flash:",
		"copy run start", "cop runn st", "copy tftp: startup-config", "conf rep flash:old",
		"copy running-configuration startup-configuration", "clear configure all",
		"boot system flash:old.bin", "request system reboot", "req sys reb", "run request system reboot",
		"factory-reset", "execute factoryreset2", "exe reb", "configure factory-default",
		"request restart system", "install add file bootflash:x.bin", "/system reboot", "system shutdown",
		"sudo reboot", "sudo -i reboot", "/sbin/reboot", "bash sudo reboot", "shutdown now", "shutdown -h +5",
		"systemctl reboot", "ls; reboot", "true && reboot", "echo `reboot`", "x $(reboot)",
		"mkfs.ext4 /dev/sda1", "dd if=/dev/zero of=/dev/sda", "rm -rf /", "ipmitool chassis power cycle",
		"!!", "!reb", "!-1", "sudo !!", "sudo -u root reboot", "env A=1 reboot", "bash -c 'reboot'", "su -c reboot",
		"eval reboot", "xargs reboot", `\reboot`, "'reboot'", "busybox reboot", "systemctl --force reboot",
		"MODE=recovery reboot", "A=1 B=2 sudo reboot",
	} {
		if !Dangerous(l) {
			t.Errorf("%q not flagged", l)
		}
	}
	for _, l := range []string{
		"show version", "sh run", "sh reload", "re", "write memory", "wr", "copy run tftp:", "conf t",
		"for i in 1 2 3; do echo $i; done", "description reload test", "interface Gi1/0/1",
		"do show ip int brief", "no shutdown", "shutdown", "show reload", "reload cancel",
		"show run | include reload", "show log | match reboot", "!", "! uplink to core", "ls /system/reboot",
		"!Command: show running-config", "!Time: Sun Oct 11 14:00:00 2026", "!!! Authorized use only !!!",
		"exec-timeout 5 0", "time-range work", "echo hello!", "sh -c reboot is show",
		"sudo -u", "sudo -g", "env A=1", // wrappers with nothing after them
	} {
		if Dangerous(l) {
			t.Errorf("%q flagged", l)
		}
	}
}

func TestLongLinesScrolled(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16, Width: 80}
	long := "description " + strings.Repeat("uplink-to-core-distribution-", 4) + "end"
	cmds := []string{"interface Gi1/0/1", long, "no shutdown"}
	got, err := pasteInto(t, d, cmds)
	if err != nil || strings.Join(got, "|") != strings.Join(cmds, "|") {
		t.Fatalf("err %v; device ran %q", err, got)
	}
}

func TestSyslogMidLine(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprintf("logging synchronous %v", sync), func(t *testing.T) {
			t.Parallel()
			d := &simdev.Device{Baud: 9600, FIFO: 16, Noise: 70 * time.Millisecond, SyncLog: sync}
			got, err := pasteInto(t, d, config)
			if err != nil {
				t.Fatalf("paste stopped: %v", err)
			}
			if d.Dropped() != 0 || strings.Join(got, "|") != strings.Join(config, "|") {
				t.Errorf("dropped %d; device ran %q", d.Dropped(), got)
			}
		})
	}
}

func TestPasswordPrompt(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	got, err := pasteInto(t, d, []string{"end", "enable", "s3cret", "conf t", "interface Gi1/0/1"})
	want := "end|enable|<password>|conf t|interface Gi1/0/1"
	if err != nil || strings.Join(got, "|") != want {
		t.Fatalf("err %v; device ran %q, want %q", err, got, want)
	}
}

func TestStopsAtCopyQuestion(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	got, err := pasteInto(t, d, []string{"end", "copy run start", "conf t"})
	var se *StopError
	if !errors.As(err, &se) || se.Line != 2 || !strings.Contains(se.Reason, "Destination filename") {
		t.Fatalf("err = %v, want a stop at the copy question", err)
	}
	if len(got) != 1 {
		t.Errorf("device ran %q; the question must not be answered", got)
	}
}

func TestEchoedErrorWordsDontStop(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16}
	cmds := []string{"interface Gi1/0/1", "description unknown command port", "no shutdown"}
	got, err := pasteInto(t, d, cmds)
	if err != nil || strings.Join(got, "|") != strings.Join(cmds, "|") {
		t.Fatalf("err %v; device ran %q", err, got)
	}
}

func TestStopsWhenNothingEchoes(t *testing.T) {
	t.Parallel()
	d := &simdev.Device{Baud: 9600, FIFO: 16, NoEcho: true}
	got, err := pasteInto(t, d, []string{"interface Gi1/0/1", "no shutdown"})
	var se *StopError
	if !errors.As(err, &se) || se.Line != 1 || !strings.Contains(se.Reason, "isn't echoing") {
		t.Fatalf("err = %v, want a stop before pressing Enter", err)
	}
	if len(got) != 0 {
		t.Errorf("device ran %q; Enter must not be pressed blind", got)
	}
}

func TestPatterns(t *testing.T) {
	questions := []string{"Proceed with reload? [confirm]", "Destination filename [startup-config]? ", "Delete flash:x? [yes/no]: ",
		"Do you want to continue? [yes,no] (no) ", " --More-- ", "-- MORE --, next page: Space", "---(more 42%)---", "-- [Q quit|D dump|down]",
		":", "(END)"} // less, after its first page and at the end
	for _, q := range questions {
		if !question.MatchString(q) {
			t.Errorf("question not recognised: %q", q)
		}
	}
	errs := []string{"% Invalid input detected at '^' marker.", "Invalid input: shutdownn", "syntax error.", "Command fail. Return code -61",
		"bad command name foo (line 1 column 1)", "bash: foo: command not found", "% Incomplete command."}
	for _, e := range errs {
		if !devErr.MatchString(e) {
			t.Errorf("error not recognised: %q", e)
		}
	}
	for _, p := range []string{"Switch(config-if)#", "user@sw1# ", "[admin@MikroTik] > ", "rommon 1 >", "Password: "} {
		r := &run{}
		if !r.isPrompt(p) {
			t.Errorf("prompt not recognised: %q", p)
		}
	}
	r := &run{}
	r.learn("Switch(config)#")
	if r.isPrompt("45%") || r.isPrompt("#####") {
		t.Error("progress output taken for a prompt once the host is known")
	}
}

func TestAtLogin(t *testing.T) {
	for line, want := range map[string]bool{
		"Password: ":                  true,
		"[sudo] password for alice: ": true,
		"login as: ":                  true,
		"Enter passphrase for key 'id_ed25519': ": true,
		"Unauthorized login prohibited. Contact:": false,
		"Switch#": false,
	} {
		if got := AtLogin(line); got != want {
			t.Errorf("AtLogin(%q) = %v, want %v", line, got, want)
		}
	}
}

// On a FreeBSD shell, Enter echoes as \r\r\n. Read as a redraw, that blanked
// the line, so a command that printed nothing waited out promptWait.
func TestNoOutputAfterDoubleCR(t *testing.T) {
	t.Parallel()
	rx := make(chan []byte, 64)
	echo := func(b []byte) error { // a shell that echoes, and after Enter prints only its prompt
		for _, ch := range b {
			if ch == '\r' {
				rx <- []byte("\r\r\nroot@fw:~ # ")
			} else {
				rx <- []byte{ch}
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &Pacer{Write: echo, Rx: rx, Baud: 115200, Prompt: "root@fw:~ # "}
	if err := p.Run(ctx, Lines("true\ngrep zzz /etc/hosts\n"), func(int) {}); err != nil {
		t.Fatalf("err = %v, want both lines run with the prompt seen after each", err)
	}
}

// less on a serial console shows the file's name where its prompt would be,
// so it's known by the command: once it goes quiet, stop, rather than wait a
// minute for a prompt that won't come.
func TestFullScreenProgramStopsQuickly(t *testing.T) {
	t.Parallel()
	rx := make(chan []byte, 64)
	pager := func(b []byte) error { // echoes, and after Enter shows a page of the file
		for _, ch := range b {
			if ch == '\r' {
				rx <- []byte("\r\r\n#!/bin/sh\r\n# rc.conf defaults\r\n/etc/defaults/rc.conf")
			} else {
				rx <- []byte{ch}
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &Pacer{Write: pager, Rx: rx, Baud: 115200, Prompt: "root@fw:~ # "}
	t0 := time.Now()
	err := p.Run(ctx, Lines("less /etc/defaults/rc.conf\n"), func(int) {})
	var se *StopError
	if !errors.As(err, &se) || !strings.Contains(se.Reason, "full-screen") || time.Since(t0) > 5*time.Second {
		t.Fatalf("err = %v after %v, want a quick stop for a full-screen program", err, time.Since(t0))
	}
	for cmd, want := range map[string]bool{
		"less /var/log/messages": true, "dmesg | less": true, "man ls": true, "top": true, "vi /etc/hosts": true,
		"top -b -n 1": true, // matched, but it prints and returns to the prompt first
		"  less file": true, "sudo less /var/log/auth.log": true, "/usr/bin/less file": true,
		"topology": false, "show manager": false, "cat file": false, "echo more": false,
	} {
		if got := fullScreen(cmd); got != want {
			t.Errorf("fullScreen(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// At a prompt bauddy doesn't recognise, a quiet screen usually means ready;
// a pager's isn't, or the next line would be typed into it.
func TestNoTypingIntoAPagerAtAnUnknownPrompt(t *testing.T) {
	t.Parallel()
	rx := make(chan []byte, 64)
	var wrote []byte
	pager := func(b []byte) error {
		wrote = append(wrote, b...)
		for _, ch := range b {
			if ch == '\r' {
				rx <- []byte("\r\n# rc.conf defaults\r\n/etc/defaults/rc.conf")
			} else {
				rx <- []byte{ch}
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &Pacer{Write: pager, Rx: rx, Baud: 115200, Prompt: ""}
	err := p.Run(ctx, Lines("less /etc/defaults/rc.conf\necho next\n"), func(int) {})
	var se *StopError
	if !errors.As(err, &se) || !strings.Contains(se.Reason, "full-screen") || strings.Contains(string(wrote), "echo next") {
		t.Fatalf("err = %v, typed %q: want a stop at the pager, with the next line not typed", err, wrote)
	}
}
