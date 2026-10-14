package serialx

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"go.bug.st/serial"
)

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

func TestChoose(t *testing.T) {
	usb := PortInfo{Path: "/dev/ttyUSB0", Key: "usb:0403:6001:A1", USB: true}
	builtin := PortInfo{Path: "/dev/ttyS0", Key: "/dev/ttyS0"}
	saved := map[string]string{"core": "usb:0403:6001:A1", "pty": "/dev/pts/7", "gone": "usb:1a86:7523:X"}
	named := func(n string) (string, bool) { k, ok := saved[n]; return k, ok }
	for _, c := range []struct {
		arg   string
		ports []PortInfo
		want  string
		err   error
	}{
		{"", []PortInfo{usb, builtin}, "/dev/ttyUSB0", nil}, // the lone USB adapter
		{"", []PortInfo{builtin}, "/dev/ttyS0", nil},
		{"", nil, "", ErrNoPorts},
		{"ttyS0", []PortInfo{usb, builtin}, "/dev/ttyS0", nil},
		{"core", []PortInfo{usb, builtin}, "/dev/ttyUSB0", nil},
		{"pty", nil, "/dev/pts/7", nil},
		{"gone", []PortInfo{usb}, "", ErrUnplugged},
		{"/dev/pts/3", nil, "/dev/pts/3", nil}, // not enumerated: try it anyway
	} {
		p, err := Choose(c.arg, c.ports, named)
		if p.Path != c.want || !errors.Is(err, c.err) {
			t.Errorf("Choose(%q) = %q, %v; want %q, %v", c.arg, p.Path, err, c.want, c.err)
		}
	}
	two := []PortInfo{usb, {Path: "/dev/ttyUSB1", USB: true}}
	var several *SeveralError
	if _, err := Choose("", two, named); !errors.As(err, &several) || len(several.Ports) != 2 {
		t.Errorf("two adapters: got %v, want a SeveralError listing both", err)
	}
}

func TestOpenError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&fs.PathError{Op: "open", Path: "/dev/ttyUSB9", Err: fs.ErrNotExist}, "doesn't exist; 'bauddy list'"},
		{errors.New("boom"), "can't open /dev/ttyUSB9: boom"},
		{portErr{serial.PortBusy, "Serial port busy"}, "in use by another program"},
		{portErr{serial.InvalidSpeed, "Invalid port speed"}, "doesn't support that baud rate or framing"},
		{portErr{serial.InvalidParity, "Port parity invalid or not supported"}, "doesn't support that baud rate or framing"},
		// The settings refused after opening; the library only says so in its text.
		{portErr{serial.InvalidSerialPort, "Invalid serial port: error configuring port: invalid argument"}, "doesn't support that baud rate or framing"},
		{portErr{serial.InvalidSerialPort, "Invalid serial port"}, "can't open /dev/ttyUSB9: Invalid serial port"},
	} {
		if got := OpenError("/dev/ttyUSB9", c.err).Error(); !strings.Contains(got, c.want) {
			t.Errorf("got %q, want it to contain %q", got, c.want)
		}
	}
}

// portErr stands in for *serial.PortError, whose fields can't be set here.
type portErr struct {
	code serial.PortErrorCode
	msg  string
}

func (e portErr) Error() string              { return e.msg }
func (e portErr) Code() serial.PortErrorCode { return e.code }
