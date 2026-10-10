package serialx

// GarbleMeter flags live output that has stopped looking like text, which
// usually means the baud rate changed (e.g. a reboot into a bootloader at a
// different speed), or 7-bit gear read as 8N1. It uses hysteresis so the
// hint doesn't flicker.
type GarbleMeter struct {
	window  []byte
	garbled bool
}

const (
	garbleWindow  = 256 // bytes of recent output to judge
	garbleMinData = 48  // don't judge until this much has arrived
	garbleOn      = 0.5 // flag when the score drops below this
	garbleOff     = 0.8 // clear only once it recovers above this
)

// Add feeds new bytes and reports whether output currently looks garbled.
func (g *GarbleMeter) Add(b []byte) bool {
	g.window = append(g.window, b...)
	if over := len(g.window) - garbleWindow; over > 0 {
		g.window = g.window[over:]
	}
	if len(g.window) < garbleMinData {
		return g.garbled
	}
	s := score(g.window)
	_, parity := sevenBit(g.window) // 7-bit text read as 8N1 scores near the line, so check it directly
	switch {
	case !g.garbled && (s < garbleOn || parity):
		g.garbled = true
	case g.garbled && s > garbleOff && !parity:
		g.garbled = false
	}
	return g.garbled
}

// Garbled reports the result of the last Add.
func (g *GarbleMeter) Garbled() bool { return g.garbled }

// Reset forgets history, e.g. after the baud rate changes.
func (g *GarbleMeter) Reset() { g.window, g.garbled = g.window[:0], false }
