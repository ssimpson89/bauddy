package serialx

import "testing"

func TestScore(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		min  float64
		max  float64
	}{
		{"empty", nil, 0, 0},
		{"login prompt", []byte("\r\nswitch01 login: "), 0.95, 1},
		{"ansi bios", []byte("\x1b[2J\x1b[1;1HPress F2 to enter setup\r\n"), 0.95, 1},
		{"garbage", []byte{0xf0, 0x80, 0x00, 0xfe, 0x9c, 0x00, 0xe0, 0x11, 0xff, 0x80, 0x00, 0xc3, 0x8f, 0x00, 0xf8, 0x80}, 0, 0.3},
		{"too short", []byte("ok"), 0, 0},
		{"systemd utf-8", []byte("[  \u2713  ] Started OpenSSH server \u25cf sshd.service\r\n"), 0.95, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := score(c.in); got < c.min || got > c.max {
				t.Errorf("score = %.2f, want %.2f..%.2f", got, c.min, c.max)
			}
		})
	}
}

func TestAnswered(t *testing.T) {
	for b, want := range map[string]bool{
		"$": true, "\r\nUser:": true, "用户:": true, "\a": true, "\x1b[0m#": true,
		"": false, "\x00": false, "$\xff": false, "\xe0\xe0": false, "\x1c": false,
	} {
		if got := Answered([]byte(b)); got != want {
			t.Errorf("Answered(%q) = %v, want %v", b, got, want)
		}
	}
}
