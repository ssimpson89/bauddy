package serialx

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"go.bug.st/serial"
)

var (
	// ErrNoPorts means no port was named and none were found.
	ErrNoPorts = errors.New("no serial ports found; is the adapter plugged in?")
	// ErrUnplugged means a saved adapter name was given but that adapter
	// isn't plugged in.
	ErrUnplugged = errors.New("isn't plugged in")
)

// SeveralError means no port was named and more than one could be meant.
type SeveralError struct{ Ports []PortInfo }

func (e *SeveralError) Error() string { return "several serial ports found" }

// Choose returns the port arg names: a port in ports (see Find), the adapter
// saved under that name (named returns its key), or failing both, arg as a
// path to try anyway (a pty, say). With no arg, a lone USB adapter is
// chosen, else a lone port; otherwise it returns a *SeveralError.
func Choose(arg string, ports []PortInfo, named func(string) (string, bool)) (PortInfo, error) {
	if arg != "" {
		if p, ok := Find(ports, arg); ok {
			return p, nil
		}
		if key, ok := named(arg); ok {
			for _, p := range ports {
				if p.Key == key {
					return p, nil
				}
			}
			if strings.HasPrefix(key, "/") {
				return Manual(key), nil // saved for a path that isn't enumerated
			}
			return PortInfo{}, fmt.Errorf("%s's adapter %w", arg, ErrUnplugged)
		}
		return Manual(arg), nil
	}

	var usb []PortInfo
	for _, p := range ports {
		if p.USB {
			usb = append(usb, p)
		}
	}
	switch {
	case len(usb) == 1:
		return usb[0], nil
	case len(ports) == 0:
		return PortInfo{}, ErrNoPorts
	case len(ports) == 1:
		return ports[0], nil
	}
	return PortInfo{}, &SeveralError{Ports: ports}
}

// IsPermissionDenied says whether err is the port refusing you access.
func IsPermissionDenied(err error) bool {
	var pe interface{ Code() serial.PortErrorCode }
	return errors.As(err, &pe) && pe.Code() == serial.PermissionDenied
}

// OpenError explains the common reasons a port won't open, with what to do
// about them.
func OpenError(path string, err error) error {
	var pe interface { // *serial.PortError
		error
		Code() serial.PortErrorCode
	}
	if errors.As(err, &pe) {
		switch pe.Code() {
		case serial.PortBusy:
			return fmt.Errorf("%s is in use by another program (screen, minicom, another bauddy); close it and try again", path)
		case serial.InvalidSpeed, serial.InvalidDataBits, serial.InvalidParity, serial.InvalidStopBits:
			return fmt.Errorf("%s won't open: the adapter doesn't support that baud rate or framing", path)
		case serial.InvalidSerialPort:
			// The port opened but the driver refused the settings. The
			// library doesn't wrap the cause, so this is the only way to tell.
			if strings.Contains(pe.Error(), "error configuring port") {
				return fmt.Errorf("%s won't open: the adapter doesn't support that baud rate or framing", path)
			}
		case serial.PermissionDenied:
			return fmt.Errorf("no permission to open %s; on Linux, add yourself to the group that owns it "+
				"(usually dialout, uucp on Arch): sudo usermod -aG dialout $USER, then log out and back in", path)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s doesn't exist; 'bauddy list' shows the ports", path)
	}
	return fmt.Errorf("can't open %s: %w", path, err)
}
