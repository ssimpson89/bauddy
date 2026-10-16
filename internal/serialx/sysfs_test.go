package serialx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSysfs builds just enough of /sys and /dev for sysfsPorts.
type fakeSysfs struct {
	t        *testing.T
	sys, dev string
}

func newFakeSysfs(t *testing.T) *fakeSysfs {
	root := t.TempDir()
	f := &fakeSysfs{t, filepath.Join(root, "sys"), filepath.Join(root, "dev")}
	for _, d := range []string{"class/tty", "bus/usb", "bus/usb-serial", "bus/pnp"} {
		f.mkdir(filepath.Join(f.sys, d))
	}
	f.mkdir(f.dev)
	return f
}

func (f *fakeSysfs) mkdir(d string) {
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSysfs) write(path, s string) {
	f.mkdir(filepath.Dir(path))
	if err := os.WriteFile(path, []byte(s+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSysfs) link(target, name string) {
	if err := os.Symlink(target, name); err != nil {
		f.t.Fatal(err)
	}
}

// tty adds /sys/class/tty/name, with its device at device (on bus) unless
// device is "", a type file unless uartType is "", and /dev/name.
func (f *fakeSysfs) tty(name, device, bus, uartType string) {
	dir := filepath.Join(f.sys, "class/tty", name)
	f.mkdir(dir)
	if device != "" {
		dev := filepath.Join(f.sys, "devices", device)
		f.mkdir(dev)
		f.link(filepath.Join(f.sys, "bus", bus), filepath.Join(dev, "subsystem"))
		f.link(dev, filepath.Join(dir, "device"))
		if !strings.HasPrefix(name, "ttyNODRIVER") {
			f.mkdir(filepath.Join(f.sys, "bus", bus, "drivers", name))
			f.link(filepath.Join(f.sys, "bus", bus, "drivers", name), filepath.Join(dev, "driver"))
		}
	}
	if uartType != "" {
		f.write(filepath.Join(dir, "type"), uartType)
	}
	f.write(filepath.Join(f.dev, name), "")
}

func (f *fakeSysfs) usb(device, vid, pid, serial, product string) {
	dir := filepath.Join(f.sys, "devices", device)
	f.write(filepath.Join(dir, "idVendor"), vid)
	f.write(filepath.Join(dir, "idProduct"), pid)
	f.write(filepath.Join(dir, "serial"), serial)
	f.write(filepath.Join(dir, "product"), product)
}

func TestSysfsPorts(t *testing.T) {
	f := newFakeSysfs(t)
	f.tty("ttyS0", "pnp0/00:01/00:01:0/00:01:0.0", "pnp", "4")                    // a real 16550A
	f.tty("ttyS1", "platform/serial8250/serial8250:0/serial8250:0.1", "pnp", "0") // no UART
	f.tty("tty0", "", "", "")                                                     // a virtual terminal
	f.tty("ttyNODRIVER0", "platform/thing", "pnp", "")                            // hardware, but no driver bound
	f.tty("rfcomm0", "", "", "")                                                  // Bluetooth, before it connects
	usbDev := "pci0000:00/0000:00:14.0/usb1/1-2"
	f.tty("ttyUSB0", usbDev+"/1-2:1.0/ttyUSB0", "usb-serial", "")
	f.usb(usbDev, "0403", "6001", "A9OY16TW", "FT232R USB UART")
	acmDev := "pci0000:00/0000:00:14.0/usb1/1-3"
	f.tty("ttyACM0", acmDev+"/1-3:1.0", "usb", "")
	f.usb(acmDev, "05a6", "0008", "", "Cisco USB Console")
	xrDev := "pci0000:00/0000:00:14.0/usb1/1-4"
	f.tty("ttyXRUSB0", xrDev+"/1-4:1.0", "usb", "") // Exar's own driver, a name the serial library doesn't know
	f.usb(xrDev, "04e2", "1410", "XR1", "XR21V1410 USB-UART")
	cloneDev := "pci0000:00/0000:00:14.0/usb1/1-5"
	f.tty("ttyUSB1", cloneDev+"/1-5:1.0/ttyUSB1", "usb-serial", "")
	f.usb(cloneDev, "1a86", "7523", "AB12 ", "USB Serial") // a clone whose serial ends in a space
	// Since Linux 6.8 a ttyS's device is a serial-base port, always with a
	// driver, so only type tells a real UART from a phantom.
	f.tty("ttyS2", "platform/serial8250/serial8250:0/serial8250:0.2", "serial-base", "0")
	f.tty("ttyS3", "pnp0/00:02/00:02:0/00:02:0.0", "serial-base", "4")
	f.tty("ttyS4", "pnp0/00:03/00:03:0/00:03:0.0", "pnp", "4")
	f.write(filepath.Join(f.sys, "class/tty/console/active"), "tty0 ttyS4 ttyUSB1") // console=ttyS4, console=ttyUSB1

	got, err := sysfsPorts(f.sys, f.dev)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	var names []string
	for _, d := range got {
		n := filepath.Base(d.Name)
		names = append(names, n)
		byName[n] = d.VID + ":" + d.PID + " " + d.SerialNumber + " " + d.Product
		if d.Name != filepath.Join(f.dev, n) {
			t.Errorf("%s: path %s, want it under %s", n, d.Name, f.dev)
		}
		if d.IsUSB != (d.VID != "") {
			t.Errorf("%s: USB %v but VID %q", n, d.IsUSB, d.VID)
		}
	}
	slices.Sort(names)
	if want := []string{"rfcomm0", "ttyACM0", "ttyS0", "ttyS3", "ttyUSB0", "ttyUSB1", "ttyXRUSB0"}; !slices.Equal(names, want) {
		t.Errorf("ports %v, want %v (no ttyS1 without a UART, no tty0, none without a driver, not the kernel's ttyS4 console)", names, want)
	}
	for n, want := range map[string]string{
		"ttyUSB0":   "0403:6001 A9OY16TW FT232R USB UART",
		"ttyACM0":   "05A6:0008  Cisco USB Console",
		"ttyXRUSB0": "04E2:1410 XR1 XR21V1410 USB-UART",
		"ttyUSB1":   "1A86:7523 AB12  USB Serial", // the space kept, as the serial library did, so its saved settings still match
		"ttyS0":     ":  ",
	} {
		if byName[n] != want {
			t.Errorf("%s: %q, want %q", n, byName[n], want)
		}
	}
}

func TestSysfsPortsWithoutSysfs(t *testing.T) {
	got, err := sysfsPorts(filepath.Join(t.TempDir(), "sys"), "/dev")
	if err != nil || len(got) != 0 {
		t.Errorf("no /sys/class/tty: got %v, %v; want no ports and no error, so a named port still opens", got, err)
	}
}
