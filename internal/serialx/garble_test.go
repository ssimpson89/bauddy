package serialx

import (
	"bytes"
	"testing"

	"go.bug.st/serial"
)

func TestGarbleMeter(t *testing.T) {
	var g GarbleMeter
	text := []byte("Gi1/0/1   uplink-fw01   connected    trunk\r\n")
	junk := []byte{0xf0, 0x80, 0x00, 0xfe, 0x9c, 0x00, 0xe0, 0x11, 0xff, 0x80, 0x00, 0xc3}

	if g.Add(bytes.Repeat(text, 4)) {
		t.Fatal("clean text flagged as garbled")
	}
	if !g.Add(bytes.Repeat(junk, 30)) {
		t.Fatal("junk not flagged")
	}
	// One clean line shouldn't clear it (hysteresis)...
	if !g.Add(text) {
		t.Fatal("cleared too eagerly")
	}
	// ...but a screenful should.
	if g.Add(bytes.Repeat(text, 8)) {
		t.Fatal("didn't clear after clean output")
	}
	g.Add(bytes.Repeat(junk, 30))
	g.Reset()
	if g.Garbled() {
		t.Fatal("reset didn't clear")
	}
}

func TestGarbleMeterFlags7Bit(t *testing.T) {
	var g GarbleMeter
	text := []byte("Gi1/0/1   uplink-fw01   connected    trunk\r\n")
	if !g.Add(withParity(bytes.Repeat(text, 4), serial.EvenParity)) {
		t.Fatal("7E1 text read as 8N1 not flagged")
	}
	g.Reset()
	if g.Add(bytes.Repeat(text, 4)) {
		t.Fatal("plain text flagged")
	}
}
