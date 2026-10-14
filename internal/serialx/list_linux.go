package serialx

import "go.bug.st/serial/enumerator"

// osPorts lists the serial ports; on Linux, from sysfs (see sysfsPorts).
func osPorts() ([]*enumerator.PortDetails, error) { return sysfsPorts("/sys", "/dev") }
