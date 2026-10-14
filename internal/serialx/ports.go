package serialx

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"go.bug.st/serial/enumerator"
)

// PortInfo describes a serial device on this machine.
type PortInfo struct {
	Path    string // /dev/ttyUSB0, /dev/cu.usbserial-1410
	ByID    string // stable /dev/serial/by-id/... path on Linux, if any
	Key     string // identifies the physical adapter for saved settings
	USB     bool
	VID     string
	PID     string
	Serial  string
	Product string
}

// OpenPath is the path bauddy should open: the stable by-id link when present,
// so automatic reconnect finds the same adapter after it re-enumerates.
func (p PortInfo) OpenPath() string {
	if p.ByID != "" {
		return p.ByID
	}
	return p.Path
}

// USBID returns "VID:PID", or "" for non-USB ports.
func (p PortInfo) USBID() string {
	if !p.USB {
		return ""
	}
	return p.VID + ":" + p.PID
}

// Manual describes a path that wasn't enumerated (a pty, an unusual driver).
func Manual(path string) PortInfo {
	return PortInfo{Path: path, Key: path}
}

// List returns usable serial ports, filtering out platform noise.
func List() ([]PortInfo, error) {
	details, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	byID := linuxByID()

	var out []PortInfo
	for _, d := range details {
		if skip(d.Name) || managementPort(d.Name, d.Product) {
			continue
		}
		out = append(out, PortInfo{
			Path: d.Name, ByID: byID[d.Name], USB: d.IsUSB,
			VID: d.VID, PID: d.PID, Serial: d.SerialNumber, Product: d.Product,
		})
	}
	assignKeys(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].USB != out[j].USB {
			return out[i].USB // USB adapters first; they're what you plugged in
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// assignKeys keys USB adapters by VID:PID:serial so saved settings follow the
// cable to any USB socket. Multi-port adapters and cheap clones can share a
// serial number; those get the device name appended so they don't collide.
func assignKeys(ports []PortInfo) {
	count := map[string]int{}
	for i := range ports {
		p := &ports[i]
		switch {
		case p.USB && p.Serial != "":
			p.Key = "usb:" + p.VID + ":" + p.PID + ":" + p.Serial
		default:
			p.Key = p.OpenPath()
		}
		count[p.Key]++
	}
	for i := range ports {
		if p := &ports[i]; count[p.Key] > 1 {
			p.Key += ":" + filepath.Base(p.OpenPath())
		}
	}
}

// Find returns the port in ports that dev names: a full path, a by-id link,
// a symlink to either, or a bare name such as ttyUSB0 or cu.usbserial-1410.
func Find(ports []PortInfo, dev string) (PortInfo, bool) {
	cands := []string{dev}
	if !strings.Contains(dev, "/") {
		cands = append(cands, "/dev/"+dev)
	}
	for _, c := range cands {
		if r, err := filepath.EvalSymlinks(c); err == nil {
			cands = append(cands, r)
		}
	}
	for _, p := range ports {
		for _, c := range cands {
			if c == p.Path || c == p.ByID {
				return p, true
			}
		}
	}
	return PortInfo{}, false
}

// skip hides ports nobody wants to connect to.
func skip(name string) bool {
	if runtime.GOOS == "darwin" {
		// /dev/tty.* waits for carrier detect; /dev/cu.* is the one to use.
		if strings.HasPrefix(name, "/dev/tty.") {
			return true
		}
		for _, noise := range []string{"Bluetooth", "debug-console", "wlan-debug"} {
			if strings.Contains(name, noise) {
				return true
			}
		}
	}
	return false
}

// managementPort spots Intel AMT's serial-over-LAN port: business laptops
// have one, and it leads to the network, not to a cable, so it's left out of
// lists; you can still name it (bauddy ttyS4). On Windows it says so in its
// name. On Linux it's taken to be any Intel PCI serial controller: a
// computer's own serial ports are on the LPC bus, and Intel's few PCI UARTs
// for embedded boards are rare enough to name by hand.
func managementPort(name, product string) bool {
	switch runtime.GOOS {
	case "windows":
		return amtName(product)
	case "linux":
		return amtSysfs("/sys", name)
	}
	return false
}

// amtSysfs checks the PCI device behind a tty in sysfs at root. Since Linux
// 6.8 the tty's device is a serial-base port, with the PCI device a level or
// two above it, so it looks up the tree for the first device with a vendor.
func amtSysfs(root, name string) bool {
	dev, err := filepath.EvalSymlinks(filepath.Join(root, "class/tty", filepath.Base(name), "device"))
	for i := 0; err == nil && i < 4; i, dev = i+1, filepath.Dir(dev) {
		if vendor, err := os.ReadFile(filepath.Join(dev, "vendor")); err == nil {
			class, _ := os.ReadFile(filepath.Join(dev, "class"))
			return amtPCI(string(vendor), string(class))
		}
	}
	return false
}

// amtName matches "Intel(R) Active Management Technology - SOL (COM3)".
func amtName(product string) bool { return strings.Contains(product, "Active Management Technology") }

// amtPCI matches an Intel (0x8086) PCI serial controller (class 0x0700xx).
func amtPCI(vendor, class string) bool {
	return strings.TrimSpace(vendor) == "0x8086" && strings.HasPrefix(strings.TrimSpace(class), "0x0700")
}

// linuxByID maps /dev/ttyUSB0 -> /dev/serial/by-id/usb-FTDI_... .
func linuxByID() map[string]string {
	m := map[string]string{}
	links, _ := filepath.Glob("/dev/serial/by-id/*")
	for _, l := range links {
		if target, err := filepath.EvalSymlinks(l); err == nil {
			m[target] = l
		}
	}
	return m
}
