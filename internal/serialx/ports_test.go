package serialx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
		{"/dev/pts/3", nil, "/dev/pts/3", nil},        // not enumerated: try it anyway
		{"ttyS4", []PortInfo{usb}, "/dev/ttyS4", nil}, // a short name for one that isn't listed
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

func TestManagementPortsAreSpotted(t *testing.T) {
	for _, c := range []struct {
		product string
		want    bool
	}{
		{"Intel(R) Active Management Technology - SOL (COM3)", true},
		{"Communications Port (COM1)", false},
		{"USB Serial Port (COM4)", false},
	} {
		if got := amtName(c.product); got != c.want {
			t.Errorf("amtName(%q) = %v", c.product, got)
		}
	}
	// sysfs before Linux 6.8 (the tty's device is the PCI device) and after
	// (it's a serial-base port below it), and a built-in port on the LPC bus.
	root := t.TempDir()
	pci := filepath.Join(root, "devices/pci0000:00/0000:00:16.3")
	for _, d := range []string{pci + "/0000:00:16.3:0/0000:00:16.3:0.0", root + "/devices/pnp0/00:01", root + "/class/tty"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(pci+"/vendor", []byte("0x8086\n"), 0o644)
	os.WriteFile(pci+"/class", []byte("0x070002\n"), 0o644)
	for tty, dev := range map[string]string{"ttyS4": pci, "ttyS5": pci + "/0000:00:16.3:0/0000:00:16.3:0.0", "ttyS0": root + "/devices/pnp0/00:01"} {
		os.MkdirAll(filepath.Join(root, "class/tty", tty), 0o755)
		if err := os.Symlink(dev, filepath.Join(root, "class/tty", tty, "device")); err != nil {
			t.Fatal(err)
		}
	}
	for tty, want := range map[string]bool{"ttyS4": true, "ttyS5": true, "ttyS0": false, "ttyUSB0": false} {
		if got := amtSysfs(root, "/dev/"+tty); got != want {
			t.Errorf("amtSysfs(%s) = %v, want %v", tty, got, want)
		}
	}

	for _, c := range []struct {
		vendor, class string
		want          bool
	}{
		{"0x8086\n", "0x070002\n", true},  // Intel AMT serial-over-LAN
		{"0x8086\n", "0x118000\n", false}, // Intel LPSS UART: a real port on embedded boards
		{"0x1c00\n", "0x070002\n", false}, // a PCI serial card
		{"", "", false},                   // a built-in port on the LPC bus
	} {
		if got := amtPCI(c.vendor, c.class); got != c.want {
			t.Errorf("amtPCI(%q, %q) = %v", c.vendor, c.class, got)
		}
	}
}
