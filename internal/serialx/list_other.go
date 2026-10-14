//go:build !linux

package serialx

import "go.bug.st/serial/enumerator"

// osPorts lists the serial ports. On macOS and Windows the serial library
// asks the system, without opening anything.
func osPorts() ([]*enumerator.PortDetails, error) { return enumerator.GetDetailedPortsList() }
