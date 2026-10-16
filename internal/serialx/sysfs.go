package serialx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.bug.st/serial/enumerator"
)

// sysfsPorts lists Linux serial ports from sysfs (at sys, with device nodes
// in dev) without opening any of them. The serial library opens each ttyS
// to see whether it's real, which resets the line settings of a console
// another program is using, such as a server's login prompt on ttyS0. It
// also misses USB adapters whose vendor drivers pick their own names
// (ttyXRUSB0, ttyCH343USB0), and hides a real port you can't open yet,
// instead of letting bauddy say which group to join.
func sysfsPorts(sys, dev string) ([]*enumerator.PortDetails, error) {
	entries, err := os.ReadDir(filepath.Join(sys, "class/tty"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no sysfs (WSL1, some containers): ports can still be named
	}
	if err != nil {
		return nil, err
	}
	active, _ := os.ReadFile(filepath.Join(sys, "class/tty/console/active"))
	consoles := strings.Fields(string(active)) // where the kernel writes its messages
	var out []*enumerator.PortDetails
	for _, e := range entries {
		tty := filepath.Join(sys, "class/tty", e.Name())
		_, err := os.Stat(filepath.Join(tty, "device/driver"))
		rfcomm := strings.HasPrefix(e.Name(), "rfcomm") // Bluetooth: may have no device yet
		if err != nil && !rfcomm {
			continue // no hardware or driver behind it: consoles, virtual terminals
		}
		if t, err := os.ReadFile(filepath.Join(tty, "type")); err == nil && strings.TrimSpace(string(t)) == "0" {
			continue // a ttyS with no UART behind it (PORT_UNKNOWN)
		}
		path := filepath.Join(dev, e.Name())
		if _, err := os.Stat(path); err != nil {
			continue
		}
		d := &enumerator.PortDetails{Name: path}
		if device, err := filepath.EvalSymlinks(filepath.Join(tty, "device")); err == nil {
			usbDetails(device, d)
		}
		if !d.IsUSB && slices.Contains(consoles, e.Name()) {
			// This computer's own console, such as ttyS0 with console=ttyS0:
			// it leads back here, and its login prompt would take half of what
			// comes in. You can still name it (bauddy ttyS0). A USB cable is
			// listed even so: it's the one you plugged in.
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// usbDetails fills in a USB adapter's IDs and names from the USB device
// above the tty's device: two levels up from a usb-serial port (ttyUSB),
// one from a USB interface (ttyACM, vendor drivers). As the serial library
// did, IDs are upper case and only the line end is trimmed from names, so
// saved settings, keyed by VID, PID and serial number, still match.
func usbDetails(device string, d *enumerator.PortDetails) {
	sub, _ := filepath.EvalSymlinks(filepath.Join(device, "subsystem"))
	var usb string
	switch filepath.Base(sub) {
	case "usb-serial":
		usb = filepath.Dir(filepath.Dir(device))
	case "usb":
		usb = filepath.Dir(device)
	default:
		return
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(usb, name))
		return strings.TrimRight(string(b), "\r\n")
	}
	vid := strings.TrimSpace(read("idVendor"))
	if vid == "" {
		return
	}
	d.IsUSB = true
	d.VID, d.PID = strings.ToUpper(vid), strings.ToUpper(strings.TrimSpace(read("idProduct")))
	d.SerialNumber, d.Product = read("serial"), read("product")
}
