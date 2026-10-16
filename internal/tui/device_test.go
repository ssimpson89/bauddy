package tui

import "testing"

func TestDeviceName(t *testing.T) {
	for line, want := range map[string]string{
		"core-sw-01#":               "core-sw-01",
		"core-sw-01>":               "core-sw-01",
		"core-sw-01(config-if)#":    "core-sw-01",
		"leaf1(config-if-Et1)# ":    "leaf1",
		"HP-2920#":                  "HP-2920",
		"admin@PA-3220>":            "PA-3220",
		"admin@PA-3220(active)>":    "PA-3220",
		"user@core-fw.example.com#": "core-fw",
		"user@edge01:~$ ":           "edge01",
		"root@OPNsense:~ #":         "",
		"root@fw01:~ #":             "fw01",
		"[root@db01 ~]# ":           "db01",
		"admin@edge01.management.rack01.building01.primary-site.example.internal>": "edge01",
		"Switch#":                 "",
		"Router>":                 "",
		"switch(config)#":         "",
		"loader>":                 "",
		"rommon 1 >":              "",
		"=>":                      "",
		"Username: ":              "",
		"Password:":               "",
		"--More--":                "",
		"core-sw-01#show version": "",
		"Interface Gi1/0/1 is up, line protocol is up>": "",
		"":   "",
		"x#": "",
	} {
		if got := deviceName(line); got != want {
			t.Errorf("deviceName(%q) = %q, want %q", line, got, want)
		}
	}
}
