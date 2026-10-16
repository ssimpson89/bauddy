package tui

import (
	"regexp"
	"strings"
)

// Prompts a device name can be read from, matched against the whole
// cursor line: the device has printed its prompt and is waiting.
var (
	// Cisco, Arista, HPE, NX-OS: core-sw-01#, core-sw-01(config-if)#, core-sw-01>
	promptIOS = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]{0,252})(?:\([^()\s]+\))?[#>] ?$`)
	// Juniper, Palo Alto, Linux, VyOS, FreeBSD: admin@core-fw>, user@host:~$, root@fw01:~ #
	promptUserAt = regexp.MustCompile(`^[A-Za-z0-9_.-]+@([A-Za-z0-9][A-Za-z0-9.-]{0,252})(?:\([^()\s]+\)|:\S*)? ?[#>$%] ?$`)
	// Linux: [root@host ~]#
	promptBracket = regexp.MustCompile(`^\[[A-Za-z0-9_.-]+@([A-Za-z0-9][A-Za-z0-9.-]{0,252})(?: [^\]]*)?\][#$] ?$`)
)

// notNames are names that say nothing about which device this is: factory
// defaults, bootloaders, and words a prompt-shaped line can hold.
var notNames = map[string]bool{
	"switch": true, "router": true, "localhost": true, "host": true, "unknown": true,
	"mikrotik": true, "ubnt": true, "vyos": true, "pfsense": true, "opnsense": true, "openwrt": true,
	"ciscoasa": true, "amnesiac": true, "raspberrypi": true, "ubuntu": true, "debian": true,
	"rommon": true, "loader": true, "grub": true, "boot": true, "cfe": true, "redboot": true, "ok": true,
}

// deviceName is the device's name if line is a prompt that carries one,
// else "": core-sw-01(config-if)# gives core-sw-01, admin@core-fw.example.com>
// gives core-fw. Factory defaults such as Switch don't count.
func deviceName(line string) string {
	var name string
	for _, re := range []*regexp.Regexp{promptIOS, promptUserAt, promptBracket} {
		if m := re.FindStringSubmatch(line); m != nil {
			name = m[1]
			break
		}
	}
	name, _, _ = strings.Cut(name, ".") // core-sw-01.example.com: the host
	if len(name) < 2 || len(name) > 63 || notNames[strings.ToLower(name)] {
		return ""
	}
	return name
}
