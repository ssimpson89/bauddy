package serialx

import "testing"

func TestAssignKeys(t *testing.T) {
	ports := []PortInfo{
		{Path: "/dev/ttyUSB0", USB: true, VID: "0403", PID: "6011", Serial: "FT1"}, // 4-port FTDI
		{Path: "/dev/ttyUSB1", USB: true, VID: "0403", PID: "6011", Serial: "FT1"},
		{Path: "/dev/ttyUSB2", USB: true, VID: "1a86", PID: "7523"}, // CH340, no serial
		{Path: "/dev/ttyUSB3", USB: true, VID: "0403", PID: "6001", Serial: "A1", ByID: "/dev/serial/by-id/usb-FTDI_A1"},
	}
	assignKeys(ports)
	want := []string{
		"usb:0403:6011:FT1:ttyUSB0",
		"usb:0403:6011:FT1:ttyUSB1",
		"/dev/ttyUSB2",
		"usb:0403:6001:A1",
	}
	for i, p := range ports {
		if p.Key != want[i] {
			t.Errorf("%s: Key = %q, want %q", p.Path, p.Key, want[i])
		}
	}
}

func TestFind(t *testing.T) {
	ports := []PortInfo{
		{Path: "/dev/ttyUSB0", ByID: "/dev/serial/by-id/usb-FTDI_A1"},
		{Path: "/dev/cu.usbserial-1410"},
	}
	for _, dev := range []string{"/dev/ttyUSB0", "ttyUSB0", "/dev/serial/by-id/usb-FTDI_A1", "cu.usbserial-1410"} {
		if _, ok := Find(ports, dev); !ok {
			t.Errorf("Find(%q) found nothing", dev)
		}
	}
	if _, ok := Find(ports, "ttyUSB9"); ok {
		t.Error("Find(ttyUSB9) matched")
	}
}
